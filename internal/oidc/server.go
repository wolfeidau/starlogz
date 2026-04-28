package oidc

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/lestrrat-go/jwx/v3/jwa"
	"github.com/lestrrat-go/jwx/v3/jwk"
	"github.com/lestrrat-go/jwx/v3/jwt"
	"github.com/modelcontextprotocol/go-sdk/auth"
	"github.com/modelcontextprotocol/go-sdk/oauthex"
	"golang.org/x/oauth2"
)

// UserUpserter persists user identity on successful GitHub login.
// Implemented by store.Store via *postgres.Store; nil is accepted (skips persistence).
type UserUpserter interface {
	UpsertUser(ctx context.Context, githubID int64, email, login string) error
}

// GrantParams holds the data needed to persist an authorization grant.
type GrantParams struct {
	JTI                string
	GitHubID           int64
	AccessToken        string
	RefreshToken       string
	AccessTokenExpiry  time.Time
	RefreshTokenExpiry time.Time
	JWTExpiry          time.Time
}

// GrantStore persists authorization grants with associated GitHub App tokens.
// Implemented by store.Store via grantStoreAdapter in internal/server; nil skips persistence.
type GrantStore interface {
	UpsertGrant(ctx context.Context, p GrantParams) error
}

// Config holds construction parameters for Server.
type Config struct {
	BaseURL            string
	GitHubClientID     string
	GitHubClientSecret string
	Users              UserUpserter // optional
	Clients            ClientStore  // optional; if set, DCR registrations are persisted
	Grants             GrantStore   // optional; if set, GitHub App tokens are persisted per grant
}

// Server is the OAuth2/OIDC authorization server for the MCP endpoint.
type Server struct {
	baseURL     *url.URL
	privkey     jwk.Key
	pubkey      jwk.Key
	jwksJSON    []byte
	authMeta    *oauthex.AuthServerMeta
	resMeta     *oauthex.ProtectedResourceMetadata
	githubOAuth *oauth2.Config
	users       UserUpserter
	clients     ClientStore
	grants      GrantStore

	revokedMu sync.RWMutex
	revoked   map[string]time.Time // jti → expiry

	pendingMu sync.Mutex
	pending   map[string]*pendingAuth // github state → pending authorization

	codesMu sync.Mutex
	codes   map[string]*pendingCode // auth code → pending token exchange
}

// NewServer constructs an OIDC Server from config and a loaded private key.
// It derives and assigns a key ID to both keys, pre-marshals the JWKS document,
// and builds the OAuth2 metadata documents.
func NewServer(cfg Config, privkey jwk.Key) (*Server, error) {
	base, err := url.Parse(cfg.BaseURL)
	if err != nil {
		return nil, fmt.Errorf("failed to parse base URL: %w", err)
	}

	pubkey, err := jwk.PublicKeyOf(privkey)
	if err != nil {
		return nil, fmt.Errorf("failed to derive public key: %w", err)
	}

	if err := jwk.AssignKeyID(pubkey); err != nil {
		return nil, fmt.Errorf("failed to assign key ID: %w", err)
	}

	if err := pubkey.Set(jwk.KeyUsageKey, "sig"); err != nil {
		return nil, fmt.Errorf("failed to set key usage: %w", err)
	}
	if err := pubkey.Set(jwk.AlgorithmKey, jwa.ES384()); err != nil {
		return nil, fmt.Errorf("failed to set key algorithm: %w", err)
	}

	kid, ok := pubkey.KeyID()
	if !ok {
		return nil, fmt.Errorf("key ID not set after assignment")
	}

	if err = privkey.Set(jwk.KeyIDKey, kid); err != nil {
		return nil, fmt.Errorf("failed to propagate key ID to private key: %w", err)
	}

	set := jwk.NewSet()
	if err = set.AddKey(pubkey); err != nil {
		return nil, fmt.Errorf("failed to build JWKS: %w", err)
	}

	jwksJSON, err := json.Marshal(set)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal JWKS: %w", err)
	}

	return &Server{
		baseURL:     base,
		privkey:     privkey,
		pubkey:      pubkey,
		jwksJSON:    jwksJSON,
		authMeta:    buildAuthServerMeta(base),
		resMeta:     buildProtectedResourceMeta(base),
		githubOAuth: newGitHubOAuthConfig(cfg.GitHubClientID, cfg.GitHubClientSecret, base.JoinPath("/auth/github/callback").String()),
		users:       cfg.Users,
		clients:     cfg.Clients,
		grants:      cfg.Grants,
		revoked:     make(map[string]time.Time),
		pending:     make(map[string]*pendingAuth),
		codes:       make(map[string]*pendingCode),
	}, nil
}

// AuthServerMeta returns the pre-built OAuth2 authorization server metadata.
func (s *Server) AuthServerMeta() *oauthex.AuthServerMeta {
	return s.authMeta
}

// ProtectedResourceMeta returns the pre-built OAuth2 protected resource metadata.
func (s *Server) ProtectedResourceMeta() *oauthex.ProtectedResourceMetadata {
	return s.resMeta
}

// ResourceMetadataURL returns the URL of the protected resource metadata endpoint,
// for use in WWW-Authenticate headers on 401 responses.
func (s *Server) ResourceMetadataURL() string {
	return s.baseURL.JoinPath("/.well-known/oauth-protected-resource").String()
}

// RevokeToken adds a jti to the blocklist until its expiry. Safe for concurrent use.
// On each call, expired entries are pruned to bound memory growth.
func (s *Server) RevokeToken(jti string, exp time.Time) {
	s.revokedMu.Lock()
	defer s.revokedMu.Unlock()
	s.revoked[jti] = exp
	now := time.Now()
	for id, expiry := range s.revoked {
		if expiry.Before(now) {
			delete(s.revoked, id)
		}
	}
}

// VerifyJWT validates a bearer token and returns auth info.
// Compatible with auth.RequireBearerToken's verify function signature.
func (s *Server) VerifyJWT(ctx context.Context, tokenString string, _ *http.Request) (*auth.TokenInfo, error) {
	verifiedToken, err := jwt.ParseString(tokenString, jwt.WithKey(jwa.ES384(), s.pubkey))
	if err != nil {
		return nil, fmt.Errorf("%w: %v", auth.ErrInvalidToken, err)
	}

	iss, ok := verifiedToken.Issuer()
	if !ok || iss != s.baseURL.String() {
		return nil, fmt.Errorf("%w: invalid issuer", auth.ErrInvalidToken)
	}

	aud, ok := verifiedToken.Audience()
	if !ok {
		return nil, fmt.Errorf("%w: missing aud claim", auth.ErrInvalidToken)
	}
	resourceURL := s.baseURL.JoinPath("/mcp").String()
	audValid := false
	for _, a := range aud {
		if a == resourceURL {
			audValid = true
			break
		}
	}
	if !audValid {
		return nil, fmt.Errorf("%w: token audience does not include resource", auth.ErrInvalidToken)
	}

	var jti string
	if err := verifiedToken.Get("jti", &jti); err != nil || jti == "" {
		return nil, fmt.Errorf("%w: missing jti claim", auth.ErrInvalidToken)
	}

	s.revokedMu.RLock()
	_, revoked := s.revoked[jti]
	s.revokedMu.RUnlock()
	if revoked {
		return nil, fmt.Errorf("%w: token has been revoked", auth.ErrInvalidToken)
	}

	var scope string
	if err := verifiedToken.Get("scope", &scope); err != nil {
		return nil, fmt.Errorf("%w: invalid token claims", auth.ErrInvalidToken)
	}

	if scope == "" {
		return nil, fmt.Errorf("%w: missing scope claim", auth.ErrInvalidToken)
	}

	expiresAt, ok := verifiedToken.Expiration()
	if !ok {
		return nil, fmt.Errorf("%w: missing expiration claim", auth.ErrInvalidToken)
	}

	sub, ok := verifiedToken.Subject()
	if !ok || sub == "" {
		return nil, fmt.Errorf("%w: missing sub claim", auth.ErrInvalidToken)
	}

	return &auth.TokenInfo{
		UserID:     sub,
		Scopes:     strings.Fields(scope),
		Expiration: expiresAt,
	}, nil
}

// IssueJWT signs and returns a new ES384 JWT for the given subject, email, scope, and JWT ID.
func (s *Server) IssueJWT(sub, email, scope, jti string) (string, error) {
	now := time.Now()
	tok, err := jwt.NewBuilder().
		Issuer(s.baseURL.String()).
		Subject(sub).
		IssuedAt(now).
		Expiration(now.Add(7*24*time.Hour)).
		Audience([]string{s.baseURL.JoinPath("/mcp").String()}).
		Claim("email", email).
		Claim("scope", scope).
		Claim("jti", jti).
		Build()
	if err != nil {
		return "", fmt.Errorf("build token: %w", err)
	}

	signed, err := jwt.Sign(tok, jwt.WithKey(jwa.ES384(), s.privkey))
	if err != nil {
		return "", fmt.Errorf("sign token: %w", err)
	}

	return string(signed), nil
}
