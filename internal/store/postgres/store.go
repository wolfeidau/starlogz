package postgres

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/wolfeidau/starlogz/internal/store"
)

var _ store.Store = (*Store)(nil)

// Store wraps a pgxpool and exposes domain-level query methods.
type Store struct {
	pool *pgxpool.Pool
	enc  *store.Encryptor
}

// New connects to PostgreSQL and returns a Store. Call Migrate before first use.
func New(ctx context.Context, dsn string, enc *store.Encryptor) (*Store, error) {
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		return nil, fmt.Errorf("connect: %w", err)
	}
	return &Store{pool: pool, enc: enc}, nil
}

func (s *Store) SetEncryptor(enc *store.Encryptor) {
	s.enc = enc
}

// Close releases all pooled connections.
func (s *Store) Close() {
	s.pool.Close()
}

// Ping verifies the database connection is alive.
func (s *Store) Ping(ctx context.Context) error {
	return s.pool.Ping(ctx)
}

// Migrate runs all pending SQL migrations.
func (s *Store) Migrate(ctx context.Context, logger *slog.Logger) error {
	return RunMigrations(ctx, s.pool, logger)
}

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
func (s *Store) GetUserByGitHubID(ctx context.Context, githubID int64) (*store.User, error) {
	var idStr string
	u := &store.User{}
	err := s.pool.QueryRow(ctx, `
		SELECT id, github_id, email, login, created_at, updated_at
		FROM users WHERE github_id = $1`,
		githubID).Scan(&idStr, &u.GitHubID, &u.Email, &u.Login, &u.CreatedAt, &u.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, store.ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get user: %w", err)
	}
	if u.ID, err = uuid.Parse(idStr); err != nil {
		return nil, fmt.Errorf("parse user id: %w", err)
	}
	return u, nil
}

// EnsureProject creates the project if it does not exist and returns it.
// If it already exists the name is updated to match the provided value.
func (s *Store) EnsureProject(ctx context.Context, ownerID uuid.UUID, slug, name string) (*store.Project, error) {
	var idStr, ownerIDStr string
	p := &store.Project{}
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

// ListProjects returns all projects owned by the caller, ordered by name.
func (s *Store) ListProjects(ctx context.Context, ownerID uuid.UUID) ([]*store.Project, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id, owner_id, slug, name, created_at
		FROM projects WHERE owner_id = $1
		ORDER BY name`,
		ownerID)
	if err != nil {
		return nil, fmt.Errorf("list projects: %w", err)
	}
	defer rows.Close()
	var projects []*store.Project
	for rows.Next() {
		var idStr, ownerIDStr string
		p := &store.Project{}
		if err := rows.Scan(&idStr, &ownerIDStr, &p.Slug, &p.Name, &p.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan project: %w", err)
		}
		if p.ID, err = uuid.Parse(idStr); err != nil {
			return nil, fmt.Errorf("parse project id: %w", err)
		}
		if p.OwnerID, err = uuid.Parse(ownerIDStr); err != nil {
			return nil, fmt.Errorf("parse owner id: %w", err)
		}
		projects = append(projects, p)
	}
	return projects, rows.Err()
}

// GetProjectBySlug fetches a project by owner and slug.
// Returns ErrNotFound if no matching row exists.
func (s *Store) GetProjectBySlug(ctx context.Context, ownerID uuid.UUID, slug string) (*store.Project, error) {
	var idStr, ownerIDStr string
	p := &store.Project{}
	err := s.pool.QueryRow(ctx, `
		SELECT id, owner_id, slug, name, created_at
		FROM projects WHERE owner_id = $1 AND slug = $2`,
		ownerID, slug).Scan(&idStr, &ownerIDStr, &p.Slug, &p.Name, &p.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, store.ErrNotFound
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

// UpsertGrant inserts or replaces a grant row and lazily prunes expired grants
// for the same GitHub user within the same transaction.
func (s *Store) UpsertGrant(ctx context.Context, g store.Grant) error {
	encAccess, err := s.enc.Seal(g.AccessToken)
	if err != nil {
		return fmt.Errorf("encrypt access token: %w", err)
	}
	encRefresh, err := s.enc.Seal(g.RefreshToken)
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
func (s *Store) GetGrant(ctx context.Context, jti string) (*store.Grant, error) {
	var g store.Grant
	var encAccess, encRefresh []byte

	err := s.pool.QueryRow(ctx, `
		SELECT jti, github_id, access_token, refresh_token,
		       access_token_expiry, refresh_token_expiry, jwt_expiry, updated_at
		FROM grants WHERE jti = $1`, jti).
		Scan(&g.JTI, &g.GitHubID, &encAccess, &encRefresh,
			&g.AccessTokenExpiry, &g.RefreshTokenExpiry, &g.JWTExpiry, &g.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, store.ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get grant: %w", err)
	}

	g.AccessToken, err = s.enc.Open(encAccess)
	if err != nil {
		return nil, fmt.Errorf("decrypt access token: %w", err)
	}
	g.RefreshToken, err = s.enc.Open(encRefresh)
	if err != nil {
		return nil, fmt.Errorf("decrypt refresh token: %w", err)
	}

	return &g, nil
}

// WriteFact creates or updates a fact. If Key is set and a live fact with that key exists in the
// project it is updated in place; otherwise a new row is inserted.
func (s *Store) WriteFact(ctx context.Context, p store.WriteFactParams) (*store.Fact, error) {
	if p.Key != "" {
		f, err := s.updateFactByKey(ctx, p)
		if err == nil {
			return f, nil
		}
		if !errors.Is(err, store.ErrNotFound) {
			return nil, err
		}
	}
	return s.insertFact(ctx, p)
}

func (s *Store) updateFactByKey(ctx context.Context, p store.WriteFactParams) (*store.Fact, error) {
	row := s.pool.QueryRow(ctx, `
		UPDATE facts
		SET content = $3, tags = $4, updated_at = now()
		WHERE project_id = $1 AND key = $2 AND deleted_at IS NULL
		RETURNING id, project_id, COALESCE(key, ''), content, tags, source_type, created_by, created_at, updated_at`,
		p.ProjectID, p.Key, p.Content, p.Tags)
	return scanFact(row)
}

func (s *Store) insertFact(ctx context.Context, p store.WriteFactParams) (*store.Fact, error) {
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
func (s *Store) SearchFacts(ctx context.Context, projectID uuid.UUID, query string, tags []string, limit int) ([]*store.Fact, error) {
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
func (s *Store) ListFacts(ctx context.Context, projectID uuid.UUID, tag string, limit int) ([]*store.Fact, error) {
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
func (s *Store) ListTags(ctx context.Context, projectID uuid.UUID, limit int) ([]store.TagCount, error) {
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
	var tags []store.TagCount
	for rows.Next() {
		var tc store.TagCount
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
func (s *Store) UpdateFact(ctx context.Context, p store.UpdateFactParams) (*store.Fact, error) {
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
	if errors.Is(err, store.ErrNotFound) {
		return nil, store.ErrNotFound
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
		return store.ErrNotFound
	}
	return nil
}

func scanFact(row pgx.Row) (*store.Fact, error) {
	var idStr, projectIDStr, createdByStr string
	f := &store.Fact{}
	err := row.Scan(&idStr, &projectIDStr, &f.Key, &f.Content, &f.Tags, &f.SourceType, &createdByStr, &f.CreatedAt, &f.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, store.ErrNotFound
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

func scanFacts(rows pgx.Rows) ([]*store.Fact, error) {
	var facts []*store.Fact
	for rows.Next() {
		var idStr, projectIDStr, createdByStr string
		f := &store.Fact{}
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

// SaveOAuthClient persists a new OAuth2 client registration.
// Returns an error if a client with the same client_id already exists.
func (s *Store) SaveOAuthClient(ctx context.Context, c store.OAuthClient) error {
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
