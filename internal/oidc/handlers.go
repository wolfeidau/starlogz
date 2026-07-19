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
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/lestrrat-go/jwx/v3/jwa"
	"github.com/lestrrat-go/jwx/v3/jwt"
	"github.com/modelcontextprotocol/go-sdk/oauthex"
	"github.com/wolfeidau/starlogz/internal/ctxlog"
	storepkg "github.com/wolfeidau/starlogz/internal/store"
	"github.com/wolfeidau/starlogz/internal/telemetry"
	"github.com/wolfeidau/starlogz/internal/wideevent"
	"golang.org/x/oauth2"
)

const (
	jwtTTL = 15 * time.Minute

	DefaultRefreshTokenGracePeriod      = 30 * time.Second
	DefaultRetiredRefreshTokenRetention = 24 * time.Hour
	maxRefreshTokenGracePeriod          = 60 * time.Second
)

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
func writeTokenResponse(ctx context.Context, w http.ResponseWriter, jwt, scope, refreshToken string, refreshExpiry, jwtExpiry time.Time) {
	expiresIn := int(time.Until(jwtExpiry) / time.Second)
	if expiresIn < 0 {
		expiresIn = 0
	}
	resp := map[string]any{
		tokenResponseFieldAccess:    jwt,
		tokenResponseFieldTokenType: "Bearer",
		tokenResponseFieldExpiresIn: expiresIn,
		tokenResponseFieldScope:     scope,
	}
	if refreshToken != "" {
		resp[tokenResponseFieldRefresh] = refreshToken
		resp[tokenResponseFieldRefreshTTL] = int(time.Until(refreshExpiry) / time.Second)
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	if err := json.NewEncoder(w).Encode(resp); err != nil {
		ctxlog.LoggerFrom(ctx).ErrorContext(ctx, "failed to write token response", slog.Any("error", err))
	}
}

// tearDownGrant revokes the JWT and deletes the grant row. Used when a grant is
// known-bad (GitHub refresh expired, GitHub returned no refresh token) and the
// client must re-authenticate. Failures are logged but never block the response.
func (s *Server) tearDownGrant(ctx context.Context, grant *storepkg.Grant, refreshToken, reason string) {
	log := ctxlog.LoggerFrom(ctx)
	if err := s.revocation.RevokeToken(ctx, grant.JTI, grant.JWTExpiry); err != nil {
		log.ErrorContext(ctx, "revoke jti for broken grant failed", slog.Any("error", err))
	}
	retired := s.newRetiredRefreshToken(refreshToken, reason, grant, "", time.Time{})
	if err := s.grants.DeleteGrant(ctx, grant.JTI, retired); err != nil && !errors.Is(err, storepkg.ErrNotFound) {
		log.ErrorContext(ctx, "delete broken grant failed", slog.Any("error", err))
	}
}

func (s *Server) newRetiredRefreshToken(refreshToken, reason string, grant *storepkg.Grant, replacementJTI string, graceExpiresAt time.Time) *storepkg.RetiredRefreshToken {
	return &storepkg.RetiredRefreshToken{
		TokenHash:      storepkg.HashRefreshToken(refreshToken),
		Reason:         reason,
		UserID:         grant.UserID,
		ClientID:       grant.ClientID,
		OldJTI:         grant.JTI,
		ReplacementJTI: replacementJTI,
		GraceExpiresAt: graceExpiresAt,
		RetainedUntil:  time.Now().Add(s.retiredRefreshTokenRetention),
	}
}

// LogoutHandler handles POST /auth/logout. It verifies the bearer token,
// extracts the jti and exp claims, and revokes the token.
func (s *Server) LogoutHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		if r.Method != http.MethodPost {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}

		log := ctxlog.LoggerFrom(r.Context())

		const prefix = "Bearer "
		authHeader := r.Header.Get("Authorization")
		if !strings.HasPrefix(authHeader, prefix) {
			log.WarnContext(r.Context(), "logout: missing bearer token")
			writeOAuthError(w, "invalid_request", "missing bearer token", http.StatusUnauthorized)
			return
		}
		tokenString := authHeader[len(prefix):]

		tok, err := jwt.ParseString(tokenString, jwt.WithKey(jwa.ES384(), s.pubkey))
		if err != nil {
			log.WarnContext(r.Context(), "logout: token verification failed", slog.Any("error", err))
			writeOAuthError(w, "invalid_token", "token verification failed", http.StatusUnauthorized)
			return
		}

		var jti string
		if err := tok.Get("jti", &jti); err != nil || jti == "" {
			log.WarnContext(r.Context(), "logout: missing jti claim")
			writeOAuthError(w, "invalid_token", "missing jti claim", http.StatusUnauthorized)
			return
		}

		exp, ok := tok.Expiration()
		if !ok {
			log.WarnContext(r.Context(), "logout: missing expiration claim")
			writeOAuthError(w, "invalid_token", "missing expiration claim", http.StatusUnauthorized)
			return
		}

		ctx := r.Context()
		log = ctxlog.LoggerFrom(ctx).With(slog.String("jti", jti))
		ctx = ctxlog.WithLogger(ctx, log)

		if err := s.revocation.RevokeToken(ctx, jti, exp); err != nil {
			// Log but still 204 — the token will expire naturally.
			log.ErrorContext(ctx, "revoke token failed", slog.Any("error", err))
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
		// Requests that cannot be assigned to a supported grant remain in access logs; wide events never guess a flow name.
		if err := r.ParseForm(); err != nil {
			ctxlog.LoggerFrom(r.Context()).WarnContext(r.Context(), "token request: failed to parse form")
			writeOAuthError(w, "invalid_request", "failed to parse request body", http.StatusBadRequest)
			return
		}

		switch r.PostForm.Get("grant_type") {
		case oauthGrantAuthorizationCode:
			s.events.HTTPHandler(wideevent.OAuthTokenExchangeCompleted, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				s.handleAuthCodeGrant(w, r, r.PostForm)
			})).ServeHTTP(w, r)
		case oauthGrantRefreshToken:
			s.events.HTTPHandler(wideevent.OAuthRefreshCompleted, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				s.handleRefreshGrant(w, r, r.PostForm)
			})).ServeHTTP(w, r)
		default:
			ctxlog.LoggerFrom(r.Context()).WarnContext(r.Context(), "token request: unsupported grant_type",
				slog.String("grant_type", r.PostForm.Get("grant_type")),
			)
			writeOAuthError(w, "unsupported_grant_type", "grant_type must be authorization_code or refresh_token", http.StatusBadRequest)
		}
	})
}

func (s *Server) handleAuthCodeGrant(w http.ResponseWriter, r *http.Request, form url.Values) {
	log := ctxlog.LoggerFrom(r.Context())

	code := form.Get("code")
	codeVerifier := form.Get("code_verifier")
	clientID := form.Get("client_id")
	if code == "" || codeVerifier == "" || clientID == "" {
		log.WarnContext(r.Context(), "auth code grant: missing code, code_verifier, or client_id")
		writeOAuthError(w, "invalid_request", "code, code_verifier, and client_id are required", http.StatusBadRequest)
		return
	}

	pc, err := s.authState.ConsumeAuthCode(r.Context(), code)
	if errors.Is(err, storepkg.ErrNotFound) {
		log.WarnContext(r.Context(), "auth code invalid or expired")
		writeOAuthError(w, "invalid_grant", "invalid or expired authorization code", http.StatusBadRequest)
		return
	}
	if err != nil {
		log.ErrorContext(r.Context(), "consume auth code failed", slog.Any("error", err))
		writeOAuthError(w, "server_error", "internal error", http.StatusInternalServerError)
		return
	}
	if clientID != pc.ClientID {
		log.WarnContext(r.Context(), "authorization code client_id mismatch",
			slog.String("request_client_id", clientID),
			slog.String("code_client_id", pc.ClientID),
		)
		writeOAuthError(w, "invalid_grant", "authorization code was not issued to this client", http.StatusBadRequest)
		return
	}

	// RFC 6749 §4.1.3: redirect_uri must be present and identical.
	if form.Get("redirect_uri") != pc.RedirectURI {
		log.WarnContext(r.Context(), "redirect_uri mismatch")
		writeOAuthError(w, "invalid_grant", "redirect_uri does not match authorization request", http.StatusBadRequest)
		return
	}

	// Verify PKCE: BASE64URL(SHA256(code_verifier)) must equal stored code_challenge.
	h := sha256.Sum256([]byte(codeVerifier))
	if base64.RawURLEncoding.EncodeToString(h[:]) != pc.CodeChallenge {
		log.WarnContext(r.Context(), "PKCE verification failed")
		writeOAuthError(w, "invalid_grant", "code_verifier does not match code_challenge", http.StatusBadRequest)
		return
	}

	jti := uuid.New().String()
	jwtExpiry := time.Now().Add(jwtTTL)
	log = log.With(
		slog.String("client_id", pc.ClientID),
		slog.String("sub", pc.Sub),
		slog.String("jti", jti),
	)
	ctx := ctxlog.WithLogger(r.Context(), log)

	tokenString, err := s.IssueJWT(pc.Sub, pc.Scope, jti, jwtExpiry)
	if err != nil {
		log.ErrorContext(ctx, "JWT issuance failed", slog.Any("error", err))
		writeOAuthError(w, "server_error", "failed to issue token", http.StatusInternalServerError)
		return
	}

	// Issue an opaque refresh token only when we have a GitHub refresh token to back it.
	var ourRefreshToken string
	if s.grants != nil && pc.AccessToken != "" && s.clientSupportsGrant(ctx, pc.ClientID, oauthGrantRefreshToken) {
		if pc.RefreshToken != "" {
			ourRefreshToken, err = generateOpaqueToken()
			if err != nil {
				log.ErrorContext(ctx, "generate refresh token failed", slog.Any("error", err))
				writeOAuthError(w, "server_error", "failed to issue refresh token", http.StatusInternalServerError)
				return
			}
		}
		grantUserID, parseErr := uuid.Parse(pc.Sub)
		if parseErr != nil {
			// sub must be a valid UUID; if not, skip grant storage but still issue the JWT.
			log.ErrorContext(ctx, "upsert grant skipped: sub is not a valid UUID", slog.Any("error", parseErr))
			ourRefreshToken = ""
		} else if err := s.grants.UpsertGrant(ctx, storepkg.Grant{
			JTI:                jti,
			UserID:             grantUserID,
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
			log.ErrorContext(ctx, "upsert grant failed", slog.Any("error", err))
			ourRefreshToken = ""
		}
	}

	log.InfoContext(ctx, "token exchange: issued JWT", slog.String("sub", pc.Sub))

	s.touchRegisteredClient(ctx, log, pc.ClientID)
	writeTokenResponse(ctx, w, tokenString, pc.Scope, ourRefreshToken, pc.RefreshTokenExpiry, jwtExpiry)
}

func (s *Server) handleRefreshGrant(w http.ResponseWriter, r *http.Request, form url.Values) {
	refreshToken := form.Get("refresh_token")
	clientID := form.Get("client_id")

	// Enrich the logger with client_id as soon as it's known.
	ctx := r.Context()
	log := ctxlog.LoggerFrom(ctx).With(slog.String("client_id", clientID))
	ctx = ctxlog.WithLogger(ctx, log)

	if refreshToken == "" || clientID == "" {
		telemetry.RecordRefreshTokenGrant(ctx, "failure", "invalid_request")
		log.WarnContext(ctx, "refresh grant: missing refresh_token or client_id",
			slog.String("outcome", "failure"),
			slog.String("reason", "invalid_request"),
		)
		writeOAuthError(w, "invalid_request", "refresh_token and client_id are required", http.StatusBadRequest)
		return
	}

	if s.grants == nil {
		telemetry.RecordRefreshTokenGrant(ctx, "failure", "unsupported")
		log.WarnContext(ctx, "refresh grant: grant store not configured",
			slog.String("outcome", "failure"),
			slog.String("reason", "unsupported"),
		)
		writeOAuthError(w, "invalid_grant", "refresh token grant not supported", http.StatusBadRequest)
		return
	}

	grant, err := s.grants.GetGrantByRefreshToken(ctx, refreshToken)
	if errors.Is(err, storepkg.ErrNotFound) {
		s.handleRetiredRefreshGrant(ctx, w, refreshToken, clientID)
		return
	}
	if err != nil {
		telemetry.RecordRefreshTokenGrant(ctx, "failure", "server_error")
		log.ErrorContext(ctx, "lookup grant by refresh token failed", slog.Any("error", err), slog.String("outcome", "failure"), slog.String("reason", "server_error"))
		writeOAuthError(w, "server_error", "internal error", http.StatusInternalServerError)
		return
	}

	// Enrich further with jti and user_id now that we have the grant.
	log = log.With(
		slog.String("jti", grant.JTI),
		slog.String("user_id", grant.UserID.String()),
	)
	ctx = ctxlog.WithLogger(ctx, log)

	// client_id is validated only when stored on the grant (best-effort per v0.2 constraints).
	if grant.ClientID != "" && grant.ClientID != clientID {
		warnFields := []any{
			slog.String("request_client_id", clientID),
			slog.String("grant_client_id", grant.ClientID),
		}
		telemetry.RecordRefreshTokenGrant(ctx, "failure", "client_mismatch")
		warnFields = append(warnFields, slog.String("outcome", "failure"), slog.String("reason", "client_mismatch"))
		log.WarnContext(ctx, "client_id mismatch on token refresh", warnFields...)
		writeOAuthError(w, "invalid_client", "client_id does not match grant", http.StatusBadRequest)
		return
	}

	if grant.RefreshToken == "" {
		telemetry.RecordRefreshTokenGrant(ctx, "failure", "github_missing_refresh")
		log.WarnContext(ctx, "grant has no GitHub refresh token", slog.String("outcome", "failure"), slog.String("reason", "github_missing_refresh"))
		writeOAuthError(w, "invalid_grant", "grant has no GitHub refresh token", http.StatusBadRequest)
		return
	}

	// If GitHub refresh token has already expired, drop the grant and force re-auth.
	if !grant.RefreshTokenExpiry.IsZero() && time.Now().After(grant.RefreshTokenExpiry) {
		telemetry.RecordRefreshTokenGrant(ctx, "failure", "github_expired")
		log.WarnContext(ctx, "GitHub refresh token expired; tearing down grant",
			slog.Time("refresh_token_expiry", grant.RefreshTokenExpiry),
			slog.String("outcome", "failure"),
			slog.String("reason", "github_expired"),
		)
		s.tearDownGrant(ctx, grant, refreshToken, storepkg.RetiredRefreshTokenReasonGitHubExpired)
		writeOAuthError(w, "invalid_grant", "GitHub refresh token has expired", http.StatusBadRequest)
		return
	}

	// Detach from the HTTP request context before calling GitHub. GitHub rotates the
	// refresh token on the first valid call, so a client disconnection mid-flight would
	// leave the new token unrecorded and the old one permanently invalidated. We use a
	// non-cancelable child so the refresh + storage always completes.
	storeCtx := ctxlog.WithLogger(context.WithoutCancel(ctx), log)

	newGHToken, identity, err := s.github.RefreshToken(storeCtx, grant.RefreshToken)
	if err != nil {
		var retrieveErr *oauth2.RetrieveError
		if errors.As(err, &retrieveErr) && retrieveErr.ErrorCode == "bad_refresh_token" {
			// GitHub has invalidated the stored refresh token. Tear down the grant so
			// future requests fail fast at DB lookup rather than hitting GitHub again.
			telemetry.RecordRefreshTokenGrant(ctx, "failure", "github_invalid")
			log.ErrorContext(ctx, "GitHub refresh token rejected; dropping grant",
				slog.String("github_error", retrieveErr.ErrorCode),
				slog.String("outcome", "failure"),
				slog.String("reason", "github_invalid"),
			)
			s.tearDownGrant(storeCtx, grant, refreshToken, storepkg.RetiredRefreshTokenReasonGitHubInvalid)
			writeOAuthError(w, "invalid_grant", "GitHub refresh token is invalid; re-authentication required", http.StatusBadRequest)
			return
		}
		telemetry.RecordRefreshTokenGrant(ctx, "failure", "server_error")
		log.ErrorContext(ctx, "GitHub token refresh failed", slog.String("outcome", "failure"), slog.String("reason", "server_error"))
		writeOAuthError(w, "server_error", "GitHub token refresh failed", http.StatusInternalServerError)
		return
	}

	// GitHub returned a fresh access token but no new refresh token, so the chain
	// can't continue. GitHub has already invalidated our stored refresh token in
	// rotation; tear the grant down and force re-auth.
	if newGHToken.RefreshToken == "" {
		telemetry.RecordRefreshTokenGrant(ctx, "failure", "github_missing_refresh")
		log.ErrorContext(ctx, "GitHub refresh response missing refresh_token; dropping grant", slog.String("outcome", "failure"), slog.String("reason", "github_missing_refresh"))
		s.tearDownGrant(storeCtx, grant, refreshToken, storepkg.RetiredRefreshTokenReasonGitHubMissingRefresh)
		writeOAuthError(w, "invalid_grant", "GitHub did not return a new refresh token; re-authentication required", http.StatusBadRequest)
		return
	}

	sub := strconv.FormatInt(identity.ID, 10)
	userID := grant.UserID
	if s.users != nil {
		user, uErr := s.users.UpsertUser(storeCtx, storepkg.GitHubProfile{
			GitHubID: identity.ID, Email: identity.Email, Login: identity.Login,
			DisplayName: identity.DisplayName, AvatarURL: identity.AvatarURL,
			ProfileURL: identity.ProfileURL, Bio: identity.Bio,
		})
		if uErr != nil {
			telemetry.RecordRefreshTokenGrant(ctx, "failure", "server_error")
			log.ErrorContext(ctx, "upsert user failed", slog.Any("error", uErr), slog.String("outcome", "failure"), slog.String("reason", "server_error"))
			writeOAuthError(w, "server_error", "internal error", http.StatusInternalServerError)
			return
		}
		sub = user.ID.String()
		userID = user.ID
	}

	newJTI := uuid.New().String()
	newOurRefreshToken, err := generateOpaqueToken()
	if err != nil {
		telemetry.RecordRefreshTokenGrant(ctx, "failure", "server_error")
		log.ErrorContext(ctx, "generate refresh token failed", slog.Any("error", err), slog.String("outcome", "failure"), slog.String("reason", "server_error"))
		writeOAuthError(w, "server_error", "failed to issue refresh token", http.StatusInternalServerError)
		return
	}
	newJWTExpiry := time.Now().Add(jwtTTL)
	newGHRefreshExpiry := extractRefreshExpiry(newGHToken)

	newGrant := storepkg.Grant{
		JTI:                newJTI,
		UserID:             userID,
		OurRefreshToken:    newOurRefreshToken,
		ClientID:           grant.ClientID,
		Scope:              grant.Scope,
		AccessToken:        newGHToken.AccessToken,
		RefreshToken:       newGHToken.RefreshToken,
		AccessTokenExpiry:  newGHToken.Expiry,
		RefreshTokenExpiry: newGHRefreshExpiry,
		JWTExpiry:          newJWTExpiry,
	}
	var graceExpiresAt time.Time
	if s.refreshTokenGracePeriod > 0 {
		graceExpiresAt = time.Now().Add(s.refreshTokenGracePeriod)
	}
	retired := s.newRetiredRefreshToken(refreshToken, storepkg.RetiredRefreshTokenReasonRotated, grant, newJTI, graceExpiresAt)
	if _, err := s.grants.RotateGrant(storeCtx, refreshToken, grant.JTI, grant.JWTExpiry, newGrant, retired); err != nil {
		if errors.Is(err, storepkg.ErrNotFound) {
			// Concurrent refresh: another request already consumed this token.
			telemetry.RecordRefreshTokenGrant(ctx, "failure", "recently_rotated_grace_expired")
			log.WarnContext(ctx, "concurrent token refresh: grant already rotated", slog.String("outcome", "failure"), slog.String("reason", "recently_rotated_grace_expired"))
			writeOAuthError(w, "invalid_grant", "refresh token already used", http.StatusBadRequest)
			return
		}
		telemetry.RecordRefreshTokenGrant(ctx, "failure", "server_error")
		log.ErrorContext(ctx, "rotate grant failed", slog.Any("error", err), slog.String("outcome", "failure"), slog.String("reason", "server_error"))
		writeOAuthError(w, "server_error", "failed to rotate grant", http.StatusInternalServerError)
		return
	}

	log.InfoContext(ctx, "token rotation: grant rotated",
		slog.String("old_jti", grant.JTI),
		slog.String("new_jti", newJTI),
		slog.String("outcome", "success"),
		slog.String("reason", "rotated"),
	)

	tokenString, err := s.IssueJWT(sub, grant.Scope, newJTI, newJWTExpiry)
	if err != nil {
		telemetry.RecordRefreshTokenGrant(ctx, "failure", "server_error")
		log.ErrorContext(ctx, "JWT issuance failed", slog.Any("error", err), slog.String("outcome", "failure"), slog.String("reason", "server_error"))
		writeOAuthError(w, "server_error", "failed to issue token", http.StatusInternalServerError)
		return
	}

	telemetry.RecordRefreshTokenGrant(ctx, "success", "rotated")
	s.touchRegisteredClient(ctx, log, clientID)
	writeTokenResponse(ctx, w, tokenString, grant.Scope, newOurRefreshToken, newGHRefreshExpiry, newJWTExpiry)
}

func (s *Server) handleRetiredRefreshGrant(ctx context.Context, w http.ResponseWriter, refreshToken, clientID string) {
	log := ctxlog.LoggerFrom(ctx)
	retired, err := s.grants.GetRetiredRefreshToken(ctx, storepkg.HashRefreshToken(refreshToken))
	if errors.Is(err, storepkg.ErrNotFound) {
		telemetry.RecordRefreshTokenGrant(ctx, "failure", "never_seen")
		log.WarnContext(ctx, "refresh token never seen", slog.String("outcome", "failure"), slog.String("reason", "never_seen"))
		writeOAuthError(w, "invalid_grant", "refresh token not found or already used", http.StatusBadRequest)
		return
	}
	if err != nil {
		telemetry.RecordRefreshTokenGrant(ctx, "failure", "server_error")
		log.ErrorContext(ctx, "lookup retired refresh token failed", slog.Any("error", err), slog.String("outcome", "failure"), slog.String("reason", "server_error"))
		writeOAuthError(w, "server_error", "internal error", http.StatusInternalServerError)
		return
	}

	log = log.With(
		slog.String("retired_reason", retired.Reason),
		slog.String("old_jti", retired.OldJTI),
		slog.String("replacement_jti", retired.ReplacementJTI),
	)
	ctx = ctxlog.WithLogger(ctx, log)

	if retired.ClientID != "" && retired.ClientID != clientID {
		telemetry.RecordRefreshTokenGrant(ctx, "failure", "client_mismatch")
		log.WarnContext(ctx, "client_id mismatch on retired refresh token",
			slog.String("outcome", "failure"),
			slog.String("reason", "client_mismatch"),
			slog.String("grant_client_id", retired.ClientID),
		)
		writeOAuthError(w, "invalid_client", "client_id does not match grant", http.StatusBadRequest)
		return
	}

	if retired.Reason != storepkg.RetiredRefreshTokenReasonRotated {
		telemetry.RecordRefreshTokenGrant(ctx, "failure", "recently_removed")
		log.WarnContext(ctx, "refresh token was recently removed",
			slog.String("outcome", "failure"),
			slog.String("reason", "recently_removed"),
			slog.String("removed_reason", retired.Reason),
		)
		writeOAuthError(w, "invalid_grant", "refresh token was recently removed", http.StatusBadRequest)
		return
	}

	if retired.GraceExpiresAt.IsZero() || time.Now().After(retired.GraceExpiresAt) {
		telemetry.RecordRefreshTokenGrant(ctx, "failure", "recently_rotated_grace_expired")
		log.WarnContext(ctx, "refresh token grace period expired",
			slog.String("outcome", "failure"),
			slog.String("reason", "recently_rotated_grace_expired"),
			slog.Time("grace_expires_at", retired.GraceExpiresAt),
		)
		writeOAuthError(w, "invalid_grant", "refresh token already used", http.StatusBadRequest)
		return
	}

	if retired.ReplacementJTI == "" {
		telemetry.RecordRefreshTokenGrant(ctx, "failure", "recently_removed")
		log.WarnContext(ctx, "retired refresh token missing replacement jti", slog.String("outcome", "failure"), slog.String("reason", "recently_removed"))
		writeOAuthError(w, "invalid_grant", "refresh token already used", http.StatusBadRequest)
		return
	}

	grant, err := s.grants.GetGrant(ctx, retired.ReplacementJTI)
	if errors.Is(err, storepkg.ErrNotFound) {
		telemetry.RecordRefreshTokenGrant(ctx, "failure", "recently_removed")
		log.WarnContext(ctx, "replacement grant not found for grace retry", slog.String("outcome", "failure"), slog.String("reason", "recently_removed"))
		writeOAuthError(w, "invalid_grant", "refresh token already used", http.StatusBadRequest)
		return
	}
	if err != nil {
		telemetry.RecordRefreshTokenGrant(ctx, "failure", "server_error")
		log.ErrorContext(ctx, "lookup replacement grant failed", slog.Any("error", err), slog.String("outcome", "failure"), slog.String("reason", "server_error"))
		writeOAuthError(w, "server_error", "internal error", http.StatusInternalServerError)
		return
	}

	if grant.ClientID != "" && grant.ClientID != clientID {
		telemetry.RecordRefreshTokenGrant(ctx, "failure", "client_mismatch")
		log.WarnContext(ctx, "client_id mismatch on replacement grant",
			slog.String("outcome", "failure"),
			slog.String("reason", "client_mismatch"),
			slog.String("grant_client_id", grant.ClientID),
		)
		writeOAuthError(w, "invalid_client", "client_id does not match grant", http.StatusBadRequest)
		return
	}

	if time.Now().After(grant.JWTExpiry) {
		telemetry.RecordRefreshTokenGrant(ctx, "failure", "recently_removed")
		log.WarnContext(ctx, "replacement grant expired for grace retry", slog.String("outcome", "failure"), slog.String("reason", "recently_removed"))
		writeOAuthError(w, "invalid_grant", "refresh token already used", http.StatusBadRequest)
		return
	}

	tokenString, err := s.IssueJWT(grant.UserID.String(), grant.Scope, grant.JTI, grant.JWTExpiry)
	if err != nil {
		telemetry.RecordRefreshTokenGrant(ctx, "failure", "server_error")
		log.ErrorContext(ctx, "JWT issuance failed for grace retry", slog.Any("error", err), slog.String("outcome", "failure"), slog.String("reason", "server_error"))
		writeOAuthError(w, "server_error", "failed to issue token", http.StatusInternalServerError)
		return
	}

	telemetry.RecordRefreshTokenGrant(ctx, "success", "grace_retry")
	log.InfoContext(ctx, "refresh token grace retry succeeded",
		slog.String("outcome", "success"),
		slog.String("reason", "grace_retry"),
		slog.String("jti", grant.JTI),
		slog.String("user_id", grant.UserID.String()),
	)
	s.touchRegisteredClient(ctx, log, clientID)
	writeTokenResponse(ctx, w, tokenString, grant.Scope, grant.OurRefreshToken, grant.RefreshTokenExpiry, grant.JWTExpiry)
}

// DCRHandler returns an HTTP handler for Dynamic Client Registration (RFC 7591).
func (s *Server) DCRHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}

		log := ctxlog.LoggerFrom(r.Context())

		var req oauthex.ClientRegistrationMetadata
		if err := decodeClientRegistration(w, r, &req); err != nil {
			log.WarnContext(r.Context(), "DCR: failed to parse request body", slog.Any("error", err))
			writeOAuthError(w, "invalid_client_metadata", err.Error(), http.StatusBadRequest)
			return
		}

		log.InfoContext(r.Context(), "DCR request",
			slog.Int("grant_type_count", len(req.GrantTypes)),
			slog.Int("response_type_count", len(req.ResponseTypes)),
			slog.Int("redirect_uri_count", len(req.RedirectURIs)),
		)

		if err := validateClientRegistrationMetadata(&req); err != nil {
			log.WarnContext(r.Context(), "DCR: invalid client metadata")
			writeOAuthError(w, "invalid_client_metadata", err.Error(), http.StatusBadRequest)
			return
		}

		normalizeClientRegistrationMetadata(&req)
		req.Scope = normalizeScope(req.Scope, defaultRegisteredClientScope)
		if err := validateSupportedScope(req.Scope); err != nil {
			log.WarnContext(r.Context(), "DCR: invalid scope")
			writeOAuthError(w, "invalid_client_metadata", err.Error(), http.StatusBadRequest)
			return
		}

		now := time.Now()
		clientID := uuid.New().String()
		ctx := r.Context()

		rec := storepkg.OAuthClient{
			ClientID:                clientID,
			ClientName:              req.ClientName,
			TokenEndpointAuthMethod: req.TokenEndpointAuthMethod,
			Scope:                   req.Scope,
			RedirectURIs:            req.RedirectURIs,
			GrantTypes:              req.GrantTypes,
			ResponseTypes:           req.ResponseTypes,
			IssuedAt:                now,
		}
		if err := s.saveRegisteredClient(ctx, rec); err != nil {
			log.ErrorContext(ctx, "failed to persist DCR client", slog.String("client_id", clientID), slog.Any("error", err))
			writeOAuthError(w, "server_error", "failed to save client registration", http.StatusInternalServerError)
			return
		}
		log.InfoContext(ctx, "DCR client registered",
			slog.String("client_id", clientID),
		)

		resp := &oauthex.ClientRegistrationResponse{
			ClientRegistrationMetadata: req,
			ClientID:                   clientID,
			ClientIDIssuedAt:           now,
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		if err := json.NewEncoder(w).Encode(resp); err != nil { //nolint:gosec // intentional: DCR response includes client_secret by spec
			log.ErrorContext(ctx, "failed to write DCR response", slog.String("client_id", clientID), slog.Any("error", err))
		}
	})
}

func (s *Server) GitHubCallbackHandler() http.Handler {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		if r.Method != http.MethodGet {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}

		q := r.URL.Query()

		log := ctxlog.LoggerFrom(r.Context())
		log.InfoContext(r.Context(), "github callback: received")

		pending, err := s.authState.ConsumePendingAuth(r.Context(), q.Get("state"))
		if errors.Is(err, storepkg.ErrNotFound) {
			http.Error(w, "invalid or expired state", http.StatusBadRequest)
			return
		}
		if err != nil {
			log.ErrorContext(r.Context(), "consume pending auth failed", slog.Any("error", err))
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}

		githubToken, identity, err := s.github.ExchangeCode(r.Context(), q.Get("code"))
		if err != nil {
			log.ErrorContext(r.Context(), "GitHub exchange failed")
			http.Error(w, "failed to authenticate with GitHub", http.StatusBadGateway)
			return
		}

		// Enrich the logger with identity fields now that we have them.
		log = log.With(
			slog.String("client_id", pending.ClientID),
			slog.Int64("github_id", identity.ID),
		)
		ctx := ctxlog.WithLogger(r.Context(), log)

		// sub is the internal user UUID; fall back to GitHub numeric ID only when no user store is wired (tests).
		sub := strconv.FormatInt(identity.ID, 10)
		if s.users != nil {
			user, uErr := s.users.UpsertUser(ctx, storepkg.GitHubProfile{
				GitHubID: identity.ID, Email: identity.Email, Login: identity.Login,
				DisplayName: identity.DisplayName, AvatarURL: identity.AvatarURL,
				ProfileURL: identity.ProfileURL, Bio: identity.Bio,
			})
			if uErr != nil {
				log.ErrorContext(ctx, "upsert user failed", slog.Any("error", uErr))
				http.Error(w, "internal error", http.StatusInternalServerError)
				return
			}
			sub = user.ID.String()
		}

		authCode := storepkg.AuthCode{
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
		}

		if pending.ConfirmationRequired {
			confirmationToken, tokenHash, tokenErr := newConfirmationToken()
			if tokenErr != nil {
				log.ErrorContext(ctx, "generate authorization confirmation token failed", slog.Any("error", tokenErr))
				http.Error(w, "internal error", http.StatusInternalServerError)
				return
			}
			if err := s.authState.StoreAuthorizationConfirmation(ctx, tokenHash, storepkg.AuthorizationConfirmation{
				AuthCode: authCode, ClientName: pending.ClientName, ClientState: pending.ClientState,
			}); err != nil {
				log.ErrorContext(ctx, "store authorization confirmation failed", slog.Any("error", err))
				http.Error(w, "internal error", http.StatusInternalServerError)
				return
			}
			if err := renderAuthorizationConfirmation(w, pending, confirmationToken); err != nil {
				log.ErrorContext(ctx, "render authorization confirmation failed", slog.Any("error", err))
			}
			return
		}

		code := uuid.New().String()
		if err := s.authState.StoreAuthCode(ctx, code, authCode); err != nil {
			log.ErrorContext(ctx, "store auth code failed", slog.Any("error", err))
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}

		redirectTo, err := authorizationResultRedirect(&storepkg.AuthorizationConfirmationResult{
			RedirectURI: pending.RedirectURI,
			ClientState: pending.ClientState,
		}, true, code)
		if err != nil {
			log.ErrorContext(ctx, "invalid redirect URI in pending auth")
			http.Error(w, "invalid redirect_uri", http.StatusInternalServerError)
			return
		}

		log.InfoContext(ctx, "GitHub auth complete",
			slog.String("sub", sub),
		)

		// redirect_uri was validated against the registered client in AuthorizeHandler before being stored.
		http.Redirect(w, r, redirectTo, http.StatusFound) //nolint:gosec
	})
	return s.events.HTTPHandler(wideevent.OAuthGitHubCallbackCompleted, handler)
}

func (s *Server) AuthorizeHandler() http.Handler {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		if r.Method != http.MethodGet {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}

		ctx := r.Context()
		log := ctxlog.LoggerFrom(ctx)
		q := r.URL.Query()

		if q.Get("response_type") != oauthCode {
			log.WarnContext(ctx, "authorize: unsupported response_type")
			writeOAuthError(w, "unsupported_response_type", "only response_type=code is supported", http.StatusBadRequest)
			return
		}

		clientID := q.Get("client_id")
		redirectURI := q.Get("redirect_uri")
		if clientID != "" {
			log = log.With(slog.String("client_id", clientID))
			ctx = ctxlog.WithLogger(ctx, log)
		}
		if redirectURI == "" {
			log.WarnContext(ctx, "authorize: missing redirect_uri")
			writeOAuthError(w, "invalid_request", "redirect_uri is required", http.StatusBadRequest)
			return
		}

		client, reqErr := s.registeredClientForAuthorize(ctx, log, clientID, redirectURI)
		if reqErr != nil {
			writeOAuthError(w, reqErr.code, reqErr.description, reqErr.status)
			return
		}

		codeChallenge := q.Get("code_challenge")
		if codeChallenge == "" {
			log.WarnContext(ctx, "authorize: missing code_challenge")
			writeOAuthError(w, "invalid_request", "code_challenge is required (PKCE mandatory)", http.StatusBadRequest)
			return
		}

		if q.Get("code_challenge_method") != pkceMethodS256 {
			log.WarnContext(ctx, "authorize: unsupported code_challenge_method")
			writeOAuthError(w, "invalid_request", "only code_challenge_method=S256 is supported", http.StatusBadRequest)
			return
		}

		rawScope := q.Get("scope")
		scope := normalizeScope(rawScope, defaultAuthorizeScope)
		if client != nil {
			registeredScope := normalizeScope(client.Scope, defaultRegisteredClientScope)
			if err := validateSupportedScope(registeredScope); err != nil {
				log.ErrorContext(ctx, "registered client has invalid scope")
				writeOAuthError(w, "server_error", "registered client has invalid scope", http.StatusInternalServerError)
				return
			}
			if len(strings.Fields(rawScope)) == 0 {
				scope = registeredScope
			} else if sc, ok := firstDisallowedScope(scope, registeredScope); ok {
				log.WarnContext(ctx, "authorize: scope not registered for client")
				writeOAuthError(w, "invalid_scope", "scope not registered for this client: "+sc, http.StatusBadRequest)
				return
			}
		}
		if err := validateSupportedScope(scope); err != nil {
			log.WarnContext(ctx, "authorize: invalid scope")
			writeOAuthError(w, "invalid_scope", err.Error(), http.StatusBadRequest)
			return
		}

		githubState := uuid.New().String()
		clientName := ""
		if client != nil {
			clientName = client.ClientName
		}
		if err := s.authState.StorePendingAuth(ctx, githubState, storepkg.PendingAuth{
			ClientID:             clientID,
			ClientName:           clientName,
			RedirectURI:          redirectURI,
			Scope:                scope,
			CodeChallenge:        codeChallenge,
			ClientState:          q.Get("state"),
			ConfirmationRequired: authorizationConfirmationRequired(clientID),
		}); err != nil {
			log.ErrorContext(ctx, "store pending auth failed", slog.Any("error", err))
			writeOAuthError(w, "server_error", "failed to store authorization state", http.StatusInternalServerError)
			return
		}
		s.touchRegisteredClient(ctx, log, clientID)

		log.InfoContext(ctx, "authorize: redirecting to GitHub")

		authURL := s.github.AuthCodeURL(githubState)
		http.Redirect(w, r, authURL, http.StatusFound)
	})
	return s.events.HTTPHandler(wideevent.OAuthAuthorizationCompleted, handler)
}
