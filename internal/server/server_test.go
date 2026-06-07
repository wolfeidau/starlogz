package server_test

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/lestrrat-go/jwx/v3/jwk"
	"github.com/stretchr/testify/require"
	"github.com/wolfeidau/starlogz/internal/oidc"
	"github.com/wolfeidau/starlogz/internal/server"
	"github.com/wolfeidau/starlogz/internal/store"
)

// memAuthState is a minimal in-memory AuthStateStore for tests.
type memAuthState struct {
	pending map[string]store.PendingAuth
	codes   map[string]store.AuthCode
}

func newMemAuthState() *memAuthState {
	return &memAuthState{pending: map[string]store.PendingAuth{}, codes: map[string]store.AuthCode{}}
}

func (m *memAuthState) StorePendingAuth(_ context.Context, state string, p store.PendingAuth) error {
	m.pending[state] = p
	return nil
}
func (m *memAuthState) ConsumePendingAuth(_ context.Context, state string) (*store.PendingAuth, error) {
	p, ok := m.pending[state]
	if !ok {
		return nil, store.ErrNotFound
	}
	delete(m.pending, state)
	return &p, nil
}
func (m *memAuthState) StoreAuthCode(_ context.Context, code string, c store.AuthCode) error {
	m.codes[code] = c
	return nil
}
func (m *memAuthState) ConsumeAuthCode(_ context.Context, code string) (*store.AuthCode, error) {
	c, ok := m.codes[code]
	if !ok {
		return nil, store.ErrNotFound
	}
	delete(m.codes, code)
	return &c, nil
}

// memRevocation is a minimal in-memory RevocationStore for tests.
type memRevocation struct{ revoked map[string]struct{} }

func newMemRevocation() *memRevocation { return &memRevocation{revoked: map[string]struct{}{}} }

func (m *memRevocation) RevokeToken(_ context.Context, jti string, _ time.Time) error {
	m.revoked[jti] = struct{}{}
	return nil
}
func (m *memRevocation) IsTokenRevoked(_ context.Context, jti string) (bool, error) {
	_, ok := m.revoked[jti]
	return ok, nil
}

// testFixture returns a running httptest.Server and an oidc.Server that shares the same
// signing key, so tests can issue valid JWTs for authenticated requests.
func testFixture(t *testing.T) (*httptest.Server, *oidc.Server) {
	return testFixtureWithConfig(t, nil)
}

func testFixtureWithConfig(t *testing.T, configure func(*server.Config)) (*httptest.Server, *oidc.Server) {
	t.Helper()

	privkey, err := ecdsa.GenerateKey(elliptic.P384(), rand.Reader)
	require.NoError(t, err)
	raw, err := jwk.Import(privkey)
	require.NoError(t, err)

	revocation := newMemRevocation()

	oidcSrv, err := oidc.NewServer(oidc.Config{
		BaseURL:    "http://localhost",
		AuthState:  newMemAuthState(),
		Revocation: revocation,
	}, raw)
	require.NoError(t, err)

	cfg := server.Config{
		BaseURL:    "http://localhost",
		PrivKey:    raw,
		Logger:     slog.Default(),
		AuthState:  newMemAuthState(),
		Revocation: revocation,
	}
	if configure != nil {
		configure(&cfg)
	}
	srv, err := server.New(cfg)
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

func TestSentryHandler_ConfiguredWrapsRequests(t *testing.T) {
	var wrapped bool
	ts, _ := testFixtureWithConfig(t, func(cfg *server.Config) {
		cfg.SentryHandler = func(next http.Handler) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				wrapped = true
				w.Header().Set("X-Sentry-Wrapped", "true")
				next.ServeHTTP(w, r)
			})
		}
	})

	resp, err := http.Get(ts.URL + "/health")
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	require.True(t, wrapped)
	require.Equal(t, "true", resp.Header.Get("X-Sentry-Wrapped"))
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

	tokenString, err := oidcSrv.IssueJWT("12345678", "insights:read", uuid.New().String(), time.Now().Add(time.Hour))
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
