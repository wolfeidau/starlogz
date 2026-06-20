package oidc

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
)

const (
	scopeInsightsRead  = "insights:read"
	scopeInsightsWrite = "insights:write"
	scopeOrgAdmin      = "org:admin"

	defaultAuthorizeScope        = scopeInsightsRead
	defaultRegisteredClientScope = scopeInsightsRead + " " + scopeInsightsWrite
)

var supportedScopes = map[string]bool{
	scopeInsightsRead:  true,
	scopeInsightsWrite: true,
	scopeOrgAdmin:      true,
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

func firstDisallowedScope(scope, allowedScope string) (string, bool) {
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
