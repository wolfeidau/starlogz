package telemetry

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetricgrpc"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/propagation"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.39.0"
)

// InitTelemetry initializes OpenTelemetry with OTLP exporters for metrics and traces.
// Configuration is read from environment variables:
//   - OTEL_EXPORTER_OTLP_ENDPOINT: The OTLP endpoint (e.g., https://api.honeycomb.io)
//   - OTEL_EXPORTER_OTLP_HEADERS: Headers for authentication (e.g., x-honeycomb-team=API_KEY)
//   - OTEL_SERVICE_NAME: Service name override (defaults to serviceName parameter)
//
// Returns a shutdown function that must be called on graceful shutdown.
// If OTEL_EXPORTER_OTLP_ENDPOINT is not set, returns a no-op immediately.
// If a provider fails to initialize, telemetry continues without it.
func InitTelemetry(ctx context.Context, serviceName, version string) (func(context.Context) error, error) {
	if os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT") == "" {
		return func(context.Context) error { return nil }, nil
	}

	res, err := resource.New(ctx,
		resource.WithAttributes(
			semconv.ServiceName(serviceName),
			semconv.ServiceVersion(version),
		),
		resource.WithFromEnv(),
		resource.WithProcess(),
		resource.WithHost(),
		resource.WithOSType(),
		resource.WithContainer(),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create resource: %w", err)
	}

	traceShutdown, err := initTraceProvider(ctx, res)
	if err != nil {
		slog.Default().Warn("tracing disabled", slog.Any("error", err))
		traceShutdown = func(context.Context) error { return nil }
	}

	metricShutdown, err := initMeterProvider(ctx, res)
	if err != nil {
		slog.Default().Warn("metrics disabled", slog.Any("error", err))
		metricShutdown = func(context.Context) error { return nil }
	}

	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{},
		propagation.Baggage{},
	))

	return func(ctx context.Context) error {
		var errs []error
		if err := traceShutdown(ctx); err != nil {
			errs = append(errs, fmt.Errorf("trace shutdown: %w", err))
		}
		if err := metricShutdown(ctx); err != nil {
			errs = append(errs, fmt.Errorf("metric shutdown: %w", err))
		}
		if len(errs) > 0 {
			return fmt.Errorf("telemetry shutdown errors: %v", errs)
		}
		return nil
	}, nil
}

func initTraceProvider(ctx context.Context, res *resource.Resource) (func(context.Context) error, error) {
	traceExporter, err := otlptracegrpc.New(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to create trace exporter: %w", err)
	}

	tp := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(traceExporter,
			sdktrace.WithBatchTimeout(5*time.Second),
			sdktrace.WithMaxExportBatchSize(512),
		),
		sdktrace.WithResource(res),
		sdktrace.WithSampler(sdktrace.AlwaysSample()), // TODO: make configurable via OTEL_TRACES_SAMPLER
	)

	otel.SetTracerProvider(tp)
	return tp.Shutdown, nil
}

func initMeterProvider(ctx context.Context, res *resource.Resource) (func(context.Context) error, error) {
	metricExporter, err := otlpmetricgrpc.New(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to create metric exporter: %w", err)
	}

	mp := sdkmetric.NewMeterProvider(
		sdkmetric.WithReader(
			sdkmetric.NewPeriodicReader(metricExporter,
				sdkmetric.WithInterval(10*time.Second),
			),
		),
		sdkmetric.WithResource(res),
	)

	otel.SetMeterProvider(mp)
	return mp.Shutdown, nil
}
