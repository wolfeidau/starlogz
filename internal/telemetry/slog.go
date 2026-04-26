package telemetry

import (
	"context"
	"fmt"
	"log/slog"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
)

// OTelHandler wraps a slog.Handler and mirrors each log record as a span event
// on the active trace span. Only fires when the span is recording, so there is
// no overhead when tracing is disabled or no span is in context.
// Use InfoContext/ErrorContext (not Info/Error) so the span can be reached.
type OTelHandler struct {
	slog.Handler
}

func NewOTelHandler(inner slog.Handler) *OTelHandler {
	return &OTelHandler{Handler: inner}
}

func (h *OTelHandler) Handle(ctx context.Context, r slog.Record) error {
	if span := trace.SpanFromContext(ctx); span.IsRecording() {
		attrs := make([]attribute.KeyValue, 0, r.NumAttrs()+1)
		attrs = append(attrs, attribute.String("log.severity", r.Level.String()))
		r.Attrs(func(a slog.Attr) bool {
			attrs = append(attrs, slogAttrToOTel("", a)...)
			return true
		})
		span.AddEvent(r.Message, trace.WithAttributes(attrs...))
	}
	return h.Handler.Handle(ctx, r)
}

func (h *OTelHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return &OTelHandler{Handler: h.Handler.WithAttrs(attrs)}
}

func (h *OTelHandler) WithGroup(name string) slog.Handler {
	return &OTelHandler{Handler: h.Handler.WithGroup(name)}
}

func slogAttrToOTel(prefix string, a slog.Attr) []attribute.KeyValue {
	key := a.Key
	if prefix != "" {
		key = prefix + "." + key
	}
	val := a.Value.Resolve()
	switch val.Kind() {
	case slog.KindBool:
		return []attribute.KeyValue{attribute.Bool(key, val.Bool())}
	case slog.KindInt64:
		return []attribute.KeyValue{attribute.Int64(key, val.Int64())}
	case slog.KindFloat64:
		return []attribute.KeyValue{attribute.Float64(key, val.Float64())}
	case slog.KindString:
		return []attribute.KeyValue{attribute.String(key, val.String())}
	case slog.KindGroup:
		var kvs []attribute.KeyValue
		for _, ga := range val.Group() {
			kvs = append(kvs, slogAttrToOTel(key, ga)...)
		}
		return kvs
	default:
		return []attribute.KeyValue{attribute.String(key, fmt.Sprintf("%v", val.Any()))}
	}
}
