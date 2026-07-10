package server

import (
	"context"
	"errors"
	"fmt"
	"math"
	"slices"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"github.com/modelcontextprotocol/go-sdk/auth"
	starlogzv1 "github.com/wolfeidau/starlogz/api/gen/proto/go/starlogz/v1"
	"github.com/wolfeidau/starlogz/internal/store"
	"google.golang.org/protobuf/types/known/timestamppb"
)

type tokenInfoContextKey struct{}

func contextWithTokenInfo(ctx context.Context, info *auth.TokenInfo) context.Context {
	return context.WithValue(ctx, tokenInfoContextKey{}, info)
}

func tokenInfoFromContext(ctx context.Context) (*auth.TokenInfo, bool) {
	info, ok := ctx.Value(tokenInfoContextKey{}).(*auth.TokenInfo)
	return info, ok && info != nil
}

type uiService struct {
	store store.Store
}

func newUIService(st store.Store) *uiService {
	return &uiService{store: st}
}

func (s *uiService) GetSession(ctx context.Context, _ *connect.Request[starlogzv1.GetSessionRequest]) (*connect.Response[starlogzv1.GetSessionResponse], error) {
	info, user, _, err := s.resolve(ctx)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&starlogzv1.GetSessionResponse{
		UserId: user.ID.String(),
		Login:  user.Login,
		Email:  user.Email,
		Scopes: info.Scopes,
	}), nil
}

func (s *uiService) ListProjects(ctx context.Context, _ *connect.Request[starlogzv1.ListProjectsRequest]) (*connect.Response[starlogzv1.ListProjectsResponse], error) {
	_, _, org, err := s.resolve(ctx)
	if err != nil {
		return nil, err
	}
	projects, err := s.store.ListProjects(ctx, org.ID)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("list projects: %w", err))
	}
	return connect.NewResponse(&starlogzv1.ListProjectsResponse{Projects: toProtoProjects(projects)}), nil
}

func (s *uiService) GetProjectDashboard(ctx context.Context, req *connect.Request[starlogzv1.GetProjectDashboardRequest]) (*connect.Response[starlogzv1.GetProjectDashboardResponse], error) {
	_, _, org, err := s.resolve(ctx)
	if err != nil {
		return nil, err
	}
	project, err := s.projectBySlug(ctx, org.ID, req.Msg.GetProject())
	if err != nil {
		return nil, err
	}
	dashboard, err := s.store.GetProjectDashboard(ctx, project.ID)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("get project dashboard: %w", err))
	}
	return connect.NewResponse(&starlogzv1.GetProjectDashboardResponse{
		Project:        toProtoProject(project),
		TotalInsights:  int32Count(dashboard.TotalInsights),
		CategoryCounts: toProtoCountBuckets(dashboard.CategoryCounts),
		SourceCounts:   toProtoCountBuckets(dashboard.SourceCounts),
		TopTags:        toProtoCountBuckets(dashboard.TopTags),
		RecentActivity: toProtoActivityBuckets(dashboard.RecentActivity),
		RecentInsights: toProtoInsights(dashboard.RecentInsights),
	}), nil
}

func (s *uiService) ListInsights(ctx context.Context, req *connect.Request[starlogzv1.ListInsightsRequest]) (*connect.Response[starlogzv1.ListInsightsResponse], error) {
	_, _, org, err := s.resolve(ctx)
	if err != nil {
		return nil, err
	}
	project, err := s.projectBySlug(ctx, org.ID, req.Msg.GetProject())
	if err != nil {
		return nil, err
	}
	limit := int(req.Msg.GetLimit())
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	insights, err := s.store.ListInsights(ctx, project.ID, req.Msg.GetTag(), limit)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("list insights: %w", err))
	}
	return connect.NewResponse(&starlogzv1.ListInsightsResponse{Insights: toProtoInsights(insights)}), nil
}

func (s *uiService) SearchInsights(ctx context.Context, req *connect.Request[starlogzv1.SearchInsightsRequest]) (*connect.Response[starlogzv1.SearchInsightsResponse], error) {
	_, _, org, err := s.resolve(ctx)
	if err != nil {
		return nil, err
	}
	project, err := s.projectBySlug(ctx, org.ID, req.Msg.GetProject())
	if err != nil {
		return nil, err
	}
	if req.Msg.GetQuery() == "" {
		return connect.NewResponse(&starlogzv1.SearchInsightsResponse{}), nil
	}
	limit := int(req.Msg.GetLimit())
	if limit <= 0 || limit > 100 {
		limit = 50
	}
	insights, err := s.store.SearchInsights(ctx, project.ID, req.Msg.GetQuery(), req.Msg.GetTags(), limit)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("search insights: %w", err))
	}
	return connect.NewResponse(&starlogzv1.SearchInsightsResponse{Insights: toProtoInsights(insights)}), nil
}

func (s *uiService) ListTags(ctx context.Context, req *connect.Request[starlogzv1.ListTagsRequest]) (*connect.Response[starlogzv1.ListTagsResponse], error) {
	_, _, org, err := s.resolve(ctx)
	if err != nil {
		return nil, err
	}
	project, err := s.projectBySlug(ctx, org.ID, req.Msg.GetProject())
	if err != nil {
		return nil, err
	}
	limit := int(req.Msg.GetLimit())
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	tags, err := s.store.ListTags(ctx, project.ID, limit)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("list tags: %w", err))
	}
	return connect.NewResponse(&starlogzv1.ListTagsResponse{Tags: toProtoTagCounts(tags)}), nil
}

func (s *uiService) resolve(ctx context.Context) (*auth.TokenInfo, *store.User, *store.Org, error) {
	if s.store == nil {
		return nil, nil, nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("database not configured"))
	}
	info, ok := tokenInfoFromContext(ctx)
	if !ok {
		return nil, nil, nil, connect.NewError(connect.CodeUnauthenticated, errors.New("missing session"))
	}
	if !slices.Contains(info.Scopes, "insights:read") {
		return nil, nil, nil, connect.NewError(connect.CodePermissionDenied, errors.New("token missing insights:read scope"))
	}
	userID, err := uuid.Parse(info.UserID)
	if err != nil {
		return nil, nil, nil, connect.NewError(connect.CodeUnauthenticated, fmt.Errorf("invalid user id: %w", err))
	}
	user, err := s.store.GetUserByID(ctx, userID)
	if err != nil {
		return nil, nil, nil, connect.NewError(connect.CodeUnauthenticated, fmt.Errorf("user not found: %w", err))
	}
	org, err := s.store.GetPersonalOrgByUserID(ctx, user.ID)
	if err != nil {
		return nil, nil, nil, connect.NewError(connect.CodeUnauthenticated, fmt.Errorf("personal org not found: %w", err))
	}
	return info, user, org, nil
}

func (s *uiService) projectBySlug(ctx context.Context, orgID uuid.UUID, slug string) (*store.Project, error) {
	if slug == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("project is required"))
	}
	project, err := s.store.GetProjectBySlug(ctx, orgID, slug)
	if errors.Is(err, store.ErrNotFound) {
		return nil, connect.NewError(connect.CodeNotFound, errors.New("project not found"))
	}
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("get project: %w", err))
	}
	return project, nil
}

func toProtoProjects(projects []*store.Project) []*starlogzv1.Project {
	out := make([]*starlogzv1.Project, len(projects))
	for i, p := range projects {
		out[i] = toProtoProject(p)
	}
	return out
}

func toProtoProject(p *store.Project) *starlogzv1.Project {
	if p == nil {
		return nil
	}
	return &starlogzv1.Project{
		Id:        p.ID.String(),
		Slug:      p.Slug,
		Name:      p.Name,
		CreatedAt: timestamppb.New(p.CreatedAt),
	}
}

func toProtoInsights(insights []*store.Insight) []*starlogzv1.Insight {
	out := make([]*starlogzv1.Insight, len(insights))
	for i, in := range insights {
		out[i] = &starlogzv1.Insight{
			Id:        in.ID.String(),
			Key:       in.Key,
			Content:   in.Content,
			Tags:      in.Tags,
			Category:  in.Category,
			Source:    in.Source,
			CreatedAt: timestamppb.New(in.CreatedAt),
			UpdatedAt: timestamppb.New(in.UpdatedAt),
		}
	}
	return out
}

func toProtoCountBuckets(buckets []store.CountBucket) []*starlogzv1.CountBucket {
	out := make([]*starlogzv1.CountBucket, len(buckets))
	for i, b := range buckets {
		out[i] = &starlogzv1.CountBucket{Name: b.Name, Count: int32Count(b.Count)}
	}
	return out
}

func toProtoTagCounts(tags []store.TagCount) []*starlogzv1.CountBucket {
	out := make([]*starlogzv1.CountBucket, len(tags))
	for i, t := range tags {
		out[i] = &starlogzv1.CountBucket{Name: t.Name, Count: int32Count(t.Count)}
	}
	return out
}

func toProtoActivityBuckets(buckets []store.ActivityBucket) []*starlogzv1.ActivityBucket {
	out := make([]*starlogzv1.ActivityBucket, len(buckets))
	for i, b := range buckets {
		out[i] = &starlogzv1.ActivityBucket{Date: b.Date, Count: int32Count(b.Count)}
	}
	return out
}

func int32Count(n int) int32 {
	if n > math.MaxInt32 {
		return math.MaxInt32
	}
	if n < 0 {
		return 0
	}
	return int32(n)
}
