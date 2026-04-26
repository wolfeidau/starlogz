package oidc

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/modelcontextprotocol/go-sdk/oauthex"
)

// ClientStore persists client registrations. Pass nil to skip persistence (v0.1 stub).
type ClientStore interface {
	SaveClient(ctx context.Context, resp *oauthex.ClientRegistrationResponse) error
}

// DCRHandler returns an HTTP handler for Dynamic Client Registration (RFC 7591).
func (s *Server) DCRHandler() http.Handler {
	return s.dcrHandler(nil)
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

		if len(req.RedirectURIs) == 0 {
			writeDCRError(w, "invalid_client_metadata", "redirect_uris is required", http.StatusBadRequest)
			return
		}

		if req.TokenEndpointAuthMethod != "" && req.TokenEndpointAuthMethod != "none" {
			writeDCRError(w, "invalid_client_metadata", "only token_endpoint_auth_method=none is supported", http.StatusBadRequest)
			return
		}

		for _, gt := range req.GrantTypes {
			if gt != "authorization_code" {
				writeDCRError(w, "invalid_client_metadata", "only grant_type=authorization_code is supported", http.StatusBadRequest)
				return
			}
		}

		// Apply RFC 7591 defaults
		if len(req.GrantTypes) == 0 {
			req.GrantTypes = []string{"authorization_code"}
		}
		if len(req.ResponseTypes) == 0 {
			req.ResponseTypes = []string{"code"}
		}
		if req.TokenEndpointAuthMethod == "" {
			req.TokenEndpointAuthMethod = "none"
		}

		resp := &oauthex.ClientRegistrationResponse{
			ClientRegistrationMetadata: req,
			ClientID:                   uuid.New().String(),
			ClientIDIssuedAt:           time.Now(),
		}

		if store != nil {
			if err := store.SaveClient(r.Context(), resp); err != nil {
				writeDCRError(w, "server_error", "failed to save client registration", http.StatusInternalServerError)
				return
			}
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		if err := json.NewEncoder(w).Encode(resp); err != nil {
			slog.Default().Error("failed to write DCR response", slog.Any("error", err))
		}
	})
}

func writeDCRError(w http.ResponseWriter, code, description string, status int) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(&oauthex.ClientRegistrationError{
		ErrorCode:        code,
		ErrorDescription: description,
	})
}
