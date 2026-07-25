package telemetry

import (
	"errors"
	"fmt"
	"log/slog"
	"testing"
	"time"

	"github.com/getsentry/sentry-go"
	sentryotel "github.com/getsentry/sentry-go/otel"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/trace"
)

// newCapturingHub returns a hub whose events are collected instead of sent.
func newCapturingHub(t *testing.T) (*sentry.Hub, *[]*sentry.Event) {
	t.Helper()

	events := []*sentry.Event{}
	client, err := sentry.NewClient(sentry.ClientOptions{
		Dsn: "https://public@example.com/1",
		Integrations: func(i []sentry.Integration) []sentry.Integration {
			return append(i, sentryotel.NewOtelIntegration())
		},
		BeforeSend: func(event *sentry.Event, _ *sentry.EventHint) *sentry.Event {
			events = append(events, event)
			return nil
		},
	})
	require.NoError(t, err)

	return sentry.NewHub(client, sentry.NewScope()), &events
}

func TestSentrySlogHandler_CapturesWrappedErrorChain(t *testing.T) {
	hub, events := newCapturingHub(t)
	ctx := sentry.SetHubOnContext(t.Context(), hub)

	boom := errors.New("connection refused")
	logger := slog.New(NewSentrySlogHandler())
	logger.ErrorContext(ctx, "failed to write insight", slog.Any("error", fmt.Errorf("write insight: %w", boom)))

	require.Len(t, *events, 1)
	event := (*events)[0]

	require.Equal(t, "failed to write insight", event.Message)
	require.Equal(t, sentry.LevelError, event.Level)
	require.Equal(t, "slog", event.Logger)

	require.Len(t, event.Exception, 2)
	require.Equal(t, "connection refused", event.Exception[0].Value)
	require.Equal(t, "write insight: connection refused", event.Exception[1].Value)

	// The error becomes the exception rather than a log field.
	logFields, ok := event.Contexts["log"]
	require.True(t, ok)
	require.NotContains(t, logFields, "error")
	require.Contains(t, logFields, "source")
}

func TestSentrySlogHandler_IgnoresLevelsBelowError(t *testing.T) {
	hub, events := newCapturingHub(t)
	ctx := sentry.SetHubOnContext(t.Context(), hub)

	logger := slog.New(NewSentrySlogHandler())
	logger.InfoContext(ctx, "started")
	logger.WarnContext(ctx, "slow query")

	require.Empty(t, *events)
}

func TestSentrySlogHandler_FlattensAttrsAndGroups(t *testing.T) {
	hub, events := newCapturingHub(t)
	ctx := sentry.SetHubOnContext(t.Context(), hub)

	logger := slog.New(NewSentrySlogHandler()).
		With(slog.String("service", "starlogz")).
		WithGroup("db").
		With(slog.Int("attempt", 2))
	logger.ErrorContext(ctx, "query failed", slog.Duration("elapsed", 150*time.Millisecond))

	require.Len(t, *events, 1)
	logFields := (*events)[0].Contexts["log"]

	require.Equal(t, "starlogz", logFields["service"])
	require.Equal(t, int64(2), logFields["db.attempt"])
	require.Equal(t, "150ms", logFields["db.elapsed"])
}

func TestSentrySlogHandler_LinksActiveOTelTrace(t *testing.T) {
	hub, events := newCapturingHub(t)
	ctx := sentry.SetHubOnContext(t.Context(), hub)

	traceID := trace.TraceID{0xd4, 0xcd, 0xa9, 0x5b, 0x65, 0x2f, 0x4a, 0x15, 0x92, 0xb4, 0x49, 0xd5, 0x92, 0x9f, 0xda, 0x1b}
	spanID := trace.SpanID{0x6e, 0x0c, 0x63, 0x25, 0x7d, 0xe3, 0x4c, 0x92}
	ctx = trace.ContextWithSpanContext(ctx, trace.NewSpanContext(trace.SpanContextConfig{
		TraceID: traceID,
		SpanID:  spanID,
	}))

	logger := slog.New(NewSentrySlogHandler())
	logger.ErrorContext(ctx, "failed to write insight", slog.Any("error", errors.New("boom")))

	require.Len(t, *events, 1)
	traceCtx := (*events)[0].Contexts["trace"]

	require.Equal(t, traceID.String(), traceCtx["trace_id"])
	require.Equal(t, spanID.String(), traceCtx["span_id"])
}

func TestSentrySlogHandler_MessageOnlyWithoutErrorAttr(t *testing.T) {
	hub, events := newCapturingHub(t)
	ctx := sentry.SetHubOnContext(t.Context(), hub)

	logger := slog.New(NewSentrySlogHandler())
	logger.ErrorContext(ctx, "invalid state", slog.String("phase", "startup"))

	require.Len(t, *events, 1)
	event := (*events)[0]

	require.Empty(t, event.Exception)
	require.Equal(t, "invalid state", event.Message)
	require.Equal(t, "startup", event.Contexts["log"]["phase"])
}
