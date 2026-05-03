package oidc

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/lestrrat-go/jwx/v3/jwa"
	"github.com/lestrrat-go/jwx/v3/jwk"
	"github.com/lestrrat-go/jwx/v3/jwt"
	"github.com/modelcontextprotocol/go-sdk/auth"
	"github.com/modelcontextprotocol/go-sdk/oauthex"
	"github.com/stretchr/testify/require"
	"github.com/wolfeidau/starlogz/internal/store"
	"golang.org/x/oauth2"
)

// --- In-memory test implementations ---

type inMemAuthState struct {
	mu      sync.Mutex
	pending map[string]store.PendingAuth
	codes   map[string]store.AuthCode
}

func newInMemAuthState() *inMemAuthState {
	return &inMemAuthState{
		pending: make(map[string]store.PendingAuth),
		codes:   make(map[string]store.AuthCode),
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
	privkey, err := ecdsa.GenerateKey(elliptic.P384(), rand.Reader)
	require.NoError(t, err)
	raw, err := jwk.Import(privkey)
	require.NoError(t, err)
	srv, err := NewServer(Config{
		BaseURL:    "http://example.com",
		AuthState:  newInMemAuthState(),
		Revocation: newInMemRevocation(),
	}, raw)
	require.NoError(t, err)
	return srv
}

func pkceChallenge(verifier string) string {
	h := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(h[:])
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
	require.ElementsMatch(t, []string{"facts:read", "facts:write", "org:admin"}, meta.ScopesSupported)
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
	require.ElementsMatch(t, []string{"facts:read", "facts:write", "org:admin"}, meta.ScopesSupported)
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

	tokenString, err := srv.IssueJWT("12345678", "user@example.com", "facts:read", uuid.New().String(), time.Now().Add(time.Hour))
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

	body := `{"redirect_uris":["https://client.example.com/callback"],"client_name":"Test Client","grant_types":["authorization_code","implicit"],"response_types":["code"],"token_endpoint_auth_method":"none","scope":"facts:read facts:write"}`
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
	require.Equal(t, []any{"authorization_code"}, grants)
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
	require.Equal(t, []any{"authorization_code"}, resp["grant_types"])
	require.Equal(t, []any{"code"}, resp["response_types"])
	require.Equal(t, "none", resp["token_endpoint_auth_method"])
}

func TestDCRHandler_WrongMethod(t *testing.T) {
	srv := newTestOIDCServer(t)
	req := httptest.NewRequest(http.MethodGet, "/oauth2/register", nil)
	w := httptest.NewRecorder()
	srv.DCRHandler().ServeHTTP(w, req)
	require.Equal(t, http.StatusMethodNotAllowed, w.Code)
}

// --- Authorize ---

func TestAuthorizeHandler_ValidParams_RedirectsToGitHub(t *testing.T) {
	srv := newTestOIDCServer(t)

	verifier := "dBjftJeZ4CVP-mB92K27uhbUJU1p1r_wW1gFWFOEjXk"
	q := url.Values{
		"response_type":         {"code"},
		"client_id":             {"test-client"},
		"redirect_uri":          {"https://client.example.com/callback"},
		"scope":                 {"facts:read"},
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
		"scope":                 {"facts:read unknown:scope"},
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

	verifier := "dBjftJeZ4CVP-mB92K27uhbUJU1p1r_wW1gFWFOEjXk"
	code := "test-auth-code-abc123"
	require.NoError(t, srv.authState.StoreAuthCode(context.Background(), code, store.AuthCode{
		Sub:           "12345678",
		GitHubID:      12345678,
		Email:         "user@example.com",
		Scope:         "facts:read facts:write",
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
	require.Equal(t, "facts:read facts:write", resp["scope"])

	tokenString, ok := resp["access_token"].(string)
	require.True(t, ok)
	info, err := srv.VerifyJWT(context.Background(), tokenString, nil)
	require.NoError(t, err)
	require.Equal(t, "12345678", info.UserID)
}

func TestTokenHandler_CodeConsumedAfterUse(t *testing.T) {
	srv := newTestOIDCServer(t)

	verifier := "dBjftJeZ4CVP-mB92K27uhbUJU1p1r_wW1gFWFOEjXk"
	code := "one-time-code-xyz"
	require.NoError(t, srv.authState.StoreAuthCode(context.Background(), code, store.AuthCode{
		Sub:           "12345678",
		GitHubID:      12345678,
		Email:         "user@example.com",
		Scope:         "facts:read",
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

func TestTokenHandler_PKCEMismatch(t *testing.T) {
	srv := newTestOIDCServer(t)

	code := "pkce-mismatch-code"
	require.NoError(t, srv.authState.StoreAuthCode(context.Background(), code, store.AuthCode{
		Sub:           "12345678",
		GitHubID:      12345678,
		Email:         "user@example.com",
		Scope:         "facts:read",
		CodeChallenge: pkceChallenge("correct-verifier-that-is-long-enough-to-be-valid"),
		RedirectURI:   "https://client.example.com/callback",
	}))

	form := url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {code},
		"code_verifier": {"wrong-verifier-that-is-also-long-enough-for-pkce"},
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
	require.NoError(t, srv.authState.StoreAuthCode(context.Background(), code, store.AuthCode{
		Sub:           "12345678",
		GitHubID:      12345678,
		Email:         "user@example.com",
		Scope:         "facts:read",
		CodeChallenge: pkceChallenge(verifier),
		RedirectURI:   "https://client.example.com/callback",
	}))

	form := url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {code},
		"code_verifier": {verifier},
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
	require.NoError(t, srv.authState.StoreAuthCode(context.Background(), code, store.AuthCode{
		Sub:           "12345678",
		GitHubID:      12345678,
		Email:         "user@example.com",
		Scope:         "facts:read",
		CodeChallenge: pkceChallenge(verifier),
		RedirectURI:   "https://client.example.com/callback",
	}))

	form := url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {code},
		"code_verifier": {verifier},
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
	raw, err := jwk.Import(privkey)
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

	verifier := "dBjftJeZ4CVP-mB92K27uhbUJU1p1r_wW1gFWFOEjXk"
	code := "grant-seam-code"
	accessExpiry := time.Now().Add(8 * time.Hour).Truncate(time.Second)
	refreshExpiry := time.Now().Add(180 * 24 * time.Hour).Truncate(time.Second)
	require.NoError(t, authState.StoreAuthCode(context.Background(), code, store.AuthCode{
		Sub:                "99887766",
		GitHubID:           99887766,
		Email:              "user@example.com",
		Scope:              "facts:read",
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
		"redirect_uri":  {"https://client.example.com/callback"},
	}
	req := httptest.NewRequest(http.MethodPost, "/oauth2/token", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	srv.TokenHandler().ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	require.Len(t, gs.calls, 1, "UpsertGrant must be called exactly once")

	p := gs.calls[0]
	require.Equal(t, int64(99887766), p.GitHubID)
	require.NotEmpty(t, p.JTI)
	require.Equal(t, "gha_access_abc", p.AccessToken)
	require.Equal(t, "ghr_refresh_xyz", p.RefreshToken)
	require.WithinDuration(t, accessExpiry, p.AccessTokenExpiry, time.Second)
	require.WithinDuration(t, refreshExpiry, p.RefreshTokenExpiry, time.Second)
	require.WithinDuration(t, time.Now().Add(7*24*time.Hour), p.JWTExpiry, 5*time.Second)
}

// --- Refresh grant ---

func newRefreshTestServer(t *testing.T, gs *testGrantStore, gh *mockGitHubConnector) *Server {
	t.Helper()
	privkey, err := ecdsa.GenerateKey(elliptic.P384(), rand.Reader)
	require.NoError(t, err)
	raw, err := jwk.Import(privkey)
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

	verifier := "dBjftJeZ4CVP-mB92K27uhbUJU1p1r_wW1gFWFOEjXk"
	code := "auth-code-with-refresh"
	require.NoError(t, srv.authState.StoreAuthCode(context.Background(), code, store.AuthCode{
		Sub:                "12345678",
		GitHubID:           12345678,
		Email:              "user@example.com",
		Scope:              "facts:read facts:write",
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
	require.Equal(t, "facts:read facts:write", gs.calls[0].Scope)
}

func TestTokenHandler_AuthCodeNoGitHubRefreshSkipsOurRefresh(t *testing.T) {
	gs := &testGrantStore{}
	srv := newRefreshTestServer(t, gs, nil)

	verifier := "dBjftJeZ4CVP-mB92K27uhbUJU1p1r_wW1gFWFOEjXk"
	code := "auth-code-no-refresh"
	require.NoError(t, srv.authState.StoreAuthCode(context.Background(), code, store.AuthCode{
		Sub:           "12345678",
		GitHubID:      12345678,
		Email:         "user@example.com",
		Scope:         "facts:read",
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
		GitHubID:           12345678,
		OurRefreshToken:    oldRefresh,
		ClientID:           "test-client",
		Scope:              "facts:read facts:write",
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
	require.Equal(t, "facts:read facts:write", resp["scope"])
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
	rotatedJTI := gs.rotateCalls[0].grant.JTI
	require.NotEqual(t, oldJTI, rotatedJTI, "jti must rotate")

	// refresh_token_expires_in is recomputed from the fresh GitHub refresh token —
	// extractRefreshExpiry falls back to ~6 months when the upstream response doesn't
	// carry refresh_token_expires_in (which is the case for the mock).
	exp, ok := resp["refresh_token_expires_in"].(float64)
	require.True(t, ok)
	require.InDelta(t, exp, (6 * 30 * 24 * time.Hour).Seconds(), 60,
		"refresh_token_expires_in must reflect the fresh GitHub refresh window")

	// Old JTI must be on the revocation list (atomic with rotation).
	revoked, err := srv.revocation.IsTokenRevoked(context.Background(), oldJTI)
	require.NoError(t, err)
	require.True(t, revoked, "previous JWT jti must be revoked after rotation")

	// New JWT must verify and carry the rotated jti, not the old one.
	tokenString, _ := resp["access_token"].(string)
	info, err := srv.VerifyJWT(context.Background(), tokenString, nil)
	require.NoError(t, err)
	require.Equal(t, "12345678", info.UserID)

	parsed, err := jwt.ParseString(tokenString, jwt.WithKey(jwa.ES384(), srv.pubkey))
	require.NoError(t, err)
	var jtiClaim string
	require.NoError(t, parsed.Get("jti", &jtiClaim))
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
		Scope:              "facts:read",
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

	revoked, err := srv.revocation.IsTokenRevoked(context.Background(), jti)
	require.NoError(t, err)
	require.True(t, revoked, "expired-grant jti must be revoked")
	require.Equal(t, []string{jti}, gs.deleteCalls, "expired grant row must be deleted")
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
	revoked, err := srv.revocation.IsTokenRevoked(context.Background(), jti)
	require.NoError(t, err)
	require.True(t, revoked, "old jti must be revoked when grant is torn down")
}

func TestTokenHandler_RefreshGrant_NoGrantStore(t *testing.T) {
	t.Helper()
	privkey, err := ecdsa.GenerateKey(elliptic.P384(), rand.Reader)
	require.NoError(t, err)
	raw, err := jwk.Import(privkey)
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

	require.NoError(t, srv.authState.StorePendingAuth(context.Background(), "valid-state", store.PendingAuth{
		RedirectURI:   "https://client.example.com/callback",
		Scope:         "facts:read",
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
			ID:    42,
			Email: "dev@example.com",
			Login: "devuser",
		},
	}

	require.NoError(t, srv.authState.StorePendingAuth(context.Background(), "cb-state", store.PendingAuth{
		RedirectURI:   "https://client.example.com/callback",
		Scope:         "facts:read",
		CodeChallenge: pkceChallenge("verifier"),
	}))

	req := httptest.NewRequest(http.MethodGet, "/auth/github/callback?state=cb-state&code=code", nil)
	w := httptest.NewRecorder()
	srv.GitHubCallbackHandler().ServeHTTP(w, req)

	require.Equal(t, http.StatusFound, w.Code)
	require.Len(t, us.calls, 1)
	require.Equal(t, int64(42), us.calls[0].githubID)
	require.Equal(t, "dev@example.com", us.calls[0].email)
	require.Equal(t, "devuser", us.calls[0].login)

	loc := w.Header().Get("Location")
	require.Contains(t, loc, "code=")
}

// --- Logout ---

func TestLogoutHandler_RevokesToken(t *testing.T) {
	srv := newTestOIDCServer(t)

	tokenString, err := srv.IssueJWT("12345678", "user@example.com", "facts:read", uuid.New().String(), time.Now().Add(time.Hour))
	require.NoError(t, err)

	_, err = srv.VerifyJWT(context.Background(), tokenString, nil)
	require.NoError(t, err)

	req := httptest.NewRequest(http.MethodPost, "/auth/logout", nil)
	req.Header.Set("Authorization", "Bearer "+tokenString)
	w := httptest.NewRecorder()
	srv.LogoutHandler().ServeHTTP(w, req)
	require.Equal(t, http.StatusNoContent, w.Code)

	_, err = srv.VerifyJWT(context.Background(), tokenString, nil)
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

	tokenString, err := srv.IssueJWT("12345678", "user@example.com", "facts:read facts:write", uuid.New().String(), time.Now().Add(time.Hour))
	require.NoError(t, err)

	info, err := srv.VerifyJWT(context.Background(), tokenString, nil)
	require.NoError(t, err)
	require.Equal(t, "12345678", info.UserID)
	require.Contains(t, info.Scopes, "facts:read")
	require.Contains(t, info.Scopes, "facts:write")
	require.False(t, info.Expiration.IsZero())
}

func TestVerifyJWT_Garbage(t *testing.T) {
	srv := newTestOIDCServer(t)
	_, err := srv.VerifyJWT(context.Background(), "not-a-jwt", nil)
	require.Error(t, err)
}

func TestVerifyJWT_WrongSigningKey(t *testing.T) {
	a := newTestOIDCServer(t)
	b := newTestOIDCServer(t)

	tokenString, err := b.IssueJWT("12345678", "user@example.com", "facts:read", uuid.New().String(), time.Now().Add(time.Hour))
	require.NoError(t, err)

	_, err = a.VerifyJWT(context.Background(), tokenString, nil)
	require.Error(t, err)
}

func TestVerifyJWT_RevokedToken(t *testing.T) {
	srv := newTestOIDCServer(t)

	tokenString, err := srv.IssueJWT("12345678", "user@example.com", "facts:read", uuid.New().String(), time.Now().Add(time.Hour))
	require.NoError(t, err)

	req := httptest.NewRequest(http.MethodPost, "/auth/logout", nil)
	req.Header.Set("Authorization", "Bearer "+tokenString)
	srv.LogoutHandler().ServeHTTP(httptest.NewRecorder(), req)

	_, err = srv.VerifyJWT(context.Background(), tokenString, nil)
	require.Error(t, err)
}

// --- IssueJWT aud claim ---

func TestIssueJWT_ContainsAudClaim(t *testing.T) {
	srv := newTestOIDCServer(t)

	tokenString, err := srv.IssueJWT("12345678", "user@example.com", "facts:read", uuid.New().String(), time.Now().Add(time.Hour))
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
		Claim("scope", "facts:read").
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
		Claim("scope", "facts:read").
		Claim("jti", uuid.New().String()).
		Build()
	require.NoError(t, err)
	signed, err := jwt.Sign(tok, jwt.WithKey(jwa.ES384(), srv.privkey))
	require.NoError(t, err)

	_, err = srv.VerifyJWT(context.Background(), string(signed), nil)
	require.Error(t, err)
}

func TestVerifyJWT_WrongAud(t *testing.T) {
	srv := newTestOIDCServer(t)

	tokenString := signCustomToken(t, srv, func(b *jwt.Builder) {
		b.Audience([]string{"https://different-resource.example.com/mcp"})
	})

	_, err := srv.VerifyJWT(context.Background(), tokenString, nil)
	require.Error(t, err)
}

func TestVerifyJWT_WrongIssuer(t *testing.T) {
	srv := newTestOIDCServer(t)

	tokenString := signCustomToken(t, srv, func(b *jwt.Builder) {
		b.Issuer("https://evil.example.com")
	})

	_, err := srv.VerifyJWT(context.Background(), tokenString, nil)
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

// --- Spies ---

type testGrantStore struct {
	mu          sync.Mutex
	calls       []store.Grant
	byRefresh   map[string]store.Grant
	rotateCalls []rotateCall
	deleteCalls []string
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

func (s *testGrantStore) RotateGrant(ctx context.Context, oldToken, oldJTI string, oldJWTExpiry time.Time, g store.Grant) (*store.Grant, error) {
	s.mu.Lock()
	s.rotateCalls = append(s.rotateCalls, rotateCall{oldToken, oldJTI, oldJWTExpiry, g})
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
	rev := s.revocation
	s.mu.Unlock()
	if rev != nil && oldJTI != "" {
		if err := rev.RevokeToken(ctx, oldJTI, oldJWTExpiry); err != nil {
			return nil, err
		}
	}
	return &g, nil
}

func (s *testGrantStore) DeleteGrant(_ context.Context, jti string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.deleteCalls = append(s.deleteCalls, jti)
	return nil
}

type upsertCall struct {
	githubID int64
	email    string
	login    string
}

type testUserUpserter struct {
	calls []upsertCall
	err   error
}

func (u *testUserUpserter) UpsertUser(_ context.Context, githubID int64, email, login string) (*store.User, error) {
	u.calls = append(u.calls, upsertCall{githubID, email, login})
	if u.err != nil {
		return nil, u.err
	}
	return &store.User{ID: uuid.New(), GitHubID: githubID, Email: email, Login: login}, nil
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
}

func (s *testClientStore) SaveClient(_ context.Context, c store.OAuthClient) error {
	s.records = append(s.records, c)
	return nil
}

func TestDCRHandler_PersistsToClientStore(t *testing.T) {
	srv := newTestOIDCServer(t)
	cs := &testClientStore{}

	body := `{"redirect_uris":["https://client.example.com/callback"],"client_name":"My Client","scope":"facts:read"}`
	req := httptest.NewRequest(http.MethodPost, "/oauth2/register", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.dcrHandler(cs).ServeHTTP(w, req)

	require.Equal(t, http.StatusCreated, w.Code)
	require.Len(t, cs.records, 1)

	r := cs.records[0]
	require.NotEmpty(t, r.ClientID)
	require.Equal(t, "My Client", r.ClientName)
	require.Equal(t, []string{"https://client.example.com/callback"}, r.RedirectURIs)
	require.Equal(t, "facts:read", r.Scope)
	require.False(t, r.IssuedAt.IsZero())
	require.True(t, r.ExpiresAt.After(r.IssuedAt), "ExpiresAt must be after IssuedAt")
	require.InDelta(t, clientRegistrationTTL.Seconds(), r.ExpiresAt.Sub(r.IssuedAt).Seconds(), 2)
}
