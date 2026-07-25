package ctxlog

import (
	"context"
	"log/slog"
)

type loggerKey struct{}
type requestIDKey struct{}
type edgeRequestIDKey struct{}

// WithLogger returns a copy of ctx carrying l.
func WithLogger(ctx context.Context, l *slog.Logger) context.Context {
	return context.WithValue(ctx, loggerKey{}, l)
}

// LoggerFrom returns the logger stored in ctx, or slog.Default() if none.
func LoggerFrom(ctx context.Context) *slog.Logger {
	if l, ok := ctx.Value(loggerKey{}).(*slog.Logger); ok && l != nil {
		return l
	}
	return slog.Default()
}

func WithRequestID(ctx context.Context, requestID string) context.Context {
	return context.WithValue(ctx, requestIDKey{}, requestID)
}

func RequestIDFrom(ctx context.Context) string {
	requestID, _ := ctx.Value(requestIDKey{}).(string)
	return requestID
}

func WithEdgeRequestID(ctx context.Context, requestID string) context.Context {
	return context.WithValue(ctx, edgeRequestIDKey{}, requestID)
}

func EdgeRequestIDFrom(ctx context.Context) string {
	requestID, _ := ctx.Value(edgeRequestIDKey{}).(string)
	return requestID
}
