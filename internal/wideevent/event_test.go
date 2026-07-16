package wideevent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"github.com/wolfeidau/starlogz/internal/clientclass"
	"github.com/wolfeidau/starlogz/internal/ctxlog"
	"go.opentelemetry.io/otel/trace"
)

type capturePublisher struct {
	events []Event
	err    error
}

type deadlinePublisher struct {
	remaining time.Duration
}

func withTestIdentity(attributes map[string]string) map[string]string {
	result := ClientIdentityAttributes(clientclass.Unknown())
	for key, value := range attributes {
		result[key] = value
	}
	return result
}

func (p *deadlinePublisher) Publish(ctx context.Context, _ Event) error {
	deadline, ok := ctx.Deadline()
	if ok {
		p.remaining = time.Until(deadline)
	}
	return nil
}

func (p *capturePublisher) Publish(_ context.Context, event Event) error {
	p.events = append(p.events, event)
	return p.err
}

func TestEmitterBuildsBoundedCorrelatedEvent(t *testing.T) {
	publisher := &capturePublisher{}
	emitter, err := NewEmitter(publisher, "dev", "v1.2.3", slog.New(slog.DiscardHandler))
	require.NoError(t, err)

	requestID := uuid.New().String()
	spanContext := trace.NewSpanContext(trace.SpanContextConfig{
		TraceID: trace.TraceID{1},
		SpanID:  trace.SpanID{1},
	})
	ctx := ctxlog.WithRequestID(t.Context(), requestID)
	ctx = trace.ContextWithSpanContext(ctx, spanContext)
	emitter.Completion(ctx, MCPToolCallCompleted, OutcomeSuccess, ReasonCompleted, time.Now(), withTestIdentity(map[string]string{
		AttributeTool:              ToolInsightSearch,
		AttributeResultCountBucket: ResultCountOneToTen,
	}))

	require.Len(t, publisher.events, 1)
	event := publisher.events[0]
	require.Equal(t, SchemaVersion, event.SchemaVersion)
	require.Equal(t, MCPToolCallCompleted, event.EventName)
	require.Equal(t, "dev", event.Environment)
	require.Equal(t, "v1.2.3", event.ServiceVersion)
	require.Equal(t, requestID, event.RequestID)
	require.Equal(t, spanContext.TraceID().String(), event.TraceID)
	require.Equal(t, withTestIdentity(map[string]string{
		AttributeTool:              ToolInsightSearch,
		AttributeResultCountBucket: ResultCountOneToTen,
	}), event.Attributes)
	require.NoError(t, event.Validate())

	encoded, err := json.Marshal(event)
	require.NoError(t, err)
	for _, prohibited := range []string{"content", "query", "tags", "email", "token", "code", "state", "error"} {
		require.NotContains(t, string(encoded), `"`+prohibited+`"`)
	}
}

func TestHTTPHandlerEmitsSuccessfulAndFailedCompletions(t *testing.T) {
	publisher := &capturePublisher{}
	emitter, err := NewEmitter(publisher, "test", "devel", slog.New(slog.DiscardHandler))
	require.NoError(t, err)

	success := emitter.HTTPHandler(OAuthAuthorizationCompleted, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Redirect(w, &http.Request{}, "https://example.com", http.StatusFound)
	}))
	success.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/oauth2/authorize", nil))

	failure := emitter.HTTPHandler(OAuthAuthorizationCompleted, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "bad request", http.StatusBadRequest)
	}))
	failure.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/oauth2/authorize", nil))

	require.Len(t, publisher.events, 2)
	require.Equal(t, OutcomeSuccess, publisher.events[0].Outcome)
	require.Equal(t, ReasonCompleted, publisher.events[0].Reason)
	require.Equal(t, OutcomeFailure, publisher.events[1].Outcome)
	require.Equal(t, ReasonInvalidRequest, publisher.events[1].Reason)
}

func TestHTTPHandlerCollectsBoundedClientIdentity(t *testing.T) {
	publisher := &capturePublisher{}
	emitter, err := NewEmitter(publisher, "test", "devel", slog.New(slog.DiscardHandler))
	require.NoError(t, err)

	handler := emitter.HTTPHandler(OAuthAuthorizationCompleted, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		SetClientIdentity(r.Context(), clientclass.FromFirstParty("starlogz-ui"))
		w.WriteHeader(http.StatusFound)
	}))
	handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/oauth2/authorize", nil))

	require.Len(t, publisher.events, 1)
	require.Equal(t, ClientIdentityAttributes(clientclass.FromFirstParty("starlogz-ui")), publisher.events[0].Attributes)
}

func TestHTTPHandlerEmitsFailureAndRepanics(t *testing.T) {
	publisher := &capturePublisher{}
	emitter, err := NewEmitter(publisher, "test", "devel", slog.New(slog.DiscardHandler))
	require.NoError(t, err)
	handler := emitter.HTTPHandler(OAuthGitHubCallbackCompleted, http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		panic("boom")
	}))

	require.Panics(t, func() {
		handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/auth/github/callback", nil))
	})
	require.Len(t, publisher.events, 1)
	require.Equal(t, OutcomeFailure, publisher.events[0].Outcome)
	require.Equal(t, ReasonServerError, publisher.events[0].Reason)
}

func TestEmitterRejectsUnboundedAttributes(t *testing.T) {
	publisher := &capturePublisher{}
	var logs bytes.Buffer
	emitter, err := NewEmitter(publisher, "test", "devel", slog.New(slog.NewJSONHandler(&logs, nil)))
	require.NoError(t, err)

	emitter.Completion(t.Context(), MCPToolCallCompleted, OutcomeSuccess, ReasonCompleted, time.Now(), map[string]string{"query": "private"})

	require.Empty(t, publisher.events)
	require.Contains(t, logs.String(), "wide event rejected")
	require.NotContains(t, logs.String(), "private")
}

func TestEmitterLogsPublishFailureWithoutReturningIt(t *testing.T) {
	publisher := &capturePublisher{err: errors.New("unavailable")}
	var logs bytes.Buffer
	emitter, err := NewEmitter(publisher, "test", "devel", slog.New(slog.NewJSONHandler(&logs, nil)))
	require.NoError(t, err)

	emitter.Completion(t.Context(), UILoginCompleted, OutcomeSuccess, ReasonCompleted, time.Now(), nil)

	require.Len(t, publisher.events, 1)
	require.Contains(t, logs.String(), "wide event publish failed")
	require.Equal(t, 1, strings.Count(logs.String(), "wide event publish failed"))
}

func TestEmitterBoundsPublishTimeout(t *testing.T) {
	publisher := &deadlinePublisher{}
	emitter, err := NewEmitter(publisher, "test", "devel", slog.New(slog.DiscardHandler))
	require.NoError(t, err)

	emitter.Completion(t.Context(), UILoginCompleted, OutcomeSuccess, ReasonCompleted, time.Now(), nil)

	require.Positive(t, publisher.remaining)
	require.LessOrEqual(t, publisher.remaining, publishTimeout)
}

func TestAllEventNamesValidate(t *testing.T) {
	names := []Name{
		OAuthAuthorizationCompleted, OAuthClientRegistrationCompleted, OAuthGitHubCallbackCompleted,
		OAuthTokenExchangeCompleted, OAuthRefreshCompleted,
		UILoginCompleted, UISessionCreated, UISessionRevoked,
		MCPClientInitialized, MCPToolCallCompleted,
	}
	for _, name := range names {
		t.Run(string(name), func(t *testing.T) {
			var attributes map[string]string
			if eventRequiresIdentity(name) {
				attributes = withTestIdentity(nil)
			}
			if name == MCPToolCallCompleted {
				attributes[AttributeTool] = ToolWhoami
			}
			event := Event{
				SchemaVersion: SchemaVersion, EventID: uuid.New().String(), EventName: name,
				OccurredAt: time.Now().UTC().Format(time.RFC3339Nano), Environment: "test",
				ServiceVersion: "devel", Outcome: OutcomeSuccess, Reason: ReasonCompleted,
				Attributes: attributes,
			}
			require.NoError(t, event.Validate())
		})
	}
}

func TestResultCountBucketValidation(t *testing.T) {
	tests := map[string]struct {
		outcome    Outcome
		attributes map[string]string
		wantError  string
	}{
		"successful search with bucket": {
			outcome: OutcomeSuccess,
			attributes: map[string]string{
				AttributeTool:              ToolInsightSearch,
				AttributeResultCountBucket: ResultCountZero,
			},
		},
		"successful list with bucket": {
			outcome: OutcomeSuccess,
			attributes: map[string]string{
				AttributeTool:              ToolInsightList,
				AttributeResultCountBucket: ResultCount101To200,
			},
		},
		"successful search without bucket": {
			outcome:    OutcomeSuccess,
			attributes: map[string]string{AttributeTool: ToolInsightSearch},
			wantError:  "result_count_bucket is required",
		},
		"failed search without bucket": {
			outcome:    OutcomeFailure,
			attributes: map[string]string{AttributeTool: ToolInsightSearch},
		},
		"bucket on another tool": {
			outcome: OutcomeSuccess,
			attributes: map[string]string{
				AttributeTool:              ToolWhoami,
				AttributeResultCountBucket: ResultCountZero,
			},
			wantError: "result_count_bucket is not allowed",
		},
		"bucket on failed search": {
			outcome: OutcomeFailure,
			attributes: map[string]string{
				AttributeTool:              ToolInsightSearch,
				AttributeResultCountBucket: ResultCountZero,
			},
			wantError: "result_count_bucket is not allowed",
		},
		"unsupported bucket": {
			outcome: OutcomeSuccess,
			attributes: map[string]string{
				AttributeTool:              ToolInsightList,
				AttributeResultCountBucket: "201+",
			},
			wantError: "unsupported result_count_bucket",
		},
	}

	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			reason := ReasonCompleted
			if test.outcome == OutcomeFailure {
				reason = ReasonFailed
			}
			event := Event{
				SchemaVersion: SchemaVersion, EventID: uuid.New().String(), EventName: MCPToolCallCompleted,
				OccurredAt: time.Now().UTC().Format(time.RFC3339Nano), Environment: "test",
				ServiceVersion: "devel", Outcome: test.outcome, Reason: reason,
				Attributes: withTestIdentity(test.attributes),
			}
			err := event.Validate()
			if test.wantError == "" {
				require.NoError(t, err)
				return
			}
			require.ErrorContains(t, err, test.wantError)
		})
	}
}

func TestClientIdentityValidation(t *testing.T) {
	valid := Event{
		SchemaVersion: SchemaVersion, EventID: uuid.New().String(), EventName: MCPClientInitialized,
		OccurredAt: time.Now().UTC().Format(time.RFC3339Nano), Environment: "test",
		ServiceVersion: "devel", Outcome: OutcomeSuccess, Reason: ReasonCompleted,
		Attributes: ClientIdentityAttributes(clientclass.FromMCP("codex-mcp-client", "144.4.0")),
	}
	require.NoError(t, valid.Validate())

	tests := map[string]map[string]string{
		"unbounded product": {
			AttributeClientProduct: "private", AttributeClientIdentitySource: clientclass.SourceMCPInitialize,
			AttributeClientIdentityConfidence: clientclass.ConfidenceDeclared,
		},
		"invalid combination": {
			AttributeClientProduct: clientclass.ProductCodex, AttributeClientIdentitySource: clientclass.SourceUnknown,
			AttributeClientIdentityConfidence: clientclass.ConfidenceDeclared,
		},
		"noncanonical major": {
			AttributeClientProduct: clientclass.ProductCodex, AttributeClientProductMajor: "0144",
			AttributeClientIdentitySource:     clientclass.SourceMCPInitialize,
			AttributeClientIdentityConfidence: clientclass.ConfidenceDeclared,
		},
	}
	for name, attributes := range tests {
		t.Run(name, func(t *testing.T) {
			event := valid
			event.Attributes = attributes
			require.Error(t, event.Validate())
		})
	}
}
