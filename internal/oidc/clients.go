package oidc

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"slices"

	storepkg "github.com/wolfeidau/starlogz/internal/store"
)

type oauthRequestError struct {
	code        string
	description string
	status      int
}

func (s *Server) clientSupportsGrant(ctx context.Context, clientID, grantType string) bool {
	if s.clients == nil {
		return true
	}
	client, err := s.clients.GetClient(ctx, clientID)
	if err != nil {
		s.logger.WarnContext(ctx, "client grant lookup failed",
			slog.String("client_id", clientID), slog.String("grant_type", grantType), slog.Any("error", err))
		return false
	}
	return slices.Contains(client.GrantTypes, grantType)
}

func (s *Server) saveRegisteredClient(ctx context.Context, c storepkg.OAuthClient) error {
	if s.clients == nil {
		return nil
	}
	return s.clients.SaveClient(ctx, c)
}

func (s *Server) touchRegisteredClient(ctx context.Context, log *slog.Logger, clientID string) {
	if s.clients == nil || clientID == "" {
		return
	}
	if err := s.clients.TouchClient(ctx, clientID); err != nil {
		log.ErrorContext(ctx, "touch oauth client failed", slog.Any("error", err))
	}
}

func (s *Server) registeredClientForAuthorize(ctx context.Context, log *slog.Logger, clientID, redirectURI string) (*storepkg.OAuthClient, *oauthRequestError) {
	if s.clients == nil {
		return nil, nil
	}
	if clientID == "" {
		log.WarnContext(ctx, "authorize: missing client_id")
		return nil, &oauthRequestError{code: "invalid_request", description: "client_id is required", status: http.StatusBadRequest}
	}
	client, err := s.clients.GetClient(ctx, clientID)
	if errors.Is(err, storepkg.ErrNotFound) {
		log.WarnContext(ctx, "authorize: unknown client_id")
		return nil, &oauthRequestError{code: "invalid_client", description: "unknown client_id", status: http.StatusBadRequest}
	}
	if err != nil {
		log.ErrorContext(ctx, "get client failed", slog.Any("error", err))
		return nil, &oauthRequestError{code: "server_error", description: "internal error", status: http.StatusInternalServerError}
	}
	if !slices.Contains(client.RedirectURIs, redirectURI) {
		log.WarnContext(ctx, "authorize: redirect_uri not registered")
		return nil, &oauthRequestError{code: "invalid_request", description: "redirect_uri not registered for this client", status: http.StatusBadRequest}
	}
	return client, nil
}
