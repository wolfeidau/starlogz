package server

import (
	"context"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	starlogzv1 "github.com/wolfeidau/starlogz/api/gen/proto/go/starlogz/v1"
	"github.com/wolfeidau/starlogz/internal/operations"
	"github.com/wolfeidau/starlogz/internal/store"
)

func TestToProtoInsightIncludesRevision(t *testing.T) {
	now := time.Now()
	insight, err := toProtoInsight(&store.Insight{
		ID: uuid.New(), Content: "content", CreatedAt: now, UpdatedAt: now, Revision: 7,
	}, "project")
	require.NoError(t, err)
	require.Equal(t, int32(7), insight.GetRevision())
}

type operationsServiceStore struct {
	store.Store
	user     *store.User
	org      *store.Org
	overview *store.OperationsOverview
	called   bool
}

type operationsTelemetryProvider struct {
	snapshot *operations.Snapshot
	called   bool
}

func (p *operationsTelemetryProvider) Get(context.Context) (*operations.Snapshot, error) {
	p.called = true
	return p.snapshot, nil
}

func (s *operationsServiceStore) GetUserByID(context.Context, uuid.UUID) (*store.User, error) {
	return s.user, nil
}

func (s *operationsServiceStore) GetPersonalOrgByUserID(context.Context, uuid.UUID) (*store.Org, error) {
	return s.org, nil
}

func (s *operationsServiceStore) GetOperationsOverview(context.Context, int) (*store.OperationsOverview, error) {
	s.called = true
	return s.overview, nil
}

func TestUIServiceOperatorAccess(t *testing.T) {
	userID := uuid.New()
	st := &operationsServiceStore{
		user: &store.User{ID: userID, GitHubID: 1234, Login: "operator"},
		org:  &store.Org{ID: uuid.New()},
		overview: &store.OperationsOverview{
			ActiveWebSessions: 2,
			ActiveOAuthGrants: 3,
		},
	}
	ctx := contextWithWebSession(t.Context(), &store.WebSession{UserID: userID})
	telemetry := &operationsTelemetryProvider{snapshot: &operations.Snapshot{
		GeneratedAt:       time.Now(),
		WindowStartedAt:   time.Now().Add(-24 * time.Hour),
		WindowEndedAt:     time.Now(),
		TotalToolCalls:    12,
		FailedToolCalls:   2,
		P95ToolDurationMS: 47,
	}}

	deniedService := newUIService(st, newOperatorAuthorizer(nil), telemetry)
	session, err := deniedService.GetSession(ctx, connect.NewRequest(&starlogzv1.GetSessionRequest{}))
	require.NoError(t, err)
	require.False(t, session.Msg.GetIsOperator())

	_, err = deniedService.GetOperationsOverview(ctx, connect.NewRequest(&starlogzv1.GetOperationsOverviewRequest{}))
	require.Equal(t, connect.CodePermissionDenied, connect.CodeOf(err))
	require.False(t, st.called)
	_, err = deniedService.GetOperationsTelemetry(ctx, connect.NewRequest(&starlogzv1.GetOperationsTelemetryRequest{}))
	require.Equal(t, connect.CodePermissionDenied, connect.CodeOf(err))
	require.False(t, telemetry.called)

	allowedService := newUIService(st, newOperatorAuthorizer([]int64{0, 1234}), telemetry)
	session, err = allowedService.GetSession(ctx, connect.NewRequest(&starlogzv1.GetSessionRequest{}))
	require.NoError(t, err)
	require.True(t, session.Msg.GetIsOperator())

	overview, err := allowedService.GetOperationsOverview(ctx, connect.NewRequest(&starlogzv1.GetOperationsOverviewRequest{Limit: 500}))
	require.NoError(t, err)
	require.True(t, st.called)
	require.Equal(t, int32(2), overview.Msg.GetActiveWebSessions())
	require.Equal(t, int32(3), overview.Msg.GetActiveOauthGrants())

	telemetryResponse, err := allowedService.GetOperationsTelemetry(ctx, connect.NewRequest(&starlogzv1.GetOperationsTelemetryRequest{}))
	require.NoError(t, err)
	require.True(t, telemetry.called)
	require.True(t, telemetryResponse.Msg.GetAvailable())
	require.Equal(t, int32(12), telemetryResponse.Msg.GetTotalToolCalls())
	require.Equal(t, int32(2), telemetryResponse.Msg.GetFailedToolCalls())
	require.Equal(t, int64(47), telemetryResponse.Msg.GetP95ToolDurationMs())
}
