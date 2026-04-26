package store

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// WriteFact creates or updates a fact. If Key is set and a live fact with that key exists in the
// project it is updated in place; otherwise a new row is inserted.
func (s *Store) WriteFact(ctx context.Context, p WriteFactParams) (*Fact, error) {
	if p.Key != "" {
		f, err := s.updateFactByKey(ctx, p)
		if err == nil {
			return f, nil
		}
		if !errors.Is(err, ErrNotFound) {
			return nil, err
		}
	}
	return s.insertFact(ctx, p)
}

func (s *Store) updateFactByKey(ctx context.Context, p WriteFactParams) (*Fact, error) {
	row := s.pool.QueryRow(ctx, `
		UPDATE facts
		SET content = $3, tags = $4, updated_at = now()
		WHERE project_id = $1 AND key = $2 AND deleted_at IS NULL
		RETURNING id, project_id, COALESCE(key, ''), content, tags, source_type, created_by, created_at, updated_at`,
		p.ProjectID, p.Key, p.Content, p.Tags)
	return scanFact(row)
}

func (s *Store) insertFact(ctx context.Context, p WriteFactParams) (*Fact, error) {
	tags := p.Tags
	if tags == nil {
		tags = []string{}
	}
	row := s.pool.QueryRow(ctx, `
		INSERT INTO facts (project_id, key, content, tags, source_type, created_by)
		VALUES ($1, NULLIF($2, ''), $3, $4, $5, $6)
		RETURNING id, project_id, COALESCE(key, ''), content, tags, source_type, created_by, created_at, updated_at`,
		p.ProjectID, p.Key, p.Content, tags, p.SourceType, p.CreatedBy)
	return scanFact(row)
}

// SearchFacts runs a full-text search over live facts in a project.
func (s *Store) SearchFacts(ctx context.Context, projectID uuid.UUID, query string, tags []string, limit int) ([]*Fact, error) {
	var rows pgx.Rows
	var err error
	if len(tags) > 0 {
		rows, err = s.pool.Query(ctx, `
			SELECT id, project_id, COALESCE(key, ''), content, tags, source_type, created_by, created_at, updated_at
			FROM facts
			WHERE project_id = $1
			  AND deleted_at IS NULL
			  AND search_vector @@ plainto_tsquery('english', $2)
			  AND tags @> $4
			ORDER BY ts_rank(search_vector, plainto_tsquery('english', $2)) DESC
			LIMIT $3`,
			projectID, query, limit, tags)
	} else {
		rows, err = s.pool.Query(ctx, `
			SELECT id, project_id, COALESCE(key, ''), content, tags, source_type, created_by, created_at, updated_at
			FROM facts
			WHERE project_id = $1
			  AND deleted_at IS NULL
			  AND search_vector @@ plainto_tsquery('english', $2)
			ORDER BY ts_rank(search_vector, plainto_tsquery('english', $2)) DESC
			LIMIT $3`,
			projectID, query, limit)
	}
	if err != nil {
		return nil, fmt.Errorf("search facts: %w", err)
	}
	defer rows.Close()
	return scanFacts(rows)
}

// ListFacts returns live facts for a project ordered by most recently updated.
func (s *Store) ListFacts(ctx context.Context, projectID uuid.UUID, tag string, limit int) ([]*Fact, error) {
	var rows pgx.Rows
	var err error
	if tag != "" {
		rows, err = s.pool.Query(ctx, `
			SELECT id, project_id, COALESCE(key, ''), content, tags, source_type, created_by, created_at, updated_at
			FROM facts
			WHERE project_id = $1 AND deleted_at IS NULL AND tags @> ARRAY[$3::text]
			ORDER BY updated_at DESC
			LIMIT $2`,
			projectID, limit, tag)
	} else {
		rows, err = s.pool.Query(ctx, `
			SELECT id, project_id, COALESCE(key, ''), content, tags, source_type, created_by, created_at, updated_at
			FROM facts
			WHERE project_id = $1 AND deleted_at IS NULL
			ORDER BY updated_at DESC
			LIMIT $2`,
			projectID, limit)
	}
	if err != nil {
		return nil, fmt.Errorf("list facts: %w", err)
	}
	defer rows.Close()
	return scanFacts(rows)
}

// ListTags returns tags for a project ordered by usage frequency.
func (s *Store) ListTags(ctx context.Context, projectID uuid.UUID, limit int) ([]TagCount, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT unnest(tags) AS tag, count(*) AS cnt
		FROM facts
		WHERE project_id = $1 AND deleted_at IS NULL
		GROUP BY tag
		ORDER BY cnt DESC
		LIMIT $2`,
		projectID, limit)
	if err != nil {
		return nil, fmt.Errorf("list tags: %w", err)
	}
	defer rows.Close()
	var tags []TagCount
	for rows.Next() {
		var tc TagCount
		if err := rows.Scan(&tc.Name, &tc.Count); err != nil {
			return nil, fmt.Errorf("scan tag: %w", err)
		}
		tags = append(tags, tc)
	}
	return tags, rows.Err()
}

// UpdateFact patches content and/or tags on an existing live fact.
// Empty Content leaves content unchanged. Nil Tags leaves tags unchanged.
// Returns ErrNotFound if the fact does not exist or is already deleted.
func (s *Store) UpdateFact(ctx context.Context, p UpdateFactParams) (*Fact, error) {
	tags := p.Tags
	if tags == nil {
		tags = []string{}
	}
	row := s.pool.QueryRow(ctx, `
		UPDATE facts SET
		  content    = CASE WHEN $2 <> '' THEN $2 ELSE content END,
		  tags       = CASE WHEN $3 THEN $4 ELSE tags END,
		  updated_at = now()
		WHERE id = $1 AND deleted_at IS NULL
		RETURNING id, project_id, COALESCE(key, ''), content, tags, source_type, created_by, created_at, updated_at`,
		p.FactID, p.Content, p.Tags != nil, tags)
	f, err := scanFact(row)
	if errors.Is(err, ErrNotFound) {
		return nil, ErrNotFound
	}
	return f, err
}

// DeleteFact soft-deletes a fact. Returns ErrNotFound if it does not exist or is already deleted.
func (s *Store) DeleteFact(ctx context.Context, factID uuid.UUID) error {
	ct, err := s.pool.Exec(ctx, `
		UPDATE facts SET deleted_at = now() WHERE id = $1 AND deleted_at IS NULL`,
		factID)
	if err != nil {
		return fmt.Errorf("delete fact: %w", err)
	}
	if ct.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func scanFact(row pgx.Row) (*Fact, error) {
	var idStr, projectIDStr, createdByStr string
	f := &Fact{}
	err := row.Scan(&idStr, &projectIDStr, &f.Key, &f.Content, &f.Tags, &f.SourceType, &createdByStr, &f.CreatedAt, &f.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("scan fact: %w", err)
	}
	if f.ID, err = uuid.Parse(idStr); err != nil {
		return nil, fmt.Errorf("parse fact id: %w", err)
	}
	if f.ProjectID, err = uuid.Parse(projectIDStr); err != nil {
		return nil, fmt.Errorf("parse project id: %w", err)
	}
	if f.CreatedBy, err = uuid.Parse(createdByStr); err != nil {
		return nil, fmt.Errorf("parse created_by: %w", err)
	}
	return f, nil
}

func scanFacts(rows pgx.Rows) ([]*Fact, error) {
	var facts []*Fact
	for rows.Next() {
		var idStr, projectIDStr, createdByStr string
		f := &Fact{}
		if err := rows.Scan(&idStr, &projectIDStr, &f.Key, &f.Content, &f.Tags, &f.SourceType, &createdByStr, &f.CreatedAt, &f.UpdatedAt); err != nil {
			return nil, fmt.Errorf("scan fact: %w", err)
		}
		var err error
		if f.ID, err = uuid.Parse(idStr); err != nil {
			return nil, fmt.Errorf("parse fact id: %w", err)
		}
		if f.ProjectID, err = uuid.Parse(projectIDStr); err != nil {
			return nil, fmt.Errorf("parse project id: %w", err)
		}
		if f.CreatedBy, err = uuid.Parse(createdByStr); err != nil {
			return nil, fmt.Errorf("parse created_by: %w", err)
		}
		facts = append(facts, f)
	}
	return facts, rows.Err()
}
