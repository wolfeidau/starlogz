package telemetry

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"time"

	"github.com/getsentry/sentry-go"
	sentryhttp "github.com/getsentry/sentry-go/http"
	sentryslog "github.com/getsentry/sentry-go/slog"
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

func NewSentrySlogHandler(ctx context.Context) slog.Handler {
	return sentryslog.Option{
		EventLevel: []slog.Level{slog.LevelError},
		AddSource:  true,
	}.NewSentryHandler(ctx)
}

func NewSentryHTTPHandler() func(http.Handler) http.Handler {
	return sentryhttp.New(sentryhttp.Options{
		Repanic:         true,
		WaitForDelivery: false,
		Timeout:         5 * time.Second,
	}).Handle
}
