package telemetry

import (
	"context"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

var refreshTokenGrantCounter metric.Int64Counter

func init() {
	refreshTokenGrantCounter, _ = otel.Meter(tracerName).Int64Counter(
		"starlogz.oauth.refresh_token_grants",
		metric.WithDescription("OAuth refresh token grant outcomes"),
	)
}

func RecordRefreshTokenGrant(ctx context.Context, outcome, reason string) {
	if refreshTokenGrantCounter == nil {
		return
	}
	refreshTokenGrantCounter.Add(ctx, 1, metric.WithAttributes(
		attribute.String("outcome", outcome),
		attribute.String("reason", reason),
	))
}

// Metrics holds OpenTelemetry metric instruments. Populated when instruments are added.
type Metrics struct{}
