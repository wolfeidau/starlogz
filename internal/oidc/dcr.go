package oidc

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/oauthex"
	"github.com/wolfeidau/starlogz/internal/store"
)

const (
	maxDCRBodyBytes            = 64 << 10
	maxRedirectURIs            = 10
	maxRedirectURILen          = 2048
	maxClientNameLen           = 256
	maxClientScopeLen          = 1024
	contentTypeApplicationJSON = "application/json"
	redirectHostLocalhost      = "localhost"
	redirectHostLoopbackIPv4   = "127.0.0.1"
	redirectHostLoopbackIPv6   = "::1"
	redirectSchemeHTTP         = "http"
	redirectSchemeHTTPS        = "https"
)

// ClientStore persists client registrations from Dynamic Client Registration.
// store.Store satisfies this interface directly.
type ClientStore interface {
	SaveClient(ctx context.Context, c store.OAuthClient) error
	GetClient(ctx context.Context, clientID string) (*store.OAuthClient, error)
	TouchClient(ctx context.Context, clientID string) error
}

// validateRedirectURIs enforces safe redirect URI policies for public MCP clients.
// Loopback HTTP is allowed for native apps; all other web callbacks require HTTPS.
func validateRedirectURIs(uris []string) error {
	if len(uris) > maxRedirectURIs {
		return fmt.Errorf("redirect_uris must contain at most %d entries", maxRedirectURIs)
	}
	for _, raw := range uris {
		if raw == "" || len(raw) > maxRedirectURILen {
			return fmt.Errorf("redirect_uri must be between 1 and %d bytes", maxRedirectURILen)
		}
		if strings.Contains(raw, "*") {
			return fmt.Errorf("redirect_uri must not contain wildcards: %q", raw)
		}
		u, err := url.Parse(raw)
		if err != nil {
			return fmt.Errorf("invalid redirect_uri %q: %w", raw, err)
		}
		if !u.IsAbs() || u.Scheme == "" {
			return fmt.Errorf("redirect_uri must be absolute: %q", raw)
		}
		if u.Fragment != "" {
			return fmt.Errorf("redirect_uri must not contain a fragment: %q", raw)
		}
		if u.User != nil {
			return fmt.Errorf("redirect_uri must not contain userinfo: %q", raw)
		}
		switch u.Scheme {
		case redirectSchemeHTTPS:
			if u.Hostname() == "" {
				return fmt.Errorf("https redirect_uri must contain a host: %q", raw)
			}
		case redirectSchemeHTTP:
			host := u.Hostname()
			if host != redirectHostLocalhost && host != redirectHostLoopbackIPv4 && host != redirectHostLoopbackIPv6 {
				return fmt.Errorf("http redirect_uri is only allowed for localhost: %q", raw)
			}
		default:
			if strings.EqualFold(u.Scheme, "javascript") || strings.EqualFold(u.Scheme, "data") {
				return fmt.Errorf("unsafe redirect_uri scheme: %q", raw)
			}
		}
	}
	return nil
}

func validateClientRegistrationMetadata(req *oauthex.ClientRegistrationMetadata) error {
	if len(req.RedirectURIs) == 0 {
		return fmt.Errorf("redirect_uris is required")
	}
	if err := validateRedirectURIs(req.RedirectURIs); err != nil {
		return err
	}
	if len(req.ClientName) > maxClientNameLen {
		return fmt.Errorf("client_name must be at most %d bytes", maxClientNameLen)
	}
	if len(req.Scope) > maxClientScopeLen {
		return fmt.Errorf("scope must be at most %d bytes", maxClientScopeLen)
	}
	if req.TokenEndpointAuthMethod != "" && req.TokenEndpointAuthMethod != tokenEndpointAuthMethodNone {
		return fmt.Errorf("only token_endpoint_auth_method=none is supported")
	}
	for _, grantType := range req.GrantTypes {
		if grantType != oauthGrantAuthorizationCode && grantType != oauthGrantRefreshToken {
			return fmt.Errorf("unsupported grant_type: %s", grantType)
		}
	}
	for _, responseType := range req.ResponseTypes {
		if responseType != oauthCode {
			return fmt.Errorf("unsupported response_type: %s", responseType)
		}
	}
	return nil
}

func decodeClientRegistration(w http.ResponseWriter, r *http.Request, dst *oauthex.ClientRegistrationMetadata) error {
	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mediaType != contentTypeApplicationJSON {
		return fmt.Errorf("Content-Type must be application/json")
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxDCRBodyBytes)
	decoder := json.NewDecoder(r.Body)
	if err := decoder.Decode(dst); err != nil {
		return fmt.Errorf("failed to parse request body: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return fmt.Errorf("request body must contain one JSON object")
	}
	return nil
}

func normalizeClientRegistrationMetadata(req *oauthex.ClientRegistrationMetadata) {
	req.GrantTypes = []string{oauthGrantAuthorizationCode, oauthGrantRefreshToken}
	req.ResponseTypes = []string{oauthCode}
	if req.TokenEndpointAuthMethod == "" {
		req.TokenEndpointAuthMethod = tokenEndpointAuthMethodNone
	}
}
