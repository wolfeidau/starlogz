package oidc

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/lestrrat-go/jwx/v3/jwa"
	"github.com/lestrrat-go/jwx/v3/jwt"
	"github.com/modelcontextprotocol/go-sdk/oauthex"
	storepkg "github.com/wolfeidau/starlogz/internal/store"
)

// LogoutHandler handles POST /auth/logout. It verifies the bearer token,
// extracts the jti and exp claims, and revokes the token.
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

		if err := s.revocation.RevokeToken(r.Context(), jti, exp); err != nil {
			// Log but still 204 — the token will expire naturally.
			slog.Default().ErrorContext(r.Context(), "revoke token failed", slog.Any("error", err))
		}
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

		r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
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

		pc, err := s.authState.ConsumeAuthCode(r.Context(), code)
		if errors.Is(err, storepkg.ErrNotFound) {
			writeOAuthError(w, "invalid_grant", "invalid or expired authorization code", http.StatusBadRequest)
			return
		}
		if err != nil {
			slog.Default().ErrorContext(r.Context(), "consume auth code failed", slog.Any("error", err))
			writeOAuthError(w, "server_error", "internal error", http.StatusInternalServerError)
			return
		}

		// RFC 6749 §4.1.3: redirect_uri must be present and identical.
		if r.FormValue("redirect_uri") != pc.RedirectURI {
			writeOAuthError(w, "invalid_grant", "redirect_uri does not match authorization request", http.StatusBadRequest)
			return
		}

		// Verify PKCE: BASE64URL(SHA256(code_verifier)) must equal stored code_challenge.
		h := sha256.Sum256([]byte(codeVerifier))
		if base64.RawURLEncoding.EncodeToString(h[:]) != pc.CodeChallenge {
			writeOAuthError(w, "invalid_grant", "code_verifier does not match code_challenge", http.StatusBadRequest)
			return
		}

		jti := uuid.New().String()
		jwtExpiry := time.Now().Add(7 * 24 * time.Hour)

		tokenString, err := s.IssueJWT(pc.Sub, pc.Email, pc.Scope, jti)
		if err != nil {
			slog.Default().ErrorContext(r.Context(), "JWT issuance failed", slog.Any("error", err))
			writeOAuthError(w, "server_error", "failed to issue token", http.StatusInternalServerError)
			return
		}

		if s.grants != nil && pc.AccessToken != "" {
			if err := s.grants.UpsertGrant(r.Context(), storepkg.Grant{
				JTI:                jti,
				GitHubID:           pc.GitHubID,
				ClientID:           pc.ClientID,
				AccessToken:        pc.AccessToken,
				RefreshToken:       pc.RefreshToken,
				AccessTokenExpiry:  pc.AccessTokenExpiry,
				RefreshTokenExpiry: pc.RefreshTokenExpiry,
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
			"scope":        pc.Scope,
		}); err != nil {
			slog.Default().ErrorContext(r.Context(), "failed to write token response", slog.Any("error", err))
		}
	})
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
			writeOAuthError(w, "invalid_client_metadata", "failed to parse request body", http.StatusBadRequest)
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
			writeOAuthError(w, "invalid_client_metadata", "redirect_uris is required", http.StatusBadRequest)
			return
		}

		if err := validateRedirectURIs(req.RedirectURIs); err != nil {
			writeOAuthError(w, "invalid_client_metadata", err.Error(), http.StatusBadRequest)
			return
		}

		if req.TokenEndpointAuthMethod != "" && req.TokenEndpointAuthMethod != "none" {
			writeOAuthError(w, "invalid_client_metadata", "only token_endpoint_auth_method=none is supported", http.StatusBadRequest)
			return
		}

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
			rec := storepkg.OAuthClient{
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
				writeOAuthError(w, "server_error", "failed to save client registration", http.StatusInternalServerError)
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
		if err := json.NewEncoder(w).Encode(resp); err != nil { //nolint:gosec // intentional: DCR response includes client_secret by spec
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

		pending, err := s.authState.ConsumePendingAuth(r.Context(), q.Get("state"))
		if errors.Is(err, storepkg.ErrNotFound) {
			http.Error(w, "invalid or expired state", http.StatusBadRequest)
			return
		}
		if err != nil {
			slog.Default().ErrorContext(r.Context(), "consume pending auth failed", slog.Any("error", err))
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}

		githubToken, identity, err := s.github.ExchangeCode(r.Context(), q.Get("code"))
		if err != nil {
			slog.Default().ErrorContext(r.Context(), "GitHub exchange failed", slog.Any("error", err))
			http.Error(w, "failed to authenticate with GitHub", http.StatusBadGateway)
			return
		}

		// sub is the JWT sub claim; use internal UUID when a user store is present.
		sub := strconv.FormatInt(identity.ID, 10)
		if s.users != nil {
			user, uErr := s.users.UpsertUser(r.Context(), identity.ID, identity.Email, identity.Login)
			if uErr != nil {
				// Log but don't fail the login — user still gets a token even if DB is momentarily down.
				slog.Default().ErrorContext(r.Context(), "upsert user failed", slog.Any("error", uErr))
			} else {
				sub = user.ID.String()
			}
		}

		code := uuid.New().String()
		if err := s.authState.StoreAuthCode(r.Context(), code, storepkg.AuthCode{
			Sub:                sub,
			GitHubID:           identity.ID,
			Email:              identity.Email,
			Scope:              pending.Scope,
			CodeChallenge:      pending.CodeChallenge,
			RedirectURI:        pending.RedirectURI,
			ClientID:           pending.ClientID,
			AccessToken:        githubToken.AccessToken,
			RefreshToken:       githubToken.RefreshToken,
			AccessTokenExpiry:  githubToken.Expiry,
			RefreshTokenExpiry: extractRefreshExpiry(githubToken),
		}); err != nil {
			slog.Default().ErrorContext(r.Context(), "store auth code failed", slog.Any("error", err))
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}

		redirectTo, err := url.Parse(pending.RedirectURI)
		if err != nil {
			slog.Default().ErrorContext(r.Context(), "invalid redirect URI in pending auth", slog.Any("error", err))
			http.Error(w, "invalid redirect_uri", http.StatusInternalServerError)
			return
		}

		rq := redirectTo.Query()
		rq.Set("code", code)
		if pending.ClientState != "" {
			rq.Set("state", pending.ClientState)
		}
		redirectTo.RawQuery = rq.Encode()

		slog.Default().InfoContext(r.Context(), "GitHub auth complete",
			slog.String("login", identity.Login),
			slog.String("email", identity.Email),
			slog.String("sub", fmt.Sprintf("%s", sub)),
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
		if err := s.authState.StorePendingAuth(r.Context(), githubState, storepkg.PendingAuth{
			ClientID:      q.Get("client_id"),
			RedirectURI:   redirectURI,
			Scope:         scope,
			CodeChallenge: codeChallenge,
			ClientState:   q.Get("state"),
		}); err != nil {
			slog.Default().ErrorContext(r.Context(), "store pending auth failed", slog.Any("error", err))
			writeOAuthError(w, "server_error", "failed to store authorization state", http.StatusInternalServerError)
			return
		}

		authURL := s.github.AuthCodeURL(githubState)
		http.Redirect(w, r, authURL, http.StatusFound)
	})
}
