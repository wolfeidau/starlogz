package oidc

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

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
