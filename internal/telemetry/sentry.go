package telemetry

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"time"

	"github.com/getsentry/sentry-go"
	sentryhttp "github.com/getsentry/sentry-go/http"
	sentryotel "github.com/getsentry/sentry-go/otel"
)

// InitSentry initializes Sentry when SENTRY_DSN is set.
func InitSentry(_ context.Context, serviceName, version string) (func(context.Context) error, bool, error) {
	dsn := os.Getenv("SENTRY_DSN")
	if dsn == "" {
		return func(context.Context) error { return nil }, false, nil
	}

	if err := sentry.Init(sentry.ClientOptions{
		Dsn:              dsn,
		Environment:      os.Getenv("SENTRY_ENVIRONMENT"),
		Release:          version,
		AttachStacktrace: true,
		EnableTracing:    false,
		SendDefaultPII:   false,
		// Traces go to the OTLP collector, so Sentry only needs to stamp issues with the trace IDs.
		Integrations: func(i []sentry.Integration) []sentry.Integration {
			return append(i, sentryotel.NewOtelIntegration())
		},
		// Errors are reported as issues; Sentry Logs are unused.
		DisableLogs: true,
		Tags: map[string]string{
			"service": serviceName,
		},
	}); err != nil {
		return nil, false, fmt.Errorf("failed to initialize sentry: %w", err)
	}

	return func(ctx context.Context) error {
		if sentry.FlushWithContext(ctx) {
			return nil
		}
		return fmt.Errorf("sentry flush timed out: %w", context.DeadlineExceeded)
	}, true, nil
}

func NewSentryHTTPHandler() func(http.Handler) http.Handler {
	return sentryhttp.New(sentryhttp.Options{
		Repanic:         true,
		WaitForDelivery: false,
		Timeout:         5 * time.Second,
	}).Handle
}
