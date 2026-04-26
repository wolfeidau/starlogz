package store

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
)

// OAuthClient is a registered OAuth2 client stored in the database.
type OAuthClient struct {
	ID                      uuid.UUID
	ClientID                string
	ClientName              string
	RedirectURIs            []string
	GrantTypes              []string
	ResponseTypes           []string
	TokenEndpointAuthMethod string
	Scope                   string
	IssuedAt                time.Time
	ExpiresAt               time.Time
}

// SaveOAuthClient persists a new OAuth2 client registration.
// Returns an error if a client with the same client_id already exists.
func (s *Store) SaveOAuthClient(ctx context.Context, c OAuthClient) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO oauth_clients
			(client_id, client_name, redirect_uris, grant_types, response_types,
			 token_endpoint_auth_method, scope, issued_at, expires_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)`,
		c.ClientID, c.ClientName, c.RedirectURIs, c.GrantTypes, c.ResponseTypes,
		c.TokenEndpointAuthMethod, c.Scope, c.IssuedAt, c.ExpiresAt)
	if err != nil {
		return fmt.Errorf("save oauth client: %w", err)
	}
	return nil
}
