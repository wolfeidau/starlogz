package oidc

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"golang.org/x/oauth2"
)

var supportedScopes = map[string]bool{
	"facts:read":  true,
	"facts:write": true,
	"org:admin":   true,
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

func writeOAuthError(w http.ResponseWriter, errCode, description string, status int) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(map[string]string{
		"error":             errCode,
		"error_description": description,
	}); err != nil {
		slog.Default().Error("failed to write OAuth error", slog.Any("error", err))
	}
}
