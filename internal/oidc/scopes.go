package oidc

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
)

const (
	defaultAuthorizeScope        = "insights:read"
	defaultRegisteredClientScope = "insights:read insights:write"
)

var supportedScopes = map[string]bool{
	"insights:read":  true,
	"insights:write": true,
	"org:admin":      true,
}

func normalizeScope(scope, fallback string) string {
	fields := strings.Fields(scope)
	if len(fields) == 0 {
		fields = strings.Fields(fallback)
	}

	seen := make(map[string]bool, len(fields))
	normalized := make([]string, 0, len(fields))
	for _, sc := range fields {
		if seen[sc] {
			continue
		}
		seen[sc] = true
		normalized = append(normalized, sc)
	}
	return strings.Join(normalized, " ")
}

func validateSupportedScope(scope string) error {
	for _, sc := range strings.Fields(scope) {
		if !supportedScopes[sc] {
			return fmt.Errorf("unknown scope: %s", sc)
		}
	}
	return nil
}

func firstScopeOutsideAllowed(scope, allowedScope string) (string, bool) {
	allowed := make(map[string]bool)
	for _, sc := range strings.Fields(allowedScope) {
		allowed[sc] = true
	}
	for _, sc := range strings.Fields(scope) {
		if !allowed[sc] {
			return sc, true
		}
	}
	return "", false
}

func writeOAuthError(w http.ResponseWriter, errCode, description string, status int) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(map[string]string{
		"error":             errCode,
		"error_description": description,
	}); err != nil {
		slog.Default().Error("failed to write OAuth error", slog.Any("error", err))
	}
}
