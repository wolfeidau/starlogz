package store

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// UpsertUser creates or updates the user record from GitHub identity.
// Implements oidc.UserUpserter.
func (s *Store) UpsertUser(ctx context.Context, githubID int64, email, login string) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO users (github_id, email, login)
		VALUES ($1, $2, $3)
		ON CONFLICT (github_id) DO UPDATE
		    SET email      = EXCLUDED.email,
		        login      = EXCLUDED.login,
		        updated_at = now()`,
		githubID, email, login)
	if err != nil {
		return fmt.Errorf("upsert user: %w", err)
	}
	return nil
}

// GetUserByGitHubID looks up a user by GitHub numeric ID.
// Returns ErrNotFound if no matching row exists.
func (s *Store) GetUserByGitHubID(ctx context.Context, githubID int64) (*User, error) {
	var idStr string
	u := &User{}
	err := s.pool.QueryRow(ctx, `
		SELECT id, github_id, email, login, created_at, updated_at
		FROM users WHERE github_id = $1`,
		githubID).Scan(&idStr, &u.GitHubID, &u.Email, &u.Login, &u.CreatedAt, &u.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get user: %w", err)
	}
	if u.ID, err = uuid.Parse(idStr); err != nil {
		return nil, fmt.Errorf("parse user id: %w", err)
	}
	return u, nil
}
