package oidc

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/modelcontextprotocol/go-sdk/oauthex"
)

// ClientRecord is the oidc-layer view of a DCR registration, using only stdlib types.
// It is passed to ClientStore.SaveClient and must not import external package types.
type ClientRecord struct {
	ClientID                string
	ClientName              string
	TokenEndpointAuthMethod string
	Scope                   string
	RedirectURIs            []string
	GrantTypes              []string
	ResponseTypes           []string
	IssuedAt                time.Time
	ExpiresAt               time.Time
}

// ClientStore persists client registrations from Dynamic Client Registration.
type ClientStore interface {
	SaveClient(ctx context.Context, r ClientRecord) error
}

// clientRegistrationTTL is how long a registered client remains valid.
const clientRegistrationTTL = 90 * 24 * time.Hour

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
			writeDCRError(w, "invalid_client_metadata", "failed to parse request body", http.StatusBadRequest)
			return
		}

		slog.Default().InfoContext(r.Context(), "DCR request",
			slog.Any("grant_types", req.GrantTypes),
			slog.Any("response_types", req.ResponseTypes),
			slog.Any("redirect_uris", req.RedirectURIs),
			slog.String("token_endpoint_auth_method", req.TokenEndpointAuthMethod),
			slog.String("client_name", req.ClientName),
		)

		if len(req.RedirectURIs) == 0 {
			writeDCRError(w, "invalid_client_metadata", "redirect_uris is required", http.StatusBadRequest)
			return
		}

		if err := validateRedirectURIs(req.RedirectURIs); err != nil {
			writeDCRError(w, "invalid_client_metadata", err.Error(), http.StatusBadRequest)
			return
		}

		if req.TokenEndpointAuthMethod != "" && req.TokenEndpointAuthMethod != "none" {
			writeDCRError(w, "invalid_client_metadata", "only token_endpoint_auth_method=none is supported", http.StatusBadRequest)
			return
		}

		// Normalise to the supported subset — always authorization_code only.
		// RFC 7591 §3.2.1: server registers the supported subset rather than rejecting.
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
			rec := ClientRecord{
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
				slog.Default().ErrorContext(r.Context(), "failed to persist DCR client", slog.Any("error", err))
				writeDCRError(w, "server_error", "failed to save client registration", http.StatusInternalServerError)
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
		if err := json.NewEncoder(w).Encode(resp); err != nil {
			slog.Default().Error("failed to write DCR response", slog.Any("error", err))
		}
	})
}

// validateRedirectURIs enforces safe redirect URI policies for public MCP clients.
// Accepts: HTTPS, http://localhost, http://127.0.0.1, and custom (non-http/https) schemes.
// Rejects: non-localhost http://, URIs with fragments, URIs with wildcards.
func validateRedirectURIs(uris []string) error {
	for _, raw := range uris {
		if strings.Contains(raw, "*") {
			return fmt.Errorf("redirect_uri must not contain wildcards: %q", raw)
		}
		u, err := url.Parse(raw)
		if err != nil {
			return fmt.Errorf("invalid redirect_uri %q: %w", raw, err)
		}
		if u.Fragment != "" {
			return fmt.Errorf("redirect_uri must not contain a fragment: %q", raw)
		}
		switch u.Scheme {
		case "https":
			// always accepted
		case "http":
			host := u.Hostname()
			if host != "localhost" && host != "127.0.0.1" {
				return fmt.Errorf("http redirect_uri is only allowed for localhost: %q", raw)
			}
		default:
			// custom schemes (cursor://, claude://, etc.) accepted for native app callbacks
		}
	}
	return nil
}

func writeDCRError(w http.ResponseWriter, code, description string, status int) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(&oauthex.ClientRegistrationError{
		ErrorCode:        code,
		ErrorDescription: description,
	})
}
