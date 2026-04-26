package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strconv"
	"time"

	"github.com/google/uuid"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/wolfeidau/starlogz/internal/store"
)

type mcpServer struct {
	logger *slog.Logger
	server *mcp.Server
	store  *store.Store
}

func newMCPServer(logger *slog.Logger, st *store.Store) *mcpServer {
	ms := &mcpServer{
		logger: logger,
		server: mcp.NewServer(&mcp.Implementation{Name: name}, nil),
		store:  st,
	}
	mcp.AddTool(ms.server, &mcp.Tool{
		Name:        "whoami",
		Description: "Returns identity and token scopes. Call this first to verify access.",
	}, ms.whoami)
	mcp.AddTool(ms.server, &mcp.Tool{
		Name:        "project_ensure",
		Description: "Creates a project if it does not exist and returns it. Use when you need a custom display name; fact_write auto-creates projects.",
	}, ms.projectEnsure)
	mcp.AddTool(ms.server, &mcp.Tool{
		Name:        "fact_write",
		Description: "Writes a fact to a project. Auto-creates the project if needed. Supply a key to upsert by stable identifier.",
	}, ms.factWrite)
	mcp.AddTool(ms.server, &mcp.Tool{
		Name:        "fact_search",
		Description: "Full-text search over live facts in a project. Returns results ordered by relevance.",
	}, ms.factSearch)
	mcp.AddTool(ms.server, &mcp.Tool{
		Name:        "fact_list",
		Description: "Lists all live facts in a project, newest first. Optionally filter by a single tag.",
	}, ms.factList)
	mcp.AddTool(ms.server, &mcp.Tool{
		Name:        "fact_delete",
		Description: "Soft-deletes a fact by ID. The fact no longer appears in list or search results.",
	}, ms.factDelete)
	return ms
}

func (ms *mcpServer) whoami(ctx context.Context, req *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, any, error) {
	userInfo := req.Extra.TokenInfo
	ms.logger.InfoContext(ctx, "whoami call", slog.String("user_id", userInfo.UserID))
	type whoamiresp struct {
		UserID string   `json:"user_id"`
		Scopes []string `json:"scopes"`
	}
	jsonData, err := json.Marshal(&whoamiresp{UserID: userInfo.UserID, Scopes: userInfo.Scopes})
	if err != nil {
		return nil, nil, fmt.Errorf("failed to marshal user data: %w", err)
	}
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: string(jsonData)}}}, nil, nil
}

type projectEnsureInput struct {
	Slug string `json:"slug"`
	Name string `json:"name"`
}

type factWriteInput struct {
	Project string   `json:"project"`
	Content string   `json:"content"`
	Key     string   `json:"key"`
	Tags    []string `json:"tags"`
}

type factSearchInput struct {
	Project string   `json:"project"`
	Query   string   `json:"query"`
	Tags    []string `json:"tags"`
	Limit   int      `json:"limit"`
}

type factListInput struct {
	Project string `json:"project"`
	Tag     string `json:"tag"`
	Limit   int    `json:"limit"`
}

type factDeleteInput struct {
	ID string `json:"id"`
}

type factResponse struct {
	ID        string   `json:"id"`
	Key       string   `json:"key,omitempty"`
	Content   string   `json:"content"`
	Tags      []string `json:"tags"`
	UpdatedAt string   `json:"updated_at"`
}

func (ms *mcpServer) projectEnsure(ctx context.Context, req *mcp.CallToolRequest, in projectEnsureInput) (*mcp.CallToolResult, any, error) {
	if ms.store == nil {
		return nil, nil, fmt.Errorf("database not configured")
	}
	user, err := ms.resolveUser(ctx, req.Extra.TokenInfo.UserID)
	if err != nil {
		return nil, nil, err
	}
	name := in.Name
	if name == "" {
		name = in.Slug
	}
	project, err := ms.store.EnsureProject(ctx, user.ID, in.Slug, name)
	if err != nil {
		return nil, nil, fmt.Errorf("ensure project: %w", err)
	}
	return jsonResult(map[string]any{
		"id":   project.ID.String(),
		"slug": project.Slug,
		"name": project.Name,
	})
}

func (ms *mcpServer) factWrite(ctx context.Context, req *mcp.CallToolRequest, in factWriteInput) (*mcp.CallToolResult, any, error) {
	if err := requireScope(req, "facts:write"); err != nil {
		return nil, nil, err
	}
	if ms.store == nil {
		return nil, nil, fmt.Errorf("database not configured")
	}
	user, err := ms.resolveUser(ctx, req.Extra.TokenInfo.UserID)
	if err != nil {
		return nil, nil, err
	}
	project, err := ms.store.EnsureProject(ctx, user.ID, in.Project, in.Project)
	if err != nil {
		return nil, nil, fmt.Errorf("ensure project: %w", err)
	}
	tags := in.Tags
	if tags == nil {
		tags = []string{}
	}
	fact, err := ms.store.WriteFact(ctx, store.WriteFactParams{
		ProjectID:  project.ID,
		Key:        in.Key,
		Content:    in.Content,
		Tags:       tags,
		SourceType: "human",
		CreatedBy:  user.ID,
	})
	if err != nil {
		return nil, nil, fmt.Errorf("write fact: %w", err)
	}
	return jsonResult(map[string]any{
		"id":      fact.ID.String(),
		"updated": !fact.CreatedAt.Equal(fact.UpdatedAt),
	})
}

func (ms *mcpServer) factSearch(ctx context.Context, req *mcp.CallToolRequest, in factSearchInput) (*mcp.CallToolResult, any, error) {
	if ms.store == nil {
		return nil, nil, fmt.Errorf("database not configured")
	}
	user, err := ms.resolveUser(ctx, req.Extra.TokenInfo.UserID)
	if err != nil {
		return nil, nil, err
	}
	project, err := ms.store.GetProjectBySlug(ctx, user.ID, in.Project)
	if errors.Is(err, store.ErrNotFound) {
		return nil, nil, fmt.Errorf("project %q not found", in.Project)
	}
	if err != nil {
		return nil, nil, fmt.Errorf("get project: %w", err)
	}
	limit := in.Limit
	if limit <= 0 || limit > 100 {
		limit = 20
	}
	facts, err := ms.store.SearchFacts(ctx, project.ID, in.Query, in.Tags, limit)
	if err != nil {
		return nil, nil, fmt.Errorf("search facts: %w", err)
	}
	return jsonResult(map[string]any{"facts": toFactResponses(facts)})
}

func (ms *mcpServer) factList(ctx context.Context, req *mcp.CallToolRequest, in factListInput) (*mcp.CallToolResult, any, error) {
	if ms.store == nil {
		return nil, nil, fmt.Errorf("database not configured")
	}
	user, err := ms.resolveUser(ctx, req.Extra.TokenInfo.UserID)
	if err != nil {
		return nil, nil, err
	}
	project, err := ms.store.GetProjectBySlug(ctx, user.ID, in.Project)
	if errors.Is(err, store.ErrNotFound) {
		return nil, nil, fmt.Errorf("project %q not found", in.Project)
	}
	if err != nil {
		return nil, nil, fmt.Errorf("get project: %w", err)
	}
	limit := in.Limit
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	facts, err := ms.store.ListFacts(ctx, project.ID, in.Tag, limit)
	if err != nil {
		return nil, nil, fmt.Errorf("list facts: %w", err)
	}
	return jsonResult(map[string]any{"facts": toFactResponses(facts)})
}

func (ms *mcpServer) factDelete(ctx context.Context, req *mcp.CallToolRequest, in factDeleteInput) (*mcp.CallToolResult, any, error) {
	if err := requireScope(req, "facts:write"); err != nil {
		return nil, nil, err
	}
	if ms.store == nil {
		return nil, nil, fmt.Errorf("database not configured")
	}
	if _, err := ms.resolveUser(ctx, req.Extra.TokenInfo.UserID); err != nil {
		return nil, nil, err
	}
	factID, err := uuid.Parse(in.ID)
	if err != nil {
		return nil, nil, fmt.Errorf("invalid fact ID: %w", err)
	}
	err = ms.store.DeleteFact(ctx, factID)
	if errors.Is(err, store.ErrNotFound) {
		return nil, nil, fmt.Errorf("fact %q not found or already deleted", in.ID)
	}
	if err != nil {
		return nil, nil, fmt.Errorf("delete fact: %w", err)
	}
	return jsonResult(map[string]any{})
}

func requireScope(req *mcp.CallToolRequest, scope string) error {
	if !slices.Contains(req.Extra.TokenInfo.Scopes, scope) {
		return fmt.Errorf("token missing required scope %q", scope)
	}
	return nil
}

func (ms *mcpServer) resolveUser(ctx context.Context, userIDStr string) (*store.User, error) {
	githubID, err := strconv.ParseInt(userIDStr, 10, 64)
	if err != nil {
		return nil, fmt.Errorf("invalid user ID in token: %w", err)
	}
	user, err := ms.store.GetUserByGitHubID(ctx, githubID)
	if err != nil {
		return nil, fmt.Errorf("user not found — please re-authenticate: %w", err)
	}
	return user, nil
}

func toFactResponses(facts []*store.Fact) []factResponse {
	out := make([]factResponse, len(facts))
	for i, f := range facts {
		out[i] = factResponse{
			ID:        f.ID.String(),
			Key:       f.Key,
			Content:   f.Content,
			Tags:      f.Tags,
			UpdatedAt: f.UpdatedAt.Format(time.RFC3339),
		}
	}
	return out
}

func jsonResult(v any) (*mcp.CallToolResult, any, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return nil, nil, fmt.Errorf("marshal result: %w", err)
	}
	return &mcp.CallToolResult{
		Content: []mcp.Content{&mcp.TextContent{Text: string(b)}},
	}, nil, nil
}
