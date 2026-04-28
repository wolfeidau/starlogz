package oidc

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/lestrrat-go/jwx/v3/jwa"
	"github.com/lestrrat-go/jwx/v3/jwk"
	"github.com/lestrrat-go/jwx/v3/jwt"
	"github.com/modelcontextprotocol/go-sdk/auth"
	"github.com/modelcontextprotocol/go-sdk/oauthex"
	"golang.org/x/oauth2"
)

// UserUpserter persists user identity on successful GitHub login.
// Implemented by *store.Store; nil is accepted (skips persistence).
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
// Implemented by *store.Store via grantStoreAdapter in internal/server; nil skips persistence.
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

func (s *Server) TokenHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}

		if err := r.ParseForm(); err != nil {
			writeOAuthError(w, "invalid_request", "failed to parse request body", http.StatusBadRequest)
			return
		}

		if r.FormValue("grant_type") != "authorization_code" {
			writeOAuthError(w, "unsupported_grant_type", "only grant_type=authorization_code is supported", http.StatusBadRequest)
			return
		}

		code := r.FormValue("code")
		codeVerifier := r.FormValue("code_verifier")
		if code == "" || codeVerifier == "" {
			writeOAuthError(w, "invalid_request", "code and code_verifier are required", http.StatusBadRequest)
			return
		}

		pc, ok := s.consumeCode(code)
		if !ok {
			writeOAuthError(w, "invalid_grant", "invalid or expired authorization code", http.StatusBadRequest)
			return
		}

		// Verify PKCE: BASE64URL(SHA256(code_verifier)) must equal stored code_challenge.
		h := sha256.Sum256([]byte(codeVerifier))
		if base64.RawURLEncoding.EncodeToString(h[:]) != pc.codeChallenge {
			writeOAuthError(w, "invalid_grant", "code_verifier does not match code_challenge", http.StatusBadRequest)
			return
		}

		jti := uuid.New().String()
		jwtExpiry := time.Now().Add(7 * 24 * time.Hour)

		tokenString, err := s.IssueJWT(pc.sub, pc.email, pc.scope, jti)
		if err != nil {
			slog.Default().ErrorContext(r.Context(), "JWT issuance failed", slog.Any("error", err))
			writeOAuthError(w, "server_error", "failed to issue token", http.StatusInternalServerError)
			return
		}

		if s.grants != nil && pc.accessToken != "" {
			githubID, _ := strconv.ParseInt(pc.sub, 10, 64)
			if err := s.grants.UpsertGrant(r.Context(), GrantParams{
				JTI:                jti,
				GitHubID:           githubID,
				AccessToken:        pc.accessToken,
				RefreshToken:       pc.refreshToken,
				AccessTokenExpiry:  pc.accessTokenExpiry,
				RefreshTokenExpiry: pc.refreshTokenExpiry,
				JWTExpiry:          jwtExpiry,
			}); err != nil {
				// Log but don't fail the token exchange — the client still gets a valid JWT.
				slog.Default().ErrorContext(r.Context(), "upsert grant failed", slog.Any("error", err))
			}
		}

		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		if err := json.NewEncoder(w).Encode(map[string]any{
			"access_token": tokenString,
			"token_type":   "Bearer",
			"expires_in":   int(7 * 24 * time.Hour / time.Second),
			"scope":        pc.scope,
		}); err != nil {
			slog.Default().ErrorContext(r.Context(), "failed to write token response", slog.Any("error", err))
		}
	})
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

// DCRHandler returns an HTTP handler for Dynamic Client Registration (RFC 7591).
func (s *Server) DCRHandler() http.Handler {
	return s.dcrHandler(s.clients)
}

func (s *Server) dcrHandler(store ClientStore) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}

		var req oauthex.ClientRegistrationMetadata
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeDCRError(w, "invalid_client_metadata", "failed to parse request body", http.StatusBadRequest)
			return
		}

		slog.Default().InfoContext(r.Context(), "DCR request",
			slog.Any("grant_types", req.GrantTypes),
			slog.Any("response_types", req.ResponseTypes),
			slog.Any("redirect_uris", req.RedirectURIs),
			slog.String("token_endpoint_auth_method", req.TokenEndpointAuthMethod),
			slog.String("client_name", req.ClientName),
		)

		if len(req.RedirectURIs) == 0 {
			writeDCRError(w, "invalid_client_metadata", "redirect_uris is required", http.StatusBadRequest)
			return
		}

		if err := validateRedirectURIs(req.RedirectURIs); err != nil {
			writeDCRError(w, "invalid_client_metadata", err.Error(), http.StatusBadRequest)
			return
		}

		if req.TokenEndpointAuthMethod != "" && req.TokenEndpointAuthMethod != "none" {
			writeDCRError(w, "invalid_client_metadata", "only token_endpoint_auth_method=none is supported", http.StatusBadRequest)
			return
		}

		// Normalise to the supported subset — always authorization_code only.
		// RFC 7591 §3.2.1: server registers the supported subset rather than rejecting.
		req.GrantTypes = []string{"authorization_code"}
		if len(req.ResponseTypes) == 0 {
			req.ResponseTypes = []string{"code"}
		}
		if req.TokenEndpointAuthMethod == "" {
			req.TokenEndpointAuthMethod = "none"
		}

		now := time.Now()
		clientID := uuid.New().String()

		if store != nil {
			rec := ClientRecord{
				ClientID:                clientID,
				ClientName:              req.ClientName,
				TokenEndpointAuthMethod: req.TokenEndpointAuthMethod,
				Scope:                   req.Scope,
				RedirectURIs:            req.RedirectURIs,
				GrantTypes:              req.GrantTypes,
				ResponseTypes:           req.ResponseTypes,
				IssuedAt:                now,
				ExpiresAt:               now.Add(clientRegistrationTTL),
			}
			if err := store.SaveClient(r.Context(), rec); err != nil {
				slog.Default().ErrorContext(r.Context(), "failed to persist DCR client", slog.Any("error", err))
				writeDCRError(w, "server_error", "failed to save client registration", http.StatusInternalServerError)
				return
			}
		}

		resp := &oauthex.ClientRegistrationResponse{
			ClientRegistrationMetadata: req,
			ClientID:                   clientID,
			ClientIDIssuedAt:           now,
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		if err := json.NewEncoder(w).Encode(resp); err != nil {
			slog.Default().Error("failed to write DCR response", slog.Any("error", err))
		}
	})
}

func (s *Server) GitHubCallbackHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}

		q := r.URL.Query()

		pending, ok := s.consumePending(q.Get("state"))
		if !ok {
			http.Error(w, "invalid or expired state", http.StatusBadRequest)
			return
		}

		githubToken, err := s.githubOAuth.Exchange(r.Context(), q.Get("code"))
		if err != nil {
			slog.Default().ErrorContext(r.Context(), "GitHub code exchange failed", slog.Any("error", err))
			http.Error(w, "failed to exchange code with GitHub", http.StatusBadGateway)
			return
		}

		httpClient := s.githubOAuth.Client(r.Context(), githubToken)
		identity, err := fetchGitHubIdentity(r.Context(), httpClient)
		if err != nil {
			slog.Default().ErrorContext(r.Context(), "GitHub identity fetch failed", slog.Any("error", err))
			http.Error(w, "failed to fetch GitHub identity", http.StatusBadGateway)
			return
		}

		if s.users != nil {
			if err := s.users.UpsertUser(r.Context(), identity.ID, identity.Email, identity.Login); err != nil {
				// Log but don't fail the login — user still gets a token even if the DB is momentarily down.
				slog.Default().ErrorContext(r.Context(), "upsert user failed", slog.Any("error", err))
			}
		}

		code := uuid.New().String()
		s.storeCode(code, &pendingCode{
			sub:                strconv.FormatInt(identity.ID, 10),
			email:              identity.Email,
			scope:              pending.scope,
			codeChallenge:      pending.codeChallenge,
			redirectURI:        pending.redirectURI,
			clientID:           pending.clientID,
			createdAt:          time.Now(),
			accessToken:        githubToken.AccessToken,
			refreshToken:       githubToken.RefreshToken,
			accessTokenExpiry:  githubToken.Expiry,
			refreshTokenExpiry: extractRefreshExpiry(githubToken),
		})

		redirectTo, err := url.Parse(pending.redirectURI)
		if err != nil {
			slog.Default().ErrorContext(r.Context(), "invalid redirect URI in pending auth", slog.Any("error", err))
			http.Error(w, "invalid redirect_uri", http.StatusInternalServerError)
			return
		}

		rq := redirectTo.Query()
		rq.Set("code", code)
		if pending.clientState != "" {
			rq.Set("state", pending.clientState)
		}
		redirectTo.RawQuery = rq.Encode()

		slog.Default().InfoContext(r.Context(), "GitHub auth complete",
			slog.String("login", identity.Login),
			slog.String("email", identity.Email),
			slog.String("sub", fmt.Sprintf("%d", identity.ID)),
		)

		http.Redirect(w, r, redirectTo.String(), http.StatusFound)
	})
}

func (s *Server) AuthorizeHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}

		q := r.URL.Query()

		if q.Get("response_type") != "code" {
			writeOAuthError(w, "unsupported_response_type", "only response_type=code is supported", http.StatusBadRequest)
			return
		}

		redirectURI := q.Get("redirect_uri")
		if redirectURI == "" {
			writeOAuthError(w, "invalid_request", "redirect_uri is required", http.StatusBadRequest)
			return
		}

		codeChallenge := q.Get("code_challenge")
		if codeChallenge == "" {
			writeOAuthError(w, "invalid_request", "code_challenge is required (PKCE mandatory)", http.StatusBadRequest)
			return
		}

		if q.Get("code_challenge_method") != "S256" {
			writeOAuthError(w, "invalid_request", "only code_challenge_method=S256 is supported", http.StatusBadRequest)
			return
		}

		scope := q.Get("scope")
		if scope == "" {
			scope = "facts:read"
		}
		for _, sc := range strings.Fields(scope) {
			if !supportedScopes[sc] {
				writeOAuthError(w, "invalid_scope", "unknown scope: "+sc, http.StatusBadRequest)
				return
			}
		}

		githubState := uuid.New().String()
		s.storePending(githubState, &pendingAuth{
			clientID:      q.Get("client_id"),
			redirectURI:   redirectURI,
			scope:         scope,
			codeChallenge: codeChallenge,
			clientState:   q.Get("state"),
			createdAt:     time.Now(),
		})

		authURL := s.githubOAuth.AuthCodeURL(githubState, oauth2.AccessTypeOnline)
		http.Redirect(w, r, authURL, http.StatusFound)
	})
}
