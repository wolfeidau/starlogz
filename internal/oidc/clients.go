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

func (s *Server) saveRegisteredClient(ctx context.Context, c storepkg.OAuthClient) error {
	if s.clients == nil {
		return nil
	}
	return s.clients.SaveClient(ctx, c)
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
		log.WarnContext(ctx, "authorize: redirect_uri not registered", slog.String("redirect_uri", redirectURI))
		return nil, &oauthRequestError{code: "invalid_request", description: "redirect_uri not registered for this client", status: http.StatusBadRequest}
	}
	return client, nil
}

func (s *Server) appendClientNames(ctx context.Context, fields []any, requestClientID, grantClientID string) []any {
	if name, ok := s.clientDisplayName(ctx, requestClientID); ok {
		fields = append(fields, slog.String("request_client_name", name))
	}
	if name, ok := s.clientDisplayName(ctx, grantClientID); ok {
		fields = append(fields, slog.String("grant_client_name", name))
	}
	return fields
}

func (s *Server) clientDisplayName(ctx context.Context, clientID string) (string, bool) {
	if s.clients == nil || clientID == "" {
		return "", false
	}
	client, err := s.clients.GetClient(ctx, clientID)
	if err != nil || client.ClientName == "" {
		return "", false
	}
	return client.ClientName, true
}
