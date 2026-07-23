package oidc

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"slices"

	storepkg "github.com/wolfeidau/starlogz/internal/store"
)

const (
	oauthErrorInvalidRequest = "invalid_request"
	oauthErrorInvalidClient  = "invalid_client"
)

type oauthRequestError struct {
	code        string
	description string
	status      int
}

type resolvedOAuthClient struct {
	ClientID        string
	ClientName      string
	RedirectURIs    []string
	Scope           string
	RefreshAllowed  bool
	IsRegisteredDCR bool
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

func (s *Server) resolveClientForAuthorize(ctx context.Context, log *slog.Logger, clientID, redirectURI string) (*resolvedOAuthClient, *oauthRequestError) {
	if clientID == "" {
		log.WarnContext(ctx, "authorize: missing client_id")
		return nil, &oauthRequestError{code: oauthErrorInvalidRequest, description: "client_id is required", status: http.StatusBadRequest}
	}
	if s.clients == nil && s.clientIDMetadataResolver == nil {
		return nil, nil
	}

	if s.clients != nil {
		client, err := s.clients.GetClient(ctx, clientID)
		if err == nil {
			if !redirectURIMatchesAny(client.RedirectURIs, redirectURI, false) {
				log.DebugContext(ctx, "authorize: redirect_uri mismatch detail",
					slog.String("request_redirect_uri", redirectURI),
					slog.Any("allowed_redirect_uris", client.RedirectURIs),
				)
				log.WarnContext(ctx, "authorize: redirect_uri not registered")
				return nil, &oauthRequestError{code: "invalid_request", description: "redirect_uri not registered for this client", status: http.StatusBadRequest}
			}
			return &resolvedOAuthClient{
				ClientID:        client.ClientID,
				ClientName:      client.ClientName,
				RedirectURIs:    client.RedirectURIs,
				Scope:           client.Scope,
				RefreshAllowed:  slices.Contains(client.GrantTypes, oauthGrantRefreshToken),
				IsRegisteredDCR: true,
			}, nil
		}
		if !errors.Is(err, storepkg.ErrNotFound) {
			log.ErrorContext(ctx, "get client failed", slog.Any("error", err))
			return nil, &oauthRequestError{code: "server_error", description: "internal error", status: http.StatusInternalServerError}
		}
	}

	if !s.cimdEnabled || s.clientIDMetadataResolver == nil {
		log.WarnContext(ctx, "authorize: unknown client_id")
		return nil, &oauthRequestError{code: oauthErrorInvalidClient, description: "unknown client_id", status: http.StatusBadRequest}
	}

	client, err := s.clientIDMetadataResolver.Resolve(ctx, clientID)
	if err != nil {
		switch {
		case errors.Is(err, ErrCIMDIneligible):
			log.WarnContext(ctx, "authorize: unknown client_id")
			return nil, &oauthRequestError{code: oauthErrorInvalidClient, description: "unknown client_id", status: http.StatusBadRequest}
		case errors.Is(err, ErrCIMDInvalidMetadata):
			log.WarnContext(ctx, "authorize: invalid client metadata")
			return nil, &oauthRequestError{code: oauthErrorInvalidClient, description: "invalid client metadata", status: http.StatusBadRequest}
		default:
			log.ErrorContext(ctx, "resolve client metadata failed", slog.Any("error", err))
			return nil, &oauthRequestError{code: "server_error", description: "internal error", status: http.StatusInternalServerError}
		}
	}
	if !redirectURIMatchesAny(client.RedirectURIs, redirectURI, true) {
		log.DebugContext(ctx, "authorize: redirect_uri mismatch detail",
			slog.String("request_redirect_uri", redirectURI),
			slog.Any("allowed_redirect_uris", client.RedirectURIs),
		)
		log.WarnContext(ctx, "authorize: redirect_uri not registered")
		return nil, &oauthRequestError{code: "invalid_request", description: "redirect_uri not registered for this client", status: http.StatusBadRequest}
	}
	return client, nil
}

func redirectURIMatchesAny(allowed []string, request string, allowLoopbackPortMismatch bool) bool {
	for _, candidate := range allowed {
		if redirectURIMatches(candidate, request, allowLoopbackPortMismatch) {
			return true
		}
	}
	return false
}

func redirectURIMatches(allowed, request string, allowLoopbackPortMismatch bool) bool {
	if allowed == request {
		return true
	}
	if !allowLoopbackPortMismatch {
		return false
	}

	allowedURL, err := url.Parse(allowed)
	if err != nil {
		return false
	}
	requestURL, err := url.Parse(request)
	if err != nil {
		return false
	}

	if allowedURL.Scheme != redirectSchemeHTTP || requestURL.Scheme != redirectSchemeHTTP {
		return false
	}
	if !isLoopbackRedirectHost(allowedURL.Hostname()) || !isLoopbackRedirectHost(requestURL.Hostname()) {
		return false
	}
	if allowedURL.Hostname() != requestURL.Hostname() {
		return false
	}
	if allowedURL.User != nil || requestURL.User != nil {
		return false
	}
	if allowedURL.Path != requestURL.Path || allowedURL.RawQuery != requestURL.RawQuery || allowedURL.Fragment != requestURL.Fragment {
		return false
	}
	return true
}

func isLoopbackRedirectHost(host string) bool {
	return host == redirectHostLocalhost || host == redirectHostLoopbackIPv4 || host == redirectHostLoopbackIPv6
}
