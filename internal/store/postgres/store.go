package postgres

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

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

// Close releases all pooled connections.
func (s *Store) Close() {
	s.pool.Close()
}

// Ping verifies the database connection is alive.
func (s *Store) Ping(ctx context.Context) error {
	return s.pool.Ping(ctx)
}

// Migrate runs all pending SQL migrations under an advisory lock.
func (s *Store) Migrate(ctx context.Context, logger *slog.Logger) error {
	return RunMigrations(ctx, s.pool, logger)
}

// UpsertUser creates or updates a user from GitHub identity. On first login a personal org
// is created and the user is added as owner. Returns the user row.
func (s *Store) UpsertUser(ctx context.Context, githubID int64, email, login string) (*store.User, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var idStr string
	u := &store.User{}
	var created bool
	err = tx.QueryRow(ctx, `
		INSERT INTO users (github_id, email, login)
		VALUES ($1, $2, $3)
		ON CONFLICT (github_id) DO UPDATE
		    SET email      = EXCLUDED.email,
		        login      = EXCLUDED.login,
		        updated_at = now()
		RETURNING id, github_id, email, login, created_at, updated_at,
		          (xmax = 0) AS created`,
		githubID, email, login).
		Scan(&idStr, &u.GitHubID, &u.Email, &u.Login, &u.CreatedAt, &u.UpdatedAt, &created)
	if err != nil {
		return nil, fmt.Errorf("upsert user: %w", err)
	}
	if u.ID, err = uuid.Parse(idStr); err != nil {
		return nil, fmt.Errorf("parse user id: %w", err)
	}

	if created {
		// Personal org slug is the GitHub login used as a display name only.
		// Uniqueness is not enforced for personal orgs — they are resolved via
		// user ID, never by slug lookup.
		var orgIDStr string
		err = tx.QueryRow(ctx, `
			INSERT INTO orgs (slug, name, kind)
			VALUES ($1, $2, 'personal')
			RETURNING id`,
			login, login).Scan(&orgIDStr)
		if err != nil {
			return nil, fmt.Errorf("insert personal org: %w", err)
		}

		orgID, parseErr := uuid.Parse(orgIDStr)
		if parseErr != nil {
			return nil, fmt.Errorf("parse org id: %w", parseErr)
		}

		_, err = tx.Exec(ctx, `
			INSERT INTO org_members (org_id, user_id, role)
			VALUES ($1, $2, 'owner')`,
			orgID, u.ID)
		if err != nil {
			return nil, fmt.Errorf("insert org member: %w", err)
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit: %w", err)
	}
	return u, nil
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
	var parseErr error
	if u.ID, parseErr = uuid.Parse(idStr); parseErr != nil {
		return nil, fmt.Errorf("parse user id: %w", parseErr)
	}
	return u, nil
}

// GetUserByID looks up a user by internal UUID.
// Returns ErrNotFound if no matching row exists.
func (s *Store) GetUserByID(ctx context.Context, id uuid.UUID) (*store.User, error) {
	u := &store.User{}
	var idStr string
	err := s.pool.QueryRow(ctx, `
		SELECT id, github_id, email, login, created_at, updated_at
		FROM users WHERE id = $1`,
		id).Scan(&idStr, &u.GitHubID, &u.Email, &u.Login, &u.CreatedAt, &u.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, store.ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get user by id: %w", err)
	}
	var parseErr error
	if u.ID, parseErr = uuid.Parse(idStr); parseErr != nil {
		return nil, fmt.Errorf("parse user id: %w", parseErr)
	}
	return u, nil
}

// GetPersonalOrgByUserID returns the personal org for the given user.
// Returns ErrNotFound if the user has no personal org.
func (s *Store) GetPersonalOrgByUserID(ctx context.Context, userID uuid.UUID) (*store.Org, error) {
	o := &store.Org{}
	var idStr string
	err := s.pool.QueryRow(ctx, `
		SELECT o.id, o.slug, o.name, o.kind, o.created_at
		FROM orgs o
		JOIN org_members om ON om.org_id = o.id
		WHERE om.user_id = $1 AND o.kind = 'personal'`,
		userID).Scan(&idStr, &o.Slug, &o.Name, &o.Kind, &o.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, store.ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get personal org: %w", err)
	}
	var parseErr error
	if o.ID, parseErr = uuid.Parse(idStr); parseErr != nil {
		return nil, fmt.Errorf("parse org id: %w", parseErr)
	}
	return o, nil
}

// EnsureProject creates the project if it does not exist and returns it.
// If it already exists the name is updated to match the provided value.
func (s *Store) EnsureProject(ctx context.Context, orgID, createdBy uuid.UUID, slug, name string) (*store.Project, error) {
	p := &store.Project{}
	var idStr, orgIDStr, createdByStr string
	err := s.pool.QueryRow(ctx, `
		INSERT INTO projects (org_id, created_by, slug, name)
		VALUES ($1, $2, $3, $4)
		ON CONFLICT (org_id, slug) DO UPDATE SET name = EXCLUDED.name
		RETURNING id, org_id, created_by, slug, name, created_at`,
		orgID, createdBy, slug, name).
		Scan(&idStr, &orgIDStr, &createdByStr, &p.Slug, &p.Name, &p.CreatedAt)
	if err != nil {
		return nil, fmt.Errorf("ensure project: %w", err)
	}
	var parseErr error
	if p.ID, parseErr = uuid.Parse(idStr); parseErr != nil {
		return nil, fmt.Errorf("parse project id: %w", parseErr)
	}
	if p.OrgID, parseErr = uuid.Parse(orgIDStr); parseErr != nil {
		return nil, fmt.Errorf("parse org id: %w", parseErr)
	}
	if p.CreatedBy, parseErr = uuid.Parse(createdByStr); parseErr != nil {
		return nil, fmt.Errorf("parse created_by: %w", parseErr)
	}
	return p, nil
}

// ListProjects returns all projects in the org, ordered by name.
func (s *Store) ListProjects(ctx context.Context, orgID uuid.UUID) ([]*store.Project, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id, org_id, created_by, slug, name, created_at
		FROM projects WHERE org_id = $1
		ORDER BY name`,
		orgID)
	if err != nil {
		return nil, fmt.Errorf("list projects: %w", err)
	}
	defer rows.Close()
	var projects []*store.Project
	for rows.Next() {
		p := &store.Project{}
		var idStr, orgIDStr, createdByStr string
		if err := rows.Scan(&idStr, &orgIDStr, &createdByStr, &p.Slug, &p.Name, &p.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan project: %w", err)
		}
		var parseErr error
		if p.ID, parseErr = uuid.Parse(idStr); parseErr != nil {
			return nil, fmt.Errorf("parse project id: %w", parseErr)
		}
		if p.OrgID, parseErr = uuid.Parse(orgIDStr); parseErr != nil {
			return nil, fmt.Errorf("parse org id: %w", parseErr)
		}
		if p.CreatedBy, parseErr = uuid.Parse(createdByStr); parseErr != nil {
			return nil, fmt.Errorf("parse created_by: %w", parseErr)
		}
		projects = append(projects, p)
	}
	return projects, rows.Err()
}

// GetProjectBySlug fetches a project by org and slug.
// Returns ErrNotFound if no matching row exists.
func (s *Store) GetProjectBySlug(ctx context.Context, orgID uuid.UUID, slug string) (*store.Project, error) {
	p := &store.Project{}
	var idStr, orgIDStr, createdByStr string
	err := s.pool.QueryRow(ctx, `
		SELECT id, org_id, created_by, slug, name, created_at
		FROM projects WHERE org_id = $1 AND slug = $2`,
		orgID, slug).Scan(&idStr, &orgIDStr, &createdByStr, &p.Slug, &p.Name, &p.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, store.ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get project: %w", err)
	}
	var parseErr error
	if p.ID, parseErr = uuid.Parse(idStr); parseErr != nil {
		return nil, fmt.Errorf("parse project id: %w", parseErr)
	}
	if p.OrgID, parseErr = uuid.Parse(orgIDStr); parseErr != nil {
		return nil, fmt.Errorf("parse org id: %w", parseErr)
	}
	if p.CreatedBy, parseErr = uuid.Parse(createdByStr); parseErr != nil {
		return nil, fmt.Errorf("parse created_by: %w", parseErr)
	}
	return p, nil
}

// UpsertGrant inserts or replaces a grant row and lazily prunes expired grants
// for the same GitHub user within the same transaction.
func (s *Store) UpsertGrant(ctx context.Context, g store.Grant) error {
	if s.enc == nil {
		return fmt.Errorf("encryption key not configured")
	}
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

	var ourRefreshToken *string
	if g.OurRefreshToken != "" {
		ourRefreshToken = &g.OurRefreshToken
	}
	var clientID *string
	if g.ClientID != "" {
		clientID = &g.ClientID
	}

	_, err = tx.Exec(ctx, `
		INSERT INTO grants (jti, github_id, our_refresh_token, client_id,
		                    access_token, refresh_token,
		                    access_token_expiry, refresh_token_expiry, jwt_expiry)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
		ON CONFLICT (jti) DO UPDATE
		    SET our_refresh_token     = EXCLUDED.our_refresh_token,
		        client_id             = EXCLUDED.client_id,
		        access_token          = EXCLUDED.access_token,
		        refresh_token         = EXCLUDED.refresh_token,
		        access_token_expiry   = EXCLUDED.access_token_expiry,
		        refresh_token_expiry  = EXCLUDED.refresh_token_expiry,
		        jwt_expiry            = EXCLUDED.jwt_expiry,
		        updated_at            = now()`,
		g.JTI, g.GitHubID, ourRefreshToken, clientID, encAccess, encRefresh,
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
	if s.enc == nil {
		return nil, fmt.Errorf("encryption key not configured")
	}
	return s.scanGrant(s.pool.QueryRow(ctx, `
		SELECT jti, github_id, COALESCE(our_refresh_token,''), COALESCE(client_id,''),
		       access_token, refresh_token,
		       access_token_expiry, refresh_token_expiry, jwt_expiry, updated_at
		FROM grants WHERE jti = $1`, jti))
}

// GetGrantByRefreshToken fetches and decrypts a grant by our_refresh_token.
func (s *Store) GetGrantByRefreshToken(ctx context.Context, token string) (*store.Grant, error) {
	if s.enc == nil {
		return nil, fmt.Errorf("encryption key not configured")
	}
	return s.scanGrant(s.pool.QueryRow(ctx, `
		SELECT jti, github_id, COALESCE(our_refresh_token,''), COALESCE(client_id,''),
		       access_token, refresh_token,
		       access_token_expiry, refresh_token_expiry, jwt_expiry, updated_at
		FROM grants WHERE our_refresh_token = $1`, token))
}

func (s *Store) scanGrant(row pgx.Row) (*store.Grant, error) {
	var g store.Grant
	var encAccess, encRefresh []byte
	err := row.Scan(&g.JTI, &g.GitHubID, &g.OurRefreshToken, &g.ClientID,
		&encAccess, &encRefresh,
		&g.AccessTokenExpiry, &g.RefreshTokenExpiry, &g.JWTExpiry, &g.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, store.ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("scan grant: %w", err)
	}

	var decErr error
	g.AccessToken, decErr = s.enc.Open(encAccess)
	if decErr != nil {
		return nil, fmt.Errorf("decrypt access token: %w", decErr)
	}
	g.RefreshToken, decErr = s.enc.Open(encRefresh)
	if decErr != nil {
		return nil, fmt.Errorf("decrypt refresh token: %w", decErr)
	}
	return &g, nil
}

// RotateGrant atomically replaces a grant row identified by oldToken with the new values in g.
// Returns ErrNotFound if oldToken does not match any row (concurrent rotation race).
func (s *Store) RotateGrant(ctx context.Context, oldToken string, g store.Grant) (*store.Grant, error) {
	if s.enc == nil {
		return nil, fmt.Errorf("encryption key not configured")
	}
	encAccess, err := s.enc.Seal(g.AccessToken)
	if err != nil {
		return nil, fmt.Errorf("encrypt access token: %w", err)
	}
	encRefresh, err := s.enc.Seal(g.RefreshToken)
	if err != nil {
		return nil, fmt.Errorf("encrypt refresh token: %w", err)
	}

	var ourRefreshToken *string
	if g.OurRefreshToken != "" {
		ourRefreshToken = &g.OurRefreshToken
	}
	var clientID *string
	if g.ClientID != "" {
		clientID = &g.ClientID
	}

	var updated store.Grant
	var encA, encR []byte
	err = s.pool.QueryRow(ctx, `
		UPDATE grants
		SET jti                  = $2,
		    our_refresh_token    = $3,
		    client_id            = $4,
		    access_token         = $5,
		    refresh_token        = $6,
		    access_token_expiry  = $7,
		    refresh_token_expiry = $8,
		    jwt_expiry           = $9,
		    updated_at           = now()
		WHERE our_refresh_token = $1
		RETURNING jti, github_id, COALESCE(our_refresh_token,''), COALESCE(client_id,''),
		          access_token, refresh_token,
		          access_token_expiry, refresh_token_expiry, jwt_expiry, updated_at`,
		oldToken, g.JTI, ourRefreshToken, clientID,
		encAccess, encRefresh, g.AccessTokenExpiry, g.RefreshTokenExpiry, g.JWTExpiry,
	).Scan(&updated.JTI, &updated.GitHubID, &updated.OurRefreshToken, &updated.ClientID,
		&encA, &encR,
		&updated.AccessTokenExpiry, &updated.RefreshTokenExpiry, &updated.JWTExpiry, &updated.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, store.ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("rotate grant: %w", err)
	}

	updated.AccessToken, err = s.enc.Open(encA)
	if err != nil {
		return nil, fmt.Errorf("decrypt access token: %w", err)
	}
	updated.RefreshToken, err = s.enc.Open(encR)
	if err != nil {
		return nil, fmt.Errorf("decrypt refresh token: %w", err)
	}
	return &updated, nil
}

// DeleteGrant removes a grant row by jti.
func (s *Store) DeleteGrant(ctx context.Context, jti string) error {
	ct, err := s.pool.Exec(ctx, `DELETE FROM grants WHERE jti = $1`, jti)
	if err != nil {
		return fmt.Errorf("delete grant: %w", err)
	}
	if ct.RowsAffected() == 0 {
		return store.ErrNotFound
	}
	return nil
}

// StorePendingAuth persists an authorization state with a 10-minute TTL.
// Lazily prunes all expired rows in the same transaction.
func (s *Store) StorePendingAuth(ctx context.Context, state string, p store.PendingAuth) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	_, err = tx.Exec(ctx, `
		INSERT INTO pending_auths (state, client_id, redirect_uri, scope, code_challenge, client_state, expires_at)
		VALUES ($1, NULLIF($2,''), $3, $4, $5, NULLIF($6,''), now() + interval '10 minutes')`,
		state, p.ClientID, p.RedirectURI, p.Scope, p.CodeChallenge, p.ClientState)
	if err != nil {
		return fmt.Errorf("insert pending auth: %w", err)
	}

	_, err = tx.Exec(ctx, `DELETE FROM pending_auths WHERE expires_at < now()`)
	if err != nil {
		return fmt.Errorf("prune pending auths: %w", err)
	}

	return tx.Commit(ctx)
}

// ConsumePendingAuth atomically deletes and returns the pending auth for the given state.
// Returns ErrNotFound for unknown or expired states.
func (s *Store) ConsumePendingAuth(ctx context.Context, state string) (*store.PendingAuth, error) {
	p := &store.PendingAuth{}
	err := s.pool.QueryRow(ctx, `
		DELETE FROM pending_auths
		WHERE state = $1 AND expires_at > now()
		RETURNING COALESCE(client_id,''), redirect_uri, scope, code_challenge, COALESCE(client_state,'')`,
		state).Scan(&p.ClientID, &p.RedirectURI, &p.Scope, &p.CodeChallenge, &p.ClientState)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, store.ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("consume pending auth: %w", err)
	}
	return p, nil
}

// StoreAuthCode persists an authorization code with a 5-minute TTL.
// Lazily prunes all expired rows in the same transaction.
func (s *Store) StoreAuthCode(ctx context.Context, code string, c store.AuthCode) error {
	if s.enc == nil {
		return fmt.Errorf("encryption key not configured")
	}

	encAccess := []byte{}
	encRefresh := []byte{}
	var err error
	if c.AccessToken != "" {
		encAccess, err = s.enc.Seal(c.AccessToken)
		if err != nil {
			return fmt.Errorf("encrypt access token: %w", err)
		}
	}
	if c.RefreshToken != "" {
		encRefresh, err = s.enc.Seal(c.RefreshToken)
		if err != nil {
			return fmt.Errorf("encrypt refresh token: %w", err)
		}
	}

	var accessExpiry, refreshExpiry *time.Time
	if !c.AccessTokenExpiry.IsZero() {
		accessExpiry = &c.AccessTokenExpiry
	}
	if !c.RefreshTokenExpiry.IsZero() {
		refreshExpiry = &c.RefreshTokenExpiry
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	_, err = tx.Exec(ctx, `
		INSERT INTO auth_codes
		    (code, sub, github_id, email, scope, code_challenge, redirect_uri, client_id,
		     access_token, refresh_token, access_token_expiry, refresh_token_expiry, expires_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, NULLIF($8,''), $9, $10, $11, $12,
		        now() + interval '5 minutes')`,
		code, c.Sub, c.GitHubID, c.Email, c.Scope, c.CodeChallenge, c.RedirectURI, c.ClientID,
		encAccess, encRefresh, accessExpiry, refreshExpiry)
	if err != nil {
		return fmt.Errorf("insert auth code: %w", err)
	}

	_, err = tx.Exec(ctx, `DELETE FROM auth_codes WHERE expires_at < now()`)
	if err != nil {
		return fmt.Errorf("prune auth codes: %w", err)
	}

	return tx.Commit(ctx)
}

// ConsumeAuthCode atomically deletes and returns the auth code record.
// Returns ErrNotFound for unknown or expired codes.
func (s *Store) ConsumeAuthCode(ctx context.Context, code string) (*store.AuthCode, error) {
	if s.enc == nil {
		return nil, fmt.Errorf("encryption key not configured")
	}

	c := &store.AuthCode{}
	var encAccess, encRefresh []byte
	var clientID *string
	var accessExpiry, refreshExpiry *time.Time

	err := s.pool.QueryRow(ctx, `
		DELETE FROM auth_codes
		WHERE code = $1 AND expires_at > now()
		RETURNING sub, github_id, email, scope, code_challenge, redirect_uri,
		          COALESCE(client_id,''), access_token, refresh_token,
		          access_token_expiry, refresh_token_expiry`,
		code).Scan(&c.Sub, &c.GitHubID, &c.Email, &c.Scope, &c.CodeChallenge, &c.RedirectURI,
		&clientID, &encAccess, &encRefresh, &accessExpiry, &refreshExpiry)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, store.ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("consume auth code: %w", err)
	}
	if clientID != nil {
		c.ClientID = *clientID
	}
	if accessExpiry != nil {
		c.AccessTokenExpiry = *accessExpiry
	}
	if refreshExpiry != nil {
		c.RefreshTokenExpiry = *refreshExpiry
	}

	if len(encAccess) > 0 {
		c.AccessToken, err = s.enc.Open(encAccess)
		if err != nil {
			return nil, fmt.Errorf("decrypt access token: %w", err)
		}
	}
	if len(encRefresh) > 0 {
		c.RefreshToken, err = s.enc.Open(encRefresh)
		if err != nil {
			return nil, fmt.Errorf("decrypt refresh token: %w", err)
		}
	}
	return c, nil
}

// RevokeToken inserts a jti into the revoked_tokens table.
// Idempotent on duplicate jti. Lazily prunes expired rows.
func (s *Store) RevokeToken(ctx context.Context, jti string, expiresAt time.Time) error {
	_, err := s.pool.Exec(ctx, `
		WITH ins AS (
		    INSERT INTO revoked_tokens (jti, expires_at)
		    VALUES ($1, $2)
		    ON CONFLICT (jti) DO NOTHING
		)
		DELETE FROM revoked_tokens WHERE expires_at < now()`,
		jti, expiresAt)
	if err != nil {
		return fmt.Errorf("revoke token: %w", err)
	}
	return nil
}

// IsTokenRevoked returns true if the jti is present and not yet expired.
func (s *Store) IsTokenRevoked(ctx context.Context, jti string) (bool, error) {
	var exists bool
	err := s.pool.QueryRow(ctx,
		`SELECT EXISTS(SELECT 1 FROM revoked_tokens WHERE jti = $1 AND expires_at > now())`,
		jti).Scan(&exists)
	if err != nil {
		return false, fmt.Errorf("check revocation: %w", err)
	}
	return exists, nil
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

// UpdateFact patches content and/or tags on an existing live fact, scoped to orgID.
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
		  AND project_id IN (SELECT id FROM projects WHERE org_id = $5)
		RETURNING id, project_id, COALESCE(key, ''), content, tags, source_type, created_by, created_at, updated_at`,
		p.FactID, p.Content, p.Tags != nil, tags, p.OrgID)
	f, err := scanFact(row)
	if errors.Is(err, store.ErrNotFound) {
		return nil, store.ErrNotFound
	}
	return f, err
}

// DeleteFact soft-deletes a fact, scoped to orgID. Returns ErrNotFound if it does not exist, is already deleted, or belongs to a different org.
func (s *Store) DeleteFact(ctx context.Context, orgID, factID uuid.UUID) error {
	ct, err := s.pool.Exec(ctx, `
		UPDATE facts SET deleted_at = now()
		WHERE id = $1 AND deleted_at IS NULL
		  AND project_id IN (SELECT id FROM projects WHERE org_id = $2)`,
		factID, orgID)
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
	var parseErr error
	if f.ID, parseErr = uuid.Parse(idStr); parseErr != nil {
		return nil, fmt.Errorf("parse fact id: %w", parseErr)
	}
	if f.ProjectID, parseErr = uuid.Parse(projectIDStr); parseErr != nil {
		return nil, fmt.Errorf("parse project id: %w", parseErr)
	}
	if f.CreatedBy, parseErr = uuid.Parse(createdByStr); parseErr != nil {
		return nil, fmt.Errorf("parse created_by: %w", parseErr)
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
		var parseErr error
		if f.ID, parseErr = uuid.Parse(idStr); parseErr != nil {
			return nil, fmt.Errorf("parse fact id: %w", parseErr)
		}
		if f.ProjectID, parseErr = uuid.Parse(projectIDStr); parseErr != nil {
			return nil, fmt.Errorf("parse project id: %w", parseErr)
		}
		if f.CreatedBy, parseErr = uuid.Parse(createdByStr); parseErr != nil {
			return nil, fmt.Errorf("parse created_by: %w", parseErr)
		}
		facts = append(facts, f)
	}
	return facts, rows.Err()
}

// SaveClient persists a new OAuth2 client registration.
func (s *Store) SaveClient(ctx context.Context, c store.OAuthClient) error {
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
