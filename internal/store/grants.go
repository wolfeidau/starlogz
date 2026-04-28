package store

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/jackc/pgx/v5"
	"golang.org/x/crypto/nacl/secretbox"
)

// Grant holds a single authorization grant with the associated GitHub App tokens.
// Tokens are stored encrypted at rest; this struct carries plaintext values.
type Grant struct {
	JTI                string
	GitHubID           int64
	AccessToken        string
	RefreshToken       string
	AccessTokenExpiry  time.Time
	RefreshTokenExpiry time.Time
	JWTExpiry          time.Time
	UpdatedAt          time.Time
}

// UpsertGrant inserts or replaces a grant row and lazily prunes expired grants
// for the same GitHub user within the same transaction.
func (s *Store) UpsertGrant(ctx context.Context, g Grant) error {
	encAccess, err := s.seal(g.AccessToken)
	if err != nil {
		return fmt.Errorf("encrypt access token: %w", err)
	}
	encRefresh, err := s.seal(g.RefreshToken)
	if err != nil {
		return fmt.Errorf("encrypt refresh token: %w", err)
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	_, err = tx.Exec(ctx, `
		INSERT INTO grants (jti, github_id, access_token, refresh_token,
		                    access_token_expiry, refresh_token_expiry, jwt_expiry)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
		ON CONFLICT (jti) DO UPDATE
		    SET access_token          = EXCLUDED.access_token,
		        refresh_token         = EXCLUDED.refresh_token,
		        access_token_expiry   = EXCLUDED.access_token_expiry,
		        refresh_token_expiry  = EXCLUDED.refresh_token_expiry,
		        jwt_expiry            = EXCLUDED.jwt_expiry,
		        updated_at            = now()`,
		g.JTI, g.GitHubID, encAccess, encRefresh,
		g.AccessTokenExpiry, g.RefreshTokenExpiry, g.JWTExpiry,
	)
	if err != nil {
		return fmt.Errorf("insert grant: %w", err)
	}

	_, err = tx.Exec(ctx,
		`DELETE FROM grants WHERE github_id = $1 AND jwt_expiry < now() AND jti != $2`,
		g.GitHubID, g.JTI,
	)
	if err != nil {
		return fmt.Errorf("prune grants: %w", err)
	}

	return tx.Commit(ctx)
}

// GetGrant fetches and decrypts a grant by JWT ID.
func (s *Store) GetGrant(ctx context.Context, jti string) (*Grant, error) {
	var g Grant
	var encAccess, encRefresh []byte

	err := s.pool.QueryRow(ctx, `
		SELECT jti, github_id, access_token, refresh_token,
		       access_token_expiry, refresh_token_expiry, jwt_expiry, updated_at
		FROM grants WHERE jti = $1`, jti).
		Scan(&g.JTI, &g.GitHubID, &encAccess, &encRefresh,
			&g.AccessTokenExpiry, &g.RefreshTokenExpiry, &g.JWTExpiry, &g.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get grant: %w", err)
	}

	g.AccessToken, err = s.open(encAccess)
	if err != nil {
		return nil, fmt.Errorf("decrypt access token: %w", err)
	}
	g.RefreshToken, err = s.open(encRefresh)
	if err != nil {
		return nil, fmt.Errorf("decrypt refresh token: %w", err)
	}

	return &g, nil
}

func (s *Store) seal(plaintext string) ([]byte, error) {
	if s.encKey == nil {
		return nil, fmt.Errorf("encryption key not configured")
	}
	var nonce [24]byte
	if _, err := io.ReadFull(rand.Reader, nonce[:]); err != nil {
		return nil, fmt.Errorf("generate nonce: %w", err)
	}
	return secretbox.Seal(nonce[:], []byte(plaintext), &nonce, s.encKey), nil
}

func (s *Store) open(encrypted []byte) (string, error) {
	if s.encKey == nil {
		return "", fmt.Errorf("encryption key not configured")
	}
	if len(encrypted) < 24 {
		return "", fmt.Errorf("ciphertext too short")
	}
	var nonce [24]byte
	copy(nonce[:], encrypted[:24])
	plaintext, ok := secretbox.Open(nil, encrypted[24:], &nonce, s.encKey)
	if !ok {
		return "", fmt.Errorf("decryption failed")
	}
	return string(plaintext), nil
}
