package oidc

import (
	"net/url"

	"github.com/modelcontextprotocol/go-sdk/oauthex"
)

const (
	oauthCode                    = "code"
	oauthGrantAuthorizationCode  = "authorization_code"
	oauthGrantRefreshToken       = "refresh_token"
	pkceMethodS256               = "S256"
	tokenEndpointAuthMethodNone  = "none"
	tokenResponseFieldAccess     = "access_token"
	tokenResponseFieldExpiresIn  = "expires_in"
	tokenResponseFieldRefresh    = "refresh_token"
	tokenResponseFieldRefreshTTL = "refresh_token_expires_in"
	tokenResponseFieldScope      = "scope"
	tokenResponseFieldTokenType  = "token_type"
)

func buildAuthServerMeta(base *url.URL, cimdSupported bool) *oauthex.AuthServerMeta {
	return &oauthex.AuthServerMeta{
		Issuer:                            base.String(),
		AuthorizationEndpoint:             base.JoinPath("/oauth2/authorize").String(),
		TokenEndpoint:                     base.JoinPath("/oauth2/token").String(),
		JWKSURI:                           base.JoinPath("/.well-known/jwks").String(),
		RegistrationEndpoint:              base.JoinPath("/oauth2/register").String(),
		ClientIDMetadataDocumentSupported: cimdSupported,
		ResponseTypesSupported:            []string{oauthCode},
		GrantTypesSupported:               []string{oauthGrantAuthorizationCode, oauthGrantRefreshToken},
		ScopesSupported:                   []string{scopeInsightsRead, scopeInsightsWrite, scopeOrgAdmin},
		CodeChallengeMethodsSupported:     []string{pkceMethodS256},
		TokenEndpointAuthMethodsSupported: []string{tokenEndpointAuthMethodNone},
	}
}

func buildProtectedResourceMeta(base *url.URL) *oauthex.ProtectedResourceMetadata {
	return &oauthex.ProtectedResourceMetadata{
		Resource:               base.JoinPath("/mcp").String(),
		ResourceName:           "Starlogz MCP Server",
		AuthorizationServers:   []string{base.String()},
		ScopesSupported:        []string{scopeInsightsRead, scopeInsightsWrite, scopeOrgAdmin},
		BearerMethodsSupported: []string{"header"},
	}
}
