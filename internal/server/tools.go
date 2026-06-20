package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"time"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/google/uuid"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/wolfeidau/starlogz/internal/ctxlog"
	"github.com/wolfeidau/starlogz/internal/store"
)

type mcpServer struct {
	server *mcp.Server
	store  store.Store
}

func newMCPServer(st store.Store) *mcpServer {
	ms := &mcpServer{
		server: mcp.NewServer(&mcp.Implementation{Name: name}, &mcp.ServerOptions{}),
		store:  st,
	}
	mcp.AddTool(ms.server, &mcp.Tool{
		Name:        "whoami",
		Description: "Returns identity and token scopes. Call this first to verify access.",
	}, ms.whoami)
	mcp.AddTool(ms.server, &mcp.Tool{
		Name:        "project_ensure",
		Description: "Creates a project if it does not exist and returns it. Use when you need a custom display name; insight_write auto-creates projects.",
		InputSchema: projectEnsureSchema,
	}, ms.projectEnsure)
	mcp.AddTool(ms.server, &mcp.Tool{
		Name:        "insight_write",
		Description: "Writes an insight to a project. Auto-creates the project if needed. Supply a key to upsert by stable identifier. Requires category and source.",
		InputSchema: insightWriteSchema,
	}, ms.insightWrite)
	mcp.AddTool(ms.server, &mcp.Tool{
		Name:        "insight_search",
		Description: "Full-text search over live insights in a project. Returns results ordered by relevance.",
		InputSchema: insightSearchSchema,
	}, ms.insightSearch)
	mcp.AddTool(ms.server, &mcp.Tool{
		Name:        "insight_list",
		Description: "Lists all live insights in a project, newest first. Optionally filter by a single tag.",
		InputSchema: insightListSchema,
	}, ms.insightList)
	mcp.AddTool(ms.server, &mcp.Tool{
		Name:        "insight_delete",
		Description: "Soft-deletes an insight by ID. The insight no longer appears in list or search results.",
		InputSchema: insightDeleteSchema,
	}, ms.insightDelete)
	mcp.AddTool(ms.server, &mcp.Tool{
		Name:        "project_list",
		Description: "Lists all projects in the caller's personal org.",
	}, ms.projectList)
	mcp.AddTool(ms.server, &mcp.Tool{
		Name:        "insight_list_tags",
		Description: "Returns tags for a project ordered by usage frequency. Call before writing tags to avoid fragmentation.",
		InputSchema: insightListTagsSchema,
	}, ms.insightListTags)
	mcp.AddTool(ms.server, &mcp.Tool{
		Name:        "insight_update",
		Description: "Updates the content and/or tags of an existing insight. Supply only the fields you want to change.",
		InputSchema: insightUpdateSchema,
	}, ms.insightUpdate)
	return ms
}

func (s *mcpServer) logger(ctx context.Context) *slog.Logger {
	return ctxlog.LoggerFrom(ctx).With(slog.String("component", "mcp"))
}

func (ms *mcpServer) whoami(ctx context.Context, req *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, any, error) {
	ms.logger(ctx).InfoContext(ctx, "whoami call", slog.String("user_id", req.Extra.TokenInfo.UserID))
	userInfo := req.Extra.TokenInfo
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

type insightWriteInput struct {
	Project  string   `json:"project"`
	Content  string   `json:"content"`
	Key      string   `json:"key"`
	Tags     []string `json:"tags"`
	Category string   `json:"category"`
	Source   string   `json:"source"`
}

type insightSearchInput struct {
	Project string   `json:"project"`
	Query   string   `json:"query"`
	Tags    []string `json:"tags"`
	Limit   int      `json:"limit"`
}

type insightListInput struct {
	Project string `json:"project"`
	Tag     string `json:"tag"`
	Limit   int    `json:"limit"`
}

type insightDeleteInput struct {
	ID string `json:"id"`
}

type projectListInput struct{}

type insightListTagsInput struct {
	Project string `json:"project"`
	Limit   int    `json:"limit"`
}

type insightUpdateInput struct {
	ID      string   `json:"id"`
	Content string   `json:"content"`
	Tags    []string `json:"tags"`
}

func inputSchemaFor[T any]() *jsonschema.Schema {
	s, err := jsonschema.For[T](nil)
	if err != nil {
		panic(fmt.Errorf("infer input schema: %w", err))
	}
	return s
}

const projectSchemaProperty = "project"

var (
	projectEnsureSchema = func() *jsonschema.Schema {
		s := inputSchemaFor[projectEnsureInput]()
		s.Properties["slug"].MinLength = jsonschema.Ptr(1)
		s.Required = []string{"slug"}
		return s
	}()

	insightWriteSchema = func() *jsonschema.Schema {
		s := inputSchemaFor[insightWriteInput]()
		s.Properties[projectSchemaProperty].MinLength = jsonschema.Ptr(1)
		s.Properties["content"].MinLength = jsonschema.Ptr(1)
		s.Properties["category"].Enum = []any{"fact", "decision", "insight", "preference", "context", "general"}
		s.Properties["source"].Enum = []any{"user", "repo", "agent", "command"}
		s.Required = []string{projectSchemaProperty, "content", "category", "source"}
		return s
	}()

	insightSearchSchema = func() *jsonschema.Schema {
		s := inputSchemaFor[insightSearchInput]()
		s.Properties[projectSchemaProperty].MinLength = jsonschema.Ptr(1)
		s.Properties["query"].MinLength = jsonschema.Ptr(1)
		s.Properties["limit"].Minimum = jsonschema.Ptr(0.0)
		s.Properties["limit"].Maximum = jsonschema.Ptr(100.0)
		s.Required = []string{projectSchemaProperty, "query"}
		return s
	}()

	insightListSchema = func() *jsonschema.Schema {
		s := inputSchemaFor[insightListInput]()
		s.Properties[projectSchemaProperty].MinLength = jsonschema.Ptr(1)
		s.Properties["limit"].Minimum = jsonschema.Ptr(0.0)
		s.Properties["limit"].Maximum = jsonschema.Ptr(200.0)
		s.Required = []string{projectSchemaProperty}
		return s
	}()

	insightDeleteSchema = func() *jsonschema.Schema {
		s := inputSchemaFor[insightDeleteInput]()
		s.Properties["id"].MinLength = jsonschema.Ptr(1)
		s.Properties["id"].Format = "uuid"
		s.Required = []string{"id"}
		return s
	}()

	insightListTagsSchema = func() *jsonschema.Schema {
		s := inputSchemaFor[insightListTagsInput]()
		s.Properties[projectSchemaProperty].MinLength = jsonschema.Ptr(1)
		s.Properties["limit"].Minimum = jsonschema.Ptr(0.0)
		s.Properties["limit"].Maximum = jsonschema.Ptr(200.0)
		s.Required = []string{projectSchemaProperty}
		return s
	}()

	insightUpdateSchema = func() *jsonschema.Schema {
		s := inputSchemaFor[insightUpdateInput]()
		s.Properties["id"].MinLength = jsonschema.Ptr(1)
		s.Properties["id"].Format = "uuid"
		s.Required = []string{"id"}
		return s
	}()
)

func normaliseTags(tags []string) []string {
	out := make([]string, len(tags))
	for i, t := range tags {
		out[i] = strings.ToLower(t)
	}
	return out
}

type tagCountResponse struct {
	Name  string `json:"name"`
	Count int    `json:"count"`
}

type insightResponse struct {
	ID        string   `json:"id"`
	Key       string   `json:"key,omitempty"`
	Content   string   `json:"content"`
	Tags      []string `json:"tags"`
	Category  string   `json:"category"`
	Source    string   `json:"source"`
	UpdatedAt string   `json:"updated_at"`
}

func (ms *mcpServer) projectEnsure(ctx context.Context, req *mcp.CallToolRequest, in projectEnsureInput) (*mcp.CallToolResult, any, error) {
	ms.logger(ctx).InfoContext(ctx, "project_ensure call", slog.String("user_id", req.Extra.TokenInfo.UserID))
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

func (ms *mcpServer) insightWrite(ctx context.Context, req *mcp.CallToolRequest, in insightWriteInput) (*mcp.CallToolResult, any, error) {
	ms.logger(ctx).InfoContext(ctx, "insight_write call", slog.String("user_id", req.Extra.TokenInfo.UserID))
	if err := requireScope(req, "insights:write"); err != nil {
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
	tags := normaliseTags(in.Tags)
	insight, err := ms.store.WriteInsight(ctx, store.WriteInsightParams{
		ProjectID: project.ID,
		Key:       in.Key,
		Content:   in.Content,
		Tags:      tags,
		Category:  in.Category,
		Source:    in.Source,
		CreatedBy: user.ID,
	})
	if err != nil {
		return nil, nil, fmt.Errorf("write insight: %w", err)
	}
	return jsonResult(map[string]any{
		"id":      insight.ID.String(),
		"updated": !insight.CreatedAt.Equal(insight.UpdatedAt),
	})
}

func (ms *mcpServer) insightSearch(ctx context.Context, req *mcp.CallToolRequest, in insightSearchInput) (*mcp.CallToolResult, any, error) {
	ms.logger(ctx).InfoContext(ctx, "insight_search call", slog.String("user_id", req.Extra.TokenInfo.UserID))
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
	insights, err := ms.store.SearchInsights(ctx, project.ID, in.Query, in.Tags, limit)
	if err != nil {
		return nil, nil, fmt.Errorf("search insights: %w", err)
	}
	return jsonResult(map[string]any{"insights": toInsightResponses(insights)})
}

func (ms *mcpServer) insightList(ctx context.Context, req *mcp.CallToolRequest, in insightListInput) (*mcp.CallToolResult, any, error) {
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
	insights, err := ms.store.ListInsights(ctx, project.ID, in.Tag, limit)
	if err != nil {
		return nil, nil, fmt.Errorf("list insights: %w", err)
	}
	return jsonResult(map[string]any{"insights": toInsightResponses(insights)})
}

func (ms *mcpServer) insightDelete(ctx context.Context, req *mcp.CallToolRequest, in insightDeleteInput) (*mcp.CallToolResult, any, error) {
	ms.logger(ctx).InfoContext(ctx, "insight_delete call", slog.String("user_id", req.Extra.TokenInfo.UserID))
	if err := requireScope(req, "insights:write"); err != nil {
		return nil, nil, err
	}
	if ms.store == nil {
		return nil, nil, fmt.Errorf("database not configured")
	}
	_, org, err := ms.resolveUserAndOrg(ctx, req.Extra.TokenInfo.UserID)
	if err != nil {
		return nil, nil, err
	}
	insightID, err := uuid.Parse(in.ID)
	if err != nil {
		return nil, nil, fmt.Errorf("invalid insight ID: %w", err)
	}
	err = ms.store.DeleteInsight(ctx, org.ID, insightID)
	if errors.Is(err, store.ErrNotFound) {
		return nil, nil, fmt.Errorf("insight %q not found or already deleted", in.ID)
	}
	if err != nil {
		return nil, nil, fmt.Errorf("delete insight: %w", err)
	}
	return jsonResult(map[string]any{})
}

func (ms *mcpServer) projectList(ctx context.Context, req *mcp.CallToolRequest, _ projectListInput) (*mcp.CallToolResult, any, error) {
	ms.logger(ctx).InfoContext(ctx, "project_list call", slog.String("user_id", req.Extra.TokenInfo.UserID))
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

func (ms *mcpServer) insightListTags(ctx context.Context, req *mcp.CallToolRequest, in insightListTagsInput) (*mcp.CallToolResult, any, error) {
	ms.logger(ctx).InfoContext(ctx, "insight_list_tags call", slog.String("user_id", req.Extra.TokenInfo.UserID))
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

func (ms *mcpServer) insightUpdate(ctx context.Context, req *mcp.CallToolRequest, in insightUpdateInput) (*mcp.CallToolResult, any, error) {
	ms.logger(ctx).InfoContext(ctx, "insight_update call", slog.String("user_id", req.Extra.TokenInfo.UserID))
	if err := requireScope(req, "insights:write"); err != nil {
		return nil, nil, err
	}
	if ms.store == nil {
		return nil, nil, fmt.Errorf("database not configured")
	}
	_, org, err := ms.resolveUserAndOrg(ctx, req.Extra.TokenInfo.UserID)
	if err != nil {
		return nil, nil, err
	}
	insightID, err := uuid.Parse(in.ID)
	if err != nil {
		return nil, nil, fmt.Errorf("invalid insight ID: %w", err)
	}
	var tags []string
	if in.Tags != nil {
		tags = normaliseTags(in.Tags)
	}
	insight, err := ms.store.UpdateInsight(ctx, store.UpdateInsightParams{
		OrgID:     org.ID,
		InsightID: insightID,
		Content:   in.Content,
		Tags:      tags,
	})
	if errors.Is(err, store.ErrNotFound) {
		return nil, nil, fmt.Errorf("insight %q not found or already deleted", in.ID)
	}
	if err != nil {
		return nil, nil, fmt.Errorf("update insight: %w", err)
	}
	return jsonResult(insightResponse{
		ID:        insight.ID.String(),
		Key:       insight.Key,
		Content:   insight.Content,
		Tags:      insight.Tags,
		Category:  insight.Category,
		Source:    insight.Source,
		UpdatedAt: insight.UpdatedAt.Format(time.RFC3339),
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
	ms.logger(ctx).InfoContext(ctx, "resolveUserAndOrg call", slog.String("user_id", userIDStr))
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

func toInsightResponses(insights []*store.Insight) []insightResponse {
	out := make([]insightResponse, len(insights))
	for i, f := range insights {
		out[i] = insightResponse{
			ID:        f.ID.String(),
			Key:       f.Key,
			Content:   f.Content,
			Tags:      f.Tags,
			Category:  f.Category,
			Source:    f.Source,
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
