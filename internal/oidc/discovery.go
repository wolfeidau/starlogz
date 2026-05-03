package oidc

import (
	"net/url"

	"github.com/modelcontextprotocol/go-sdk/oauthex"
)

func buildAuthServerMeta(base *url.URL) *oauthex.AuthServerMeta {
	return &oauthex.AuthServerMeta{
		Issuer:                            base.String(),
		AuthorizationEndpoint:             base.JoinPath("/oauth2/authorize").String(),
		TokenEndpoint:                     base.JoinPath("/oauth2/token").String(),
		JWKSURI:                           base.JoinPath("/.well-known/jwks").String(),
		RegistrationEndpoint:              base.JoinPath("/oauth2/register").String(),
		ResponseTypesSupported:            []string{"code"},
		GrantTypesSupported:               []string{"authorization_code", "refresh_token"},
		ScopesSupported:                   []string{"facts:read", "facts:write", "org:admin"},
		CodeChallengeMethodsSupported:     []string{"S256"},
		TokenEndpointAuthMethodsSupported: []string{"none"},
	}
}

func buildProtectedResourceMeta(base *url.URL) *oauthex.ProtectedResourceMetadata {
	return &oauthex.ProtectedResourceMetadata{
		Resource:               base.JoinPath("/mcp").String(),
		ResourceName:           "Starlogz MCP Server",
		AuthorizationServers:   []string{base.String()},
		ScopesSupported:        []string{"facts:read", "facts:write", "org:admin"},
		BearerMethodsSupported: []string{"header"},
	}
}
