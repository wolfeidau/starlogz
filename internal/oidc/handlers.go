package oidc

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/lestrrat-go/jwx/v3/jwa"
	"github.com/lestrrat-go/jwx/v3/jwt"
	"github.com/modelcontextprotocol/go-sdk/oauthex"
	storepkg "github.com/wolfeidau/starlogz/internal/store"
)

const jwtTTL = 15 * time.Minute

// generateOpaqueToken returns a base64url-encoded 32-byte random value used as our_refresh_token.
func generateOpaqueToken() (string, error) {
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b[:]), nil
}

// writeTokenResponse writes an RFC 6749 token endpoint response with no-store caching.
// refreshToken/refreshExpiry are added only when refreshToken is non-empty.
func writeTokenResponse(ctx context.Context, w http.ResponseWriter, jwt, scope, refreshToken string, refreshExpiry time.Time) {
	resp := map[string]any{
		"access_token": jwt,
		"token_type":   "Bearer",
		"expires_in":   int(jwtTTL / time.Second),
		"scope":        scope,
	}
	if refreshToken != "" {
		resp["refresh_token"] = refreshToken
		resp["refresh_token_expires_in"] = int(time.Until(refreshExpiry) / time.Second)
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	if err := json.NewEncoder(w).Encode(resp); err != nil {
		slog.Default().ErrorContext(ctx, "failed to write token response", slog.Any("error", err))
	}
}

// tearDownGrant revokes the JWT and deletes the grant row. Used when a grant is
// known-bad (GitHub refresh expired, GitHub returned no refresh token) and the
// client must re-authenticate. Failures are logged but never block the response.
func (s *Server) tearDownGrant(ctx context.Context, jti string, jwtExpiry time.Time) {
	if err := s.revocation.RevokeToken(ctx, jti, jwtExpiry); err != nil {
		s.logger.ErrorContext(ctx, "revoke jti for broken grant failed", slog.Any("error", err))
	}
	if err := s.grants.DeleteGrant(ctx, jti); err != nil && !errors.Is(err, storepkg.ErrNotFound) {
		s.logger.ErrorContext(ctx, "delete broken grant failed", slog.Any("error", err))
	}
}

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
			writeOAuthError(w, "invalid_request", "missing bearer token", http.StatusUnauthorized)
			return
		}
		tokenString := authHeader[len(prefix):]

		tok, err := jwt.ParseString(tokenString, jwt.WithKey(jwa.ES384(), s.pubkey))
		if err != nil {
			writeOAuthError(w, "invalid_token", "token verification failed", http.StatusUnauthorized)
			return
		}

		var jti string
		if err := tok.Get("jti", &jti); err != nil || jti == "" {
			writeOAuthError(w, "invalid_token", "missing jti claim", http.StatusUnauthorized)
			return
		}

		exp, ok := tok.Expiration()
		if !ok {
			writeOAuthError(w, "invalid_token", "missing expiration claim", http.StatusUnauthorized)
			return
		}

		if err := s.revocation.RevokeToken(r.Context(), jti, exp); err != nil {
			// Log but still 204 — the token will expire naturally.
			s.logger.ErrorContext(r.Context(), "revoke token failed", slog.Any("error", err))
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
		if _, err := w.Write(s.jwksJSON); err != nil {
			s.logger.Error("failed to write JWKS response", slog.Any("error", err))
		}
	})
}

// DiscoveryHandler serves the OAuth2 authorization server metadata document.
func (s *Server) DiscoveryHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
			s.logger.Error("failed to write discovery response", slog.Any("error", err))
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

		switch r.PostForm.Get("grant_type") {
		case "authorization_code":
			s.handleAuthCodeGrant(w, r, r.PostForm)
		case "refresh_token":
			s.handleRefreshGrant(w, r, r.PostForm)
		default:
			writeOAuthError(w, "unsupported_grant_type", "grant_type must be authorization_code or refresh_token", http.StatusBadRequest)
		}
	})
}

func (s *Server) handleAuthCodeGrant(w http.ResponseWriter, r *http.Request, form url.Values) {
	code := form.Get("code")
	codeVerifier := form.Get("code_verifier")
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
		s.logger.ErrorContext(r.Context(), "consume auth code failed", slog.Any("error", err))
		writeOAuthError(w, "server_error", "internal error", http.StatusInternalServerError)
		return
	}

	// RFC 6749 §4.1.3: redirect_uri must be present and identical.
	if form.Get("redirect_uri") != pc.RedirectURI {
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
	jwtExpiry := time.Now().Add(jwtTTL)

	tokenString, err := s.IssueJWT(pc.Sub, pc.Email, pc.Scope, jti, jwtExpiry)
	if err != nil {
		s.logger.ErrorContext(r.Context(), "JWT issuance failed", slog.Any("error", err))
		writeOAuthError(w, "server_error", "failed to issue token", http.StatusInternalServerError)
		return
	}

	// Issue an opaque refresh token only when we have a GitHub refresh token to back it.
	var ourRefreshToken string
	if s.grants != nil && pc.AccessToken != "" {
		if pc.RefreshToken != "" {
			ourRefreshToken, err = generateOpaqueToken()
			if err != nil {
				s.logger.ErrorContext(r.Context(), "generate refresh token failed", slog.Any("error", err))
				writeOAuthError(w, "server_error", "failed to issue refresh token", http.StatusInternalServerError)
				return
			}
		}
		if err := s.grants.UpsertGrant(r.Context(), storepkg.Grant{
			JTI:                jti,
			GitHubID:           pc.GitHubID,
			OurRefreshToken:    ourRefreshToken,
			ClientID:           pc.ClientID,
			Scope:              pc.Scope,
			AccessToken:        pc.AccessToken,
			RefreshToken:       pc.RefreshToken,
			AccessTokenExpiry:  pc.AccessTokenExpiry,
			RefreshTokenExpiry: pc.RefreshTokenExpiry,
			JWTExpiry:          jwtExpiry,
		}); err != nil {
			// Log but don't fail the token exchange — the client still gets a valid JWT.
			s.logger.ErrorContext(r.Context(), "upsert grant failed", slog.Any("error", err))
			ourRefreshToken = ""
		}
	}

	s.logger.InfoContext(r.Context(), "token exchange: issued JWT",
		slog.String("sub", pc.Sub),
		slog.String("scope", pc.Scope),
		slog.String("jti", jti),
		slog.Bool("refresh_token", ourRefreshToken != ""),
	)

	writeTokenResponse(r.Context(), w, tokenString, pc.Scope, ourRefreshToken, pc.RefreshTokenExpiry)
}

func (s *Server) handleRefreshGrant(w http.ResponseWriter, r *http.Request, form url.Values) {
	refreshToken := form.Get("refresh_token")
	clientID := form.Get("client_id")
	if refreshToken == "" || clientID == "" {
		writeOAuthError(w, "invalid_request", "refresh_token and client_id are required", http.StatusBadRequest)
		return
	}

	if s.grants == nil {
		writeOAuthError(w, "invalid_grant", "refresh token grant not supported", http.StatusBadRequest)
		return
	}

	grant, err := s.grants.GetGrantByRefreshToken(r.Context(), refreshToken)
	if errors.Is(err, storepkg.ErrNotFound) {
		writeOAuthError(w, "invalid_grant", "refresh token not found or already used", http.StatusBadRequest)
		return
	}
	if err != nil {
		s.logger.ErrorContext(r.Context(), "lookup grant by refresh token failed", slog.Any("error", err))
		writeOAuthError(w, "server_error", "internal error", http.StatusInternalServerError)
		return
	}

	// client_id is validated only when stored on the grant (best-effort per v0.2 constraints).
	if grant.ClientID != "" && grant.ClientID != clientID {
		writeOAuthError(w, "invalid_client", "client_id does not match grant", http.StatusBadRequest)
		return
	}

	if grant.RefreshToken == "" {
		writeOAuthError(w, "invalid_grant", "grant has no GitHub refresh token", http.StatusBadRequest)
		return
	}

	// If GitHub refresh token has already expired, drop the grant and force re-auth.
	if !grant.RefreshTokenExpiry.IsZero() && time.Now().After(grant.RefreshTokenExpiry) {
		s.tearDownGrant(r.Context(), grant.JTI, grant.JWTExpiry)
		writeOAuthError(w, "invalid_grant", "GitHub refresh token has expired", http.StatusBadRequest)
		return
	}

	// Detach from the HTTP request context before calling GitHub. GitHub rotates the
	// refresh token on the first valid call, so a client disconnection mid-flight would
	// leave the new token unrecorded and the old one permanently invalidated. We use a
	// non-cancelable child so the refresh + storage always completes.
	storeCtx := context.WithoutCancel(r.Context())

	newGHToken, identity, err := s.github.RefreshToken(storeCtx, grant.RefreshToken)
	if err != nil {
		s.logger.ErrorContext(r.Context(), "GitHub token refresh failed", slog.Any("error", err))
		writeOAuthError(w, "server_error", "GitHub token refresh failed", http.StatusInternalServerError)
		return
	}

	// GitHub returned a fresh access token but no new refresh token, so the chain
	// can't continue. GitHub has already invalidated our stored refresh token in
	// rotation; tear the grant down and force re-auth.
	if newGHToken.RefreshToken == "" {
		s.logger.ErrorContext(r.Context(), "GitHub refresh response missing refresh_token; dropping grant",
			slog.String("jti", grant.JTI))
		s.tearDownGrant(storeCtx, grant.JTI, grant.JWTExpiry)
		writeOAuthError(w, "invalid_grant", "GitHub did not return a new refresh token; re-authentication required", http.StatusBadRequest)
		return
	}

	sub := strconv.FormatInt(identity.ID, 10)
	if s.users != nil {
		user, uErr := s.users.UpsertUser(storeCtx, identity.ID, identity.Email, identity.Login)
		if uErr != nil {
			s.logger.ErrorContext(r.Context(), "upsert user failed", slog.Any("error", uErr))
			writeOAuthError(w, "server_error", "internal error", http.StatusInternalServerError)
			return
		}
		sub = user.ID.String()
	}

	newJTI := uuid.New().String()
	newOurRefreshToken, err := generateOpaqueToken()
	if err != nil {
		s.logger.ErrorContext(r.Context(), "generate refresh token failed", slog.Any("error", err))
		writeOAuthError(w, "server_error", "failed to issue refresh token", http.StatusInternalServerError)
		return
	}
	newJWTExpiry := time.Now().Add(jwtTTL)
	newGHRefreshExpiry := extractRefreshExpiry(newGHToken)

	newGrant := storepkg.Grant{
		JTI:                newJTI,
		GitHubID:           grant.GitHubID,
		OurRefreshToken:    newOurRefreshToken,
		ClientID:           grant.ClientID,
		Scope:              grant.Scope,
		AccessToken:        newGHToken.AccessToken,
		RefreshToken:       newGHToken.RefreshToken,
		AccessTokenExpiry:  newGHToken.Expiry,
		RefreshTokenExpiry: newGHRefreshExpiry,
		JWTExpiry:          newJWTExpiry,
	}
	if _, err := s.grants.RotateGrant(storeCtx, refreshToken, grant.JTI, grant.JWTExpiry, newGrant); err != nil {
		if errors.Is(err, storepkg.ErrNotFound) {
			// Concurrent refresh: another request already consumed this token.
			writeOAuthError(w, "invalid_grant", "refresh token already used", http.StatusBadRequest)
			return
		}
		s.logger.ErrorContext(r.Context(), "rotate grant failed", slog.Any("error", err))
		writeOAuthError(w, "server_error", "failed to rotate grant", http.StatusInternalServerError)
		return
	}

	s.logger.InfoContext(r.Context(), "token rotation: grant rotated",
		slog.Int64("github_id", grant.GitHubID),
		slog.String("old_jti", grant.JTI),
		slog.String("new_jti", newJTI),
	)

	tokenString, err := s.IssueJWT(sub, identity.Email, grant.Scope, newJTI, newJWTExpiry)
	if err != nil {
		s.logger.ErrorContext(r.Context(), "JWT issuance failed", slog.Any("error", err))
		writeOAuthError(w, "server_error", "failed to issue token", http.StatusInternalServerError)
		return
	}

	writeTokenResponse(r.Context(), w, tokenString, grant.Scope, newOurRefreshToken, newGHRefreshExpiry)
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

		s.logger.InfoContext(r.Context(), "DCR request",
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
				s.logger.ErrorContext(r.Context(), "failed to persist DCR client", slog.Any("error", err))
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
			s.logger.Error("failed to write DCR response", slog.Any("error", err))
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

		s.logger.InfoContext(r.Context(), "github callback: received")

		pending, err := s.authState.ConsumePendingAuth(r.Context(), q.Get("state"))
		if errors.Is(err, storepkg.ErrNotFound) {
			http.Error(w, "invalid or expired state", http.StatusBadRequest)
			return
		}
		if err != nil {
			s.logger.ErrorContext(r.Context(), "consume pending auth failed", slog.Any("error", err))
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}

		githubToken, identity, err := s.github.ExchangeCode(r.Context(), q.Get("code"))
		if err != nil {
			s.logger.ErrorContext(r.Context(), "GitHub exchange failed", slog.Any("error", err))
			http.Error(w, "failed to authenticate with GitHub", http.StatusBadGateway)
			return
		}

		// sub is the internal user UUID; fall back to GitHub numeric ID only when no user store is wired (tests).
		sub := strconv.FormatInt(identity.ID, 10)
		if s.users != nil {
			user, uErr := s.users.UpsertUser(r.Context(), identity.ID, identity.Email, identity.Login)
			if uErr != nil {
				s.logger.ErrorContext(r.Context(), "upsert user failed", slog.Any("error", uErr))
				http.Error(w, "internal error", http.StatusInternalServerError)
				return
			}
			sub = user.ID.String()
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
			s.logger.ErrorContext(r.Context(), "store auth code failed", slog.Any("error", err))
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}

		redirectTo, err := url.Parse(pending.RedirectURI)
		if err != nil {
			s.logger.ErrorContext(r.Context(), "invalid redirect URI in pending auth", slog.Any("error", err))
			http.Error(w, "invalid redirect_uri", http.StatusInternalServerError)
			return
		}

		rq := redirectTo.Query()
		rq.Set("code", code)
		if pending.ClientState != "" {
			rq.Set("state", pending.ClientState)
		}
		redirectTo.RawQuery = rq.Encode()

		s.logger.InfoContext(r.Context(), "GitHub auth complete",
			slog.String("login", identity.Login),
			slog.String("email", identity.Email),
			slog.String("sub", sub),
		)

		// redirect_uri was validated against the registered client in AuthorizeHandler before being stored.
		http.Redirect(w, r, redirectTo.String(), http.StatusFound) //nolint:gosec
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

		clientID := q.Get("client_id")
		redirectURI := q.Get("redirect_uri")
		if redirectURI == "" {
			writeOAuthError(w, "invalid_request", "redirect_uri is required", http.StatusBadRequest)
			return
		}

		if s.clients != nil {
			if clientID == "" {
				writeOAuthError(w, "invalid_request", "client_id is required", http.StatusBadRequest)
				return
			}
			client, err := s.clients.GetClient(r.Context(), clientID)
			if errors.Is(err, storepkg.ErrNotFound) {
				writeOAuthError(w, "invalid_client", "unknown client_id", http.StatusBadRequest)
				return
			}
			if err != nil {
				s.logger.ErrorContext(r.Context(), "get client failed", slog.Any("error", err))
				writeOAuthError(w, "server_error", "internal error", http.StatusInternalServerError)
				return
			}
			if !slices.Contains(client.RedirectURIs, redirectURI) {
				writeOAuthError(w, "invalid_request", "redirect_uri not registered for this client", http.StatusBadRequest)
				return
			}
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
			ClientID:      clientID,
			RedirectURI:   redirectURI,
			Scope:         scope,
			CodeChallenge: codeChallenge,
			ClientState:   q.Get("state"),
		}); err != nil {
			s.logger.ErrorContext(r.Context(), "store pending auth failed", slog.Any("error", err))
			writeOAuthError(w, "server_error", "failed to store authorization state", http.StatusInternalServerError)
			return
		}

		s.logger.InfoContext(r.Context(), "authorize: redirecting to GitHub",
			slog.String("client_id", clientID),
			slog.String("scope", scope),
		)

		authURL := s.github.AuthCodeURL(githubState)
		http.Redirect(w, r, authURL, http.StatusFound)
	})
}
