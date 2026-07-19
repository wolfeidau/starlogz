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
	"github.com/wolfeidau/starlogz/internal/wideevent"
)

type captureEventPublisher struct {
	events []wideevent.Event
}

func (p *captureEventPublisher) Publish(_ context.Context, event wideevent.Event) error {
	p.events = append(p.events, event)
	return nil
}

// memAuthState is a minimal in-memory AuthStateStore for tests.
type memAuthState struct {
	pending       map[string]store.PendingAuth
	codes         map[string]store.AuthCode
	confirmations map[string]store.AuthorizationConfirmation
}

func newMemAuthState() *memAuthState {
	return &memAuthState{pending: map[string]store.PendingAuth{}, codes: map[string]store.AuthCode{}, confirmations: map[string]store.AuthorizationConfirmation{}}
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
func (m *memAuthState) StoreAuthorizationConfirmation(_ context.Context, tokenHash []byte, c store.AuthorizationConfirmation) error {
	m.confirmations[string(tokenHash)] = c
	return nil
}
func (m *memAuthState) CompleteAuthorizationConfirmation(_ context.Context, tokenHash []byte, approve bool, code string) (*store.AuthorizationConfirmationResult, error) {
	c, ok := m.confirmations[string(tokenHash)]
	if !ok {
		return nil, store.ErrNotFound
	}
	delete(m.confirmations, string(tokenHash))
	if approve {
		m.codes[code] = c.AuthCode
	}
	return &store.AuthorizationConfirmationResult{RedirectURI: c.RedirectURI, ClientState: c.ClientState}, nil
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
	t.Helper()
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

func TestNewRejectsUISessionIdleTTLAboveAbsoluteTTL(t *testing.T) {
	privkey, err := ecdsa.GenerateKey(elliptic.P384(), rand.Reader)
	require.NoError(t, err)
	raw, err := jwk.Import(privkey)
	require.NoError(t, err)

	_, err = server.New(server.Config{
		BaseURL: "http://localhost", PrivKey: raw, Logger: slog.Default(),
		AuthState: newMemAuthState(), Revocation: newMemRevocation(),
		UISessionIdleTTL: 2 * time.Hour, UISessionTTL: time.Hour,
	})
	require.EqualError(t, err, "UI session idle TTL must not exceed absolute TTL")
}

func TestCoreHTTPFlowsEmitBoundedCompletionEvents(t *testing.T) {
	publisher := &captureEventPublisher{}
	emitter, err := wideevent.NewEmitter(publisher, "test", "devel", slog.New(slog.DiscardHandler))
	require.NoError(t, err)
	ts, _ := testFixtureWithConfig(t, func(cfg *server.Config) {
		cfg.Events = emitter
	})
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}}

	tests := []struct {
		method string
		path   string
		body   string
	}{
		{method: http.MethodGet, path: "/oauth2/authorize?response_type=invalid"},
		{method: http.MethodGet, path: "/auth/github/callback"},
		{method: http.MethodPost, path: "/oauth2/authorize/confirm", body: "token=invalid&decision=approve"},
		{method: http.MethodPost, path: "/oauth2/token", body: "grant_type=authorization_code"},
		{method: http.MethodPost, path: "/oauth2/token", body: "grant_type=refresh_token"},
		{method: http.MethodGet, path: "/login"},
		{method: http.MethodGet, path: "/ui/auth/callback"},
		{method: http.MethodPost, path: "/logout"},
	}
	for _, tc := range tests {
		req, reqErr := http.NewRequest(tc.method, ts.URL+tc.path, strings.NewReader(tc.body))
		require.NoError(t, reqErr)
		if tc.body != "" {
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		}
		resp, reqErr := client.Do(req)
		require.NoError(t, reqErr)
		require.NoError(t, resp.Body.Close())
	}

	require.Len(t, publisher.events, 8)
	require.Equal(t, []wideevent.Name{
		wideevent.OAuthAuthorizationCompleted,
		wideevent.OAuthGitHubCallbackCompleted,
		wideevent.OAuthAuthorizationConfirmationCompleted,
		wideevent.OAuthTokenExchangeCompleted,
		wideevent.OAuthRefreshCompleted,
		wideevent.UILoginCompleted,
		wideevent.UISessionCreated,
		wideevent.UISessionRevoked,
	}, eventNames(publisher.events))
	for _, event := range publisher.events {
		require.NoError(t, event.Validate())
		require.NotEmpty(t, event.RequestID)
	}
	require.Equal(t, wideevent.OutcomeFailure, publisher.events[2].Outcome)
	require.Equal(t, wideevent.OutcomeSuccess, publisher.events[7].Outcome)
}

func TestUnclassifiedTokenRequestsDoNotEmitWideEvents(t *testing.T) {
	publisher := &captureEventPublisher{}
	emitter, err := wideevent.NewEmitter(publisher, "test", "devel", slog.New(slog.DiscardHandler))
	require.NoError(t, err)
	ts, _ := testFixtureWithConfig(t, func(cfg *server.Config) {
		cfg.Events = emitter
	})

	tests := []struct {
		name       string
		method     string
		body       string
		wantStatus int
	}{
		{name: "wrong method", method: http.MethodGet, wantStatus: http.StatusMethodNotAllowed},
		{name: "unsupported grant", method: http.MethodPost, body: "grant_type=client_credentials", wantStatus: http.StatusBadRequest},
		{name: "unparseable form", method: http.MethodPost, body: "%", wantStatus: http.StatusBadRequest},
		{name: "oversized form", method: http.MethodPost, body: strings.Repeat("x", (1<<20)+1), wantStatus: http.StatusBadRequest},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			req, reqErr := http.NewRequest(test.method, ts.URL+"/oauth2/token", strings.NewReader(test.body))
			require.NoError(t, reqErr)
			if test.method == http.MethodPost {
				req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			}
			resp, reqErr := http.DefaultClient.Do(req)
			require.NoError(t, reqErr)
			defer func() { require.NoError(t, resp.Body.Close()) }()
			require.Equal(t, test.wantStatus, resp.StatusCode)
		})
	}

	require.Empty(t, publisher.events)
}

func eventNames(events []wideevent.Event) []wideevent.Name {
	names := make([]wideevent.Name, len(events))
	for i, event := range events {
		names[i] = event.EventName
	}
	return names
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

func TestDashboard_Route(t *testing.T) {
	ts, _ := testFixture(t)

	resp, err := http.Get(ts.URL + "/dashboard")
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Contains(t, resp.Header.Get("Content-Type"), "text/html")
}

func TestPublicAsset_Route(t *testing.T) {
	ts, _ := testFixture(t)

	resp, err := http.Get(ts.URL + "/public/dashboard.js")
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Contains(t, resp.Header.Get("Content-Type"), "javascript")
}

func TestGlobalSecurityHeadersCoverRoutesAndPreflights(t *testing.T) {
	ts, _ := testFixture(t)
	tests := []struct {
		method string
		path   string
	}{
		{method: http.MethodGet, path: "/dashboard"},
		{method: http.MethodGet, path: "/health"},
		{method: http.MethodGet, path: "/public/dashboard.js"},
		{method: http.MethodGet, path: "/missing"},
		{method: http.MethodOptions, path: "/oauth2/token"},
	}
	for _, test := range tests {
		req, err := http.NewRequest(test.method, ts.URL+test.path, nil)
		require.NoError(t, err)
		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		require.NoError(t, resp.Body.Close())
		require.Equal(t, "nosniff", resp.Header.Get("X-Content-Type-Options"))
		require.Equal(t, "DENY", resp.Header.Get("X-Frame-Options"))
		require.Equal(t, "no-referrer", resp.Header.Get("Referrer-Policy"))
		require.NotEmpty(t, resp.Header.Get("Content-Security-Policy"))
		require.Empty(t, resp.Header.Get("Strict-Transport-Security"))
	}
}

func TestHSTSUsesConfiguredServerURL(t *testing.T) {
	ts, _ := testFixtureWithConfig(t, func(cfg *server.Config) {
		cfg.BaseURL = "https://starlogz.example"
	})
	resp, err := http.Get(ts.URL + "/health")
	require.NoError(t, err)
	defer func() { require.NoError(t, resp.Body.Close()) }()
	require.Equal(t, "max-age=31536000", resp.Header.Get("Strict-Transport-Security"))
}

func TestInteractiveAuthResponsesAreNotCached(t *testing.T) {
	ts, _ := testFixture(t)
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}}
	tests := []struct {
		method, path, body string
	}{
		{method: http.MethodGet, path: "/login"},
		{method: http.MethodGet, path: "/ui/auth/callback"},
		{method: http.MethodPost, path: "/logout"},
		{method: http.MethodGet, path: "/oauth2/authorize?response_type=invalid"},
		{method: http.MethodGet, path: "/auth/github/callback"},
		{method: http.MethodPost, path: "/auth/logout"},
		{method: http.MethodPost, path: "/oauth2/authorize/confirm", body: "token=invalid&decision=approve"},
	}
	for _, test := range tests {
		req, err := http.NewRequest(test.method, ts.URL+test.path, strings.NewReader(test.body))
		require.NoError(t, err)
		if test.body != "" {
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		}
		resp, err := client.Do(req)
		require.NoError(t, err)
		require.NoError(t, resp.Body.Close())
		require.Equal(t, "no-store", resp.Header.Get("Cache-Control"), test.path)
	}
}

func TestUIRPC_MissingSession(t *testing.T) {
	ts, _ := testFixture(t)

	resp, err := http.Post(ts.URL+"/starlogz.v1.UIService/GetSession", "application/json", strings.NewReader("{}"))
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	require.Equal(t, http.StatusUnauthorized, resp.StatusCode)
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
