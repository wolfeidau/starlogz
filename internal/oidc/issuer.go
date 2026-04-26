package oidc

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
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

// Config holds construction parameters for Server.
type Config struct {
	BaseURL            string
	GitHubClientID     string
	GitHubClientSecret string
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

// LogoutHandler handles POST /auth/logout. It verifies the bearer token,
// extracts the jti and exp claims, and adds the token to the revocation blocklist.
func (s *Server) LogoutHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}

		const prefix = "Bearer "
		authHeader := r.Header.Get("Authorization")
		if !strings.HasPrefix(authHeader, prefix) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"error":"unauthorized","error_description":"missing bearer token"}`))
			return
		}
		tokenString := authHeader[len(prefix):]

		tok, err := jwt.ParseString(tokenString, jwt.WithKey(jwa.ES384(), s.pubkey))
		if err != nil {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"error":"invalid_token","error_description":"token verification failed"}`))
			return
		}

		var jti string
		if err := tok.Get("jti", &jti); err != nil || jti == "" {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"error":"invalid_token","error_description":"missing jti claim"}`))
			return
		}

		exp, ok := tok.Expiration()
		if !ok {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"error":"invalid_token","error_description":"missing expiration claim"}`))
			return
		}

		s.RevokeToken(jti, exp)
		w.WriteHeader(http.StatusNoContent)
	})
}

// JWKSHandler serves the public key set for token verification by clients.
func (s *Server) JWKSHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "public, max-age=86400")
		w.Header().Set("Access-Control-Allow-Origin", "*")
		if _, err := w.Write(s.jwksJSON); err != nil {
			slog.Default().Error("failed to write JWKS response", slog.Any("error", err))
		}
	})
}

// DiscoveryHandler serves the OAuth2 authorization server metadata document.
// Register at both /.well-known/oauth-authorization-server (RFC 8414) and
// /.well-known/openid-configuration (OIDC fallback) — the go-sdk client tries
// the RFC 8414 path first when the issuer URL has no path component.
func (s *Server) DiscoveryHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type")

		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}

		if r.Method != http.MethodGet {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "public, max-age=86400")
		if err := json.NewEncoder(w).Encode(s.authMeta); err != nil {
			slog.Default().Error("failed to write discovery response", slog.Any("error", err))
		}
	})
}

