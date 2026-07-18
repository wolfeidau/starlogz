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
	"github.com/wolfeidau/starlogz/internal/wideevent"
)

type mcpServer struct {
	server *mcp.Server
	store  store.Store
	events *wideevent.Emitter
}

type toolEventMetadata struct {
	resultCount int
}

func newMCPServer(st store.Store, eventEmitter *wideevent.Emitter) *mcpServer {
	if eventEmitter == nil {
		eventEmitter = wideevent.NewNoopEmitter()
	}
	ms := &mcpServer{
		server: mcp.NewServer(&mcp.Implementation{Name: name}, &mcp.ServerOptions{}),
		store:  st,
		events: eventEmitter,
	}
	mcp.AddTool(ms.server, &mcp.Tool{
		Name:        "whoami",
		Description: "Returns identity and token scopes. Call this first to verify access.",
	}, trackTool(ms, wideevent.ToolWhoami, ms.whoami))
	mcp.AddTool(ms.server, &mcp.Tool{
		Name:        "project_ensure",
		Description: "Creates a project if it does not exist and returns it. Use when you need a custom display name; insight_write auto-creates projects.",
		InputSchema: projectEnsureSchema,
	}, trackTool(ms, wideevent.ToolProjectEnsure, ms.projectEnsure))
	mcp.AddTool(ms.server, &mcp.Tool{
		Name:        "insight_write",
		Description: "Writes an insight to a project. Auto-creates the project if needed. Supply a key to upsert by stable identifier and expected_revision for optimistic concurrency. Requires category and source.",
		InputSchema: insightWriteSchema,
	}, trackTool(ms, wideevent.ToolInsightWrite, ms.insightWrite))
	mcp.AddTool(ms.server, &mcp.Tool{
		Name:        "insight_get",
		Description: "Retrieves one insight by ID or key with bounded outgoing links and backlinks.",
		InputSchema: insightGetSchema,
	}, trackTool(ms, wideevent.ToolInsightGet, ms.insightGet))
	mcp.AddTool(ms.server, &mcp.Tool{
		Name:        "insight_history",
		Description: "Lists immutable revisions for an insight, newest first, with opaque cursor continuation. Includes soft-deleted insights.",
		InputSchema: insightHistorySchema,
	}, trackTool(ms, wideevent.ToolInsightHistory, ms.insightHistory))
	mcp.AddTool(ms.server, &mcp.Tool{
		Name:        "insight_restore",
		Description: "Restores an insight from an immutable revision as a new live revision. Requires optimistic concurrency.",
		InputSchema: insightRestoreSchema,
	}, trackTool(ms, wideevent.ToolInsightRestore, ms.insightRestore))
	mcp.AddTool(ms.server, &mcp.Tool{
		Name:        "insight_search",
		Description: "Full-text search over live insights in a project. query_mode=all requires every term. query_mode=web supports explicit OR, quoted phrases, and -excluded terms. tag_mode controls whether all or any supplied tags must match. Returns results ordered by relevance and supports opaque cursor continuation.",
		InputSchema: insightSearchSchema,
	}, trackTool(ms, wideevent.ToolInsightSearch, ms.insightSearch))
	mcp.AddTool(ms.server, &mcp.Tool{
		Name:        "insight_list",
		Description: "Lists live insights in a project, newest first. Optionally filter by a single tag or continue with an opaque cursor.",
		InputSchema: insightListSchema,
	}, trackTool(ms, wideevent.ToolInsightList, ms.insightList))
	mcp.AddTool(ms.server, &mcp.Tool{
		Name:        "insight_delete",
		Description: "Soft-deletes an insight by ID. Supply expected_revision for optimistic concurrency. The insight no longer appears in list or search results.",
		InputSchema: insightDeleteSchema,
	}, trackTool(ms, wideevent.ToolInsightDelete, ms.insightDelete))
	mcp.AddTool(ms.server, &mcp.Tool{
		Name:        "project_list",
		Description: "Lists all projects in the caller's personal org.",
	}, trackTool(ms, wideevent.ToolProjectList, ms.projectList))
	mcp.AddTool(ms.server, &mcp.Tool{
		Name:        "insight_list_tags",
		Description: "Returns tags for a project ordered by usage frequency. Call before writing tags to avoid fragmentation.",
		InputSchema: insightListTagsSchema,
	}, trackTool(ms, wideevent.ToolInsightListTags, ms.insightListTags))
	mcp.AddTool(ms.server, &mcp.Tool{
		Name:        "insight_update",
		Description: "Updates the content and/or tags of an existing insight. Supply only the fields you want to change and expected_revision for optimistic concurrency.",
		InputSchema: insightUpdateSchema,
	}, trackTool(ms, wideevent.ToolInsightUpdate, ms.insightUpdate))
	return ms
}

func trackTool[Input any](ms *mcpServer, tool string, handler func(context.Context, *mcp.CallToolRequest, Input) (*mcp.CallToolResult, any, error)) func(context.Context, *mcp.CallToolRequest, Input) (*mcp.CallToolResult, any, error) {
	return func(ctx context.Context, req *mcp.CallToolRequest, input Input) (*mcp.CallToolResult, any, error) {
		started := time.Now()
		result, output, err := handler(ctx, req, input)
		outcome, reason := wideevent.OutcomeSuccess, wideevent.ReasonCompleted
		attributes := map[string]string{wideevent.AttributeTool: tool}
		if err != nil || result != nil && result.IsError {
			outcome, reason = wideevent.OutcomeFailure, wideevent.ReasonFailed
		} else if metadata, ok := output.(toolEventMetadata); ok {
			attributes[wideevent.AttributeResultCountBucket] = resultCountBucket(metadata.resultCount)
			output = nil
		}
		ms.events.Completion(ctx, wideevent.MCPToolCallCompleted, outcome, reason, started, attributes)
		return result, output, err
	}
}

func resultCountBucket(count int) string {
	switch {
	case count == 0:
		return wideevent.ResultCountZero
	case count <= 10:
		return wideevent.ResultCountOneToTen
	case count <= 50:
		return wideevent.ResultCountElevenTo50
	case count <= 100:
		return wideevent.ResultCount51To100
	default:
		return wideevent.ResultCount101To200
	}
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
	Project          string   `json:"project"`
	Content          string   `json:"content"`
	Key              string   `json:"key"`
	Tags             []string `json:"tags"`
	Category         string   `json:"category"`
	Source           string   `json:"source"`
	ExpectedRevision *int     `json:"expected_revision"`
}

type insightSearchInput struct {
	Project   string   `json:"project"`
	Query     string   `json:"query"`
	QueryMode string   `json:"query_mode"`
	Tags      []string `json:"tags"`
	TagMode   string   `json:"tag_mode"`
	Limit     int      `json:"limit"`
	Cursor    string   `json:"cursor"`
}

type insightGetInput struct {
	Project       string `json:"project"`
	ID            string `json:"id"`
	Key           string `json:"key"`
	RelationLimit int    `json:"relation_limit"`
}

type insightHistoryInput struct {
	Project string `json:"project"`
	ID      string `json:"id"`
	Limit   int    `json:"limit"`
	Cursor  string `json:"cursor"`
}

type insightRestoreInput struct {
	Project          string `json:"project"`
	ID               string `json:"id"`
	TargetRevision   int    `json:"target_revision"`
	ExpectedRevision int    `json:"expected_revision"`
}

type insightListInput struct {
	Project string `json:"project"`
	Tag     string `json:"tag"`
	Limit   int    `json:"limit"`
	Cursor  string `json:"cursor"`
}

type insightDeleteInput struct {
	ID               string `json:"id"`
	ExpectedRevision *int   `json:"expected_revision"`
}

type projectListInput struct{}

type insightListTagsInput struct {
	Project string `json:"project"`
	Limit   int    `json:"limit"`
}

type insightUpdateInput struct {
	ID               string   `json:"id"`
	Content          string   `json:"content"`
	Tags             []string `json:"tags"`
	ExpectedRevision *int     `json:"expected_revision"`
}

func inputSchemaFor[T any]() *jsonschema.Schema {
	s, err := jsonschema.For[T](nil)
	if err != nil {
		panic(fmt.Errorf("infer input schema: %w", err))
	}
	return s
}

const (
	projectSchemaProperty  = "project"
	revisionResultProperty = "revision"
	toolErrorCodeProperty  = "code"
	uuidSchemaFormat       = "uuid"
)

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
		s.Properties["expected_revision"].Minimum = jsonschema.Ptr(0.0)
		s.Properties["expected_revision"].Maximum = jsonschema.Ptr(float64(store.MaxInsightRevision))
		s.Required = []string{projectSchemaProperty, "content", "category", "source"}
		return s
	}()

	insightGetSchema = func() *jsonschema.Schema {
		s := inputSchemaFor[insightGetInput]()
		s.Properties[projectSchemaProperty].MinLength = jsonschema.Ptr(1)
		s.Properties["id"].MinLength = jsonschema.Ptr(1)
		s.Properties["id"].Format = uuidSchemaFormat
		s.Properties["key"].MinLength = jsonschema.Ptr(1)
		s.Properties["relation_limit"].Minimum = jsonschema.Ptr(1.0)
		s.Properties["relation_limit"].Maximum = jsonschema.Ptr(100.0)
		s.Required = []string{projectSchemaProperty}
		s.OneOf = []*jsonschema.Schema{
			{Required: []string{"id"}, Not: &jsonschema.Schema{Required: []string{"key"}}},
			{Required: []string{"key"}, Not: &jsonschema.Schema{Required: []string{"id"}}},
		}
		return s
	}()

	insightHistorySchema = func() *jsonschema.Schema {
		s := inputSchemaFor[insightHistoryInput]()
		s.Properties[projectSchemaProperty].MinLength = jsonschema.Ptr(1)
		s.Properties["id"].MinLength = jsonschema.Ptr(1)
		s.Properties["id"].Format = uuidSchemaFormat
		s.Properties["limit"].Minimum = jsonschema.Ptr(0.0)
		s.Properties["limit"].Maximum = jsonschema.Ptr(100.0)
		s.Required = []string{projectSchemaProperty, "id"}
		return s
	}()

	insightRestoreSchema = func() *jsonschema.Schema {
		s := inputSchemaFor[insightRestoreInput]()
		s.Properties[projectSchemaProperty].MinLength = jsonschema.Ptr(1)
		s.Properties["id"].MinLength = jsonschema.Ptr(1)
		s.Properties["id"].Format = uuidSchemaFormat
		s.Properties["target_revision"].Minimum = jsonschema.Ptr(1.0)
		s.Properties["target_revision"].Maximum = jsonschema.Ptr(float64(store.MaxInsightRevision))
		s.Properties["expected_revision"].Minimum = jsonschema.Ptr(1.0)
		s.Properties["expected_revision"].Maximum = jsonschema.Ptr(float64(store.MaxInsightRevision))
		s.Required = []string{projectSchemaProperty, "id", "target_revision", "expected_revision"}
		return s
	}()

	// Cursor bounds remain in the decoder so MCP reports the stable invalid_cursor code.
	insightSearchSchema = func() *jsonschema.Schema {
		s := inputSchemaFor[insightSearchInput]()
		s.Properties[projectSchemaProperty].MinLength = jsonschema.Ptr(1)
		s.Properties["query"].MinLength = jsonschema.Ptr(1)
		s.Properties["query_mode"].Enum = []any{"all", "web"}
		s.Properties["tag_mode"].Enum = []any{"all", "any"}
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
		s.Properties["id"].Format = uuidSchemaFormat
		s.Properties["expected_revision"].Minimum = jsonschema.Ptr(1.0)
		s.Properties["expected_revision"].Maximum = jsonschema.Ptr(float64(store.MaxInsightRevision))
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
		s.Properties["id"].Format = uuidSchemaFormat
		s.Properties["expected_revision"].Minimum = jsonschema.Ptr(1.0)
		s.Properties["expected_revision"].Maximum = jsonschema.Ptr(float64(store.MaxInsightRevision))
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
	ID           string                        `json:"id"`
	Key          string                        `json:"key,omitempty"`
	Content      string                        `json:"content"`
	Tags         []string                      `json:"tags"`
	Category     string                        `json:"category"`
	Source       string                        `json:"source"`
	UpdatedAt    string                        `json:"updated_at"`
	Revision     int                           `json:"revision"`
	LinkWarnings *[]insightLinkWarningResponse `json:"warnings,omitempty"`
}

type insightLinkWarningResponse struct {
	Code      string `json:"code"`
	TargetKey string `json:"target_key"`
}

type insightRevisionResponse struct {
	Revision  int      `json:"revision"`
	Operation string   `json:"operation"`
	Key       string   `json:"key,omitempty"`
	Content   string   `json:"content"`
	Tags      []string `json:"tags"`
	Category  string   `json:"category"`
	Source    string   `json:"source"`
	DeletedAt string   `json:"deleted_at,omitempty"`
	ChangedBy string   `json:"changed_by,omitempty"`
	ChangedAt string   `json:"changed_at"`
}

type insightHistoryResponse struct {
	InsightID       string                    `json:"insight_id"`
	Key             string                    `json:"key,omitempty"`
	CurrentRevision int                       `json:"current_revision"`
	Deleted         bool                      `json:"deleted"`
	Revisions       []insightRevisionResponse `json:"revisions"`
	NextCursor      string                    `json:"next_cursor,omitempty"`
}

type insightLinkResponse struct {
	TargetKey string `json:"target_key"`
	Resolved  bool   `json:"resolved"`
	ID        string `json:"id,omitempty"`
	Category  string `json:"category,omitempty"`
	UpdatedAt string `json:"updated_at,omitempty"`
}

type insightBacklinkResponse struct {
	ID        string `json:"id"`
	Key       string `json:"key,omitempty"`
	Category  string `json:"category"`
	UpdatedAt string `json:"updated_at"`
}

type insightGetResponse struct {
	Insight            insightResponse           `json:"insight"`
	Links              []insightLinkResponse     `json:"links"`
	Backlinks          []insightBacklinkResponse `json:"backlinks"`
	LinkCount          int                       `json:"link_count"`
	BacklinkCount      int                       `json:"backlink_count"`
	LinksTruncated     bool                      `json:"links_truncated"`
	BacklinksTruncated bool                      `json:"backlinks_truncated"`
}

func toInsightLinkWarningResponses(warnings []store.InsightLinkWarning) []insightLinkWarningResponse {
	out := make([]insightLinkWarningResponse, len(warnings))
	for i, warning := range warnings {
		out[i] = insightLinkWarningResponse{Code: warning.Code, TargetKey: warning.TargetKey}
	}
	return out
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
	if in.Key == "" && in.ExpectedRevision != nil && *in.ExpectedRevision > 0 {
		return nil, nil, store.ErrInvalidExpectedRevision
	}
	user, org, err := ms.resolveUserAndOrg(ctx, req.Extra.TokenInfo.UserID)
	if err != nil {
		return nil, nil, err
	}
	var project *store.Project
	if in.ExpectedRevision != nil && *in.ExpectedRevision > 0 {
		project, err = ms.store.GetProjectBySlug(ctx, org.ID, in.Project)
		if errors.Is(err, store.ErrNotFound) {
			result, _, resultErr := revisionConflictResult(&store.RevisionConflictError{
				Expected: *in.ExpectedRevision,
				Current:  0,
			})
			return result, nil, resultErr
		}
	} else {
		project, err = ms.store.EnsureProject(ctx, org.ID, user.ID, in.Project, in.Project)
	}
	if err != nil {
		return nil, nil, fmt.Errorf("resolve project: %w", err)
	}
	tags := normaliseTags(in.Tags)
	insight, err := ms.store.WriteInsight(ctx, store.WriteInsightParams{
		ProjectID:        project.ID,
		Key:              in.Key,
		Content:          in.Content,
		Tags:             tags,
		Category:         in.Category,
		Source:           in.Source,
		CreatedBy:        user.ID,
		ExpectedRevision: in.ExpectedRevision,
	})
	if result, ok, resultErr := revisionConflictResult(err); ok {
		return result, nil, resultErr
	}
	if err != nil {
		return nil, nil, fmt.Errorf("write insight: %w", err)
	}
	warnings := toInsightLinkWarningResponses(insight.LinkWarnings)
	return jsonResult(map[string]any{
		"id":                   insight.ID.String(),
		"updated":              !insight.CreatedAt.Equal(insight.UpdatedAt),
		revisionResultProperty: insight.Revision,
		"warnings":             warnings,
	})
}

func (ms *mcpServer) insightGet(ctx context.Context, req *mcp.CallToolRequest, in insightGetInput) (*mcp.CallToolResult, any, error) {
	ms.logger(ctx).InfoContext(ctx, "insight_get call", slog.String("user_id", req.Extra.TokenInfo.UserID))
	if ms.store == nil {
		return nil, nil, fmt.Errorf("database not configured")
	}
	if (in.ID == "") == (in.Key == "") {
		return nil, nil, fmt.Errorf("exactly one of id or key is required")
	}
	relationLimit := in.RelationLimit
	if relationLimit == 0 {
		relationLimit = 50
	}
	if relationLimit < 1 || relationLimit > 100 {
		return nil, nil, fmt.Errorf("relation_limit must be between 1 and 100")
	}
	params := store.GetInsightParams{Key: in.Key, RelationLimit: relationLimit}
	var err error
	if in.ID != "" {
		params.InsightID, err = uuid.Parse(in.ID)
		if err != nil {
			return nil, nil, fmt.Errorf("invalid insight ID: %w", err)
		}
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
	params.ProjectID = project.ID
	detail, err := ms.store.GetInsight(ctx, params)
	if errors.Is(err, store.ErrNotFound) {
		return nil, nil, fmt.Errorf("insight not found")
	}
	if err != nil {
		return nil, nil, fmt.Errorf("get insight: %w", err)
	}
	return jsonResult(toInsightGetResponse(detail))
}

func toInsightGetResponse(detail *store.InsightDetail) insightGetResponse {
	insight := detail.Insight
	links := make([]insightLinkResponse, len(detail.Links))
	for i, link := range detail.Links {
		links[i] = insightLinkResponse{TargetKey: link.TargetKey, Resolved: link.Resolved}
		if link.Resolved {
			links[i].ID = link.ID.String()
			links[i].Category = link.Category
			links[i].UpdatedAt = link.UpdatedAt.Format(time.RFC3339)
		}
	}
	backlinks := make([]insightBacklinkResponse, len(detail.Backlinks))
	for i, backlink := range detail.Backlinks {
		backlinks[i] = insightBacklinkResponse{
			ID: backlink.ID.String(), Key: backlink.Key, Category: backlink.Category,
			UpdatedAt: backlink.UpdatedAt.Format(time.RFC3339),
		}
	}
	return insightGetResponse{
		Insight: insightResponse{
			ID: insight.ID.String(), Key: insight.Key, Content: insight.Content, Tags: insight.Tags,
			Category: insight.Category, Source: insight.Source, UpdatedAt: insight.UpdatedAt.Format(time.RFC3339),
			Revision: insight.Revision,
		},
		Links: links, Backlinks: backlinks, LinkCount: detail.LinkCount, BacklinkCount: detail.BacklinkCount,
		LinksTruncated: detail.LinksTruncated, BacklinksTruncated: detail.BacklinksTruncated,
	}
}

func (ms *mcpServer) insightHistory(ctx context.Context, req *mcp.CallToolRequest, in insightHistoryInput) (*mcp.CallToolResult, any, error) {
	ms.logger(ctx).InfoContext(ctx, "insight_history call", slog.String("user_id", req.Extra.TokenInfo.UserID))
	if ms.store == nil {
		return nil, nil, fmt.Errorf("database not configured")
	}
	insightID, err := uuid.Parse(in.ID)
	if err != nil {
		return nil, nil, fmt.Errorf("invalid insight ID: %w", err)
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
	after, err := decodeInsightHistoryCursor(in.Cursor, project.ID, insightID)
	if err != nil {
		return nil, nil, errInvalidCursor
	}
	page, err := ms.store.ListInsightHistory(ctx, store.ListInsightHistoryParams{
		ProjectID: project.ID, InsightID: insightID, Limit: limit, After: after,
	})
	if errors.Is(err, store.ErrNotFound) {
		return nil, nil, fmt.Errorf("insight not found")
	}
	if err != nil {
		return nil, nil, fmt.Errorf("list insight history: %w", err)
	}
	response := toInsightHistoryResponse(page)
	if page.NextCursor != nil {
		response.NextCursor, err = encodeInsightHistoryCursor(project.ID, insightID, page.NextCursor)
		if err != nil {
			return nil, nil, fmt.Errorf("encode next cursor: %w", err)
		}
	}
	result, _, err := jsonResult(response)
	return result, toolEventMetadata{resultCount: len(page.Revisions)}, err
}

func (ms *mcpServer) insightRestore(ctx context.Context, req *mcp.CallToolRequest, in insightRestoreInput) (*mcp.CallToolResult, any, error) {
	ms.logger(ctx).InfoContext(ctx, "insight_restore call", slog.String("user_id", req.Extra.TokenInfo.UserID))
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
	insightID, err := uuid.Parse(in.ID)
	if err != nil {
		return nil, nil, fmt.Errorf("invalid insight ID: %w", err)
	}
	project, err := ms.store.GetProjectBySlug(ctx, org.ID, in.Project)
	if errors.Is(err, store.ErrNotFound) {
		return nil, nil, fmt.Errorf("project %q not found", in.Project)
	}
	if err != nil {
		return nil, nil, fmt.Errorf("get project: %w", err)
	}
	insight, err := ms.store.RestoreInsight(ctx, store.RestoreInsightParams{
		ProjectID: project.ID, InsightID: insightID, TargetRevision: in.TargetRevision,
		ExpectedRevision: in.ExpectedRevision, ChangedBy: user.ID,
	})
	if result, ok, resultErr := revisionConflictResult(err); ok {
		return result, nil, resultErr
	}
	if errors.Is(err, store.ErrInsightKeyConflict) {
		result, resultErr := codedToolErrorResult(map[string]any{toolErrorCodeProperty: "key_conflict"})
		return result, nil, resultErr
	}
	if errors.Is(err, store.ErrInsightRevisionNotFound) {
		result, resultErr := codedToolErrorResult(map[string]any{
			toolErrorCodeProperty: "revision_not_found", "target_revision": in.TargetRevision,
		})
		return result, nil, resultErr
	}
	if errors.Is(err, store.ErrNotFound) {
		return nil, nil, fmt.Errorf("insight not found")
	}
	if err != nil {
		return nil, nil, fmt.Errorf("restore insight: %w", err)
	}
	warnings := toInsightLinkWarningResponses(insight.LinkWarnings)
	return jsonResult(map[string]any{
		"id": insight.ID.String(), revisionResultProperty: insight.Revision, "warnings": warnings,
	})
}

func toInsightHistoryResponse(page *store.InsightHistoryPage) insightHistoryResponse {
	revisions := make([]insightRevisionResponse, len(page.Revisions))
	for i, revision := range page.Revisions {
		revisions[i] = insightRevisionResponse{
			Revision: revision.Revision, Operation: revision.Operation, Key: revision.Key,
			Content: revision.Content, Tags: revision.Tags, Category: revision.Category,
			Source: revision.Source, ChangedAt: revision.ChangedAt.Format(time.RFC3339Nano),
		}
		if revision.DeletedAt != nil {
			revisions[i].DeletedAt = revision.DeletedAt.Format(time.RFC3339Nano)
		}
		if revision.ChangedBy != nil {
			revisions[i].ChangedBy = revision.ChangedBy.String()
		}
	}
	return insightHistoryResponse{
		InsightID: page.InsightID.String(), Key: page.Key, CurrentRevision: page.CurrentRevision,
		Deleted: page.DeletedAt != nil, Revisions: revisions,
	}
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
	queryMode := store.SearchQueryMode(in.QueryMode)
	if queryMode == "" {
		queryMode = store.SearchQueryModeAll
	}
	tagMode := store.SearchTagMode(in.TagMode)
	if tagMode == "" {
		tagMode = store.SearchTagModeAll
	}
	query := in.Query
	tags := canonicalSearchTags(normaliseTags(in.Tags))
	after, err := decodeInsightSearchCursor(in.Cursor, project.ID, query, queryMode, tags, tagMode)
	if err != nil {
		return nil, nil, errInvalidCursor
	}
	page, err := ms.store.SearchInsights(ctx, store.SearchInsightsParams{
		ProjectID: project.ID, Query: query, QueryMode: queryMode,
		Tags: tags, TagMode: tagMode, Limit: limit, After: after,
	})
	if err != nil {
		return nil, nil, fmt.Errorf("search insights: %w", err)
	}
	output := map[string]any{"insights": toInsightResponses(page.Insights)}
	if page.NextCursor != nil {
		nextCursor, err := encodeInsightSearchCursor(project.ID, query, queryMode, tags, tagMode, page.NextCursor)
		if err != nil {
			return nil, nil, fmt.Errorf("encode next cursor: %w", err)
		}
		output["next_cursor"] = nextCursor
	}
	result, _, err := jsonResult(output)
	return result, toolEventMetadata{resultCount: len(page.Insights)}, err
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
	after, err := decodeInsightListCursor(in.Cursor, project.ID, in.Tag)
	if err != nil {
		return nil, nil, errInvalidCursor
	}
	page, err := ms.store.ListInsights(ctx, store.ListInsightsParams{ProjectID: project.ID, Tag: in.Tag, Limit: limit, After: after})
	if err != nil {
		return nil, nil, fmt.Errorf("list insights: %w", err)
	}
	output := map[string]any{"insights": toInsightResponses(page.Insights)}
	if page.NextCursor != nil {
		nextCursor, err := encodeInsightListCursor(project.ID, in.Tag, page.NextCursor)
		if err != nil {
			return nil, nil, fmt.Errorf("encode next cursor: %w", err)
		}
		output["next_cursor"] = nextCursor
	}
	result, _, err := jsonResult(output)
	return result, toolEventMetadata{resultCount: len(page.Insights)}, err
}

func (ms *mcpServer) insightDelete(ctx context.Context, req *mcp.CallToolRequest, in insightDeleteInput) (*mcp.CallToolResult, any, error) {
	ms.logger(ctx).InfoContext(ctx, "insight_delete call", slog.String("user_id", req.Extra.TokenInfo.UserID))
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
	insightID, err := uuid.Parse(in.ID)
	if err != nil {
		return nil, nil, fmt.Errorf("invalid insight ID: %w", err)
	}
	revision, err := ms.store.DeleteInsight(ctx, store.DeleteInsightParams{
		OrgID: org.ID, InsightID: insightID, ChangedBy: user.ID, ExpectedRevision: in.ExpectedRevision,
	})
	if result, ok, resultErr := revisionConflictResult(err); ok {
		return result, nil, resultErr
	}
	if errors.Is(err, store.ErrNotFound) {
		return nil, nil, fmt.Errorf("insight %q not found or already deleted", in.ID)
	}
	if err != nil {
		return nil, nil, fmt.Errorf("delete insight: %w", err)
	}
	return jsonResult(map[string]any{revisionResultProperty: revision})
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
	user, org, err := ms.resolveUserAndOrg(ctx, req.Extra.TokenInfo.UserID)
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
		OrgID:            org.ID,
		InsightID:        insightID,
		Content:          in.Content,
		Tags:             tags,
		ChangedBy:        user.ID,
		ExpectedRevision: in.ExpectedRevision,
	})
	if result, ok, resultErr := revisionConflictResult(err); ok {
		return result, nil, resultErr
	}
	if errors.Is(err, store.ErrNotFound) {
		return nil, nil, fmt.Errorf("insight %q not found or already deleted", in.ID)
	}
	if err != nil {
		return nil, nil, fmt.Errorf("update insight: %w", err)
	}
	response := insightResponse{
		ID:        insight.ID.String(),
		Key:       insight.Key,
		Content:   insight.Content,
		Tags:      insight.Tags,
		Category:  insight.Category,
		Source:    insight.Source,
		UpdatedAt: insight.UpdatedAt.Format(time.RFC3339),
		Revision:  insight.Revision,
	}
	if insight.ContentChanged {
		warnings := toInsightLinkWarningResponses(insight.LinkWarnings)
		response.LinkWarnings = &warnings
	}
	return jsonResult(response)
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
			Revision:  f.Revision,
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

func revisionConflictResult(err error) (*mcp.CallToolResult, bool, error) {
	var conflict *store.RevisionConflictError
	if !errors.As(err, &conflict) {
		return nil, false, nil
	}
	result, marshalErr := codedToolErrorResult(map[string]any{
		toolErrorCodeProperty: "revision_conflict",
		"expected_revision":   conflict.Expected,
		"current_revision":    conflict.Current,
	})
	return result, true, marshalErr
}

func codedToolErrorResult(body map[string]any) (*mcp.CallToolResult, error) {
	result, _, err := jsonResult(body)
	if result != nil {
		result.IsError = true
	}
	return result, err
}
