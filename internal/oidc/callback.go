package oidc

import (
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/google/uuid"
	"golang.org/x/oauth2"
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

// extractRefreshExpiry reads the refresh_token_expires_in field from the GitHub App
// token response. Falls back to six months if the field is absent.
func extractRefreshExpiry(token *oauth2.Token) time.Time {
	if v := token.Extra("refresh_token_expires_in"); v != nil {
		if secs, ok := v.(float64); ok && secs > 0 {
			return time.Now().Add(time.Duration(secs) * time.Second)
		}
	}
	return time.Now().Add(6 * 30 * 24 * time.Hour)
}
