package server_test

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/lestrrat-go/jwx/v3/jwk"
	"github.com/stretchr/testify/require"
	"github.com/wolfeidau/starlogz/internal/oidc"
	"github.com/wolfeidau/starlogz/internal/server"
)

// testFixture returns a running httptest.Server and an oidc.Server that shares the same
// signing key, so tests can issue valid JWTs for authenticated requests.
func testFixture(t *testing.T) (*httptest.Server, *oidc.Server) {
	t.Helper()

	privkey, err := ecdsa.GenerateKey(elliptic.P384(), rand.Reader)
	require.NoError(t, err)
	raw, err := jwk.Import(privkey)
	require.NoError(t, err)

	oidcSrv, err := oidc.NewServer(oidc.Config{BaseURL: "http://localhost"}, raw)
	require.NoError(t, err)

	srv, err := server.New(server.Config{
		BaseURL: "http://localhost",
		PrivKey: raw,
		Logger:  slog.Default(),
	})
	require.NoError(t, err)

	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)

	return ts, oidcSrv
}

// --- Health ---

func TestHealth_OK(t *testing.T) {
	ts, _ := testFixture(t)

	resp, err := http.Get(ts.URL + "/health")
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Equal(t, "application/json", resp.Header.Get("Content-Type"))

	var body map[string]string
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&body))
	require.Equal(t, "healthy", body["status"])
	require.NotEmpty(t, body["time"])
}

func TestHealth_WrongMethod(t *testing.T) {
	ts, _ := testFixture(t)

	resp, err := http.Post(ts.URL+"/health", "application/json", nil)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	require.Equal(t, http.StatusMethodNotAllowed, resp.StatusCode)
}

// --- Discovery routing ---

func TestDiscovery_RFC8414(t *testing.T) {
	ts, _ := testFixture(t)

	resp, err := http.Get(ts.URL + "/.well-known/oauth-authorization-server")
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	require.Equal(t, http.StatusOK, resp.StatusCode)

	var meta map[string]any
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&meta))
	require.Equal(t, "http://localhost", meta["issuer"])
}

func TestDiscovery_OIDCFallback(t *testing.T) {
	ts, _ := testFixture(t)

	resp, err := http.Get(ts.URL + "/.well-known/openid-configuration")
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	require.Equal(t, http.StatusOK, resp.StatusCode)

	var meta map[string]any
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&meta))
	require.Equal(t, "http://localhost", meta["issuer"])
}

func TestJWKS_Route(t *testing.T) {
	ts, _ := testFixture(t)

	resp, err := http.Get(ts.URL + "/.well-known/jwks")
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	require.Equal(t, http.StatusOK, resp.StatusCode)

	var keySet struct {
		Keys []map[string]any `json:"keys"`
	}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&keySet))
	require.Len(t, keySet.Keys, 1)
	require.Equal(t, "EC", keySet.Keys[0]["kty"])
}

func TestProtectedResourceMetadata_Route(t *testing.T) {
	ts, _ := testFixture(t)

	resp, err := http.Get(ts.URL + "/.well-known/oauth-protected-resource")
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	require.Equal(t, http.StatusOK, resp.StatusCode)

	var meta map[string]any
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&meta))
	require.NotEmpty(t, meta["resource"])
	require.NotEmpty(t, meta["authorization_servers"])
}

// --- MCP auth boundary ---

func TestMCP_NoAuth_Returns401(t *testing.T) {
	ts, _ := testFixture(t)

	resp, err := http.Post(ts.URL+"/mcp", "application/json", strings.NewReader(`{}`))
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	require.Equal(t, http.StatusUnauthorized, resp.StatusCode)

	// WWW-Authenticate must reference the protected resource metadata URL
	wwwAuth := resp.Header.Get("WWW-Authenticate")
	require.Contains(t, wwwAuth, "Bearer")
	require.Contains(t, wwwAuth, "oauth-protected-resource")
}

func TestMCP_ValidJWT_PassesAuth(t *testing.T) {
	ts, oidcSrv := testFixture(t)

	tokenString, err := oidcSrv.IssueJWT("12345678", "user@example.com", "facts:read")
	require.NoError(t, err)

	req, err := http.NewRequest(http.MethodPost, ts.URL+"/mcp", strings.NewReader(`{}`))
	require.NoError(t, err)
	req.Header.Set("Authorization", "Bearer "+tokenString)
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	// Auth passed — must not be a 401
	require.NotEqual(t, http.StatusUnauthorized, resp.StatusCode)
}

func TestMCP_Delete_Returns200(t *testing.T) {
	ts, _ := testFixture(t)

	req, err := http.NewRequest(http.MethodDelete, ts.URL+"/mcp", nil)
	require.NoError(t, err)

	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	// Special-cased DELETE bypasses auth and returns 200 (not 204) for Service Worker compat
	require.Equal(t, http.StatusOK, resp.StatusCode)
}

func TestMCP_WrongToken_Returns401(t *testing.T) {
	ts, _ := testFixture(t)

	req, err := http.NewRequest(http.MethodPost, ts.URL+"/mcp", strings.NewReader(`{}`))
	require.NoError(t, err)
	req.Header.Set("Authorization", "Bearer not-a-valid-jwt")
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	require.Equal(t, http.StatusUnauthorized, resp.StatusCode)
}
