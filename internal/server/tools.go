package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"time"

	"github.com/google/uuid"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/wolfeidau/starlogz/internal/store"
)

type mcpServer struct {
	logger *slog.Logger
	server *mcp.Server
	store  store.Store
}

func newMCPServer(logger *slog.Logger, st store.Store) *mcpServer {
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
	mcp.AddTool(ms.server, &mcp.Tool{
		Name:        "project_list",
		Description: "Lists all projects in the caller's personal org.",
	}, ms.projectList)
	mcp.AddTool(ms.server, &mcp.Tool{
		Name:        "fact_list_tags",
		Description: "Returns tags for a project ordered by usage frequency. Call before writing tags to avoid fragmentation.",
	}, ms.factListTags)
	mcp.AddTool(ms.server, &mcp.Tool{
		Name:        "fact_update",
		Description: "Updates the content and/or tags of an existing fact. Supply only the fields you want to change.",
	}, ms.factUpdate)
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

type projectListInput struct{}

type factListTagsInput struct {
	Project string `json:"project"`
	Limit   int    `json:"limit"`
}

type factUpdateInput struct {
	ID      string   `json:"id"`
	Content string   `json:"content"`
	Tags    []string `json:"tags"`
}

type tagCountResponse struct {
	Name  string `json:"name"`
	Count int    `json:"count"`
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
	user, org, err := ms.resolveUserAndOrg(ctx, req.Extra.TokenInfo.UserID)
	if err != nil {
		return nil, nil, err
	}
	name := in.Name
	if name == "" {
		name = in.Slug
	}
	project, err := ms.store.EnsureProject(ctx, org.ID, user.ID, in.Slug, name)
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
	user, org, err := ms.resolveUserAndOrg(ctx, req.Extra.TokenInfo.UserID)
	if err != nil {
		return nil, nil, err
	}
	project, err := ms.store.EnsureProject(ctx, org.ID, user.ID, in.Project, in.Project)
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
	_, org, err := ms.resolveUserAndOrg(ctx, req.Extra.TokenInfo.UserID)
	if err != nil {
		return nil, nil, err
	}
	project, err := ms.store.GetProjectBySlug(ctx, org.ID, in.Project)
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
	_, org, err := ms.resolveUserAndOrg(ctx, req.Extra.TokenInfo.UserID)
	if err != nil {
		return nil, nil, err
	}
	project, err := ms.store.GetProjectBySlug(ctx, org.ID, in.Project)
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
	if _, _, err := ms.resolveUserAndOrg(ctx, req.Extra.TokenInfo.UserID); err != nil {
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

func (ms *mcpServer) projectList(ctx context.Context, req *mcp.CallToolRequest, _ projectListInput) (*mcp.CallToolResult, any, error) {
	if ms.store == nil {
		return nil, nil, fmt.Errorf("database not configured")
	}
	_, org, err := ms.resolveUserAndOrg(ctx, req.Extra.TokenInfo.UserID)
	if err != nil {
		return nil, nil, err
	}
	projects, err := ms.store.ListProjects(ctx, org.ID)
	if err != nil {
		return nil, nil, fmt.Errorf("list projects: %w", err)
	}
	type projectResp struct {
		ID        string `json:"id"`
		Slug      string `json:"slug"`
		Name      string `json:"name"`
		CreatedAt string `json:"created_at"`
	}
	out := make([]projectResp, len(projects))
	for i, p := range projects {
		out[i] = projectResp{
			ID:        p.ID.String(),
			Slug:      p.Slug,
			Name:      p.Name,
			CreatedAt: p.CreatedAt.Format(time.RFC3339),
		}
	}
	return jsonResult(map[string]any{"projects": out})
}

func (ms *mcpServer) factListTags(ctx context.Context, req *mcp.CallToolRequest, in factListTagsInput) (*mcp.CallToolResult, any, error) {
	if ms.store == nil {
		return nil, nil, fmt.Errorf("database not configured")
	}
	_, org, err := ms.resolveUserAndOrg(ctx, req.Extra.TokenInfo.UserID)
	if err != nil {
		return nil, nil, err
	}
	project, err := ms.store.GetProjectBySlug(ctx, org.ID, in.Project)
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
	tags, err := ms.store.ListTags(ctx, project.ID, limit)
	if err != nil {
		return nil, nil, fmt.Errorf("list tags: %w", err)
	}
	out := make([]tagCountResponse, len(tags))
	for i, t := range tags {
		out[i] = tagCountResponse{Name: t.Name, Count: t.Count}
	}
	return jsonResult(map[string]any{"tags": out})
}

func (ms *mcpServer) factUpdate(ctx context.Context, req *mcp.CallToolRequest, in factUpdateInput) (*mcp.CallToolResult, any, error) {
	if err := requireScope(req, "facts:write"); err != nil {
		return nil, nil, err
	}
	if ms.store == nil {
		return nil, nil, fmt.Errorf("database not configured")
	}
	if _, _, err := ms.resolveUserAndOrg(ctx, req.Extra.TokenInfo.UserID); err != nil {
		return nil, nil, err
	}
	factID, err := uuid.Parse(in.ID)
	if err != nil {
		return nil, nil, fmt.Errorf("invalid fact ID: %w", err)
	}
	fact, err := ms.store.UpdateFact(ctx, store.UpdateFactParams{
		FactID:  factID,
		Content: in.Content,
		Tags:    in.Tags,
	})
	if errors.Is(err, store.ErrNotFound) {
		return nil, nil, fmt.Errorf("fact %q not found or already deleted", in.ID)
	}
	if err != nil {
		return nil, nil, fmt.Errorf("update fact: %w", err)
	}
	return jsonResult(factResponse{
		ID:        fact.ID.String(),
		Key:       fact.Key,
		Content:   fact.Content,
		Tags:      fact.Tags,
		UpdatedAt: fact.UpdatedAt.Format(time.RFC3339),
	})
}

func requireScope(req *mcp.CallToolRequest, scope string) error {
	if !slices.Contains(req.Extra.TokenInfo.Scopes, scope) {
		return fmt.Errorf("token missing required scope %q", scope)
	}
	return nil
}

// resolveUserAndOrg looks up the user by UUID (from JWT sub) and their personal org.
func (ms *mcpServer) resolveUserAndOrg(ctx context.Context, userIDStr string) (*store.User, *store.Org, error) {
	userID, err := uuid.Parse(userIDStr)
	if err != nil {
		return nil, nil, fmt.Errorf("invalid user ID in token: %w", err)
	}
	user, err := ms.store.GetUserByID(ctx, userID)
	if err != nil {
		return nil, nil, fmt.Errorf("user not found — please re-authenticate: %w", err)
	}
	org, err := ms.store.GetPersonalOrgByUserID(ctx, user.ID)
	if err != nil {
		return nil, nil, fmt.Errorf("personal org not found — please re-authenticate: %w", err)
	}
	return user, org, nil
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
