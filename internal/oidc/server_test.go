package oidc

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/lestrrat-go/jwx/v4/jwa"
	"github.com/lestrrat-go/jwx/v4/jwk"
	"github.com/lestrrat-go/jwx/v4/jwt"
	"github.com/modelcontextprotocol/go-sdk/auth"
	"github.com/modelcontextprotocol/go-sdk/oauthex"
	"github.com/stretchr/testify/require"
	"github.com/wolfeidau/starlogz/internal/ctxlog"
	"github.com/wolfeidau/starlogz/internal/logattr"
	"github.com/wolfeidau/starlogz/internal/store"
	"golang.org/x/oauth2"
)

// --- In-memory test implementations ---

type inMemAuthState struct {
	mu                      sync.Mutex
	pending                 map[string]store.PendingAuth
	codes                   map[string]store.AuthCode
	confirmations           map[string]store.AuthorizationConfirmation
	storeConfirmationErr    error
	completeConfirmationErr error
}

func newInMemAuthState() *inMemAuthState {
	return &inMemAuthState{
		pending:       make(map[string]store.PendingAuth),
		codes:         make(map[string]store.AuthCode),
		confirmations: make(map[string]store.AuthorizationConfirmation),
	}
}

func (s *inMemAuthState) StorePendingAuth(_ context.Context, state string, p store.PendingAuth) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pending[state] = p
	return nil
}

func (s *inMemAuthState) ConsumePendingAuth(_ context.Context, state string) (*store.PendingAuth, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	p, ok := s.pending[state]
	if !ok {
		return nil, store.ErrNotFound
	}
	delete(s.pending, state)
	return &p, nil
}

func (s *inMemAuthState) StoreAuthCode(_ context.Context, code string, c store.AuthCode) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.codes[code] = c
	return nil
}

func (s *inMemAuthState) StoreAuthorizationConfirmation(_ context.Context, tokenHash []byte, c store.AuthorizationConfirmation) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.storeConfirmationErr != nil {
		return s.storeConfirmationErr
	}
	s.confirmations[string(tokenHash)] = c
	return nil
}

func (s *inMemAuthState) CompleteAuthorizationConfirmation(_ context.Context, tokenHash []byte, approve bool, code string) (*store.AuthorizationConfirmationResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.completeConfirmationErr != nil {
		return nil, s.completeConfirmationErr
	}
	c, ok := s.confirmations[string(tokenHash)]
	if !ok {
		return nil, store.ErrNotFound
	}
	delete(s.confirmations, string(tokenHash))
	if approve {
		s.codes[code] = c.AuthCode
	}
	return &store.AuthorizationConfirmationResult{RedirectURI: c.RedirectURI, ClientState: c.ClientState}, nil
}

func (s *inMemAuthState) ConsumeAuthCode(_ context.Context, code string) (*store.AuthCode, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	c, ok := s.codes[code]
	if !ok {
		return nil, store.ErrNotFound
	}
	delete(s.codes, code)
	return &c, nil
}

type inMemRevocation struct {
	mu      sync.RWMutex
	revoked map[string]time.Time
}

func newInMemRevocation() *inMemRevocation {
	return &inMemRevocation{revoked: make(map[string]time.Time)}
}

func (r *inMemRevocation) RevokeToken(_ context.Context, jti string, expiresAt time.Time) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.revoked[jti] = expiresAt
	return nil
}

func (r *inMemRevocation) IsTokenRevoked(_ context.Context, jti string) (bool, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	exp, ok := r.revoked[jti]
	if !ok {
		return false, nil
	}
	return exp.After(time.Now()), nil
}

func newTestOIDCServer(t *testing.T) *Server {
	t.Helper()
	raw := newTestJWK(t)
	srv, err := NewServer(Config{
		BaseURL:    "http://example.com",
		AuthState:  newInMemAuthState(),
		Revocation: newInMemRevocation(),
	}, raw)
	require.NoError(t, err)
	return srv
}

func newTestJWK(t *testing.T) jwk.Key {
	t.Helper()
	privkey, err := ecdsa.GenerateKey(elliptic.P384(), rand.Reader)
	require.NoError(t, err)
	raw, err := jwk.Import[jwk.Key](privkey)
	require.NoError(t, err)
	return raw
}

func pkceChallenge(verifier string) string {
	h := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(h[:])
}

func TestDurationOrDefault(t *testing.T) {
	def := 30 * time.Second
	require.Equal(t, def, durationOrDefault(nil, def))
	require.Equal(t, time.Duration(0), durationOrDefault(durationPtr(0), def))
	require.Equal(t, 2*time.Hour, durationOrDefault(durationPtr(2*time.Hour), def))
}

func TestNewServer_RefreshTokenDurationDefaults(t *testing.T) {
	srv := newTestOIDCServer(t)
	require.Equal(t, DefaultRefreshTokenGracePeriod, srv.refreshTokenGracePeriod)
	require.Equal(t, DefaultRetiredRefreshTokenRetention, srv.retiredRefreshTokenRetention)
}

func TestNewServer_RefreshTokenDurationOverrides(t *testing.T) {
	grace := time.Duration(0)
	retention := 2 * time.Hour
	srv, err := NewServer(Config{
		BaseURL:                      "http://example.com",
		AuthState:                    newInMemAuthState(),
		Revocation:                   newInMemRevocation(),
		RefreshTokenGracePeriod:      &grace,
		RetiredRefreshTokenRetention: &retention,
	}, newTestJWK(t))
	require.NoError(t, err)
	require.Equal(t, time.Duration(0), srv.refreshTokenGracePeriod)
	require.Equal(t, 2*time.Hour, srv.retiredRefreshTokenRetention)
}

func TestNewServer_RefreshTokenDurationValidation(t *testing.T) {
	cases := []struct {
		name      string
		grace     *time.Duration
		retention *time.Duration
	}{
		{name: "negative grace", grace: durationPtr(-time.Second)},
		{name: "grace too high", grace: durationPtr(61 * time.Second)},
		{name: "zero retention", retention: durationPtr(0)},
		{name: "retention less than grace", grace: durationPtr(10 * time.Second), retention: durationPtr(5 * time.Second)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := NewServer(Config{
				BaseURL:                      "http://example.com",
				AuthState:                    newInMemAuthState(),
				Revocation:                   newInMemRevocation(),
				RefreshTokenGracePeriod:      tc.grace,
				RetiredRefreshTokenRetention: tc.retention,
			}, newTestJWK(t))
			require.Error(t, err)
		})
	}
}

func durationPtr(d time.Duration) *time.Duration {
	return &d
}

// --- Discovery ---

func TestDiscoveryHandler_ReturnsAuthServerMeta(t *testing.T) {
	srv := newTestOIDCServer(t)
	req := httptest.NewRequest(http.MethodGet, "/.well-known/oauth-authorization-server", nil)
	w := httptest.NewRecorder()
	srv.DiscoveryHandler().ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	require.Equal(t, "application/json", w.Header().Get("Content-Type"))
	require.Equal(t, "public, max-age=86400", w.Header().Get("Cache-Control"))

	var meta map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &meta))
	require.Equal(t, "http://example.com", meta["issuer"])
	require.Equal(t, "http://example.com/oauth2/authorize", meta["authorization_endpoint"])
	require.Equal(t, "http://example.com/oauth2/token", meta["token_endpoint"])
	require.Equal(t, "http://example.com/oauth2/register", meta["registration_endpoint"])
	require.Equal(t, "http://example.com/.well-known/jwks", meta["jwks_uri"])

	methods, ok := meta["code_challenge_methods_supported"].([]any)
	require.True(t, ok)
	require.Contains(t, methods, "S256")
}

func TestDiscoveryHandler_Options_NoContent(t *testing.T) {
	srv := newTestOIDCServer(t)
	req := httptest.NewRequest(http.MethodOptions, "/.well-known/oauth-authorization-server", nil)
	w := httptest.NewRecorder()
	srv.DiscoveryHandler().ServeHTTP(w, req)
	require.Equal(t, http.StatusNoContent, w.Code)
}

func TestDiscoveryHandler_WrongMethod(t *testing.T) {
	srv := newTestOIDCServer(t)
	req := httptest.NewRequest(http.MethodPost, "/.well-known/oauth-authorization-server", nil)
	w := httptest.NewRecorder()
	srv.DiscoveryHandler().ServeHTTP(w, req)
	require.Equal(t, http.StatusMethodNotAllowed, w.Code)
}

// --- JWKS ---

func TestJWKSHandler_ReturnsEC_P384_Key(t *testing.T) {
	srv := newTestOIDCServer(t)
	req := httptest.NewRequest(http.MethodGet, "/.well-known/jwks", nil)
	w := httptest.NewRecorder()
	srv.JWKSHandler().ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	require.Equal(t, "application/json", w.Header().Get("Content-Type"))
	require.Equal(t, "public, max-age=86400", w.Header().Get("Cache-Control"))

	var keySet struct {
		Keys []map[string]any `json:"keys"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &keySet))
	require.Len(t, keySet.Keys, 1)

	k := keySet.Keys[0]
	require.Equal(t, "EC", k["kty"])
	require.Equal(t, "P-384", k["crv"])
	require.Equal(t, "sig", k["use"])
	require.Equal(t, "ES384", k["alg"])
	require.NotEmpty(t, k["kid"])
	require.NotEmpty(t, k["x"])
	require.NotEmpty(t, k["y"])
}

func TestJWKSHandler_WrongMethod(t *testing.T) {
	srv := newTestOIDCServer(t)
	req := httptest.NewRequest(http.MethodPost, "/.well-known/jwks", nil)
	w := httptest.NewRecorder()
	srv.JWKSHandler().ServeHTTP(w, req)
	require.Equal(t, http.StatusMethodNotAllowed, w.Code)
}

// --- Spec compliance: RFC 8414 Authorization Server Metadata ---

func TestDiscoveryHandler_RFC8414_SpecCompliance(t *testing.T) {
	srv := newTestOIDCServer(t)
	req := httptest.NewRequest(http.MethodGet, "/.well-known/oauth-authorization-server", nil)
	w := httptest.NewRecorder()
	srv.DiscoveryHandler().ServeHTTP(w, req)
	require.Equal(t, http.StatusOK, w.Code)

	var meta oauthex.AuthServerMeta
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &meta))

	require.NotEmpty(t, meta.Issuer)

	for _, ep := range []string{
		meta.AuthorizationEndpoint,
		meta.TokenEndpoint,
		meta.JWKSURI,
		meta.RegistrationEndpoint,
	} {
		require.NotEmpty(t, ep)
		require.True(t, strings.HasPrefix(ep, meta.Issuer), "endpoint %q must be under issuer %q", ep, meta.Issuer)
	}

	require.Equal(t, []string{"code"}, meta.ResponseTypesSupported)
	require.Equal(t, []string{"authorization_code", "refresh_token"}, meta.GrantTypesSupported)
	require.Equal(t, []string{"S256"}, meta.CodeChallengeMethodsSupported)
	require.Equal(t, []string{"none"}, meta.TokenEndpointAuthMethodsSupported)
	require.ElementsMatch(t, []string{"insights:read", "insights:write", "org:admin"}, meta.ScopesSupported)
}

// --- Spec compliance: RFC 9728 Protected Resource Metadata ---

func TestProtectedResourceMetadata_RFC9728_SpecCompliance(t *testing.T) {
	srv := newTestOIDCServer(t)
	req := httptest.NewRequest(http.MethodGet, "/.well-known/oauth-protected-resource", nil)
	w := httptest.NewRecorder()
	auth.ProtectedResourceMetadataHandler(srv.ProtectedResourceMeta()).ServeHTTP(w, req)
	require.Equal(t, http.StatusOK, w.Code)

	var meta oauthex.ProtectedResourceMetadata
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &meta))

	require.NotEmpty(t, meta.Resource)
	require.True(t, strings.HasSuffix(meta.Resource, "/mcp"), "resource %q must end in /mcp", meta.Resource)

	require.Len(t, meta.AuthorizationServers, 1)
	issuer := meta.AuthorizationServers[0]
	require.NotContains(t, issuer, ".well-known", "authorization_servers must be the issuer, not the discovery URL")

	require.True(t, strings.HasPrefix(meta.Resource, issuer), "resource %q must be under issuer %q", meta.Resource, issuer)
	require.Equal(t, []string{"header"}, meta.BearerMethodsSupported)
	require.ElementsMatch(t, []string{"insights:read", "insights:write", "org:admin"}, meta.ScopesSupported)
}

// --- Spec compliance: RFC 7517 JWKS kid cross-document consistency ---

func TestJWKS_KidMatchesJWTHeader(t *testing.T) {
	srv := newTestOIDCServer(t)

	req := httptest.NewRequest(http.MethodGet, "/.well-known/jwks", nil)
	w := httptest.NewRecorder()
	srv.JWKSHandler().ServeHTTP(w, req)
	require.Equal(t, http.StatusOK, w.Code)

	var keySet struct {
		Keys []struct {
			Kid string `json:"kid"`
			Kty string `json:"kty"`
		} `json:"keys"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &keySet))
	require.Len(t, keySet.Keys, 1)
	jwksKid := keySet.Keys[0].Kid
	require.NotEmpty(t, jwksKid)

	tokenString, err := srv.IssueJWT("12345678", "insights:read", uuid.New().String(), time.Now().Add(time.Hour))
	require.NoError(t, err)

	parts := strings.SplitN(tokenString, ".", 3)
	require.Len(t, parts, 3, "JWT must have three dot-separated parts")

	headerJSON, err := base64.RawURLEncoding.DecodeString(parts[0])
	require.NoError(t, err)

	var jwtHeader struct {
		Alg string `json:"alg"`
		Kid string `json:"kid"`
	}
	require.NoError(t, json.Unmarshal(headerJSON, &jwtHeader))

	require.Equal(t, "ES384", jwtHeader.Alg)
	require.Equal(t, jwksKid, jwtHeader.Kid, "JWT kid header must match the kid in the JWKS")
}

// --- DCR ---

func TestDCRHandler_Success(t *testing.T) {
	srv := newTestOIDCServer(t)

	body := `{"redirect_uris":["https://client.example.com/callback"],"client_name":"Test Client","grant_types":["authorization_code","refresh_token"],"response_types":["code"],"token_endpoint_auth_method":"none","scope":"insights:read insights:write"}`
	req := httptest.NewRequest(http.MethodPost, "/oauth2/register", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.DCRHandler().ServeHTTP(w, req)

	require.Equal(t, http.StatusCreated, w.Code)

	var resp map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	require.NotEmpty(t, resp["client_id"])
	require.Empty(t, resp["client_secret"])
	require.NotZero(t, resp["client_id_issued_at"])

	grants, ok := resp["grant_types"].([]any)
	require.True(t, ok)
	require.Equal(t, []any{"authorization_code", "refresh_token"}, grants)
}

func TestDCRHandler_MissingRedirectURIs(t *testing.T) {
	srv := newTestOIDCServer(t)

	body := `{"client_name":"Test Client"}`
	req := httptest.NewRequest(http.MethodPost, "/oauth2/register", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.DCRHandler().ServeHTTP(w, req)

	require.Equal(t, http.StatusBadRequest, w.Code)
	var errResp map[string]string
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &errResp))
	require.Equal(t, "invalid_client_metadata", errResp["error"])
	require.Contains(t, errResp["error_description"], "redirect_uris")
}

func TestDCRHandler_UnsupportedAuthMethod(t *testing.T) {
	srv := newTestOIDCServer(t)

	body := `{"redirect_uris":["https://client.example.com/callback"],"token_endpoint_auth_method":"client_secret_basic"}`
	req := httptest.NewRequest(http.MethodPost, "/oauth2/register", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.DCRHandler().ServeHTTP(w, req)

	require.Equal(t, http.StatusBadRequest, w.Code)
	var errResp map[string]string
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &errResp))
	require.Equal(t, "invalid_client_metadata", errResp["error"])
}

func TestDCRHandler_DefaultsApplied(t *testing.T) {
	srv := newTestOIDCServer(t)

	body := `{"redirect_uris":["https://client.example.com/callback"]}`
	req := httptest.NewRequest(http.MethodPost, "/oauth2/register", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.DCRHandler().ServeHTTP(w, req)

	require.Equal(t, http.StatusCreated, w.Code)

	var resp map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	require.Equal(t, []any{"authorization_code", "refresh_token"}, resp["grant_types"])
	require.Equal(t, []any{"code"}, resp["response_types"])
	require.Equal(t, "none", resp["token_endpoint_auth_method"])
	require.Equal(t, defaultRegisteredClientScope, resp["scope"])
}

func TestDCRHandler_WrongMethod(t *testing.T) {
	srv := newTestOIDCServer(t)
	req := httptest.NewRequest(http.MethodGet, "/oauth2/register", nil)
	w := httptest.NewRecorder()
	srv.DCRHandler().ServeHTTP(w, req)
	require.Equal(t, http.StatusMethodNotAllowed, w.Code)
}

func TestDCRHandler_UnknownScope(t *testing.T) {
	srv := newTestOIDCServer(t)

	body := `{"redirect_uris":["https://client.example.com/callback"],"scope":"insights:read unknown:scope"}`
	req := httptest.NewRequest(http.MethodPost, "/oauth2/register", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.DCRHandler().ServeHTTP(w, req)

	require.Equal(t, http.StatusBadRequest, w.Code)
	var errResp map[string]string
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &errResp))
	require.Equal(t, "invalid_client_metadata", errResp["error"])
	require.Contains(t, errResp["error_description"], "unknown:scope")
}

func TestDCRHandler_RejectsUnsupportedGrantType(t *testing.T) {
	srv := newTestOIDCServer(t)
	body := `{"redirect_uris":["https://client.example.com/callback"],"grant_types":["implicit"]}`
	req := httptest.NewRequest(http.MethodPost, "/oauth2/register", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.DCRHandler().ServeHTTP(w, req)

	require.Equal(t, http.StatusBadRequest, w.Code)
	require.Contains(t, w.Body.String(), "unsupported grant_type")
}

func TestDCRHandler_RejectsUnsupportedResponseType(t *testing.T) {
	srv := newTestOIDCServer(t)
	body := `{"redirect_uris":["https://client.example.com/callback"],"response_types":["token"]}`
	req := httptest.NewRequest(http.MethodPost, "/oauth2/register", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.DCRHandler().ServeHTTP(w, req)

	require.Equal(t, http.StatusBadRequest, w.Code)
	require.Contains(t, w.Body.String(), "unsupported response_type")
}

func TestDCRHandler_AcceptsFieldLimits(t *testing.T) {
	srv := newTestOIDCServer(t)
	prefix := "https://client.example.com/"
	redirectURIs := []string{prefix + strings.Repeat("a", maxRedirectURILen-len(prefix))}
	for i := 1; i < maxRedirectURIs; i++ {
		redirectURIs = append(redirectURIs, fmt.Sprintf("https://client%d.example.com/callback", i))
	}
	body, err := json.Marshal(map[string]any{
		"redirect_uris": redirectURIs,
		"client_name":   strings.Repeat("n", maxClientNameLen),
	})
	require.NoError(t, err)
	req := httptest.NewRequest(http.MethodPost, "/oauth2/register", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.DCRHandler().ServeHTTP(w, req)

	require.Equal(t, http.StatusCreated, w.Code)
}

func TestDCRHandler_RejectsFieldLimits(t *testing.T) {
	prefix := "https://client.example.com/"
	tests := []struct {
		name      string
		metadata  map[string]any
		wantError string
	}{
		{
			name: "too many redirect URIs",
			metadata: map[string]any{
				"redirect_uris": func() []string {
					uris := make([]string, maxRedirectURIs+1)
					for i := range uris {
						uris[i] = fmt.Sprintf("https://client%d.example.com/callback", i)
					}
					return uris
				}(),
			},
			wantError: "at most 10 entries",
		},
		{
			name: "redirect URI too long",
			metadata: map[string]any{
				"redirect_uris": []string{prefix + strings.Repeat("a", maxRedirectURILen-len(prefix)+1)},
			},
			wantError: "between 1 and 2048 bytes",
		},
		{
			name: "client name too long",
			metadata: map[string]any{
				"redirect_uris": []string{"https://client.example.com/callback"},
				"client_name":   strings.Repeat("n", maxClientNameLen+1),
			},
			wantError: "client_name must be at most 256 bytes",
		},
		{
			name: "scope too long",
			metadata: map[string]any{
				"redirect_uris": []string{"https://client.example.com/callback"},
				"scope":         strings.Repeat("s", maxClientScopeLen+1),
			},
			wantError: "scope must be at most 1024 bytes",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := newTestOIDCServer(t)
			body, err := json.Marshal(tt.metadata)
			require.NoError(t, err)
			req := httptest.NewRequest(http.MethodPost, "/oauth2/register", bytes.NewReader(body))
			req.Header.Set("Content-Type", "application/json")
			w := httptest.NewRecorder()
			srv.DCRHandler().ServeHTTP(w, req)

			require.Equal(t, http.StatusBadRequest, w.Code)
			require.Contains(t, w.Body.String(), tt.wantError)
		})
	}
}

func TestDCRHandler_RejectsInvalidContentType(t *testing.T) {
	srv := newTestOIDCServer(t)
	req := httptest.NewRequest(http.MethodPost, "/oauth2/register", strings.NewReader(`{"redirect_uris":["https://client.example.com/callback"]}`))
	req.Header.Set("Content-Type", "text/plain")
	w := httptest.NewRecorder()
	srv.DCRHandler().ServeHTTP(w, req)

	require.Equal(t, http.StatusBadRequest, w.Code)
	require.Contains(t, w.Body.String(), "Content-Type must be application/json")
}

func TestDCRHandler_RejectsTrailingJSON(t *testing.T) {
	srv := newTestOIDCServer(t)
	body := `{"redirect_uris":["https://client.example.com/callback"]} {}`
	req := httptest.NewRequest(http.MethodPost, "/oauth2/register", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.DCRHandler().ServeHTTP(w, req)

	require.Equal(t, http.StatusBadRequest, w.Code)
	require.Contains(t, w.Body.String(), "one JSON object")
}

func TestDCRHandler_RejectsOversizedBody(t *testing.T) {
	srv := newTestOIDCServer(t)
	body := `{"redirect_uris":["https://client.example.com/callback"],"client_name":"` + strings.Repeat("x", maxDCRBodyBytes) + `"}`
	req := httptest.NewRequest(http.MethodPost, "/oauth2/register", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.DCRHandler().ServeHTTP(w, req)

	require.Equal(t, http.StatusBadRequest, w.Code)
	require.Contains(t, w.Body.String(), "request body too large")
}

// --- Authorize ---

func TestAuthorizeHandler_ValidParams_RedirectsToGitHub(t *testing.T) {
	srv := newTestOIDCServer(t)

	verifier := "dBjftJeZ4CVP-mB92K27uhbUJU1p1r_wW1gFWFOEjXk"
	q := url.Values{
		"response_type":         {"code"},
		"client_id":             {"test-client"},
		"redirect_uri":          {"https://client.example.com/callback"},
		"scope":                 {"insights:read"},
		"state":                 {"random-state-xyz"},
		"code_challenge":        {pkceChallenge(verifier)},
		"code_challenge_method": {"S256"},
	}

	req := httptest.NewRequest(http.MethodGet, "/oauth2/authorize?"+q.Encode(), nil)
	w := httptest.NewRecorder()
	srv.AuthorizeHandler().ServeHTTP(w, req)

	require.Equal(t, http.StatusFound, w.Code)
	require.Contains(t, w.Header().Get("Location"), "github.com")
}

func TestAuthorizeHandler_DefaultsScope(t *testing.T) {
	srv := newTestOIDCServer(t)

	q := url.Values{
		"response_type":         {"code"},
		"client_id":             {"test-client"},
		"redirect_uri":          {"https://client.example.com/callback"},
		"code_challenge":        {pkceChallenge("dBjftJeZ4CVP-mB92K27uhbUJU1p1r_wW1gFWFOEjXk")},
		"code_challenge_method": {"S256"},
	}

	req := httptest.NewRequest(http.MethodGet, "/oauth2/authorize?"+q.Encode(), nil)
	w := httptest.NewRecorder()
	srv.AuthorizeHandler().ServeHTTP(w, req)

	require.Equal(t, http.StatusFound, w.Code)
	location, err := url.Parse(w.Header().Get("Location"))
	require.NoError(t, err)
	pending, err := srv.authState.ConsumePendingAuth(t.Context(), location.Query().Get("state"))
	require.NoError(t, err)
	require.Equal(t, defaultAuthorizeScope, pending.Scope)
}

func TestAuthorizeHandler_UsesRegisteredScopeWhenRequestOmitsScope(t *testing.T) {
	srv := newTestOIDCServer(t)
	clients := &testClientStore{records: []store.OAuthClient{{
		ClientID:     "registered-client",
		RedirectURIs: []string{"https://client.example.com/callback"},
		Scope:        "insights:read insights:write",
	}}}
	srv.clients = clients

	q := url.Values{
		"response_type":         {"code"},
		"client_id":             {"registered-client"},
		"redirect_uri":          {"https://client.example.com/callback"},
		"code_challenge":        {pkceChallenge("dBjftJeZ4CVP-mB92K27uhbUJU1p1r_wW1gFWFOEjXk")},
		"code_challenge_method": {"S256"},
	}

	req := httptest.NewRequest(http.MethodGet, "/oauth2/authorize?"+q.Encode(), nil)
	w := httptest.NewRecorder()
	srv.AuthorizeHandler().ServeHTTP(w, req)

	require.Equal(t, http.StatusFound, w.Code)
	location, err := url.Parse(w.Header().Get("Location"))
	require.NoError(t, err)
	pending, err := srv.authState.ConsumePendingAuth(t.Context(), location.Query().Get("state"))
	require.NoError(t, err)
	require.Equal(t, "insights:read insights:write", pending.Scope)
	require.Equal(t, "", pending.ClientName)
	require.True(t, pending.ConfirmationRequired)
	require.Equal(t, []string{"registered-client"}, clients.touches)
}

func TestAuthorizeHandler_OnlyStarlogzUIBypassesConfirmation(t *testing.T) {
	srv := newTestOIDCServer(t)
	srv.clients = &testClientStore{records: []store.OAuthClient{
		{ClientID: "starlogz-ui", ClientName: "Dashboard", RedirectURIs: []string{"http://example.com/ui/auth/callback"}, Scope: "insights:read"},
		{ClientID: "external", ClientName: "starlogz-ui", RedirectURIs: []string{"https://client.example.com/callback"}, Scope: "insights:read"},
	}}

	for _, test := range []struct {
		clientID, redirectURI string
		wantConfirmation      bool
	}{
		{clientID: "starlogz-ui", redirectURI: "http://example.com/ui/auth/callback", wantConfirmation: false},
		{clientID: "external", redirectURI: "https://client.example.com/callback", wantConfirmation: true},
	} {
		q := url.Values{
			"response_type": {"code"}, "client_id": {test.clientID}, "redirect_uri": {test.redirectURI},
			"code_challenge": {pkceChallenge("verifier")}, "code_challenge_method": {"S256"},
		}
		req := httptest.NewRequest(http.MethodGet, "/oauth2/authorize?"+q.Encode(), nil)
		w := httptest.NewRecorder()
		srv.AuthorizeHandler().ServeHTTP(w, req)
		require.Equal(t, http.StatusFound, w.Code)
		location, err := url.Parse(w.Header().Get("Location"))
		require.NoError(t, err)
		pending, err := srv.authState.ConsumePendingAuth(t.Context(), location.Query().Get("state"))
		require.NoError(t, err)
		require.Equal(t, test.wantConfirmation, pending.ConfirmationRequired)
	}
}

func TestAuthorizeHandler_RejectsScopeOutsideRegisteredClient(t *testing.T) {
	srv := newTestOIDCServer(t)
	srv.clients = &testClientStore{records: []store.OAuthClient{{
		ClientID:     "read-only-client",
		RedirectURIs: []string{"https://client.example.com/callback"},
		Scope:        "insights:read",
	}}}

	q := url.Values{
		"response_type":         {"code"},
		"client_id":             {"read-only-client"},
		"redirect_uri":          {"https://client.example.com/callback"},
		"scope":                 {"insights:read insights:write"},
		"code_challenge":        {pkceChallenge("dBjftJeZ4CVP-mB92K27uhbUJU1p1r_wW1gFWFOEjXk")},
		"code_challenge_method": {"S256"},
	}

	req := httptest.NewRequest(http.MethodGet, "/oauth2/authorize?"+q.Encode(), nil)
	w := httptest.NewRecorder()
	srv.AuthorizeHandler().ServeHTTP(w, req)

	require.Equal(t, http.StatusBadRequest, w.Code)
	var errResp map[string]string
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &errResp))
	require.Equal(t, "invalid_scope", errResp["error"])
	require.Contains(t, errResp["error_description"], "insights:write")
}

func TestAuthorizeHandler_MissingCodeChallenge(t *testing.T) {
	srv := newTestOIDCServer(t)

	q := url.Values{
		"response_type": {"code"},
		"redirect_uri":  {"https://client.example.com/callback"},
	}

	req := httptest.NewRequest(http.MethodGet, "/oauth2/authorize?"+q.Encode(), nil)
	w := httptest.NewRecorder()
	srv.AuthorizeHandler().ServeHTTP(w, req)

	require.Equal(t, http.StatusBadRequest, w.Code)
	var errResp map[string]string
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &errResp))
	require.Equal(t, "invalid_request", errResp["error"])
}

func TestAuthorizeHandler_PlainChallengeMethodRejected(t *testing.T) {
	srv := newTestOIDCServer(t)

	q := url.Values{
		"response_type":         {"code"},
		"redirect_uri":          {"https://client.example.com/callback"},
		"code_challenge":        {"abc123"},
		"code_challenge_method": {"plain"},
	}

	req := httptest.NewRequest(http.MethodGet, "/oauth2/authorize?"+q.Encode(), nil)
	w := httptest.NewRecorder()
	srv.AuthorizeHandler().ServeHTTP(w, req)

	require.Equal(t, http.StatusBadRequest, w.Code)
	var errResp map[string]string
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &errResp))
	require.Equal(t, "invalid_request", errResp["error"])
}

func TestAuthorizeHandler_UnknownScope(t *testing.T) {
	srv := newTestOIDCServer(t)

	q := url.Values{
		"response_type":         {"code"},
		"redirect_uri":          {"https://client.example.com/callback"},
		"code_challenge":        {"abc123"},
		"code_challenge_method": {"S256"},
		"scope":                 {"insights:read unknown:scope"},
	}

	req := httptest.NewRequest(http.MethodGet, "/oauth2/authorize?"+q.Encode(), nil)
	w := httptest.NewRecorder()
	srv.AuthorizeHandler().ServeHTTP(w, req)

	require.Equal(t, http.StatusBadRequest, w.Code)
	var errResp map[string]string
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &errResp))
	require.Equal(t, "invalid_scope", errResp["error"])
}

func TestAuthorizeHandler_MissingRedirectURI(t *testing.T) {
	srv := newTestOIDCServer(t)

	q := url.Values{
		"response_type":         {"code"},
		"code_challenge":        {"abc123"},
		"code_challenge_method": {"S256"},
	}

	req := httptest.NewRequest(http.MethodGet, "/oauth2/authorize?"+q.Encode(), nil)
	w := httptest.NewRecorder()
	srv.AuthorizeHandler().ServeHTTP(w, req)

	require.Equal(t, http.StatusBadRequest, w.Code)
}

func TestAuthorizeHandler_WrongResponseType(t *testing.T) {
	srv := newTestOIDCServer(t)

	q := url.Values{
		"response_type":         {"token"},
		"redirect_uri":          {"https://client.example.com/callback"},
		"code_challenge":        {pkceChallenge("dBjftJeZ4CVP-mB92K27uhbUJU1p1r_wW1gFWFOEjXk")},
		"code_challenge_method": {"S256"},
	}

	req := httptest.NewRequest(http.MethodGet, "/oauth2/authorize?"+q.Encode(), nil)
	w := httptest.NewRecorder()
	srv.AuthorizeHandler().ServeHTTP(w, req)

	require.Equal(t, http.StatusBadRequest, w.Code)
	var errResp map[string]string
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &errResp))
	require.Equal(t, "unsupported_response_type", errResp["error"])
}

func TestAuthorizeHandler_WrongMethod(t *testing.T) {
	srv := newTestOIDCServer(t)
	req := httptest.NewRequest(http.MethodPost, "/oauth2/authorize", nil)
	w := httptest.NewRecorder()
	srv.AuthorizeHandler().ServeHTTP(w, req)
	require.Equal(t, http.StatusMethodNotAllowed, w.Code)
}

// --- Token ---

func TestTokenHandler_ValidExchange(t *testing.T) {
	srv := newTestOIDCServer(t)
	clients := &testClientStore{}
	srv.clients = clients

	verifier := "dBjftJeZ4CVP-mB92K27uhbUJU1p1r_wW1gFWFOEjXk"
	code := "test-auth-code-abc123"
	require.NoError(t, srv.authState.StoreAuthCode(t.Context(), code, store.AuthCode{
		ClientID:      "test-client",
		Sub:           "12345678",
		GitHubID:      12345678,
		Email:         "user@example.com",
		Scope:         "insights:read insights:write",
		CodeChallenge: pkceChallenge(verifier),
		RedirectURI:   "https://client.example.com/callback",
	}))

	form := url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {code},
		"code_verifier": {verifier},
		"client_id":     {"test-client"},
		"redirect_uri":  {"https://client.example.com/callback"},
	}

	req := httptest.NewRequest(http.MethodPost, "/oauth2/token", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	srv.TokenHandler().ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	require.Equal(t, "no-store", w.Header().Get("Cache-Control"))

	var resp map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	require.NotEmpty(t, resp["access_token"])
	require.Equal(t, "Bearer", resp["token_type"])
	require.Equal(t, "insights:read insights:write", resp["scope"])

	tokenString, ok := resp["access_token"].(string)
	require.True(t, ok)
	info, err := srv.VerifyJWT(t.Context(), tokenString, nil)
	require.NoError(t, err)
	require.Equal(t, "12345678", info.UserID)
	require.Equal(t, []string{"test-client"}, clients.touches)
}

func TestTokenHandler_CodeConsumedAfterUse(t *testing.T) {
	srv := newTestOIDCServer(t)

	verifier := "dBjftJeZ4CVP-mB92K27uhbUJU1p1r_wW1gFWFOEjXk"
	code := "one-time-code-xyz"
	require.NoError(t, srv.authState.StoreAuthCode(t.Context(), code, store.AuthCode{
		ClientID:      "test-client",
		Sub:           "12345678",
		GitHubID:      12345678,
		Email:         "user@example.com",
		Scope:         "insights:read",
		CodeChallenge: pkceChallenge(verifier),
		RedirectURI:   "https://client.example.com/callback",
	}))

	form := url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {code},
		"code_verifier": {verifier},
		"client_id":     {"test-client"},
		"redirect_uri":  {"https://client.example.com/callback"},
	}

	// First use succeeds
	req := httptest.NewRequest(http.MethodPost, "/oauth2/token", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	srv.TokenHandler().ServeHTTP(w, req)
	require.Equal(t, http.StatusOK, w.Code)

	// Second use with same code must fail
	req = httptest.NewRequest(http.MethodPost, "/oauth2/token", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w = httptest.NewRecorder()
	srv.TokenHandler().ServeHTTP(w, req)
	require.Equal(t, http.StatusBadRequest, w.Code)
	var errResp map[string]string
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &errResp))
	require.Equal(t, "invalid_grant", errResp["error"])
}

func TestTokenHandler_InvalidCode(t *testing.T) {
	srv := newTestOIDCServer(t)

	form := url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {"nonexistent-code"},
		"code_verifier": {"dBjftJeZ4CVP-mB92K27uhbUJU1p1r_wW1gFWFOEjXk"},
		"client_id":     {"test-client"},
	}

	req := httptest.NewRequest(http.MethodPost, "/oauth2/token", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	srv.TokenHandler().ServeHTTP(w, req)

	require.Equal(t, http.StatusBadRequest, w.Code)
	var errResp map[string]string
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &errResp))
	require.Equal(t, "invalid_grant", errResp["error"])
}

func TestTokenHandlerDoesNotLogOAuthSecrets(t *testing.T) {
	srv := newTestOIDCServer(t)
	var output bytes.Buffer
	logger := slog.New(logattr.NewPrivacyHandler(slog.NewJSONHandler(&output, nil)))
	const code = "secret-authorization-code"
	const verifier = "secret-code-verifier"
	const storedRedirect = "https://stored.example.com/secret-callback"
	const requestRedirect = "https://request.example.com/secret-callback"
	require.NoError(t, srv.authState.StoreAuthCode(t.Context(), code, store.AuthCode{
		ClientID:      "test-client",
		Sub:           "12345678",
		Scope:         "insights:read",
		CodeChallenge: pkceChallenge(verifier),
		RedirectURI:   storedRedirect,
	}))
	form := url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {code},
		"code_verifier": {verifier},
		"client_id":     {"test-client"},
		"redirect_uri":  {requestRedirect},
	}
	req := httptest.NewRequest(http.MethodPost, "/oauth2/token", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req = req.WithContext(ctxlog.WithLogger(req.Context(), logger))
	w := httptest.NewRecorder()

	srv.TokenHandler().ServeHTTP(w, req)

	require.Equal(t, http.StatusBadRequest, w.Code)
	require.Contains(t, output.String(), "redirect_uri mismatch")
	for _, secret := range []string{code, verifier, storedRedirect, requestRedirect} {
		require.NotContains(t, output.String(), secret)
	}
}

func TestTokenHandler_MissingClientID(t *testing.T) {
	srv := newTestOIDCServer(t)
	form := url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {"code"},
		"code_verifier": {"verifier"},
	}
	req := httptest.NewRequest(http.MethodPost, "/oauth2/token", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	srv.TokenHandler().ServeHTTP(w, req)

	require.Equal(t, http.StatusBadRequest, w.Code)
	require.Contains(t, w.Body.String(), "client_id")
}

func TestTokenHandler_ClientIDMismatch(t *testing.T) {
	srv := newTestOIDCServer(t)
	verifier := "dBjftJeZ4CVP-mB92K27uhbUJU1p1r_wW1gFWFOEjXk"
	code := "client-mismatch-code"
	require.NoError(t, srv.authState.StoreAuthCode(t.Context(), code, store.AuthCode{
		ClientID:      "client-a",
		Sub:           "12345678",
		Scope:         "insights:read",
		CodeChallenge: pkceChallenge(verifier),
		RedirectURI:   "https://client.example.com/callback",
	}))
	form := url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {code},
		"code_verifier": {verifier},
		"client_id":     {"client-b"},
		"redirect_uri":  {"https://client.example.com/callback"},
	}
	req := httptest.NewRequest(http.MethodPost, "/oauth2/token", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	srv.TokenHandler().ServeHTTP(w, req)

	require.Equal(t, http.StatusBadRequest, w.Code)
	require.Contains(t, w.Body.String(), "invalid_grant")
}

func TestTokenHandler_PKCEMismatch(t *testing.T) {
	srv := newTestOIDCServer(t)

	code := "pkce-mismatch-code"
	require.NoError(t, srv.authState.StoreAuthCode(t.Context(), code, store.AuthCode{
		ClientID:      "test-client",
		Sub:           "12345678",
		GitHubID:      12345678,
		Email:         "user@example.com",
		Scope:         "insights:read",
		CodeChallenge: pkceChallenge("correct-verifier-that-is-long-enough-to-be-valid"),
		RedirectURI:   "https://client.example.com/callback",
	}))

	form := url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {code},
		"code_verifier": {"wrong-verifier-that-is-also-long-enough-for-pkce"},
		"client_id":     {"test-client"},
		"redirect_uri":  {"https://client.example.com/callback"},
	}

	req := httptest.NewRequest(http.MethodPost, "/oauth2/token", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	srv.TokenHandler().ServeHTTP(w, req)

	require.Equal(t, http.StatusBadRequest, w.Code)
	var errResp map[string]string
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &errResp))
	require.Equal(t, "invalid_grant", errResp["error"])
}

func TestTokenHandler_RedirectURIMismatch(t *testing.T) {
	srv := newTestOIDCServer(t)

	verifier := "dBjftJeZ4CVP-mB92K27uhbUJU1p1r_wW1gFWFOEjXk"
	code := "redirect-mismatch-code"
	require.NoError(t, srv.authState.StoreAuthCode(t.Context(), code, store.AuthCode{
		ClientID:      "test-client",
		Sub:           "12345678",
		GitHubID:      12345678,
		Email:         "user@example.com",
		Scope:         "insights:read",
		CodeChallenge: pkceChallenge(verifier),
		RedirectURI:   "https://client.example.com/callback",
	}))

	form := url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {code},
		"code_verifier": {verifier},
		"client_id":     {"test-client"},
		"redirect_uri":  {"https://attacker.example.com/callback"},
	}

	req := httptest.NewRequest(http.MethodPost, "/oauth2/token", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	srv.TokenHandler().ServeHTTP(w, req)

	require.Equal(t, http.StatusBadRequest, w.Code)
	var errResp map[string]string
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &errResp))
	require.Equal(t, "invalid_grant", errResp["error"])
}

func TestTokenHandler_MissingRedirectURI(t *testing.T) {
	srv := newTestOIDCServer(t)

	verifier := "dBjftJeZ4CVP-mB92K27uhbUJU1p1r_wW1gFWFOEjXk"
	code := "missing-redirect-code"
	require.NoError(t, srv.authState.StoreAuthCode(t.Context(), code, store.AuthCode{
		ClientID:      "test-client",
		Sub:           "12345678",
		GitHubID:      12345678,
		Email:         "user@example.com",
		Scope:         "insights:read",
		CodeChallenge: pkceChallenge(verifier),
		RedirectURI:   "https://client.example.com/callback",
	}))

	form := url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {code},
		"code_verifier": {verifier},
		"client_id":     {"test-client"},
		// redirect_uri intentionally absent
	}

	req := httptest.NewRequest(http.MethodPost, "/oauth2/token", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	srv.TokenHandler().ServeHTTP(w, req)

	require.Equal(t, http.StatusBadRequest, w.Code)
	var errResp map[string]string
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &errResp))
	require.Equal(t, "invalid_grant", errResp["error"])
}

func TestTokenHandler_UnsupportedGrantType(t *testing.T) {
	srv := newTestOIDCServer(t)

	form := url.Values{
		"grant_type": {"client_credentials"},
	}

	req := httptest.NewRequest(http.MethodPost, "/oauth2/token", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	srv.TokenHandler().ServeHTTP(w, req)

	require.Equal(t, http.StatusBadRequest, w.Code)
	var errResp map[string]string
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &errResp))
	require.Equal(t, "unsupported_grant_type", errResp["error"])
}

func TestTokenHandler_WrongMethod(t *testing.T) {
	srv := newTestOIDCServer(t)
	req := httptest.NewRequest(http.MethodGet, "/oauth2/token", nil)
	w := httptest.NewRecorder()
	srv.TokenHandler().ServeHTTP(w, req)
	require.Equal(t, http.StatusMethodNotAllowed, w.Code)
}

func TestTokenHandler_GrantStoreSeam(t *testing.T) {
	privkey, err := ecdsa.GenerateKey(elliptic.P384(), rand.Reader)
	require.NoError(t, err)
	raw, err := jwk.Import[jwk.Key](privkey)
	require.NoError(t, err)

	gs := &testGrantStore{}
	authState := newInMemAuthState()
	srv, err := NewServer(Config{
		BaseURL:    "http://example.com",
		Grants:     gs,
		AuthState:  authState,
		Revocation: newInMemRevocation(),
	}, raw)
	require.NoError(t, err)

	userID := uuid.New()
	verifier := "dBjftJeZ4CVP-mB92K27uhbUJU1p1r_wW1gFWFOEjXk"
	code := "grant-seam-code"
	accessExpiry := time.Now().Add(8 * time.Hour).Truncate(time.Second)
	refreshExpiry := time.Now().Add(180 * 24 * time.Hour).Truncate(time.Second)
	require.NoError(t, authState.StoreAuthCode(t.Context(), code, store.AuthCode{
		ClientID:           "test-client",
		Sub:                userID.String(),
		GitHubID:           99887766,
		Email:              "user@example.com",
		Scope:              "insights:read",
		CodeChallenge:      pkceChallenge(verifier),
		RedirectURI:        "https://client.example.com/callback",
		AccessToken:        "gha_access_abc",
		RefreshToken:       "ghr_refresh_xyz",
		AccessTokenExpiry:  accessExpiry,
		RefreshTokenExpiry: refreshExpiry,
	}))

	form := url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {code},
		"code_verifier": {verifier},
		"client_id":     {"test-client"},
		"redirect_uri":  {"https://client.example.com/callback"},
	}
	req := httptest.NewRequest(http.MethodPost, "/oauth2/token", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	srv.TokenHandler().ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	require.Len(t, gs.calls, 1, "UpsertGrant must be called exactly once")

	p := gs.calls[0]
	require.Equal(t, userID, p.UserID)
	require.NotEmpty(t, p.JTI)
	require.Equal(t, "gha_access_abc", p.AccessToken)
	require.Equal(t, "ghr_refresh_xyz", p.RefreshToken)
	require.WithinDuration(t, accessExpiry, p.AccessTokenExpiry, time.Second)
	require.WithinDuration(t, refreshExpiry, p.RefreshTokenExpiry, time.Second)
	require.WithinDuration(t, time.Now().Add(15*time.Minute), p.JWTExpiry, 5*time.Second)
}

// --- Refresh grant ---

func newRefreshTestServer(t *testing.T, gs *testGrantStore, gh *mockGitHubConnector) *Server {
	t.Helper()
	privkey, err := ecdsa.GenerateKey(elliptic.P384(), rand.Reader)
	require.NoError(t, err)
	raw, err := jwk.Import[jwk.Key](privkey)
	require.NoError(t, err)
	rev := newInMemRevocation()
	gs.revocation = rev
	srv, err := NewServer(Config{
		BaseURL:    "http://example.com",
		Grants:     gs,
		AuthState:  newInMemAuthState(),
		Revocation: rev,
	}, raw)
	require.NoError(t, err)
	if gh != nil {
		srv.github = gh
	}
	return srv
}

func TestTokenHandler_AuthCodeIssuesRefreshToken(t *testing.T) {
	gs := &testGrantStore{}
	srv := newRefreshTestServer(t, gs, nil)

	userID := uuid.New()
	verifier := "dBjftJeZ4CVP-mB92K27uhbUJU1p1r_wW1gFWFOEjXk"
	code := "auth-code-with-refresh"
	require.NoError(t, srv.authState.StoreAuthCode(t.Context(), code, store.AuthCode{
		Sub:                userID.String(),
		GitHubID:           12345678,
		Email:              "user@example.com",
		Scope:              "insights:read insights:write",
		CodeChallenge:      pkceChallenge(verifier),
		RedirectURI:        "https://client.example.com/callback",
		ClientID:           "test-client",
		AccessToken:        "gha_access",
		RefreshToken:       "ghr_refresh",
		AccessTokenExpiry:  time.Now().Add(8 * time.Hour),
		RefreshTokenExpiry: time.Now().Add(180 * 24 * time.Hour),
	}))

	form := url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {code},
		"code_verifier": {verifier},
		"client_id":     {"test-client"},
		"redirect_uri":  {"https://client.example.com/callback"},
	}
	req := httptest.NewRequest(http.MethodPost, "/oauth2/token", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	srv.TokenHandler().ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	var resp map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	require.NotEmpty(t, resp["refresh_token"], "refresh_token must be present when GH refresh token exists")
	require.Greater(t, resp["refresh_token_expires_in"].(float64), float64(0))
	require.Len(t, gs.calls, 1)
	require.NotEmpty(t, gs.calls[0].OurRefreshToken)
	require.Equal(t, "insights:read insights:write", gs.calls[0].Scope)
}

func TestTokenHandler_AuthCodeClientWithoutRefreshGrantSkipsGrantPersistence(t *testing.T) {
	gs := &testGrantStore{}
	srv := newRefreshTestServer(t, gs, nil)
	srv.clients = &testClientStore{records: []store.OAuthClient{{
		ClientID: "web-client", GrantTypes: []string{"authorization_code"},
	}}}

	verifier := "web-client-verifier"
	code := "web-client-auth-code"
	require.NoError(t, srv.authState.StoreAuthCode(t.Context(), code, store.AuthCode{
		Sub: uuid.New().String(), Scope: "insights:read", CodeChallenge: pkceChallenge(verifier),
		RedirectURI: "https://client.example.com/callback", ClientID: "web-client",
		AccessToken: "gha_access", RefreshToken: "ghr_refresh",
		AccessTokenExpiry: time.Now().Add(8 * time.Hour), RefreshTokenExpiry: time.Now().Add(180 * 24 * time.Hour),
	}))

	form := url.Values{
		"grant_type": {"authorization_code"}, "code": {code}, "code_verifier": {verifier},
		"client_id": {"web-client"}, "redirect_uri": {"https://client.example.com/callback"},
	}
	req := httptest.NewRequest(http.MethodPost, "/oauth2/token", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	srv.TokenHandler().ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	var resp map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	require.NotEmpty(t, resp["access_token"])
	_, hasRefresh := resp["refresh_token"]
	require.False(t, hasRefresh)
	require.Empty(t, gs.calls)
}

func TestTokenHandler_AuthCodeNoGitHubRefreshSkipsOurRefresh(t *testing.T) {
	gs := &testGrantStore{}
	srv := newRefreshTestServer(t, gs, nil)

	userID := uuid.New()
	verifier := "dBjftJeZ4CVP-mB92K27uhbUJU1p1r_wW1gFWFOEjXk"
	code := "auth-code-no-refresh"
	require.NoError(t, srv.authState.StoreAuthCode(t.Context(), code, store.AuthCode{
		Sub:           userID.String(),
		GitHubID:      12345678,
		Email:         "user@example.com",
		Scope:         "insights:read",
		CodeChallenge: pkceChallenge(verifier),
		RedirectURI:   "https://client.example.com/callback",
		ClientID:      "test-client",
		AccessToken:   "gha_access",
		// RefreshToken empty — OAuth App, not GitHub App with expiring tokens.
	}))

	form := url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {code},
		"code_verifier": {verifier},
		"client_id":     {"test-client"},
		"redirect_uri":  {"https://client.example.com/callback"},
	}
	req := httptest.NewRequest(http.MethodPost, "/oauth2/token", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	srv.TokenHandler().ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	var resp map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	_, hasRefresh := resp["refresh_token"]
	require.False(t, hasRefresh, "no refresh_token should be issued without a GitHub refresh token")
	require.Len(t, gs.calls, 1)
	require.Empty(t, gs.calls[0].OurRefreshToken)
}

func TestTokenHandler_RefreshGrant_HappyPath(t *testing.T) {
	gs := &testGrantStore{}
	gh := &mockGitHubConnector{
		refreshToken: &oauth2.Token{
			AccessToken:  "gha_new_access",
			RefreshToken: "ghr_new_refresh",
			Expiry:       time.Now().Add(8 * time.Hour),
		},
		identity: &githubIdentity{ID: 12345678, Email: "user@example.com", Login: "user"},
	}
	srv := newRefreshTestServer(t, gs, gh)

	oldRefresh := "old-opaque-refresh-token"
	oldJTI := uuid.New().String()
	gs.seed(store.Grant{
		JTI:                oldJTI,
		OurRefreshToken:    oldRefresh,
		ClientID:           "test-client",
		Scope:              "insights:read insights:write",
		AccessToken:        "gha_old_access",
		RefreshToken:       "ghr_old_refresh",
		AccessTokenExpiry:  time.Now().Add(1 * time.Hour),
		RefreshTokenExpiry: time.Now().Add(180 * 24 * time.Hour),
		JWTExpiry:          time.Now().Add(24 * time.Hour),
	})

	form := url.Values{
		"grant_type":    {"refresh_token"},
		"refresh_token": {oldRefresh},
		"client_id":     {"test-client"},
	}
	req := httptest.NewRequest(http.MethodPost, "/oauth2/token", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	srv.TokenHandler().ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	require.Equal(t, "no-store", w.Header().Get("Cache-Control"))

	var resp map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	require.NotEmpty(t, resp["access_token"])
	require.Equal(t, "Bearer", resp["token_type"])
	require.Equal(t, "insights:read insights:write", resp["scope"])
	newRefresh, _ := resp["refresh_token"].(string)
	require.NotEmpty(t, newRefresh)
	require.NotEqual(t, oldRefresh, newRefresh, "refresh token must rotate")

	require.Equal(t, []string{"ghr_old_refresh"}, gh.refreshCalls,
		"GitHub refresh must be called with the stored GitHub refresh token, not our opaque one")
	require.Len(t, gs.rotateCalls, 1)
	require.Equal(t, oldRefresh, gs.rotateCalls[0].oldToken)
	require.Equal(t, oldJTI, gs.rotateCalls[0].oldJTI, "old jti must be passed to RotateGrant for atomic revocation")
	require.Equal(t, "gha_new_access", gs.rotateCalls[0].grant.AccessToken)
	require.Equal(t, "ghr_new_refresh", gs.rotateCalls[0].grant.RefreshToken)
	require.NotNil(t, gs.rotateCalls[0].retired)
	require.Equal(t, store.RetiredRefreshTokenReasonRotated, gs.rotateCalls[0].retired.Reason)
	require.Equal(t, store.HashRefreshToken(oldRefresh), gs.rotateCalls[0].retired.TokenHash)
	rotatedJTI := gs.rotateCalls[0].grant.JTI
	require.NotEqual(t, oldJTI, rotatedJTI, "jti must rotate")
	require.Equal(t, rotatedJTI, gs.rotateCalls[0].retired.ReplacementJTI)
	require.WithinDuration(t, time.Now().Add(DefaultRefreshTokenGracePeriod), gs.rotateCalls[0].retired.GraceExpiresAt, 5*time.Second)

	// refresh_token_expires_in is recomputed from the fresh GitHub refresh token —
	// extractRefreshExpiry falls back to ~6 months when the upstream response doesn't
	// carry refresh_token_expires_in (which is the case for the mock).
	exp, ok := resp["refresh_token_expires_in"].(float64)
	require.True(t, ok)
	require.InDelta(t, exp, (6 * 30 * 24 * time.Hour).Seconds(), 60,
		"refresh_token_expires_in must reflect the fresh GitHub refresh window")

	// Old JTI must be on the revocation list (atomic with rotation).
	revoked, err := srv.revocation.IsTokenRevoked(t.Context(), oldJTI)
	require.NoError(t, err)
	require.True(t, revoked, "previous JWT jti must be revoked after rotation")

	// New JWT must verify and carry the rotated jti, not the old one.
	tokenString, _ := resp["access_token"].(string)
	info, err := srv.VerifyJWT(t.Context(), tokenString, nil)
	require.NoError(t, err)
	require.Equal(t, "12345678", info.UserID)

	parsed, err := jwt.ParseString(tokenString, jwt.WithKey(jwa.ES384(), srv.pubkey))
	require.NoError(t, err)
	jtiClaim, ok := parsed.JwtID()
	require.True(t, ok)
	require.Equal(t, rotatedJTI, jtiClaim, "JWT jti must match the rotated grant's JTI")
	require.NotEqual(t, oldJTI, jtiClaim)
}

func TestTokenHandler_RefreshGrant_GrantMissingGitHubRefresh(t *testing.T) {
	gs := &testGrantStore{}
	gh := &mockGitHubConnector{}
	srv := newRefreshTestServer(t, gs, gh)

	gs.seed(store.Grant{
		JTI:                uuid.New().String(),
		OurRefreshToken:    "rt-no-gh-refresh",
		ClientID:           "test-client",
		RefreshToken:       "", // grant exists but its GitHub refresh token field is empty
		RefreshTokenExpiry: time.Now().Add(time.Hour),
		JWTExpiry:          time.Now().Add(time.Hour),
	})

	form := url.Values{
		"grant_type":    {"refresh_token"},
		"refresh_token": {"rt-no-gh-refresh"},
		"client_id":     {"test-client"},
	}
	req := httptest.NewRequest(http.MethodPost, "/oauth2/token", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	srv.TokenHandler().ServeHTTP(w, req)

	require.Equal(t, http.StatusBadRequest, w.Code)
	var errResp map[string]string
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &errResp))
	require.Equal(t, "invalid_grant", errResp["error"])
	require.Empty(t, gh.refreshCalls, "GitHub must not be called when the stored refresh token is empty")
	require.Empty(t, gs.rotateCalls)
}

func TestTokenHandler_RefreshGrant_UpsertUserFails(t *testing.T) {
	gs := &testGrantStore{}
	gh := &mockGitHubConnector{
		refreshToken: &oauth2.Token{
			AccessToken:  "gha_new",
			RefreshToken: "ghr_new",
			Expiry:       time.Now().Add(8 * time.Hour),
		},
		identity: &githubIdentity{ID: 7, Email: "u@example.com", Login: "u"},
	}
	srv := newRefreshTestServer(t, gs, gh)
	srv.users = &testUserUpserter{err: fmt.Errorf("db down")}

	gs.seed(store.Grant{
		JTI:                uuid.New().String(),
		OurRefreshToken:    "rt-upsert-fail",
		ClientID:           "test-client",
		RefreshToken:       "ghr-old",
		RefreshTokenExpiry: time.Now().Add(time.Hour),
		JWTExpiry:          time.Now().Add(time.Hour),
	})

	form := url.Values{
		"grant_type":    {"refresh_token"},
		"refresh_token": {"rt-upsert-fail"},
		"client_id":     {"test-client"},
	}
	req := httptest.NewRequest(http.MethodPost, "/oauth2/token", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	srv.TokenHandler().ServeHTTP(w, req)

	require.Equal(t, http.StatusInternalServerError, w.Code)
	var errResp map[string]string
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &errResp))
	require.Equal(t, "server_error", errResp["error"])
	require.Empty(t, gs.rotateCalls, "must not rotate when the user upsert fails")
}

func TestTokenHandler_RefreshGrant_EmptyGrantClientIDSkipsCheck(t *testing.T) {
	gs := &testGrantStore{}
	gh := &mockGitHubConnector{
		refreshToken: &oauth2.Token{
			AccessToken:  "gha_new",
			RefreshToken: "ghr_new",
			Expiry:       time.Now().Add(8 * time.Hour),
		},
		identity: &githubIdentity{ID: 99, Email: "u@example.com", Login: "u"},
	}
	srv := newRefreshTestServer(t, gs, gh)

	gs.seed(store.Grant{
		JTI:                uuid.New().String(),
		OurRefreshToken:    "rt-no-clientid",
		ClientID:           "", // grant has no stored client_id — best-effort skip per v0.2 spec
		Scope:              "insights:read",
		RefreshToken:       "ghr-old",
		RefreshTokenExpiry: time.Now().Add(time.Hour),
		JWTExpiry:          time.Now().Add(time.Hour),
	})

	form := url.Values{
		"grant_type":    {"refresh_token"},
		"refresh_token": {"rt-no-clientid"},
		"client_id":     {"any-client-id-works"},
	}
	req := httptest.NewRequest(http.MethodPost, "/oauth2/token", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	srv.TokenHandler().ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	require.Len(t, gs.rotateCalls, 1, "rotation must proceed when grant.ClientID is empty")
}

func TestTokenHandler_RefreshGrant_UnknownToken(t *testing.T) {
	gs := &testGrantStore{}
	srv := newRefreshTestServer(t, gs, &mockGitHubConnector{})

	form := url.Values{
		"grant_type":    {"refresh_token"},
		"refresh_token": {"never-issued"},
		"client_id":     {"test-client"},
	}
	req := httptest.NewRequest(http.MethodPost, "/oauth2/token", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	srv.TokenHandler().ServeHTTP(w, req)

	require.Equal(t, http.StatusBadRequest, w.Code)
	var errResp map[string]string
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &errResp))
	require.Equal(t, "invalid_grant", errResp["error"])
}

func TestTokenHandler_RefreshGrant_GraceRetry(t *testing.T) {
	gs := &testGrantStore{}
	gh := &mockGitHubConnector{}
	srv := newRefreshTestServer(t, gs, gh)

	userID := uuid.New()
	replacement := store.Grant{
		JTI:                uuid.New().String(),
		UserID:             userID,
		OurRefreshToken:    "current-refresh-token",
		ClientID:           "test-client",
		Scope:              "insights:read insights:write",
		RefreshToken:       "ghr-current",
		RefreshTokenExpiry: time.Now().Add(180 * 24 * time.Hour),
		JWTExpiry:          time.Now().Add(15 * time.Minute),
	}
	gs.seed(replacement)
	gs.retired = append(gs.retired, store.RetiredRefreshToken{
		TokenHash:      store.HashRefreshToken("old-refresh-token"),
		Reason:         store.RetiredRefreshTokenReasonRotated,
		UserID:         userID,
		ClientID:       "test-client",
		OldJTI:         uuid.New().String(),
		ReplacementJTI: replacement.JTI,
		GraceExpiresAt: time.Now().Add(DefaultRefreshTokenGracePeriod),
		RetainedUntil:  time.Now().Add(24 * time.Hour),
	})

	form := url.Values{
		"grant_type":    {"refresh_token"},
		"refresh_token": {"old-refresh-token"},
		"client_id":     {"test-client"},
	}
	req := httptest.NewRequest(http.MethodPost, "/oauth2/token", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	srv.TokenHandler().ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	require.Empty(t, gh.refreshCalls, "grace retry must not call GitHub")
	require.Empty(t, gs.rotateCalls, "grace retry must not rotate again")

	var resp map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	require.Equal(t, replacement.OurRefreshToken, resp["refresh_token"])
	tokenString, _ := resp["access_token"].(string)
	info, err := srv.VerifyJWT(t.Context(), tokenString, nil)
	require.NoError(t, err)
	require.Equal(t, userID.String(), info.UserID)
}

func TestTokenHandler_RefreshGrant_GraceExpired(t *testing.T) {
	gs := &testGrantStore{}
	gh := &mockGitHubConnector{}
	srv := newRefreshTestServer(t, gs, gh)

	gs.retired = append(gs.retired, store.RetiredRefreshToken{
		TokenHash:      store.HashRefreshToken("old-refresh-token"),
		Reason:         store.RetiredRefreshTokenReasonRotated,
		ClientID:       "test-client",
		ReplacementJTI: uuid.New().String(),
		GraceExpiresAt: time.Now().Add(-time.Second),
		RetainedUntil:  time.Now().Add(24 * time.Hour),
	})

	form := url.Values{
		"grant_type":    {"refresh_token"},
		"refresh_token": {"old-refresh-token"},
		"client_id":     {"test-client"},
	}
	req := httptest.NewRequest(http.MethodPost, "/oauth2/token", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	srv.TokenHandler().ServeHTTP(w, req)

	require.Equal(t, http.StatusBadRequest, w.Code)
	require.Empty(t, gh.refreshCalls)
	require.Empty(t, gs.rotateCalls)
	var errResp map[string]string
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &errResp))
	require.Equal(t, "invalid_grant", errResp["error"])
}

func TestTokenHandler_RefreshGrant_GraceDisabled(t *testing.T) {
	gs := &testGrantStore{}
	gh := &mockGitHubConnector{}
	srv := newRefreshTestServer(t, gs, gh)
	srv.refreshTokenGracePeriod = 0

	oldRefresh := "old-refresh-token"
	gs.retired = append(gs.retired, store.RetiredRefreshToken{
		TokenHash:      store.HashRefreshToken(oldRefresh),
		Reason:         store.RetiredRefreshTokenReasonRotated,
		ClientID:       "test-client",
		ReplacementJTI: uuid.New().String(),
		RetainedUntil:  time.Now().Add(24 * time.Hour),
	})

	form := url.Values{
		"grant_type":    {"refresh_token"},
		"refresh_token": {oldRefresh},
		"client_id":     {"test-client"},
	}
	req := httptest.NewRequest(http.MethodPost, "/oauth2/token", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	srv.TokenHandler().ServeHTTP(w, req)

	require.Equal(t, http.StatusBadRequest, w.Code)
	require.Empty(t, gh.refreshCalls)
	require.Empty(t, gs.rotateCalls)
	var errResp map[string]string
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &errResp))
	require.Equal(t, "invalid_grant", errResp["error"])
}

func TestTokenHandler_RefreshGrant_RecentlyRemoved(t *testing.T) {
	gs := &testGrantStore{}
	srv := newRefreshTestServer(t, gs, &mockGitHubConnector{})

	gs.retired = append(gs.retired, store.RetiredRefreshToken{
		TokenHash:     store.HashRefreshToken("removed-refresh-token"),
		Reason:        store.RetiredRefreshTokenReasonGitHubExpired,
		ClientID:      "test-client",
		OldJTI:        uuid.New().String(),
		RetainedUntil: time.Now().Add(24 * time.Hour),
	})

	form := url.Values{
		"grant_type":    {"refresh_token"},
		"refresh_token": {"removed-refresh-token"},
		"client_id":     {"test-client"},
	}
	req := httptest.NewRequest(http.MethodPost, "/oauth2/token", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	srv.TokenHandler().ServeHTTP(w, req)

	require.Equal(t, http.StatusBadRequest, w.Code)
	var errResp map[string]string
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &errResp))
	require.Equal(t, "invalid_grant", errResp["error"])
}

func TestTokenHandler_RefreshGrant_ClientIDMismatch(t *testing.T) {
	gs := &testGrantStore{}
	srv := newRefreshTestServer(t, gs, &mockGitHubConnector{})

	gs.seed(store.Grant{
		JTI:                uuid.New().String(),
		OurRefreshToken:    "rt-mismatch",
		ClientID:           "client-A",
		RefreshToken:       "ghr",
		RefreshTokenExpiry: time.Now().Add(time.Hour),
	})

	form := url.Values{
		"grant_type":    {"refresh_token"},
		"refresh_token": {"rt-mismatch"},
		"client_id":     {"client-B"},
	}
	req := httptest.NewRequest(http.MethodPost, "/oauth2/token", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	srv.TokenHandler().ServeHTTP(w, req)

	require.Equal(t, http.StatusBadRequest, w.Code)
	var errResp map[string]string
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &errResp))
	require.Equal(t, "invalid_client", errResp["error"])
	require.Empty(t, gs.rotateCalls, "must not rotate on client mismatch")
}

func TestTokenHandler_RefreshGrant_GitHubRefreshExpired(t *testing.T) {
	gs := &testGrantStore{}
	srv := newRefreshTestServer(t, gs, &mockGitHubConnector{})

	jti := uuid.New().String()
	gs.seed(store.Grant{
		JTI:                jti,
		OurRefreshToken:    "rt-expired",
		ClientID:           "test-client",
		RefreshToken:       "ghr-expired",
		RefreshTokenExpiry: time.Now().Add(-time.Hour),
		JWTExpiry:          time.Now().Add(time.Hour),
	})

	form := url.Values{
		"grant_type":    {"refresh_token"},
		"refresh_token": {"rt-expired"},
		"client_id":     {"test-client"},
	}
	req := httptest.NewRequest(http.MethodPost, "/oauth2/token", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	srv.TokenHandler().ServeHTTP(w, req)

	require.Equal(t, http.StatusBadRequest, w.Code)
	var errResp map[string]string
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &errResp))
	require.Equal(t, "invalid_grant", errResp["error"])

	revoked, err := srv.revocation.IsTokenRevoked(t.Context(), jti)
	require.NoError(t, err)
	require.True(t, revoked, "expired-grant jti must be revoked")
	require.Equal(t, []string{jti}, gs.deleteCalls, "expired grant row must be deleted")
	require.Len(t, gs.retired, 1)
	require.Equal(t, store.RetiredRefreshTokenReasonGitHubExpired, gs.retired[0].Reason)
	require.Equal(t, store.HashRefreshToken("rt-expired"), gs.retired[0].TokenHash)
}

func TestTokenHandler_RefreshGrant_MissingParams(t *testing.T) {
	srv := newRefreshTestServer(t, &testGrantStore{}, nil)

	cases := []url.Values{
		{"grant_type": {"refresh_token"}, "client_id": {"c"}},
		{"grant_type": {"refresh_token"}, "refresh_token": {"rt"}},
		{"grant_type": {"refresh_token"}},
	}
	for _, form := range cases {
		req := httptest.NewRequest(http.MethodPost, "/oauth2/token", strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		w := httptest.NewRecorder()
		srv.TokenHandler().ServeHTTP(w, req)
		require.Equal(t, http.StatusBadRequest, w.Code)
		var errResp map[string]string
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &errResp))
		require.Equal(t, "invalid_request", errResp["error"])
	}
}

func TestTokenHandler_RefreshGrant_GitHubRefreshAPIFails(t *testing.T) {
	gs := &testGrantStore{}
	gh := &mockGitHubConnector{refreshErr: fmt.Errorf("github 5xx")}
	srv := newRefreshTestServer(t, gs, gh)

	jti := uuid.New().String()
	gs.seed(store.Grant{
		JTI:                jti,
		OurRefreshToken:    "rt-gh-fail",
		ClientID:           "test-client",
		RefreshToken:       "ghr",
		RefreshTokenExpiry: time.Now().Add(time.Hour),
		JWTExpiry:          time.Now().Add(time.Hour),
	})

	form := url.Values{
		"grant_type":    {"refresh_token"},
		"refresh_token": {"rt-gh-fail"},
		"client_id":     {"test-client"},
	}
	req := httptest.NewRequest(http.MethodPost, "/oauth2/token", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	srv.TokenHandler().ServeHTTP(w, req)

	require.Equal(t, http.StatusInternalServerError, w.Code)
	var errResp map[string]string
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &errResp))
	require.Equal(t, "server_error", errResp["error"])
	require.Empty(t, gs.rotateCalls, "must not rotate when GitHub refresh fails")
	require.Empty(t, gs.deleteCalls, "must not delete the grant on transient GitHub failure")
}

func TestTokenHandler_RefreshGrant_GitHubReturnsNoRefreshToken(t *testing.T) {
	gs := &testGrantStore{}
	gh := &mockGitHubConnector{
		// GitHub returned a new access token but no new refresh token — chain broken.
		refreshToken: &oauth2.Token{AccessToken: "gha_new", Expiry: time.Now().Add(8 * time.Hour)},
		identity:     &githubIdentity{ID: 1, Email: "u@example.com", Login: "u"},
	}
	srv := newRefreshTestServer(t, gs, gh)

	jti := uuid.New().String()
	gs.seed(store.Grant{
		JTI:                jti,
		OurRefreshToken:    "rt-noref",
		ClientID:           "test-client",
		RefreshToken:       "ghr-old",
		RefreshTokenExpiry: time.Now().Add(time.Hour),
		JWTExpiry:          time.Now().Add(time.Hour),
	})

	form := url.Values{
		"grant_type":    {"refresh_token"},
		"refresh_token": {"rt-noref"},
		"client_id":     {"test-client"},
	}
	req := httptest.NewRequest(http.MethodPost, "/oauth2/token", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	srv.TokenHandler().ServeHTTP(w, req)

	require.Equal(t, http.StatusBadRequest, w.Code)
	var errResp map[string]string
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &errResp))
	require.Equal(t, "invalid_grant", errResp["error"])

	// Grant must be torn down so future requests with the same opaque token also fail fast.
	require.Equal(t, []string{jti}, gs.deleteCalls, "broken grant must be deleted")
	require.Empty(t, gs.rotateCalls, "must not rotate when GitHub did not return a refresh token")
	revoked, err := srv.revocation.IsTokenRevoked(t.Context(), jti)
	require.NoError(t, err)
	require.True(t, revoked, "old jti must be revoked when grant is torn down")
}

func TestTokenHandler_RefreshGrant_NoGrantStore(t *testing.T) {
	t.Helper()
	privkey, err := ecdsa.GenerateKey(elliptic.P384(), rand.Reader)
	require.NoError(t, err)
	raw, err := jwk.Import[jwk.Key](privkey)
	require.NoError(t, err)
	srv, err := NewServer(Config{
		BaseURL:    "http://example.com",
		AuthState:  newInMemAuthState(),
		Revocation: newInMemRevocation(),
		// Grants intentionally nil — refresh grant should fail fast.
	}, raw)
	require.NoError(t, err)

	form := url.Values{
		"grant_type":    {"refresh_token"},
		"refresh_token": {"anything"},
		"client_id":     {"test-client"},
	}
	req := httptest.NewRequest(http.MethodPost, "/oauth2/token", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	srv.TokenHandler().ServeHTTP(w, req)

	require.Equal(t, http.StatusBadRequest, w.Code)
	var errResp map[string]string
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &errResp))
	require.Equal(t, "invalid_grant", errResp["error"])
}

func TestTokenHandler_RefreshGrant_RotateNotFound(t *testing.T) {
	gs := &testGrantStore{rotateErr: store.ErrNotFound}
	gh := &mockGitHubConnector{
		refreshToken: &oauth2.Token{AccessToken: "a", RefreshToken: "b", Expiry: time.Now().Add(time.Hour)},
		identity:     &githubIdentity{ID: 1, Email: "u@example.com", Login: "u"},
	}
	srv := newRefreshTestServer(t, gs, gh)

	gs.seed(store.Grant{
		JTI:                uuid.New().String(),
		OurRefreshToken:    "rt-race",
		ClientID:           "test-client",
		RefreshToken:       "ghr",
		RefreshTokenExpiry: time.Now().Add(time.Hour),
		JWTExpiry:          time.Now().Add(time.Hour),
	})

	form := url.Values{
		"grant_type":    {"refresh_token"},
		"refresh_token": {"rt-race"},
		"client_id":     {"test-client"},
	}
	req := httptest.NewRequest(http.MethodPost, "/oauth2/token", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	srv.TokenHandler().ServeHTTP(w, req)

	require.Equal(t, http.StatusBadRequest, w.Code)
	var errResp map[string]string
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &errResp))
	require.Equal(t, "invalid_grant", errResp["error"])
}

// --- GitHub callback ---

func TestGitHubCallbackHandler_WrongMethod(t *testing.T) {
	srv := newTestOIDCServer(t)
	req := httptest.NewRequest(http.MethodPost, "/auth/github/callback", nil)
	w := httptest.NewRecorder()
	srv.GitHubCallbackHandler().ServeHTTP(w, req)
	require.Equal(t, http.StatusMethodNotAllowed, w.Code)
}

func TestGitHubCallbackHandler_InvalidState(t *testing.T) {
	srv := newTestOIDCServer(t)
	req := httptest.NewRequest(http.MethodGet, "/auth/github/callback?state=no-such-state&code=anything", nil)
	w := httptest.NewRecorder()
	srv.GitHubCallbackHandler().ServeHTTP(w, req)
	require.Equal(t, http.StatusBadRequest, w.Code)
}

func TestGitHubCallbackHandler_ExchangeError(t *testing.T) {
	srv := newTestOIDCServer(t)
	srv.github = &mockGitHubConnector{err: fmt.Errorf("GitHub is down")}

	require.NoError(t, srv.authState.StorePendingAuth(t.Context(), "valid-state", store.PendingAuth{
		RedirectURI:   "https://client.example.com/callback",
		Scope:         "insights:read",
		CodeChallenge: pkceChallenge("verifier"),
	}))

	req := httptest.NewRequest(http.MethodGet, "/auth/github/callback?state=valid-state&code=bad-code", nil)
	w := httptest.NewRecorder()
	srv.GitHubCallbackHandler().ServeHTTP(w, req)
	require.Equal(t, http.StatusBadGateway, w.Code)
}

func TestGitHubCallbackHandler_UpsertUserCalled(t *testing.T) {
	us := &testUserUpserter{}
	srv := newTestOIDCServer(t)
	srv.users = us
	srv.github = &mockGitHubConnector{
		token: &oauth2.Token{AccessToken: "gha_test", RefreshToken: "ghr_test"},
		identity: &githubIdentity{
			ID: 42, Email: "dev@example.com", Login: "devuser", DisplayName: "Dev User",
			AvatarURL: "https://avatars.example/dev", ProfileURL: "https://example/dev", Bio: "Developer",
		},
	}

	require.NoError(t, srv.authState.StorePendingAuth(t.Context(), "cb-state", store.PendingAuth{
		RedirectURI:   "https://client.example.com/callback",
		Scope:         "insights:read",
		CodeChallenge: pkceChallenge("verifier"),
	}))

	req := httptest.NewRequest(http.MethodGet, "/auth/github/callback?state=cb-state&code=code", nil)
	w := httptest.NewRecorder()
	srv.GitHubCallbackHandler().ServeHTTP(w, req)

	require.Equal(t, http.StatusFound, w.Code)
	require.Len(t, us.calls, 1)
	require.Equal(t, int64(42), us.calls[0].profile.GitHubID)
	require.Equal(t, "dev@example.com", us.calls[0].profile.Email)
	require.Equal(t, "devuser", us.calls[0].profile.Login)
	require.Equal(t, "Dev User", us.calls[0].profile.DisplayName)
	require.Equal(t, "https://avatars.example/dev", us.calls[0].profile.AvatarURL)

	loc := w.Header().Get("Location")
	require.Contains(t, loc, "code=")
}

func TestExternalAuthorizationRequiresConfirmationAndApprovalIssuesUsableCode(t *testing.T) {
	srv := newTestOIDCServer(t)
	authState := srv.authState.(*inMemAuthState)
	verifier := "dBjftJeZ4CVP-mB92K27uhbUJU1p1r_wW1gFWFOEjXk"
	redirectURI := "https://client.example.com/callback?keep=value&code=old&error=old&state=old"
	srv.github = &mockGitHubConnector{
		token:    &oauth2.Token{AccessToken: "gha_test", RefreshToken: "ghr_test", Expiry: time.Now().Add(time.Hour)},
		identity: &githubIdentity{ID: 42, Email: "dev@example.com", Login: "devuser"},
	}
	require.NoError(t, srv.authState.StorePendingAuth(t.Context(), "external-state", store.PendingAuth{
		ClientID: "external-client", ClientName: `<script>alert("x")</script>`, RedirectURI: redirectURI,
		Scope: "insights:read insights:write", CodeChallenge: pkceChallenge(verifier),
		ClientState: "opaque-client-state", ConfirmationRequired: true,
	}))

	callbackReq := httptest.NewRequest(http.MethodGet, "/auth/github/callback?state=external-state&code=github-code", nil)
	callbackW := httptest.NewRecorder()
	srv.GitHubCallbackHandler().ServeHTTP(callbackW, callbackReq)

	require.Equal(t, http.StatusOK, callbackW.Code)
	require.Equal(t, "no-store", callbackW.Header().Get("Cache-Control"))
	confirmationCSP := callbackW.Header().Get("Content-Security-Policy")
	require.Contains(t, confirmationCSP, "default-src 'none'")
	require.Contains(t, confirmationCSP, "form-action 'self' https://client.example.com")
	require.Contains(t, callbackW.Body.String(), `&lt;script&gt;alert(&#34;x&#34;)&lt;/script&gt;`)
	require.NotContains(t, callbackW.Body.String(), `<script>alert`)
	require.Contains(t, callbackW.Body.String(), "insights:read")
	require.Contains(t, callbackW.Body.String(), "insights:write")
	require.Contains(t, callbackW.Body.String(), "keep=value&amp;code=old")
	require.Contains(t, callbackW.Body.String(), `id="authorization-confirmation"`)
	require.Contains(t, callbackW.Body.String(), `if (submitted)`)
	require.Contains(t, callbackW.Body.String(), `decision.name = "decision"`)
	require.Contains(t, callbackW.Body.String(), `button.disabled = true`)
	require.Empty(t, authState.codes)
	require.Len(t, authState.confirmations, 1)

	token := confirmationTokenFromBody(t, callbackW.Body.String())
	form := url.Values{"token": {token}, "decision": {"approve"}}
	confirmReq := httptest.NewRequest(http.MethodPost, confirmationPath, strings.NewReader(form.Encode()))
	confirmReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	confirmW := httptest.NewRecorder()
	srv.AuthorizationConfirmationHandler().ServeHTTP(confirmW, confirmReq)

	require.Equal(t, http.StatusSeeOther, confirmW.Code)
	location, err := url.Parse(confirmW.Header().Get("Location"))
	require.NoError(t, err)
	require.Equal(t, "value", location.Query().Get("keep"))
	require.Equal(t, "opaque-client-state", location.Query().Get("state"))
	require.Empty(t, location.Query().Get("error"))
	code := location.Query().Get("code")
	require.NotEmpty(t, code)

	tokenForm := url.Values{
		"grant_type": {"authorization_code"}, "code": {code}, "code_verifier": {verifier},
		"client_id": {"external-client"}, "redirect_uri": {redirectURI},
	}
	tokenReq := httptest.NewRequest(http.MethodPost, "/oauth2/token", strings.NewReader(tokenForm.Encode()))
	tokenReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	tokenW := httptest.NewRecorder()
	srv.TokenHandler().ServeHTTP(tokenW, tokenReq)
	require.Equal(t, http.StatusOK, tokenW.Code)

	replayReq := httptest.NewRequest(http.MethodPost, confirmationPath, strings.NewReader(form.Encode()))
	replayReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	replayW := httptest.NewRecorder()
	srv.AuthorizationConfirmationHandler().ServeHTTP(replayW, replayReq)
	require.Equal(t, http.StatusBadRequest, replayW.Code)
}

func TestAuthorizationConfirmationDenialRedirectsWithoutCode(t *testing.T) {
	srv := newTestOIDCServer(t)
	authState := srv.authState.(*inMemAuthState)
	token, tokenHash, err := newConfirmationToken()
	require.NoError(t, err)
	require.NoError(t, authState.StoreAuthorizationConfirmation(t.Context(), tokenHash, store.AuthorizationConfirmation{
		AuthCode:    store.AuthCode{RedirectURI: "https://client.example.com/callback?keep=1&code=old&error_description=old"},
		ClientState: "original-state",
	}))
	form := url.Values{"token": {token}, "decision": {"deny"}}
	req := httptest.NewRequest(http.MethodPost, confirmationPath, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	srv.AuthorizationConfirmationHandler().ServeHTTP(w, req)

	require.Equal(t, http.StatusSeeOther, w.Code)
	location, err := url.Parse(w.Header().Get("Location"))
	require.NoError(t, err)
	require.Equal(t, "access_denied", location.Query().Get("error"))
	require.Equal(t, "original-state", location.Query().Get("state"))
	require.Equal(t, "1", location.Query().Get("keep"))
	require.Empty(t, location.Query().Get("code"))
	require.Empty(t, location.Query().Get("error_description"))
	require.Empty(t, authState.codes)
}

func TestAuthorizationResultRedirectPreservesUnrelatedRawQuery(t *testing.T) {
	require.NoError(t, validateRedirectURIs([]string{"https://client.example.com/callback?tenant=a;b&keep=1"}))
	tests := []struct {
		name         string
		redirectURI  string
		clientState  string
		approve      bool
		code         string
		wantRawQuery string
	}{
		{
			name: "raw semicolon", redirectURI: "https://client.example.com/callback?tenant=a;b&keep=1",
			clientState: "new-state", approve: true, code: "new-code",
			wantRawQuery: "tenant=a;b&keep=1&code=new-code&state=new-state",
		},
		{
			name:        "duplicates and empty values",
			redirectURI: "https://client.example.com/callback?flag&empty=&dup=1&dup=2&code=old&error=old&error_description=old&state=old",
			clientState: "new-state", approve: false,
			wantRawQuery: "flag&empty=&dup=1&dup=2&error=access_denied&state=new-state",
		},
		{
			name:        "encoded reserved names",
			redirectURI: "https://client.example.com/callback?%63ode=old&%73tate=old&na%6De=v%2f",
			approve:     true, code: "new-code",
			wantRawQuery: "na%6De=v%2f&code=new-code",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := authorizationResultRedirect(&store.AuthorizationConfirmationResult{
				RedirectURI: test.redirectURI, ClientState: test.clientState,
			}, test.approve, test.code)
			require.NoError(t, err)
			parsed, err := url.Parse(got)
			require.NoError(t, err)
			require.Equal(t, test.wantRawQuery, parsed.RawQuery)
		})
	}
}

func TestRenderAuthorizationConfirmationEscapesUnnamedClientAndRedirect(t *testing.T) {
	w := httptest.NewRecorder()
	err := renderAuthorizationConfirmation(w, &store.PendingAuth{
		ClientID:    `<img src=x onerror="alert(1)">`,
		RedirectURI: `https://client.example.com/callback?next=<script>alert(1)</script>`,
		Scope:       scopeInsightsRead,
	}, "confirmation-token")
	require.NoError(t, err)
	require.Contains(t, w.Body.String(), "Authorize Unnamed client")
	require.Contains(t, w.Body.String(), `&lt;img src=x onerror=&#34;alert(1)&#34;&gt;`)
	require.Contains(t, w.Body.String(), `next=&lt;script&gt;alert(1)&lt;/script&gt;`)
	require.NotContains(t, w.Body.String(), `<img src=x`)
	require.NotContains(t, w.Body.String(), `<script>alert`)
	require.Contains(t, w.Body.String(), `<form id="authorization-confirmation" method="post" action="`+confirmationPath+`">`)
}

func TestAuthorizationConfirmationFormActionSource(t *testing.T) {
	tests := []struct {
		name        string
		redirectURI string
		want        string
	}{
		{name: "https", redirectURI: "https://client.example.com:8443/callback?tenant=one", want: "https://client.example.com:8443"},
		{name: "localhost", redirectURI: "http://localhost:55519/callback", want: "http://localhost:55519"},
		{name: "IPv6 loopback", redirectURI: "http://[::1]:55519/callback", want: "http://[::1]:55519"},
		{name: "international hostname", redirectURI: "https://bücher.example/callback", want: "https://xn--bcher-kva.example"},
		{name: "case normalization", redirectURI: "HTTPS://CLIENT.EXAMPLE/callback", want: "https://client.example"},
		{name: "custom scheme", redirectURI: "claude://auth/callback", want: "claude:"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := authorizationConfirmationFormActionSource(test.redirectURI)
			require.NoError(t, err)
			require.Equal(t, test.want, got)
		})
	}
}

func TestAuthorizationConfirmationFormActionSourceRejectsUnsafeHost(t *testing.T) {
	_, err := authorizationConfirmationFormActionSource("https://client.example;form-action%20*/callback")
	require.Error(t, err)
}

func TestGitHubCallbackConfirmationStoreFailureReturnsGenericError(t *testing.T) {
	srv := newTestOIDCServer(t)
	authState := srv.authState.(*inMemAuthState)
	authState.storeConfirmationErr = errors.New("database unavailable")
	srv.github = &mockGitHubConnector{
		token:    &oauth2.Token{AccessToken: "gha_test"},
		identity: &githubIdentity{ID: 42, Email: "dev@example.com", Login: "devuser"},
	}
	require.NoError(t, authState.StorePendingAuth(t.Context(), "store-failure-state", store.PendingAuth{
		ClientID: "external-client", RedirectURI: "https://client.example.com/callback",
		Scope: scopeInsightsRead, CodeChallenge: "challenge", ConfirmationRequired: true,
	}))

	req := httptest.NewRequest(http.MethodGet, "/auth/github/callback?state=store-failure-state&code=github-code", nil)
	w := httptest.NewRecorder()
	srv.GitHubCallbackHandler().ServeHTTP(w, req)
	require.Equal(t, http.StatusInternalServerError, w.Code)
	require.Contains(t, w.Body.String(), "internal error")
	require.NotContains(t, w.Body.String(), "database unavailable")
	require.Empty(t, authState.confirmations)
	require.Empty(t, authState.codes)
}

func TestAuthorizationConfirmationCompletionFailureReturnsGenericError(t *testing.T) {
	srv := newTestOIDCServer(t)
	authState := srv.authState.(*inMemAuthState)
	token, tokenHash, err := newConfirmationToken()
	require.NoError(t, err)
	require.NoError(t, authState.StoreAuthorizationConfirmation(t.Context(), tokenHash, store.AuthorizationConfirmation{
		AuthCode: store.AuthCode{RedirectURI: "https://client.example.com/callback"},
	}))
	authState.completeConfirmationErr = errors.New("database unavailable")
	form := url.Values{"token": {token}, "decision": {confirmationDecisionApprove}}
	req := httptest.NewRequest(http.MethodPost, confirmationPath, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", confirmationFormContentType)
	w := httptest.NewRecorder()
	srv.AuthorizationConfirmationHandler().ServeHTTP(w, req)
	require.Equal(t, http.StatusInternalServerError, w.Code)
	require.Contains(t, w.Body.String(), "internal error")
	require.NotContains(t, w.Body.String(), "database unavailable")
	require.Len(t, authState.confirmations, 1)
	require.Empty(t, authState.codes)
}

func TestAuthorizationConfirmationRejectsMalformedAndCrossOriginRequests(t *testing.T) {
	srv := newTestOIDCServer(t)
	tests := []struct {
		name, method, target, contentType, body string
		headers                                 map[string]string
		wantStatus                              int
	}{
		{name: "wrong method", method: http.MethodGet, target: confirmationPath, wantStatus: http.StatusMethodNotAllowed},
		{name: "query", method: http.MethodPost, target: confirmationPath + "?x=1", contentType: "application/x-www-form-urlencoded", body: "token=x&decision=approve", wantStatus: http.StatusBadRequest},
		{name: "wrong media type", method: http.MethodPost, target: confirmationPath, contentType: "application/json", body: `{}`, wantStatus: http.StatusUnsupportedMediaType},
		{name: "unknown field", method: http.MethodPost, target: confirmationPath, contentType: "application/x-www-form-urlencoded", body: "token=x&decision=approve&redirect_uri=x", wantStatus: http.StatusBadRequest},
		{name: "duplicate", method: http.MethodPost, target: confirmationPath, contentType: "application/x-www-form-urlencoded", body: "token=x&token=y&decision=approve", wantStatus: http.StatusBadRequest},
		{name: "oversized", method: http.MethodPost, target: confirmationPath, contentType: "application/x-www-form-urlencoded", body: strings.Repeat("x", (4<<10)+1), wantStatus: http.StatusRequestEntityTooLarge},
		{name: "cross site", method: http.MethodPost, target: confirmationPath, contentType: "application/x-www-form-urlencoded", body: "token=x&decision=approve", headers: map[string]string{"Sec-Fetch-Site": "cross-site"}, wantStatus: http.StatusForbidden},
		{name: "foreign origin", method: http.MethodPost, target: confirmationPath, contentType: "application/x-www-form-urlencoded", body: "token=x&decision=approve", headers: map[string]string{"Origin": "https://attacker.example"}, wantStatus: http.StatusForbidden},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			req := httptest.NewRequest(test.method, test.target, strings.NewReader(test.body))
			if test.contentType != "" {
				req.Header.Set("Content-Type", test.contentType)
			}
			for key, value := range test.headers {
				req.Header.Set(key, value)
			}
			w := httptest.NewRecorder()
			srv.AuthorizationConfirmationHandler().ServeHTTP(w, req)
			require.Equal(t, test.wantStatus, w.Code)
			require.Equal(t, "no-store", w.Header().Get("Cache-Control"))
		})
	}
}

func TestAuthorizationConfirmationConcurrentApprovalHasOneWinner(t *testing.T) {
	srv := newTestOIDCServer(t)
	token, tokenHash, err := newConfirmationToken()
	require.NoError(t, err)
	require.NoError(t, srv.authState.StoreAuthorizationConfirmation(t.Context(), tokenHash, store.AuthorizationConfirmation{
		AuthCode: store.AuthCode{RedirectURI: "https://client.example.com/callback"},
	}))
	form := url.Values{"token": {token}, "decision": {"approve"}}.Encode()
	statuses := make(chan int, 2)
	for range 2 {
		go func() {
			req := httptest.NewRequest(http.MethodPost, confirmationPath, strings.NewReader(form))
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			w := httptest.NewRecorder()
			srv.AuthorizationConfirmationHandler().ServeHTTP(w, req)
			statuses <- w.Code
		}()
	}
	got := []int{<-statuses, <-statuses}
	require.ElementsMatch(t, []int{http.StatusSeeOther, http.StatusBadRequest}, got)
}

func TestAuthorizationConfirmationConcurrentApproveAndDenyHaveOneWinner(t *testing.T) {
	srv := newTestOIDCServer(t)
	authState := srv.authState.(*inMemAuthState)
	token, tokenHash, err := newConfirmationToken()
	require.NoError(t, err)
	require.NoError(t, authState.StoreAuthorizationConfirmation(t.Context(), tokenHash, store.AuthorizationConfirmation{
		AuthCode: store.AuthCode{RedirectURI: "https://client.example.com/callback"},
	}))
	type submissionResult struct {
		decision string
		status   int
	}
	results := make(chan submissionResult, 2)
	for _, decision := range []string{confirmationDecisionApprove, confirmationDecisionDeny} {
		go func(decision string) {
			form := url.Values{"token": {token}, "decision": {decision}}.Encode()
			req := httptest.NewRequest(http.MethodPost, confirmationPath, strings.NewReader(form))
			req.Header.Set("Content-Type", confirmationFormContentType)
			w := httptest.NewRecorder()
			srv.AuthorizationConfirmationHandler().ServeHTTP(w, req)
			results <- submissionResult{decision: decision, status: w.Code}
		}(decision)
	}
	got := []submissionResult{<-results, <-results}
	var winner string
	for _, result := range got {
		if result.status == http.StatusSeeOther {
			winner = result.decision
		} else {
			require.Equal(t, http.StatusBadRequest, result.status)
		}
	}
	require.NotEmpty(t, winner)
	if winner == confirmationDecisionApprove {
		require.Len(t, authState.codes, 1)
	} else {
		require.Empty(t, authState.codes)
	}
}

func confirmationTokenFromBody(t *testing.T, body string) string {
	t.Helper()
	matches := regexp.MustCompile(`name="token" value="([^"]+)"`).FindStringSubmatch(body)
	require.Len(t, matches, 2)
	return matches[1]
}

// --- Logout ---

func TestLogoutHandler_RevokesToken(t *testing.T) {
	srv := newTestOIDCServer(t)

	tokenString, err := srv.IssueJWT("12345678", "insights:read", uuid.New().String(), time.Now().Add(time.Hour))
	require.NoError(t, err)

	_, err = srv.VerifyJWT(t.Context(), tokenString, nil)
	require.NoError(t, err)

	req := httptest.NewRequest(http.MethodPost, "/auth/logout", nil)
	req.Header.Set("Authorization", "Bearer "+tokenString)
	w := httptest.NewRecorder()
	srv.LogoutHandler().ServeHTTP(w, req)
	require.Equal(t, http.StatusNoContent, w.Code)

	_, err = srv.VerifyJWT(t.Context(), tokenString, nil)
	require.Error(t, err)
}

func TestLogoutHandler_MissingToken(t *testing.T) {
	srv := newTestOIDCServer(t)
	req := httptest.NewRequest(http.MethodPost, "/auth/logout", nil)
	w := httptest.NewRecorder()
	srv.LogoutHandler().ServeHTTP(w, req)
	require.Equal(t, http.StatusUnauthorized, w.Code)
}

func TestLogoutHandler_InvalidToken(t *testing.T) {
	srv := newTestOIDCServer(t)
	req := httptest.NewRequest(http.MethodPost, "/auth/logout", nil)
	req.Header.Set("Authorization", "Bearer not-a-valid-jwt")
	w := httptest.NewRecorder()
	srv.LogoutHandler().ServeHTTP(w, req)
	require.Equal(t, http.StatusUnauthorized, w.Code)
}

func TestLogoutHandler_WrongMethod(t *testing.T) {
	srv := newTestOIDCServer(t)
	req := httptest.NewRequest(http.MethodGet, "/auth/logout", nil)
	w := httptest.NewRecorder()
	srv.LogoutHandler().ServeHTTP(w, req)
	require.Equal(t, http.StatusMethodNotAllowed, w.Code)
}

// --- VerifyJWT ---

func TestVerifyJWT_ValidToken(t *testing.T) {
	srv := newTestOIDCServer(t)

	tokenString, err := srv.IssueJWT("12345678", "insights:read insights:write", uuid.New().String(), time.Now().Add(time.Hour))
	require.NoError(t, err)

	info, err := srv.VerifyJWT(t.Context(), tokenString, nil)
	require.NoError(t, err)
	require.Equal(t, "12345678", info.UserID)
	require.Contains(t, info.Scopes, "insights:read")
	require.Contains(t, info.Scopes, "insights:write")
	require.False(t, info.Expiration.IsZero())
}

func TestVerifyJWT_Garbage(t *testing.T) {
	srv := newTestOIDCServer(t)
	_, err := srv.VerifyJWT(t.Context(), "not-a-jwt", nil)
	require.Error(t, err)
}

func TestVerifyJWT_WrongSigningKey(t *testing.T) {
	a := newTestOIDCServer(t)
	b := newTestOIDCServer(t)

	tokenString, err := b.IssueJWT("12345678", "insights:read", uuid.New().String(), time.Now().Add(time.Hour))
	require.NoError(t, err)

	_, err = a.VerifyJWT(t.Context(), tokenString, nil)
	require.Error(t, err)
}

func TestVerifyJWT_RevokedToken(t *testing.T) {
	srv := newTestOIDCServer(t)

	tokenString, err := srv.IssueJWT("12345678", "insights:read", uuid.New().String(), time.Now().Add(time.Hour))
	require.NoError(t, err)

	req := httptest.NewRequest(http.MethodPost, "/auth/logout", nil)
	req.Header.Set("Authorization", "Bearer "+tokenString)
	srv.LogoutHandler().ServeHTTP(httptest.NewRecorder(), req)

	_, err = srv.VerifyJWT(t.Context(), tokenString, nil)
	require.Error(t, err)
}

// --- IssueJWT aud claim ---

func TestIssueJWT_ContainsAudClaim(t *testing.T) {
	srv := newTestOIDCServer(t)

	tokenString, err := srv.IssueJWT("12345678", "insights:read", uuid.New().String(), time.Now().Add(time.Hour))
	require.NoError(t, err)

	tok, err := jwt.ParseString(tokenString, jwt.WithKey(jwa.ES384(), srv.pubkey))
	require.NoError(t, err)

	aud, ok := tok.Audience()
	require.True(t, ok, "aud claim must be present")
	require.Contains(t, aud, "http://example.com/mcp")
}

// --- VerifyJWT aud / iss validation ---

func signCustomToken(t *testing.T, srv *Server, extra func(*jwt.Builder)) string {
	t.Helper()
	b := jwt.NewBuilder().
		Issuer("http://example.com").
		Subject("12345678").
		IssuedAt(time.Now()).
		Expiration(time.Now().Add(time.Hour)).
		Audience([]string{"http://example.com/mcp"}).
		Claim("scope", "insights:read").
		Claim("jti", uuid.New().String())
	if extra != nil {
		extra(b)
	}
	tok, err := b.Build()
	require.NoError(t, err)
	signed, err := jwt.Sign(tok, jwt.WithKey(jwa.ES384(), srv.privkey))
	require.NoError(t, err)
	return string(signed)
}

func TestVerifyJWT_MissingAud(t *testing.T) {
	srv := newTestOIDCServer(t)

	tok, err := jwt.NewBuilder().
		Issuer("http://example.com").
		Subject("12345678").
		IssuedAt(time.Now()).
		Expiration(time.Now().Add(time.Hour)).
		Claim("scope", "insights:read").
		Claim("jti", uuid.New().String()).
		Build()
	require.NoError(t, err)
	signed, err := jwt.Sign(tok, jwt.WithKey(jwa.ES384(), srv.privkey))
	require.NoError(t, err)

	_, err = srv.VerifyJWT(t.Context(), string(signed), nil)
	require.Error(t, err)
}

func TestVerifyJWT_WrongAud(t *testing.T) {
	srv := newTestOIDCServer(t)

	tokenString := signCustomToken(t, srv, func(b *jwt.Builder) {
		b.Audience([]string{"https://different-resource.example.com/mcp"})
	})

	_, err := srv.VerifyJWT(t.Context(), tokenString, nil)
	require.Error(t, err)
}

func TestVerifyJWT_WrongIssuer(t *testing.T) {
	srv := newTestOIDCServer(t)

	tokenString := signCustomToken(t, srv, func(b *jwt.Builder) {
		b.Issuer("https://evil.example.com")
	})

	_, err := srv.VerifyJWT(t.Context(), tokenString, nil)
	require.Error(t, err)
}

// --- Redirect URI validation ---

func TestValidateRedirectURIs_AcceptsHTTPS(t *testing.T) {
	require.NoError(t, validateRedirectURIs([]string{"https://client.example.com/callback"}))
}

func TestValidateRedirectURIs_AcceptsLocalhost(t *testing.T) {
	require.NoError(t, validateRedirectURIs([]string{"http://localhost:4000/callback"}))
	require.NoError(t, validateRedirectURIs([]string{"http://localhost/callback"}))
}

func TestValidateRedirectURIs_AcceptsLoopback(t *testing.T) {
	require.NoError(t, validateRedirectURIs([]string{"http://127.0.0.1:4000/callback"}))
	require.NoError(t, validateRedirectURIs([]string{"http://[::1]:4000/callback"}))
}

func TestValidateRedirectURIs_AcceptsCustomScheme(t *testing.T) {
	require.NoError(t, validateRedirectURIs([]string{"cursor://callback"}))
	require.NoError(t, validateRedirectURIs([]string{"claude://auth/callback"}))
}

func TestValidateRedirectURIs_RejectsNonLocalhostHTTP(t *testing.T) {
	err := validateRedirectURIs([]string{"http://evil.example.com/callback"})
	require.Error(t, err)
	require.Contains(t, err.Error(), "only allowed for localhost")
}

func TestValidateRedirectURIs_RejectsFragment(t *testing.T) {
	err := validateRedirectURIs([]string{"https://client.example.com/callback#token"})
	require.Error(t, err)
	require.Contains(t, err.Error(), "fragment")
}

func TestValidateRedirectURIs_RejectsWildcard(t *testing.T) {
	err := validateRedirectURIs([]string{"https://*.example.com/callback"})
	require.Error(t, err)
	require.Contains(t, err.Error(), "wildcard")
}

func TestValidateRedirectURIs_RejectsMalformedURIs(t *testing.T) {
	for _, uri := range []string{
		"callback",
		"/callback",
		"https:///callback",
		"https://user:pass@client.example.com/callback",
		"javascript:alert(1)",
		"data:text/plain,callback",
	} {
		t.Run(uri, func(t *testing.T) {
			require.Error(t, validateRedirectURIs([]string{uri}))
		})
	}
}

// --- Spies ---

type testGrantStore struct {
	mu          sync.Mutex
	calls       []store.Grant
	byRefresh   map[string]store.Grant
	rotateCalls []rotateCall
	deleteCalls []string
	retired     []store.RetiredRefreshToken
	rotateErr   error
	// revocation, when set, receives the old jti atomically with the rotation —
	// mirrors the postgres tx-based rotate+revoke.
	revocation RevocationStore
}

type rotateCall struct {
	oldToken     string
	oldJTI       string
	oldJWTExpiry time.Time
	grant        store.Grant
	retired      *store.RetiredRefreshToken
}

func (s *testGrantStore) seed(g store.Grant) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.byRefresh == nil {
		s.byRefresh = make(map[string]store.Grant)
	}
	s.byRefresh[g.OurRefreshToken] = g
}

func (s *testGrantStore) UpsertGrant(_ context.Context, g store.Grant) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls = append(s.calls, g)
	if g.OurRefreshToken != "" {
		if s.byRefresh == nil {
			s.byRefresh = make(map[string]store.Grant)
		}
		s.byRefresh[g.OurRefreshToken] = g
	}
	return nil
}

func (s *testGrantStore) GetGrantByRefreshToken(_ context.Context, token string) (*store.Grant, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	g, ok := s.byRefresh[token]
	if !ok {
		return nil, store.ErrNotFound
	}
	return &g, nil
}

func (s *testGrantStore) GetGrant(_ context.Context, jti string) (*store.Grant, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, g := range s.byRefresh {
		if g.JTI == jti {
			return &g, nil
		}
	}
	return nil, store.ErrNotFound
}

func (s *testGrantStore) GetRetiredRefreshToken(_ context.Context, tokenHash []byte) (*store.RetiredRefreshToken, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, rt := range s.retired {
		if string(rt.TokenHash) == string(tokenHash) {
			cp := rt
			return &cp, nil
		}
	}
	return nil, store.ErrNotFound
}

func (s *testGrantStore) RotateGrant(ctx context.Context, oldToken, oldJTI string, oldJWTExpiry time.Time, g store.Grant, retired *store.RetiredRefreshToken) (*store.Grant, error) {
	s.mu.Lock()
	s.rotateCalls = append(s.rotateCalls, rotateCall{oldToken, oldJTI, oldJWTExpiry, g, retired})
	if s.rotateErr != nil {
		s.mu.Unlock()
		return nil, s.rotateErr
	}
	if _, ok := s.byRefresh[oldToken]; !ok {
		s.mu.Unlock()
		return nil, store.ErrNotFound
	}
	delete(s.byRefresh, oldToken)
	if g.OurRefreshToken != "" {
		s.byRefresh[g.OurRefreshToken] = g
	}
	if retired != nil {
		s.retired = append(s.retired, *retired)
	}
	rev := s.revocation
	s.mu.Unlock()
	if rev != nil && oldJTI != "" {
		if err := rev.RevokeToken(ctx, oldJTI, oldJWTExpiry); err != nil {
			return nil, err
		}
	}
	return &g, nil
}

func (s *testGrantStore) DeleteGrant(_ context.Context, jti string, retired *store.RetiredRefreshToken) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.deleteCalls = append(s.deleteCalls, jti)
	if retired != nil {
		s.retired = append(s.retired, *retired)
	}
	return nil
}

type upsertCall struct {
	profile store.GitHubProfile
}

type testUserUpserter struct {
	calls []upsertCall
	err   error
}

func (u *testUserUpserter) UpsertUser(_ context.Context, profile store.GitHubProfile) (*store.User, error) {
	u.calls = append(u.calls, upsertCall{profile: profile})
	if u.err != nil {
		return nil, u.err
	}
	return &store.User{ID: uuid.New(), GitHubID: profile.GitHubID, Email: profile.Email, Login: profile.Login}, nil
}

type mockGitHubConnector struct {
	identity *githubIdentity
	token    *oauth2.Token
	err      error

	refreshToken *oauth2.Token
	refreshErr   error
	refreshCalls []string
}

func (m *mockGitHubConnector) AuthCodeURL(state string) string {
	return "https://github.com/login/oauth/authorize?state=" + state
}

func (m *mockGitHubConnector) ExchangeCode(_ context.Context, _ string) (*oauth2.Token, *githubIdentity, error) {
	return m.token, m.identity, m.err
}

func (m *mockGitHubConnector) RefreshToken(_ context.Context, refreshToken string) (*oauth2.Token, *githubIdentity, error) {
	m.refreshCalls = append(m.refreshCalls, refreshToken)
	if m.refreshErr != nil {
		return nil, nil, m.refreshErr
	}
	tok := m.refreshToken
	if tok == nil {
		tok = m.token
	}
	return tok, m.identity, nil
}

type testClientStore struct {
	records []store.OAuthClient
	touches []string
}

func (s *testClientStore) TouchClient(_ context.Context, clientID string) error {
	s.touches = append(s.touches, clientID)
	return nil
}

func (s *testClientStore) SaveClient(_ context.Context, c store.OAuthClient) error {
	s.records = append(s.records, c)
	return nil
}

func (s *testClientStore) GetClient(_ context.Context, clientID string) (*store.OAuthClient, error) {
	for _, c := range s.records {
		if c.ClientID == clientID {
			return &c, nil
		}
	}
	return nil, store.ErrNotFound
}

func TestDCRHandler_PersistsToClientStore(t *testing.T) {
	srv := newTestOIDCServer(t)
	cs := &testClientStore{}
	srv.clients = cs

	body := `{"redirect_uris":["https://client.example.com/callback"],"client_name":"My Client","scope":"insights:read"}`
	req := httptest.NewRequest(http.MethodPost, "/oauth2/register", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.DCRHandler().ServeHTTP(w, req)

	require.Equal(t, http.StatusCreated, w.Code)
	require.Len(t, cs.records, 1)

	r := cs.records[0]
	require.NotEmpty(t, r.ClientID)
	require.Equal(t, "My Client", r.ClientName)
	require.Equal(t, []string{"https://client.example.com/callback"}, r.RedirectURIs)
	require.Equal(t, "insights:read", r.Scope)
	require.False(t, r.IssuedAt.IsZero())
	require.Nil(t, r.ExpiresAt)
}
