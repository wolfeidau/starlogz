package logattr

import (
	"bytes"
	"log/slog"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestPrivacyHandlerDropsProhibitedAttributes(t *testing.T) {
	var output bytes.Buffer
	logger := slog.New(NewPrivacyHandler(slog.NewJSONHandler(&output, nil))).With(
		slog.String("state", "secret-state"),
		slog.String("component", "oidc"),
	)

	logger.InfoContext(t.Context(), "request",
		slog.String("query", "secret-query"),
		slog.String("scope", "secret-scope"),
		slog.String("new_refresh_token", "secret-token"),
		slog.String("grant_client_name", "secret-client-name"),
		slog.Group("oauth", slog.String("code", "secret-code"), slog.String("outcome", "failure")),
	)
	logger.WithGroup("email").InfoContext(t.Context(), "grouped", slog.String("value", "secret-email"))

	require.Contains(t, output.String(), `"component":"oidc"`)
	require.Contains(t, output.String(), `"outcome":"failure"`)
	for _, secret := range []string{"secret-state", "secret-query", "secret-scope", "secret-token", "secret-client-name", "secret-code", "secret-email"} {
		require.NotContains(t, output.String(), secret)
	}
}
