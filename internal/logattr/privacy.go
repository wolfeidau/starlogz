package logattr

import (
	"context"
	"log/slog"
	"strings"
)

var prohibitedKeys = map[string]struct{}{
	"access_token":             {},
	"authorization":            {},
	"client_ip":                {},
	"client_name":              {},
	"code":                     {},
	"code_challenge":           {},
	"code_verifier":            {},
	"content":                  {},
	"cookie":                   {},
	"email":                    {},
	"github_error_description": {},
	"id_token":                 {},
	"login":                    {},
	"query":                    {},
	"redirect_uri":             {},
	"redirect_uris":            {},
	"refresh_token":            {},
	"remote_addr":              {},
	"request_client_name":      {},
	"request_uri":              {},
	"requested_scope":          {},
	"source_ip":                {},
	"state":                    {},
	"stored_uri":               {},
	"tag":                      {},
	"tags":                     {},
	"token":                    {},
	"user_agent":               {},
	"x_forwarded_for":          {},
}

type privacyHandler struct {
	slog.Handler
	blocked bool
}

// NewPrivacyHandler filters keys only; callers must not put secrets in messages or values under safe keys.
func NewPrivacyHandler(handler slog.Handler) slog.Handler {
	return &privacyHandler{Handler: handler}
}

func (h *privacyHandler) Handle(ctx context.Context, record slog.Record) error {
	clean := slog.NewRecord(record.Time, record.Level, record.Message, record.PC)
	if !h.blocked {
		record.Attrs(func(attr slog.Attr) bool {
			if attr, ok := privacySafeAttr(attr); ok {
				clean.AddAttrs(attr)
			}
			return true
		})
	}
	return h.Handler.Handle(ctx, clean)
}

func (h *privacyHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	if h.blocked {
		return h
	}
	clean := make([]slog.Attr, 0, len(attrs))
	for _, attr := range attrs {
		if attr, ok := privacySafeAttr(attr); ok {
			clean = append(clean, attr)
		}
	}
	return &privacyHandler{Handler: h.Handler.WithAttrs(clean)}
}

func (h *privacyHandler) WithGroup(name string) slog.Handler {
	if h.blocked {
		return h
	}
	if isProhibitedKey(name) {
		// Fail closed because attributes below a prohibited group have lost their privacy context.
		return &privacyHandler{Handler: h.Handler, blocked: true}
	}
	return &privacyHandler{Handler: h.Handler.WithGroup(name)}
}

func privacySafeAttr(attr slog.Attr) (slog.Attr, bool) {
	if isProhibitedKey(attr.Key) {
		return slog.Attr{}, false
	}
	attr.Value = attr.Value.Resolve()
	if attr.Value.Kind() != slog.KindGroup {
		return attr, true
	}
	group := attr.Value.Group()
	clean := make([]slog.Attr, 0, len(group))
	for _, child := range group {
		if child, ok := privacySafeAttr(child); ok {
			clean = append(clean, child)
		}
	}
	if len(clean) == 0 {
		return slog.Attr{}, false
	}
	return slog.Group(attr.Key, attrsToAny(clean)...), true
}

func isProhibitedKey(key string) bool {
	key = strings.ToLower(key)
	_, prohibited := prohibitedKeys[key]
	return prohibited || strings.HasSuffix(key, "_token") || strings.HasSuffix(key, "_client_name")
}

func attrsToAny(attrs []slog.Attr) []any {
	values := make([]any, len(attrs))
	for i, attr := range attrs {
		values[i] = attr
	}
	return values
}
