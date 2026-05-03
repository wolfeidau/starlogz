package oidc

import (
	"encoding/json"
	"log/slog"
	"net/http"
)

var supportedScopes = map[string]bool{
	"facts:read":  true,
	"facts:write": true,
	"org:admin":   true,
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
