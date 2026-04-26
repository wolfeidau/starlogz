package store

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// EnsureProject creates the project if it does not exist and returns it.
// If it already exists the name is updated to match the provided value.
func (s *Store) EnsureProject(ctx context.Context, ownerID uuid.UUID, slug, name string) (*Project, error) {
	var idStr, ownerIDStr string
	p := &Project{}
	err := s.pool.QueryRow(ctx, `
		INSERT INTO projects (owner_id, slug, name)
		VALUES ($1, $2, $3)
		ON CONFLICT (owner_id, slug) DO UPDATE SET name = EXCLUDED.name
		RETURNING id, owner_id, slug, name, created_at`,
		ownerID, slug, name).Scan(&idStr, &ownerIDStr, &p.Slug, &p.Name, &p.CreatedAt)
	if err != nil {
		return nil, fmt.Errorf("ensure project: %w", err)
	}
	if p.ID, err = uuid.Parse(idStr); err != nil {
		return nil, fmt.Errorf("parse project id: %w", err)
	}
	if p.OwnerID, err = uuid.Parse(ownerIDStr); err != nil {
		return nil, fmt.Errorf("parse owner id: %w", err)
	}
	return p, nil
}

// GetProjectBySlug fetches a project by owner and slug.
// Returns ErrNotFound if no matching row exists.
func (s *Store) GetProjectBySlug(ctx context.Context, ownerID uuid.UUID, slug string) (*Project, error) {
	var idStr, ownerIDStr string
	p := &Project{}
	err := s.pool.QueryRow(ctx, `
		SELECT id, owner_id, slug, name, created_at
		FROM projects WHERE owner_id = $1 AND slug = $2`,
		ownerID, slug).Scan(&idStr, &ownerIDStr, &p.Slug, &p.Name, &p.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get project: %w", err)
	}
	if p.ID, err = uuid.Parse(idStr); err != nil {
		return nil, fmt.Errorf("parse project id: %w", err)
	}
	if p.OwnerID, err = uuid.Parse(ownerIDStr); err != nil {
		return nil, fmt.Errorf("parse owner id: %w", err)
	}
	return p, nil
}
