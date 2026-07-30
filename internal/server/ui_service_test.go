package server

import (
	"context"
	"log/slog"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	starlogzv1 "github.com/wolfeidau/starlogz/api/gen/proto/go/starlogz/v1"
	"github.com/wolfeidau/starlogz/internal/operations"
	"github.com/wolfeidau/starlogz/internal/store"
	"github.com/wolfeidau/starlogz/internal/wideevent"
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

	revokedWebSessionID uuid.UUID
	revokedOAuthGrantID uuid.UUID
	actorUserID         uuid.UUID
	retiredUntil        time.Time
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

func (s *operationsServiceStore) RevokeWebSessionByID(_ context.Context, id, actorUserID uuid.UUID) error {
	s.revokedWebSessionID = id
	s.actorUserID = actorUserID
	return nil
}

func (s *operationsServiceStore) RevokeOAuthGrantByID(_ context.Context, id, actorUserID uuid.UUID, retiredUntil time.Time) error {
	s.revokedOAuthGrantID = id
	s.actorUserID = actorUserID
	s.retiredUntil = retiredUntil
	return nil
}

type operationsEventPublisher struct {
	events []wideevent.Event
}

func (p *operationsEventPublisher) Publish(_ context.Context, event wideevent.Event) error {
	p.events = append(p.events, event)
	return nil
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
	webSessionID := uuid.New()
	ctx := contextWithWebSession(t.Context(), &store.WebSession{ID: webSessionID, UserID: userID})
	telemetry := &operationsTelemetryProvider{snapshot: &operations.Snapshot{
		GeneratedAt:       time.Now(),
		WindowStartedAt:   time.Now().Add(-24 * time.Hour),
		WindowEndedAt:     time.Now(),
		TotalToolCalls:    12,
		FailedToolCalls:   2,
		P95ToolDurationMS: 47,
	}}

	deniedService := newUIService(st, newOperatorAuthorizer(nil), telemetry, nil, 24*time.Hour)
	session, err := deniedService.GetSession(ctx, connect.NewRequest(&starlogzv1.GetSessionRequest{}))
	require.NoError(t, err)
	require.False(t, session.Msg.GetIsOperator())
	require.Equal(t, webSessionID.String(), session.Msg.GetWebSessionId())

	_, err = deniedService.GetOperationsOverview(ctx, connect.NewRequest(&starlogzv1.GetOperationsOverviewRequest{}))
	require.Equal(t, connect.CodePermissionDenied, connect.CodeOf(err))
	require.False(t, st.called)
	_, err = deniedService.GetOperationsTelemetry(ctx, connect.NewRequest(&starlogzv1.GetOperationsTelemetryRequest{}))
	require.Equal(t, connect.CodePermissionDenied, connect.CodeOf(err))
	require.False(t, telemetry.called)
	_, err = deniedService.RevokeOperationsWebSession(ctx, connect.NewRequest(&starlogzv1.RevokeOperationsWebSessionRequest{
		Id: uuid.NewString(),
	}))
	require.Equal(t, connect.CodePermissionDenied, connect.CodeOf(err))
	require.Equal(t, uuid.Nil, st.revokedWebSessionID)

	allowedService := newUIService(st, newOperatorAuthorizer([]int64{0, 1234}), telemetry, nil, 24*time.Hour)
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

func TestUIServiceOperatorRevocations(t *testing.T) {
	userID := uuid.New()
	st := &operationsServiceStore{
		user: &store.User{ID: userID, GitHubID: 1234, Login: "operator"},
		org:  &store.Org{ID: uuid.New()},
	}
	currentSessionID := uuid.New()
	ctx := contextWithWebSession(t.Context(), &store.WebSession{ID: currentSessionID, UserID: userID})
	publisher := &operationsEventPublisher{}
	emitter, err := wideevent.NewEmitter(publisher, "test", "devel", slog.New(slog.DiscardHandler))
	require.NoError(t, err)
	service := newUIService(st, newOperatorAuthorizer([]int64{1234}), nil, emitter, 2*time.Hour)

	_, err = service.RevokeOperationsWebSession(ctx, connect.NewRequest(&starlogzv1.RevokeOperationsWebSessionRequest{
		Id: currentSessionID.String(),
	}))
	require.Equal(t, connect.CodeFailedPrecondition, connect.CodeOf(err))

	_, err = service.RevokeOperationsWebSession(ctx, connect.NewRequest(&starlogzv1.RevokeOperationsWebSessionRequest{
		Id: "not-a-uuid",
	}))
	require.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))

	webSessionID := uuid.New()
	_, err = service.RevokeOperationsWebSession(ctx, connect.NewRequest(&starlogzv1.RevokeOperationsWebSessionRequest{
		Id: webSessionID.String(),
	}))
	require.NoError(t, err)
	require.Equal(t, webSessionID, st.revokedWebSessionID)
	require.Equal(t, userID, st.actorUserID)

	grantID := uuid.New()
	before := time.Now().Add(2 * time.Hour)
	_, err = service.RevokeOperationsOAuthGrant(ctx, connect.NewRequest(&starlogzv1.RevokeOperationsOAuthGrantRequest{
		Id: grantID.String(),
	}))
	require.NoError(t, err)
	require.Equal(t, grantID, st.revokedOAuthGrantID)
	require.Equal(t, userID, st.actorUserID)
	require.WithinDuration(t, before, st.retiredUntil, time.Second)

	require.Len(t, publisher.events, 4)
	require.Equal(t, wideevent.OperatorWebSessionRevokeCompleted, publisher.events[0].EventName)
	require.Equal(t, wideevent.OutcomeFailure, publisher.events[0].Outcome)
	require.Equal(t, wideevent.ReasonInvalidRequest, publisher.events[0].Reason)
	require.Equal(t, wideevent.OperatorWebSessionRevokeCompleted, publisher.events[1].EventName)
	require.Equal(t, wideevent.OutcomeFailure, publisher.events[1].Outcome)
	require.Equal(t, wideevent.ReasonInvalidRequest, publisher.events[1].Reason)
	require.Equal(t, wideevent.OperatorWebSessionRevokeCompleted, publisher.events[2].EventName)
	require.Equal(t, wideevent.OutcomeSuccess, publisher.events[2].Outcome)
	require.Equal(t, wideevent.OperatorOAuthGrantRevokeCompleted, publisher.events[3].EventName)
	require.Equal(t, wideevent.OutcomeSuccess, publisher.events[3].Outcome)
}
