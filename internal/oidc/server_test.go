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

func newTestOIDCServer(t *testing.T) *Server {
	t.Helper()
	privkey, err := ecdsa.GenerateKey(elliptic.P384(), rand.Reader)
	require.NoError(t, err)
	raw, err := jwk.Import(privkey)
	require.NoError(t, err)
	srv, err := NewServer(Config{BaseURL: "http://example.com"}, raw)
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
	require.Equal(t, "*", w.Header().Get("Access-Control-Allow-Origin"))

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

	// RFC 8414 §2: issuer MUST be present and MUST be an https or http URL.
	require.NotEmpty(t, meta.Issuer)

	// All endpoint URLs must share the same origin as the issuer.
	for _, ep := range []string{
		meta.AuthorizationEndpoint,
		meta.TokenEndpoint,
		meta.JWKSURI,
		meta.RegistrationEndpoint,
	} {
		require.NotEmpty(t, ep)
		require.True(t, strings.HasPrefix(ep, meta.Issuer), "endpoint %q must be under issuer %q", ep, meta.Issuer)
	}

	// v0.1 only supports authorization_code with PKCE — no implicit, no refresh tokens.
	require.Equal(t, []string{"code"}, meta.ResponseTypesSupported)
	require.Equal(t, []string{"authorization_code"}, meta.GrantTypesSupported)

	// PKCE is mandatory: only S256 is advertised (RFC 7636).
	require.Equal(t, []string{"S256"}, meta.CodeChallengeMethodsSupported)

	// Public clients only (RFC 7591 §2): no client_secret issued.
	require.Equal(t, []string{"none"}, meta.TokenEndpointAuthMethodsSupported)

	// All three application scopes must be advertised.
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

	// RFC 9728 §3: resource MUST identify the protected resource (the MCP endpoint).
	require.NotEmpty(t, meta.Resource)
	require.True(t, strings.HasSuffix(meta.Resource, "/mcp"), "resource %q must end in /mcp", meta.Resource)

	// authorization_servers must contain the issuer URL, NOT the discovery URL.
	// MCP clients construct the discovery URL themselves from the issuer (auth.md §Discovery documents).
	require.Len(t, meta.AuthorizationServers, 1)
	issuer := meta.AuthorizationServers[0]
	require.NotContains(t, issuer, ".well-known", "authorization_servers must be the issuer, not the discovery URL")

	// The issuer must also be the base of the resource URL.
	require.True(t, strings.HasPrefix(meta.Resource, issuer), "resource %q must be under issuer %q", meta.Resource, issuer)

	// Only Bearer header transport is supported (no query-param or form-body).
	require.Equal(t, []string{"header"}, meta.BearerMethodsSupported)

	// All three application scopes must be advertised.
	require.ElementsMatch(t, []string{"facts:read", "facts:write", "org:admin"}, meta.ScopesSupported)
}

// --- Spec compliance: RFC 7517 JWKS kid cross-document consistency ---

func TestJWKS_KidMatchesJWTHeader(t *testing.T) {
	srv := newTestOIDCServer(t)

	// Fetch the JWKS and extract the single key's kid.
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

	// Issue a JWT and decode its header to read the kid claim.
	tokenString, err := srv.IssueJWT("12345678", "user@example.com", "facts:read", uuid.New().String())
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
	require.Empty(t, resp["client_secret"]) // public clients only — no secret issued
	require.NotZero(t, resp["client_id_issued_at"])

	// Unsupported grant types normalised to authorization_code only (RFC 7591 §3.2.1)
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

	// Minimal request — grant_types, response_types, and auth method should be defaulted
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
		// no scope — server should default to facts:read
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
		"response_type": {"token"},
		"redirect_uri":  {"https://client.example.com/callback"},
		"code_challenge": {pkceChallenge("dBjftJeZ4CVP-mB92K27uhbUJU1p1r_wW1gFWFOEjXk")},
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
	srv.storeCode(code, &pendingCode{
		sub:           "12345678",
		email:         "user@example.com",
		scope:         "facts:read facts:write",
		codeChallenge: pkceChallenge(verifier),
		redirectURI:   "https://client.example.com/callback",
		createdAt:     time.Now(),
	})

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

	// Issued JWT must be verifiable
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
	srv.storeCode(code, &pendingCode{
		sub:           "12345678",
		email:         "user@example.com",
		scope:         "facts:read",
		codeChallenge: pkceChallenge(verifier),
		redirectURI:   "https://client.example.com/callback",
		createdAt:     time.Now(),
	})

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
	srv.storeCode(code, &pendingCode{
		sub:           "12345678",
		email:         "user@example.com",
		scope:         "facts:read",
		codeChallenge: pkceChallenge("correct-verifier-that-is-long-enough-to-be-valid"),
		redirectURI:   "https://client.example.com/callback",
		createdAt:     time.Now(),
	})

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
	srv.storeCode(code, &pendingCode{
		sub:           "12345678",
		email:         "user@example.com",
		scope:         "facts:read",
		codeChallenge: pkceChallenge(verifier),
		redirectURI:   "https://client.example.com/callback",
		createdAt:     time.Now(),
	})

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
	srv.storeCode(code, &pendingCode{
		sub:           "12345678",
		email:         "user@example.com",
		scope:         "facts:read",
		codeChallenge: pkceChallenge(verifier),
		redirectURI:   "https://client.example.com/callback",
		createdAt:     time.Now(),
	})

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
	srv, err := NewServer(Config{BaseURL: "http://example.com", Grants: gs}, raw)
	require.NoError(t, err)

	verifier := "dBjftJeZ4CVP-mB92K27uhbUJU1p1r_wW1gFWFOEjXk"
	code := "grant-seam-code"
	accessExpiry := time.Now().Add(8 * time.Hour).Truncate(time.Second)
	refreshExpiry := time.Now().Add(180 * 24 * time.Hour).Truncate(time.Second)
	srv.storeCode(code, &pendingCode{
		sub:                "99887766",
		email:              "user@example.com",
		scope:              "facts:read",
		codeChallenge:      pkceChallenge(verifier),
		redirectURI:        "https://client.example.com/callback",
		createdAt:          time.Now(),
		accessToken:        "gha_access_abc",
		refreshToken:       "ghr_refresh_xyz",
		accessTokenExpiry:  accessExpiry,
		refreshTokenExpiry: refreshExpiry,
	})

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

	srv.storePending("valid-state", &pendingAuth{
		redirectURI:   "https://client.example.com/callback",
		scope:         "facts:read",
		codeChallenge: pkceChallenge("verifier"),
		createdAt:     time.Now(),
	})

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

	srv.storePending("cb-state", &pendingAuth{
		redirectURI:   "https://client.example.com/callback",
		scope:         "facts:read",
		codeChallenge: pkceChallenge("verifier"),
		createdAt:     time.Now(),
	})

	req := httptest.NewRequest(http.MethodGet, "/auth/github/callback?state=cb-state&code=code", nil)
	w := httptest.NewRecorder()
	srv.GitHubCallbackHandler().ServeHTTP(w, req)

	require.Equal(t, http.StatusFound, w.Code)
	require.Len(t, us.calls, 1)
	require.Equal(t, int64(42), us.calls[0].githubID)
	require.Equal(t, "dev@example.com", us.calls[0].email)
	require.Equal(t, "devuser", us.calls[0].login)

	// Authorization code must be set in the redirect location.
	loc := w.Header().Get("Location")
	require.Contains(t, loc, "code=")
}

// --- Logout ---

func TestLogoutHandler_RevokesToken(t *testing.T) {
	srv := newTestOIDCServer(t)

	tokenString, err := srv.IssueJWT("12345678", "user@example.com", "facts:read", uuid.New().String())
	require.NoError(t, err)

	// Token must be valid before logout
	_, err = srv.VerifyJWT(context.Background(), tokenString, nil)
	require.NoError(t, err)

	req := httptest.NewRequest(http.MethodPost, "/auth/logout", nil)
	req.Header.Set("Authorization", "Bearer "+tokenString)
	w := httptest.NewRecorder()
	srv.LogoutHandler().ServeHTTP(w, req)
	require.Equal(t, http.StatusNoContent, w.Code)

	// Token must be rejected after logout
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

	tokenString, err := srv.IssueJWT("12345678", "user@example.com", "facts:read facts:write", uuid.New().String())
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

	tokenString, err := b.IssueJWT("12345678", "user@example.com", "facts:read", uuid.New().String())
	require.NoError(t, err)

	_, err = a.VerifyJWT(context.Background(), tokenString, nil)
	require.Error(t, err)
}

func TestVerifyJWT_RevokedToken(t *testing.T) {
	srv := newTestOIDCServer(t)

	tokenString, err := srv.IssueJWT("12345678", "user@example.com", "facts:read", uuid.New().String())
	require.NoError(t, err)

	// Manually revoke by extracting jti via logout handler
	req := httptest.NewRequest(http.MethodPost, "/auth/logout", nil)
	req.Header.Set("Authorization", "Bearer "+tokenString)
	srv.LogoutHandler().ServeHTTP(httptest.NewRecorder(), req)

	_, err = srv.VerifyJWT(context.Background(), tokenString, nil)
	require.Error(t, err)
}

// --- IssueJWT aud claim ---

func TestIssueJWT_ContainsAudClaim(t *testing.T) {
	srv := newTestOIDCServer(t)

	tokenString, err := srv.IssueJWT("12345678", "user@example.com", "facts:read", uuid.New().String())
	require.NoError(t, err)

	tok, err := jwt.ParseString(tokenString, jwt.WithKey(jwa.ES384(), srv.pubkey))
	require.NoError(t, err)

	aud, ok := tok.Audience()
	require.True(t, ok, "aud claim must be present")
	require.Contains(t, aud, "http://example.com/mcp")
}

// --- VerifyJWT aud / iss validation ---

// signCustomToken builds and signs a JWT with the server's private key, using the given claims.
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

	// Build a token that intentionally omits the aud claim by overriding it with empty.
	// Because jwt.Builder stores claims by key, setting aud to nil removes it.
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
	calls []store.Grant
}

func (s *testGrantStore) UpsertGrant(_ context.Context, g store.Grant) error {
	s.calls = append(s.calls, g)
	return nil
}

type upsertCall struct {
	githubID int64
	email    string
	login    string
}

type testUserUpserter struct {
	calls []upsertCall
}

func (u *testUserUpserter) UpsertUser(_ context.Context, githubID int64, email, login string) error {
	u.calls = append(u.calls, upsertCall{githubID, email, login})
	return nil
}

type mockGitHubConnector struct {
	identity *githubIdentity
	token    *oauth2.Token
	err      error
}

func (m *mockGitHubConnector) AuthCodeURL(state string) string {
	return "https://github.com/login/oauth/authorize?state=" + state
}

func (m *mockGitHubConnector) ExchangeCode(_ context.Context, _ string) (*oauth2.Token, *githubIdentity, error) {
	return m.token, m.identity, m.err
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
