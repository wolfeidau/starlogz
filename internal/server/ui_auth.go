package server

import (
	"context"
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
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/wolfeidau/starlogz/internal/ctxlog"
	"github.com/wolfeidau/starlogz/internal/oidc"
	"github.com/wolfeidau/starlogz/internal/store"
	"github.com/wolfeidau/starlogz/internal/wideevent"
)

const (
	uiClientID              = "starlogz-ui"
	uiSessionCookie         = "starlogz_session"
	uiStateCookie           = "starlogz_ui_state"
	uiVerifierCookie        = "starlogz_ui_verifier"
	uiScope                 = "insights:read insights:write"
	authorizationCodeValue  = "code"
	defaultUISessionIdleTTL = 7 * 24 * time.Hour
	defaultUISessionTTL     = 30 * 24 * time.Hour
	uiSessionTouchInterval  = time.Hour
)

func (s *Server) loginHandler(baseURL string) http.HandlerFunc {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if s.store == nil {
			http.Error(w, "database not configured", http.StatusServiceUnavailable)
			return
		}

		redirectURI := baseURL + "/ui/auth/callback"
		now := time.Now()
		if err := s.ensureUIClient(r.Context(), redirectURI, now); err != nil {
			ctxlog.LoggerFrom(r.Context()).ErrorContext(r.Context(), "save UI OAuth client failed", slog.Any("error", err))
			http.Error(w, "Internal Server Error", http.StatusInternalServerError)
			return
		}

		state, err := randomBase64(32)
		if err != nil {
			http.Error(w, "Internal Server Error", http.StatusInternalServerError)
			return
		}
		verifier, err := randomBase64(64)
		if err != nil {
			http.Error(w, "Internal Server Error", http.StatusInternalServerError)
			return
		}
		challenge := pkceChallenge(verifier)

		cookieBase := cookieSettings(r, 10*time.Minute)
		http.SetCookie(w, namedCookie(uiStateCookie, state, cookieBase))
		http.SetCookie(w, namedCookie(uiVerifierCookie, verifier, cookieBase))

		q := url.Values{
			"client_id":             {uiClientID},
			"redirect_uri":          {redirectURI},
			"response_type":         {authorizationCodeValue},
			"scope":                 {uiScope},
			"state":                 {state},
			"code_challenge":        {challenge},
			"code_challenge_method": {"S256"},
		}
		http.Redirect(w, r, "/oauth2/authorize?"+q.Encode(), http.StatusFound)
	})
	return s.events.HTTPHandler(wideevent.UILoginCompleted, handler).ServeHTTP
}

func (s *Server) uiCallbackHandler(oidcServer *oidc.Server, baseURL string) http.HandlerFunc {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}

		q := r.URL.Query()
		if q.Get("error") != "" {
			http.Error(w, q.Get("error"), http.StatusBadRequest)
			return
		}
		code := q.Get(authorizationCodeValue)
		state := q.Get("state")
		if code == "" || state == "" {
			http.Error(w, "missing code or state", http.StatusBadRequest)
			return
		}
		stateCookie, err := r.Cookie(uiStateCookie)
		if err != nil || stateCookie.Value != state {
			http.Error(w, "invalid state", http.StatusBadRequest)
			return
		}
		verifierCookie, err := r.Cookie(uiVerifierCookie)
		if err != nil || verifierCookie.Value == "" {
			http.Error(w, "missing verifier", http.StatusBadRequest)
			return
		}

		tokenResp, err := exchangeUICode(r.Context(), oidcServer, code, verifierCookie.Value, baseURL+"/ui/auth/callback")
		if err != nil {
			ctxlog.LoggerFrom(r.Context()).WarnContext(r.Context(), "UI token exchange failed")
			http.Error(w, "token exchange failed", http.StatusBadRequest)
			return
		}

		info, err := oidcServer.VerifyJWT(r.Context(), tokenResp.AccessToken, r)
		if err != nil {
			http.Error(w, "invalid token exchange response", http.StatusBadRequest)
			return
		}
		userID, err := uuid.Parse(info.UserID)
		if err != nil {
			http.Error(w, "invalid token subject", http.StatusBadRequest)
			return
		}
		rawSession, err := randomBase64(32)
		if err != nil {
			http.Error(w, "Internal Server Error", http.StatusInternalServerError)
			return
		}
		now := time.Now()
		if _, err := s.store.CreateWebSession(r.Context(), store.WebSession{
			TokenHash: store.HashSessionToken(rawSession), UserID: userID,
			IdleExpiresAt: now.Add(s.uiSessionIdleTTL), ExpiresAt: now.Add(s.uiSessionTTL),
		}); err != nil {
			ctxlog.LoggerFrom(r.Context()).ErrorContext(r.Context(), "create UI session failed", slog.Any("error", err))
			http.Error(w, "Internal Server Error", http.StatusInternalServerError)
			return
		}

		http.SetCookie(w, namedCookie(uiSessionCookie, rawSession, cookieSettings(r, s.uiSessionTTL)))
		clearCookie(w, r, uiStateCookie)
		clearCookie(w, r, uiVerifierCookie)
		http.Redirect(w, r, "/dashboard", http.StatusFound)
	})
	return s.events.HTTPHandler(wideevent.UISessionCreated, handler).ServeHTTP
}

func (s *Server) uiLogoutHandler() http.HandlerFunc {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if c, err := r.Cookie(uiSessionCookie); err == nil && c.Value != "" {
			if err := s.store.RevokeWebSessionByTokenHash(r.Context(), store.HashSessionToken(c.Value)); err != nil && !errors.Is(err, store.ErrNotFound) {
				ctxlog.LoggerFrom(r.Context()).WarnContext(r.Context(), "UI session revoke failed", slog.Any("error", err))
			}
		}
		clearCookie(w, r, uiSessionCookie)
		http.Redirect(w, r, "/", http.StatusFound)
	})
	return s.events.HTTPHandler(wideevent.UISessionRevoked, handler).ServeHTTP
}

func (s *Server) uiAuthMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.store == nil {
			http.Error(w, "Unauthorized", http.StatusUnauthorized)
			return
		}
		c, err := r.Cookie(uiSessionCookie)
		if err != nil || c.Value == "" {
			http.Error(w, "Unauthorized", http.StatusUnauthorized)
			return
		}
		session, err := s.store.GetWebSessionByTokenHash(r.Context(), store.HashSessionToken(c.Value))
		if errors.Is(err, store.ErrNotFound) {
			clearCookie(w, r, uiSessionCookie)
			http.Error(w, "Unauthorized", http.StatusUnauthorized)
			return
		}
		if err != nil {
			ctxlog.LoggerFrom(r.Context()).ErrorContext(r.Context(), "load UI session failed", slog.Any("error", err))
			http.Error(w, "Internal Server Error", http.StatusInternalServerError)
			return
		}
		now := time.Now()
		if now.Sub(session.LastSeenAt) >= uiSessionTouchInterval {
			err := s.store.TouchWebSession(r.Context(), session.ID, now, now.Add(s.uiSessionIdleTTL))
			if errors.Is(err, store.ErrNotFound) {
				clearCookie(w, r, uiSessionCookie)
				http.Error(w, "Unauthorized", http.StatusUnauthorized)
				return
			}
			if err != nil {
				ctxlog.LoggerFrom(r.Context()).ErrorContext(r.Context(), "touch UI session failed", slog.Any("error", err))
				http.Error(w, "Internal Server Error", http.StatusInternalServerError)
				return
			}
		}
		next.ServeHTTP(w, r.WithContext(contextWithWebSession(r.Context(), session)))
	})
}

func (s *Server) ensureUIClient(ctx context.Context, redirectURI string, now time.Time) error {
	return s.store.UpsertClient(ctx, store.OAuthClient{
		ClientID:                uiClientID,
		ClientName:              "starlogz UI",
		RedirectURIs:            []string{redirectURI},
		GrantTypes:              []string{"authorization_code"},
		ResponseTypes:           []string{authorizationCodeValue},
		TokenEndpointAuthMethod: "none",
		Scope:                   uiScope,
		IssuedAt:                now,
	})
}

type uiTokenResponse struct {
	AccessToken string `json:"access_token"`
	ExpiresIn   int    `json:"expires_in"`
}

func exchangeUICode(ctx context.Context, oidcServer *oidc.Server, code, verifier, redirectURI string) (*uiTokenResponse, error) {
	form := url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {code},
		"redirect_uri":  {redirectURI},
		"client_id":     {uiClientID},
		"code_verifier": {verifier},
	}
	req := httptest.NewRequest(http.MethodPost, "/oauth2/token", strings.NewReader(form.Encode())).WithContext(ctx)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rr := httptest.NewRecorder()
	oidcServer.TokenHandler().ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		return nil, fmt.Errorf("token endpoint returned %d: %s", rr.Code, strings.TrimSpace(rr.Body.String()))
	}
	var out uiTokenResponse
	if err := json.NewDecoder(rr.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("decode token response: %w", err)
	}
	if out.AccessToken == "" {
		return nil, fmt.Errorf("token response missing access_token")
	}
	return &out, nil
}

type cookieConfig struct {
	MaxAge int
	Secure bool
}

func cookieSettings(r *http.Request, ttl time.Duration) cookieConfig {
	return cookieConfig{
		MaxAge: int(ttl / time.Second),
		Secure: r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https",
	}
}

func namedCookie(name, value string, cfg cookieConfig) *http.Cookie {
	// Secure is intentionally false on plain localhost development, and true for TLS/proxied HTTPS.
	//nolint:gosec
	return &http.Cookie{
		Name:     name,
		Value:    value,
		Path:     "/",
		MaxAge:   cfg.MaxAge,
		HttpOnly: true,
		Secure:   cfg.Secure,
		SameSite: http.SameSiteLaxMode,
	}
}

func clearCookie(w http.ResponseWriter, r *http.Request, name string) {
	http.SetCookie(w, namedCookie(name, "", cookieConfig{MaxAge: -1, Secure: cookieSettings(r, 0).Secure}))
}

func randomBase64(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

func pkceChallenge(verifier string) string {
	sum := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

func (s *Server) redirectIfSession(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/" {
			if c, err := r.Cookie(uiSessionCookie); err == nil && c.Value != "" && s.store != nil {
				if _, err := s.store.GetWebSessionByTokenHash(r.Context(), store.HashSessionToken(c.Value)); err == nil {
					http.Redirect(w, r, "/dashboard", http.StatusFound)
					return
				}
			}
		}
		next.ServeHTTP(w, r)
	})
}
