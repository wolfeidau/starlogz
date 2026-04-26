package oidc

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/lestrrat-go/jwx/v3/jwa"
	"github.com/lestrrat-go/jwx/v3/jwt"
)

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

		tokenString, err := s.issueJWT(pc.sub, pc.email, pc.scope)
		if err != nil {
			slog.Default().ErrorContext(r.Context(), "JWT issuance failed", slog.Any("error", err))
			writeOAuthError(w, "server_error", "failed to issue token", http.StatusInternalServerError)
			return
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

func (s *Server) issueJWT(sub, email, scope string) (string, error) {
	now := time.Now()
	tok, err := jwt.NewBuilder().
		Issuer(s.baseURL.String()).
		Subject(sub).
		IssuedAt(now).
		Expiration(now.Add(7 * 24 * time.Hour)).
		Claim("email", email).
		Claim("scope", scope).
		Claim("jti", uuid.New().String()).
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
