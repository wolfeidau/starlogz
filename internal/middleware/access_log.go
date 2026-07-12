package middleware

import (
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/wolfeidau/starlogz/internal/ctxlog"
	"go.opentelemetry.io/otel/trace"
)

type responseWriter struct {
	http.ResponseWriter
	status int
	bytes  int64
}

func (rw *responseWriter) WriteHeader(status int) {
	if rw.status != 0 {
		return
	}
	rw.status = status
	rw.ResponseWriter.WriteHeader(status)
}

func (rw *responseWriter) Write(b []byte) (int, error) {
	if rw.status == 0 {
		rw.status = http.StatusOK
	}
	n, err := rw.ResponseWriter.Write(b)
	rw.bytes += int64(n)
	return n, err
}

// Unwrap allows http.ResponseController to reach the underlying ResponseWriter.
func (rw *responseWriter) Unwrap() http.ResponseWriter {
	return rw.ResponseWriter
}

// AccessLog returns middleware that logs each request's method, path, status,
// duration, and response size. It seeds a request_id into the context logger
// so all log lines within a request share a correlation field.
// Place inside otelhttp so trace context is available.
func AccessLog(logger *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			reqLogger := logger.With(
				slog.String("request_id", uuid.New().String()),
			)
			r = r.WithContext(ctxlog.WithLogger(r.Context(), reqLogger))

			start := time.Now()
			rw := &responseWriter{ResponseWriter: w}

			next.ServeHTTP(rw, r)
			if rw.status == 0 {
				rw.status = http.StatusOK
			}

			reqLogger.InfoContext(r.Context(), "http_request",
				slog.String("trace_id", trace.SpanContextFromContext(r.Context()).TraceID().String()),
				slog.String("method", r.Method),
				slog.String("route", routePattern(r)),
				slog.Int("status", rw.status),
				slog.Int64("duration_ms", time.Since(start).Milliseconds()),
				slog.Int64("response_bytes", rw.bytes),
			)
		})
	}
}

func routePattern(r *http.Request) string {
	if r.Pattern == "" {
		return "unmatched"
	}
	parts := strings.Fields(r.Pattern)
	return parts[len(parts)-1]
}
