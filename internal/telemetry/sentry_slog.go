package telemetry

import (
	"context"
	"fmt"
	"log/slog"
	"maps"
	"runtime"
	"time"

	"github.com/getsentry/sentry-go"
)

const (
	sentryLoggerName = "slog"
	sentryErrorKey   = "error"
)

// sentryHandler reports error records as Sentry issues; sentry-go's slog
// integration dropped issue creation in v0.48.0 in favour of CaptureException.
type sentryHandler struct {
	prefix string
	fields map[string]any
}

func NewSentrySlogHandler() slog.Handler {
	return &sentryHandler{}
}

func (h *sentryHandler) Enabled(_ context.Context, level slog.Level) bool {
	return level >= slog.LevelError
}

func (h *sentryHandler) Handle(ctx context.Context, record slog.Record) error {
	hub := sentry.GetHubFromContext(ctx)
	if hub == nil {
		hub = sentry.CurrentHub()
	}

	fields := make(map[string]any, len(h.fields)+record.NumAttrs()+1)
	maps.Copy(fields, h.fields)

	var logged error
	record.Attrs(func(attr slog.Attr) bool {
		if h.prefix == "" && attr.Key == sentryErrorKey {
			if err, ok := attr.Value.Resolve().Any().(error); ok {
				logged = err
				return true
			}
		}
		flattenAttr(fields, h.prefix, attr)
		return true
	})

	if source := recordSource(record); source != "" {
		fields["source"] = source
	}

	event := sentry.NewEvent()
	event.Level = sentry.LevelError
	event.Message = record.Message
	event.Logger = sentryLoggerName
	event.Timestamp = record.Time.UTC()
	// Grouping keys off the wrapped %w chain, which carries more than the message.
	event.SetException(logged, maxErrorDepth(hub))

	if len(fields) > 0 {
		event.Contexts["log"] = fields
	}

	// The hint carries the context the OTel linking integration reads trace IDs from.
	hub.CaptureEventWithHint(event, &sentry.EventHint{Context: ctx})
	return nil
}

func (h *sentryHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	if len(attrs) == 0 {
		return h
	}
	fields := make(map[string]any, len(h.fields)+len(attrs))
	maps.Copy(fields, h.fields)
	for _, attr := range attrs {
		flattenAttr(fields, h.prefix, attr)
	}
	return &sentryHandler{prefix: h.prefix, fields: fields}
}

func (h *sentryHandler) WithGroup(name string) slog.Handler {
	if name == "" {
		return h
	}
	return &sentryHandler{prefix: h.prefix + name + ".", fields: h.fields}
}

func maxErrorDepth(hub *sentry.Hub) int {
	if client := hub.Client(); client != nil {
		return client.Options().MaxErrorDepth
	}
	return 100
}

func recordSource(record slog.Record) string {
	if record.PC == 0 {
		return ""
	}
	frame, _ := runtime.CallersFrames([]uintptr{record.PC}).Next()
	if frame.File == "" {
		return ""
	}
	return fmt.Sprintf("%s:%d", frame.File, frame.Line)
}

// Values are narrowed to JSON-safe types because a marshal failure drops the whole event.
func flattenAttr(dst map[string]any, prefix string, attr slog.Attr) {
	val := attr.Value.Resolve()

	if val.Kind() == slog.KindGroup {
		group := val.Group()
		if len(group) == 0 {
			return
		}
		childPrefix := prefix
		if attr.Key != "" {
			childPrefix = prefix + attr.Key + "."
		}
		for _, child := range group {
			flattenAttr(dst, childPrefix, child)
		}
		return
	}

	if attr.Key == "" {
		return
	}
	key := prefix + attr.Key

	switch val.Kind() {
	case slog.KindBool:
		dst[key] = val.Bool()
	case slog.KindInt64:
		dst[key] = val.Int64()
	case slog.KindUint64:
		dst[key] = val.Uint64()
	case slog.KindFloat64:
		dst[key] = val.Float64()
	case slog.KindString:
		dst[key] = val.String()
	case slog.KindDuration:
		dst[key] = val.Duration().String()
	case slog.KindTime:
		dst[key] = val.Time().Format(time.RFC3339)
	default:
		dst[key] = fmt.Sprint(val.Any())
	}
}
