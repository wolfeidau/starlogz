package oidc

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
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
	"golang.org/x/oauth2"
)

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

		// RFC 6749 §4.1.3: redirect_uri must be present and identical to the one in the authorization request.
		if r.FormValue("redirect_uri") != pc.redirectURI {
			writeOAuthError(w, "invalid_grant", "redirect_uri does not match authorization request", http.StatusBadRequest)
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
