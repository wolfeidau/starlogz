package oidc

import (
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/google/uuid"
)

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
		githubID, email, err := fetchGitHubIdentity(r.Context(), httpClient)
		if err != nil {
			slog.Default().ErrorContext(r.Context(), "GitHub identity fetch failed", slog.Any("error", err))
			http.Error(w, "failed to fetch GitHub identity", http.StatusBadGateway)
			return
		}

		code := uuid.New().String()
		s.storeCode(code, &pendingCode{
			sub:           strconv.FormatInt(githubID, 10),
			email:         email,
			scope:         pending.scope,
			codeChallenge: pending.codeChallenge,
			redirectURI:   pending.redirectURI,
			clientID:      pending.clientID,
			createdAt:     time.Now(),
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
			slog.String("email", email),
			slog.String("sub", fmt.Sprintf("%d", githubID)),
		)

		http.Redirect(w, r, redirectTo.String(), http.StatusFound)
	})
}
