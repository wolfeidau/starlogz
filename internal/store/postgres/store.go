package postgres

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/wolfeidau/starlogz/internal/ctxlog"
	"github.com/wolfeidau/starlogz/internal/insightlinks"
	"github.com/wolfeidau/starlogz/internal/store"
)

var _ store.Store = (*Store)(nil)

// dbtx is satisfied by both *pgxpool.Pool and pgx.Tx, letting query helpers
// run either directly against the pool or within an explicit transaction.
type dbtx interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// Store wraps a pgxpool and exposes domain-level query methods.
type Store struct {
	pool *pgxpool.Pool
	enc  *store.Encryptor
}

// New connects to PostgreSQL and returns a Store. Call Migrate before first use.
func New(ctx context.Context, dsn string, enc *store.Encryptor) (*Store, error) {
	config, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}

	pool, err := pgxpool.NewWithConfig(ctx, config)
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
func (s *Store) UpsertUser(ctx context.Context, profile store.GitHubProfile) (*store.User, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var idStr string
	u := &store.User{}
	var created bool
	err = tx.QueryRow(ctx, `
		INSERT INTO users (github_id, email, login, display_name, avatar_url, profile_url, bio)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
		ON CONFLICT (github_id) DO UPDATE
		    SET email              = EXCLUDED.email,
		        login              = EXCLUDED.login,
		        display_name       = EXCLUDED.display_name,
		        avatar_url         = EXCLUDED.avatar_url,
		        profile_url        = EXCLUDED.profile_url,
		        bio                = EXCLUDED.bio,
		        profile_updated_at = now(),
		        updated_at         = now()
		RETURNING id, github_id, email, login, display_name, avatar_url, profile_url, bio,
		          profile_updated_at, created_at, updated_at,
		          (xmax = 0) AS created`,
		profile.GitHubID, profile.Email, profile.Login, profile.DisplayName,
		profile.AvatarURL, profile.ProfileURL, profile.Bio).
		Scan(&idStr, &u.GitHubID, &u.Email, &u.Login, &u.DisplayName, &u.AvatarURL,
			&u.ProfileURL, &u.Bio, &u.ProfileUpdatedAt, &u.CreatedAt, &u.UpdatedAt, &created)
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
			profile.Login, profile.Login).Scan(&orgIDStr)
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
	s.logger(ctx).DebugContext(ctx, "getting user by github id", slog.Int64("github_id", githubID))

	var idStr string
	u := &store.User{}
	err := s.pool.QueryRow(ctx, `
		SELECT id, github_id, email, login, display_name, avatar_url, profile_url, bio,
		       profile_updated_at, created_at, updated_at
		FROM users WHERE github_id = $1`,
		githubID).Scan(&idStr, &u.GitHubID, &u.Email, &u.Login, &u.DisplayName, &u.AvatarURL,
		&u.ProfileURL, &u.Bio, &u.ProfileUpdatedAt, &u.CreatedAt, &u.UpdatedAt)
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
	s.logger(ctx).DebugContext(ctx, "getting user by id", slog.String("id", id.String()))

	u := &store.User{}
	var idStr string
	err := s.pool.QueryRow(ctx, `
		SELECT id, github_id, email, login, display_name, avatar_url, profile_url, bio,
		       profile_updated_at, created_at, updated_at
		FROM users WHERE id = $1`,
		id).Scan(&idStr, &u.GitHubID, &u.Email, &u.Login, &u.DisplayName, &u.AvatarURL,
		&u.ProfileURL, &u.Bio, &u.ProfileUpdatedAt, &u.CreatedAt, &u.UpdatedAt)
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

func (s *Store) CreateWebSession(ctx context.Context, session store.WebSession) (*store.WebSession, error) {
	var idStr, userIDStr string
	var lastSeenAt any
	if !session.LastSeenAt.IsZero() {
		lastSeenAt = session.LastSeenAt
	}
	err := s.pool.QueryRow(ctx, `
		WITH pruned AS (
			DELETE FROM web_sessions
			WHERE revoked_at IS NOT NULL OR idle_expires_at <= now() OR expires_at <= now()
		)
		INSERT INTO web_sessions (token_hash, user_id, last_seen_at, idle_expires_at, expires_at)
		VALUES ($1, $2, COALESCE($3, now()), $4, $5)
		RETURNING id, token_hash, user_id, created_at, last_seen_at, idle_expires_at, expires_at`,
		session.TokenHash, session.UserID, lastSeenAt, session.IdleExpiresAt, session.ExpiresAt).
		Scan(&idStr, &session.TokenHash, &userIDStr, &session.CreatedAt, &session.LastSeenAt,
			&session.IdleExpiresAt, &session.ExpiresAt)
	if err != nil {
		return nil, fmt.Errorf("create web session: %w", err)
	}
	if session.ID, err = uuid.Parse(idStr); err != nil {
		return nil, fmt.Errorf("parse web session id: %w", err)
	}
	if session.UserID, err = uuid.Parse(userIDStr); err != nil {
		return nil, fmt.Errorf("parse web session user id: %w", err)
	}
	return &session, nil
}

func (s *Store) GetWebSessionByTokenHash(ctx context.Context, tokenHash []byte) (*store.WebSession, error) {
	var session store.WebSession
	var idStr, userIDStr string
	err := s.pool.QueryRow(ctx, `
		SELECT id, token_hash, user_id, created_at, last_seen_at, idle_expires_at, expires_at
		FROM web_sessions
		WHERE token_hash = $1 AND revoked_at IS NULL
		  AND idle_expires_at > now() AND expires_at > now()`, tokenHash).
		Scan(&idStr, &session.TokenHash, &userIDStr, &session.CreatedAt, &session.LastSeenAt,
			&session.IdleExpiresAt, &session.ExpiresAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, store.ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get web session: %w", err)
	}
	if session.ID, err = uuid.Parse(idStr); err != nil {
		return nil, fmt.Errorf("parse web session id: %w", err)
	}
	if session.UserID, err = uuid.Parse(userIDStr); err != nil {
		return nil, fmt.Errorf("parse web session user id: %w", err)
	}
	return &session, nil
}

func (s *Store) TouchWebSession(ctx context.Context, id uuid.UUID, lastSeenAt, idleExpiresAt time.Time) error {
	ct, err := s.pool.Exec(ctx, `
		UPDATE web_sessions
		SET last_seen_at = $2, idle_expires_at = LEAST($3, expires_at)
		WHERE id = $1 AND revoked_at IS NULL
		  AND idle_expires_at > now() AND expires_at > now()`, id, lastSeenAt, idleExpiresAt)
	if err != nil {
		return fmt.Errorf("touch web session: %w", err)
	}
	if ct.RowsAffected() == 0 {
		return store.ErrNotFound
	}
	return nil
}

func (s *Store) RevokeWebSessionByTokenHash(ctx context.Context, tokenHash []byte) error {
	ct, err := s.pool.Exec(ctx, `
		UPDATE web_sessions SET revoked_at = now()
		WHERE token_hash = $1 AND revoked_at IS NULL`, tokenHash)
	if err != nil {
		return fmt.Errorf("revoke web session: %w", err)
	}
	if ct.RowsAffected() == 0 {
		return store.ErrNotFound
	}
	return nil
}

// GetPersonalOrgByUserID returns the personal org for the given user.
// Returns ErrNotFound if the user has no personal org.
func (s *Store) GetPersonalOrgByUserID(ctx context.Context, userID uuid.UUID) (*store.Org, error) {
	s.logger(ctx).DebugContext(ctx, "getting personal org by user id", slog.String("user_id", userID.String()))

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

// ListOrgs returns every org, ordered by kind then name.
func (s *Store) ListOrgs(ctx context.Context) ([]*store.Org, error) {
	s.logger(ctx).DebugContext(ctx, "listing orgs")

	rows, err := s.pool.Query(ctx, `
		SELECT id, slug, name, kind, created_at
		FROM orgs
		ORDER BY kind, name`)
	if err != nil {
		return nil, fmt.Errorf("list orgs: %w", err)
	}
	defer rows.Close()
	var orgs []*store.Org
	for rows.Next() {
		o := &store.Org{}
		var idStr string
		if err := rows.Scan(&idStr, &o.Slug, &o.Name, &o.Kind, &o.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan org: %w", err)
		}
		var parseErr error
		if o.ID, parseErr = uuid.Parse(idStr); parseErr != nil {
			return nil, fmt.Errorf("parse org id: %w", parseErr)
		}
		orgs = append(orgs, o)
	}
	return orgs, rows.Err()
}

// EnsureProject creates the project if it does not exist and returns it.
// If it already exists the name is updated to match the provided value.
func (s *Store) EnsureProject(ctx context.Context, orgID, createdBy uuid.UUID, slug, name string) (*store.Project, error) {
	s.logger(ctx).DebugContext(ctx, "ensuring project", slog.String("org_id", orgID.String()), slog.String("created_by", createdBy.String()), slog.String("slug", slug), slog.String("name", name))
	return ensureProjectTx(ctx, s.pool, orgID, createdBy, slug, name)
}

func ensureProjectTx(ctx context.Context, db dbtx, orgID, createdBy uuid.UUID, slug, name string) (*store.Project, error) {
	p := &store.Project{}
	var idStr, orgIDStr, createdByStr string
	err := db.QueryRow(ctx, `
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
	s.logger(ctx).DebugContext(ctx, "listing projects", slog.String("org_id", orgID.String()))

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
	s.logger(ctx).DebugContext(ctx, "getting project by slug", slog.String("org_id", orgID.String()), slog.String("slug", slug))

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
	s.logger(ctx).DebugContext(ctx, "upserting grant")

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
		INSERT INTO grants (jti, user_id, our_refresh_token, client_id, scope,
		                    access_token, refresh_token,
		                    access_token_expiry, refresh_token_expiry, jwt_expiry)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
		ON CONFLICT (jti) DO UPDATE
		    SET our_refresh_token     = EXCLUDED.our_refresh_token,
		        client_id             = EXCLUDED.client_id,
		        scope                 = EXCLUDED.scope,
		        access_token          = EXCLUDED.access_token,
		        refresh_token         = EXCLUDED.refresh_token,
		        access_token_expiry   = EXCLUDED.access_token_expiry,
		        refresh_token_expiry  = EXCLUDED.refresh_token_expiry,
		        jwt_expiry            = EXCLUDED.jwt_expiry,
		        updated_at            = now()`,
		g.JTI, g.UserID, ourRefreshToken, clientID, g.Scope, encAccess, encRefresh,
		g.AccessTokenExpiry, g.RefreshTokenExpiry, g.JWTExpiry,
	)
	if err != nil {
		return fmt.Errorf("insert grant: %w", err)
	}

	_, err = tx.Exec(ctx,
		`DELETE FROM grants WHERE user_id = $1 AND client_id IS NOT DISTINCT FROM $2 AND jwt_expiry < now() AND jti != $3`,
		g.UserID, clientID, g.JTI,
	)
	if err != nil {
		return fmt.Errorf("prune grants: %w", err)
	}

	return tx.Commit(ctx)
}

// GetGrant fetches and decrypts a grant by JWT ID.
func (s *Store) GetGrant(ctx context.Context, jti string) (*store.Grant, error) {
	s.logger(ctx).DebugContext(ctx, "getting grant by jti", slog.String("jti", jti))

	if s.enc == nil {
		return nil, fmt.Errorf("encryption key not configured")
	}
	return s.scanGrant(s.pool.QueryRow(ctx, `
		SELECT jti, user_id, COALESCE(our_refresh_token,''), COALESCE(client_id,''), scope,
		       access_token, refresh_token,
		       access_token_expiry, refresh_token_expiry, jwt_expiry, updated_at
		FROM grants WHERE jti = $1`, jti))
}

// GetGrantByRefreshToken fetches and decrypts a grant by our_refresh_token.
func (s *Store) GetGrantByRefreshToken(ctx context.Context, token string) (*store.Grant, error) {
	s.logger(ctx).DebugContext(ctx, "getting grant by refresh token")

	if s.enc == nil {
		return nil, fmt.Errorf("encryption key not configured")
	}
	return s.scanGrant(s.pool.QueryRow(ctx, `
		SELECT jti, user_id, COALESCE(our_refresh_token,''), COALESCE(client_id,''), scope,
		       access_token, refresh_token,
		       access_token_expiry, refresh_token_expiry, jwt_expiry, updated_at
		FROM grants WHERE our_refresh_token = $1`, token))
}

// GetRetiredRefreshToken returns retained metadata for a consumed or deleted refresh token.
func (s *Store) GetRetiredRefreshToken(ctx context.Context, tokenHash []byte) (*store.RetiredRefreshToken, error) {
	s.logger(ctx).DebugContext(ctx, "getting retired refresh token")

	return scanRetiredRefreshToken(s.pool.QueryRow(ctx, `
		SELECT token_hash, reason, user_id, COALESCE(client_id,''), COALESCE(old_jti,''),
		       COALESCE(replacement_jti,''), grace_expires_at, retained_until, created_at
		FROM retired_refresh_tokens
		WHERE token_hash = $1 AND retained_until > now()`, tokenHash))
}

func (s *Store) scanGrant(row pgx.Row) (*store.Grant, error) {
	var g store.Grant
	var userIDStr string
	var encAccess, encRefresh []byte
	err := row.Scan(&g.JTI, &userIDStr, &g.OurRefreshToken, &g.ClientID, &g.Scope,
		&encAccess, &encRefresh,
		&g.AccessTokenExpiry, &g.RefreshTokenExpiry, &g.JWTExpiry, &g.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, store.ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("scan grant: %w", err)
	}
	if g.UserID, err = uuid.Parse(userIDStr); err != nil {
		return nil, fmt.Errorf("parse user_id: %w", err)
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

func scanRetiredRefreshToken(row pgx.Row) (*store.RetiredRefreshToken, error) {
	var rt store.RetiredRefreshToken
	var userIDStr *string
	var graceExpiresAt *time.Time
	err := row.Scan(&rt.TokenHash, &rt.Reason, &userIDStr, &rt.ClientID, &rt.OldJTI,
		&rt.ReplacementJTI, &graceExpiresAt, &rt.RetainedUntil, &rt.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, store.ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("scan retired refresh token: %w", err)
	}
	if userIDStr != nil {
		parsed, parseErr := uuid.Parse(*userIDStr)
		if parseErr != nil {
			return nil, fmt.Errorf("parse retired token user_id: %w", parseErr)
		}
		rt.UserID = parsed
	}
	if graceExpiresAt != nil {
		rt.GraceExpiresAt = *graceExpiresAt
	}
	return &rt, nil
}

func (s *Store) insertRetiredRefreshTokenTx(ctx context.Context, tx pgx.Tx, rt *store.RetiredRefreshToken) error {
	if rt == nil {
		return nil
	}
	if len(rt.TokenHash) == 0 {
		return fmt.Errorf("retired refresh token hash is required")
	}
	if rt.Reason == "" {
		return fmt.Errorf("retired refresh token reason is required")
	}
	retainedUntil := rt.RetainedUntil
	if retainedUntil.IsZero() {
		retainedUntil = time.Now().Add(24 * time.Hour)
	}
	var userID any
	if rt.UserID != uuid.Nil {
		userID = rt.UserID
	}
	var clientID any
	if rt.ClientID != "" {
		clientID = rt.ClientID
	}
	var oldJTI any
	if rt.OldJTI != "" {
		oldJTI = rt.OldJTI
	}
	var replacementJTI any
	if rt.ReplacementJTI != "" {
		replacementJTI = rt.ReplacementJTI
	}
	var graceExpiresAt any
	if !rt.GraceExpiresAt.IsZero() {
		graceExpiresAt = rt.GraceExpiresAt
	}

	_, err := tx.Exec(ctx, `
		INSERT INTO retired_refresh_tokens
		    (token_hash, reason, user_id, client_id, old_jti, replacement_jti, grace_expires_at, retained_until)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
		ON CONFLICT (token_hash) DO UPDATE
		    SET reason           = EXCLUDED.reason,
		        user_id          = EXCLUDED.user_id,
		        client_id        = EXCLUDED.client_id,
		        old_jti          = EXCLUDED.old_jti,
		        replacement_jti  = EXCLUDED.replacement_jti,
		        grace_expires_at = EXCLUDED.grace_expires_at,
		        retained_until   = EXCLUDED.retained_until`,
		rt.TokenHash, rt.Reason, userID, clientID, oldJTI, replacementJTI, graceExpiresAt, retainedUntil)
	if err != nil {
		return fmt.Errorf("insert retired refresh token: %w", err)
	}

	_, err = tx.Exec(ctx, `DELETE FROM retired_refresh_tokens WHERE retained_until < now()`)
	if err != nil {
		return fmt.Errorf("prune retired refresh tokens: %w", err)
	}
	return nil
}

// RotateGrant atomically replaces a grant row identified by oldToken and records
// the old jti as revoked in the same transaction. Returns ErrNotFound if oldToken
// does not match any row (concurrent rotation race).
func (s *Store) RotateGrant(ctx context.Context, oldToken, oldJTI string, oldJWTExpiry time.Time, g store.Grant, retired *store.RetiredRefreshToken) (*store.Grant, error) {
	s.logger(ctx).DebugContext(ctx, "rotating grant",
		slog.String("old_jti", oldJTI),
		slog.Time("old_jwt_expiry", oldJWTExpiry),
		slog.String("new_jti", g.JTI),
		slog.String("client_id", g.ClientID),
	)

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

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var updated store.Grant
	var updatedUserIDStr string
	var encA, encR []byte
	err = tx.QueryRow(ctx, `
		UPDATE grants
		SET jti                  = $2,
		    our_refresh_token    = $3,
		    client_id            = $4,
		    scope                = $5,
		    access_token         = $6,
		    refresh_token        = $7,
		    access_token_expiry  = $8,
		    refresh_token_expiry = $9,
		    jwt_expiry           = $10,
		    updated_at           = now()
		WHERE our_refresh_token = $1
		RETURNING jti, user_id, COALESCE(our_refresh_token,''), COALESCE(client_id,''), scope,
		          access_token, refresh_token,
		          access_token_expiry, refresh_token_expiry, jwt_expiry, updated_at`,
		oldToken, g.JTI, ourRefreshToken, clientID, g.Scope,
		encAccess, encRefresh, g.AccessTokenExpiry, g.RefreshTokenExpiry, g.JWTExpiry,
	).Scan(&updated.JTI, &updatedUserIDStr, &updated.OurRefreshToken, &updated.ClientID, &updated.Scope,
		&encA, &encR,
		&updated.AccessTokenExpiry, &updated.RefreshTokenExpiry, &updated.JWTExpiry, &updated.UpdatedAt)
	if err == nil {
		updated.UserID, err = uuid.Parse(updatedUserIDStr)
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, store.ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("rotate grant: %w", err)
	}

	if oldJTI != "" {
		_, err = tx.Exec(ctx, `
			INSERT INTO revoked_tokens (jti, expires_at)
			VALUES ($1, $2)
			ON CONFLICT (jti) DO NOTHING`,
			oldJTI, oldJWTExpiry)
		if err != nil {
			return nil, fmt.Errorf("revoke old jti: %w", err)
		}
	}

	if err := s.insertRetiredRefreshTokenTx(ctx, tx, retired); err != nil {
		return nil, err
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit rotate grant: %w", err)
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
func (s *Store) DeleteGrant(ctx context.Context, jti string, retired *store.RetiredRefreshToken) error {
	s.logger(ctx).DebugContext(ctx, "deleting grant by jti", slog.String("jti", jti))

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	ct, err := tx.Exec(ctx, `DELETE FROM grants WHERE jti = $1`, jti)
	if err != nil {
		return fmt.Errorf("delete grant: %w", err)
	}
	if ct.RowsAffected() == 0 {
		return store.ErrNotFound
	}
	if err := s.insertRetiredRefreshTokenTx(ctx, tx, retired); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// StorePendingAuth persists an authorization state with a 10-minute TTL.
// Lazily prunes all expired rows in the same transaction.
func (s *Store) StorePendingAuth(ctx context.Context, state string, p store.PendingAuth) error {
	s.logger(ctx).DebugContext(ctx, "storing pending auth")

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
	s.logger(ctx).DebugContext(ctx, "consuming pending auth")

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
	s.logger(ctx).DebugContext(ctx, "storing auth code")

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
	s.logger(ctx).DebugContext(ctx, "consuming auth code")

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
	s.logger(ctx).DebugContext(ctx, "revoking token", slog.Time("expires_at", expiresAt))

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
	s.logger(ctx).DebugContext(ctx, "token revocation check", slog.Bool("revoked", exists))
	return exists, nil
}

// WriteInsight creates or updates an insight. If Key is set and a live insight with that key exists in the
// project it is updated in place; otherwise a new row is inserted.
func (s *Store) WriteInsight(ctx context.Context, p store.WriteInsightParams) (*store.Insight, error) {
	s.logger(ctx).DebugContext(ctx, "writing insight", slog.String("project_id", p.ProjectID.String()), slog.String("key", p.Key), slog.String("category", p.Category), slog.String("source", p.Source), slog.String("created_by", p.CreatedBy.String()))
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	insight, err := writeInsightTx(ctx, tx, p)
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit tx: %w", err)
	}
	return insight, nil
}

// GetInsight returns one live insight and its immediate project-local relationships.
func (s *Store) GetInsight(ctx context.Context, p store.GetInsightParams) (*store.InsightDetail, error) {
	// Counts and bounded rows must come from one snapshot under concurrent writes.
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return nil, fmt.Errorf("begin get insight tx: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var row pgx.Row
	if p.InsightID != uuid.Nil {
		row = tx.QueryRow(ctx, `
			SELECT id, project_id, COALESCE(key, ''), content, tags, category, source, created_by, created_at, updated_at
			FROM insights
			WHERE project_id = $1 AND id = $2 AND deleted_at IS NULL`,
			p.ProjectID, p.InsightID)
	} else {
		row = tx.QueryRow(ctx, `
			SELECT id, project_id, COALESCE(key, ''), content, tags, category, source, created_by, created_at, updated_at
			FROM insights
			WHERE project_id = $1 AND key = $2 AND deleted_at IS NULL`,
			p.ProjectID, p.Key)
	}
	insight, err := scanInsight(row)
	if err != nil {
		return nil, err
	}

	detail := &store.InsightDetail{Insight: insight}
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM insight_links WHERE source_insight_id = $1`, insight.ID).Scan(&detail.LinkCount); err != nil {
		return nil, fmt.Errorf("count insight links: %w", err)
	}
	linkRows, err := tx.Query(ctx, `
		SELECT links.target_key, COALESCE(target.id::text, ''), COALESCE(target.category, ''), target.updated_at
		FROM insight_links links
		LEFT JOIN insights target
		  ON target.project_id = $2
		 AND target.key = links.target_key
		 AND target.deleted_at IS NULL
		WHERE links.source_insight_id = $1
		ORDER BY links.target_key COLLATE "C" ASC
		LIMIT $3`, insight.ID, p.ProjectID, p.RelationLimit)
	if err != nil {
		return nil, fmt.Errorf("query insight links: %w", err)
	}
	defer linkRows.Close()
	for linkRows.Next() {
		var link store.InsightLinkReference
		var id string
		var updatedAt *time.Time
		if err := linkRows.Scan(&link.TargetKey, &id, &link.Category, &updatedAt); err != nil {
			return nil, fmt.Errorf("scan insight link: %w", err)
		}
		if id != "" {
			link.ID, err = uuid.Parse(id)
			if err != nil {
				return nil, fmt.Errorf("parse insight link ID: %w", err)
			}
			link.Resolved = true
			link.UpdatedAt = *updatedAt
		}
		detail.Links = append(detail.Links, link)
	}
	if err := linkRows.Err(); err != nil {
		return nil, fmt.Errorf("iterate insight links: %w", err)
	}
	detail.LinksTruncated = detail.LinkCount > len(detail.Links)

	// Keyless insights cannot be link targets, so they cannot have backlinks.
	if insight.Key != "" {
		if err := tx.QueryRow(ctx, `
			SELECT count(*)
			FROM insight_links links
			JOIN insights source ON source.id = links.source_insight_id
			WHERE links.target_key = $1
			  AND source.project_id = $2
			  AND source.deleted_at IS NULL`, insight.Key, p.ProjectID).Scan(&detail.BacklinkCount); err != nil {
			return nil, fmt.Errorf("count insight backlinks: %w", err)
		}
		backlinkRows, err := tx.Query(ctx, `
			SELECT source.id, COALESCE(source.key, ''), source.category, source.updated_at
			FROM insight_links links
			JOIN insights source ON source.id = links.source_insight_id
			WHERE links.target_key = $1
			  AND source.project_id = $2
			  AND source.deleted_at IS NULL
			ORDER BY source.updated_at DESC, source.id DESC
			LIMIT $3`, insight.Key, p.ProjectID, p.RelationLimit)
		if err != nil {
			return nil, fmt.Errorf("query insight backlinks: %w", err)
		}
		defer backlinkRows.Close()
		for backlinkRows.Next() {
			var backlink store.InsightBacklink
			if err := backlinkRows.Scan(&backlink.ID, &backlink.Key, &backlink.Category, &backlink.UpdatedAt); err != nil {
				return nil, fmt.Errorf("scan insight backlink: %w", err)
			}
			detail.Backlinks = append(detail.Backlinks, backlink)
		}
		if err := backlinkRows.Err(); err != nil {
			return nil, fmt.Errorf("iterate insight backlinks: %w", err)
		}
		detail.BacklinksTruncated = detail.BacklinkCount > len(detail.Backlinks)
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit get insight tx: %w", err)
	}
	return detail, nil
}

func writeInsightTx(ctx context.Context, db dbtx, p store.WriteInsightParams) (*store.Insight, error) {
	var insight *store.Insight
	var err error
	if p.Key != "" {
		insight, err = updateInsightByKeyTx(ctx, db, p)
		if err == nil {
			return syncInsightContentTx(ctx, db, insight)
		}
		if !errors.Is(err, store.ErrNotFound) {
			return nil, err
		}
	}
	insight, err = insertInsightTx(ctx, db, p)
	if err != nil {
		return nil, err
	}
	return syncInsightContentTx(ctx, db, insight)
}

func syncInsightContentTx(ctx context.Context, db dbtx, insight *store.Insight) (*store.Insight, error) {
	targets := insightlinks.Targets(insight.Content)
	stored := targets[:0]
	for _, target := range targets {
		if target == insight.Key && insight.Key != "" {
			insight.LinkWarnings = append(insight.LinkWarnings, store.InsightLinkWarning{
				Code:      store.InsightLinkWarningSelf,
				TargetKey: target,
			})
			continue
		}
		stored = append(stored, target)
	}

	if err := syncInsightLinksTx(ctx, db, insight.ID, stored); err != nil {
		return nil, err
	}
	if len(stored) == 0 {
		return insight, nil
	}
	unresolved, err := unresolvedInsightLinksTx(ctx, db, insight.ID, insight.ProjectID)
	if err != nil {
		return nil, err
	}
	for _, target := range unresolved {
		insight.LinkWarnings = append(insight.LinkWarnings, store.InsightLinkWarning{
			Code:      store.InsightLinkWarningUnresolved,
			TargetKey: target,
		})
	}
	sort.Slice(insight.LinkWarnings, func(i, j int) bool {
		if insight.LinkWarnings[i].TargetKey == insight.LinkWarnings[j].TargetKey {
			return insight.LinkWarnings[i].Code < insight.LinkWarnings[j].Code
		}
		return insight.LinkWarnings[i].TargetKey < insight.LinkWarnings[j].TargetKey
	})
	return insight, nil
}

func syncInsightLinksTx(ctx context.Context, db dbtx, sourceID uuid.UUID, targets []string) error {
	_, err := db.Exec(ctx, `
		WITH desired AS (
		    SELECT unnest($2::text[]) AS target_key
		), deleted AS (
		    DELETE FROM insight_links
		    WHERE source_insight_id = $1
		      AND NOT EXISTS (
		          SELECT 1 FROM desired WHERE desired.target_key = insight_links.target_key
		      )
		)
		INSERT INTO insight_links (source_insight_id, target_key)
		SELECT $1, target_key FROM desired
		ON CONFLICT (source_insight_id, target_key) DO NOTHING`,
		sourceID, targets)
	if err != nil {
		return fmt.Errorf("sync insight links: %w", err)
	}
	return nil
}

func unresolvedInsightLinksTx(ctx context.Context, db dbtx, sourceID, projectID uuid.UUID) ([]string, error) {
	rows, err := db.Query(ctx, `
		SELECT links.target_key
		FROM insight_links links
		WHERE links.source_insight_id = $1
		  AND NOT EXISTS (
		      SELECT 1
		      FROM insights target
		      WHERE target.project_id = $2
		        AND target.key = links.target_key
		        AND target.deleted_at IS NULL
		  )
		ORDER BY links.target_key COLLATE "C" ASC`,
		sourceID, projectID)
	if err != nil {
		return nil, fmt.Errorf("query unresolved insight links: %w", err)
	}
	defer rows.Close()

	var targets []string
	for rows.Next() {
		var target string
		if err := rows.Scan(&target); err != nil {
			return nil, fmt.Errorf("scan unresolved insight link: %w", err)
		}
		targets = append(targets, target)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate unresolved insight links: %w", err)
	}
	return targets, nil
}

func updateInsightByKeyTx(ctx context.Context, db dbtx, p store.WriteInsightParams) (*store.Insight, error) {
	tags := p.Tags
	if tags == nil {
		tags = []string{}
	}
	category := p.Category
	if category == "" {
		category = "general"
	}
	source := p.Source
	if source == "" {
		source = "user"
	}
	row := db.QueryRow(ctx, `
		UPDATE insights
		SET content = $3, tags = $4, category = $5, source = $6, updated_at = now()
		WHERE project_id = $1 AND key = $2 AND deleted_at IS NULL
		RETURNING id, project_id, COALESCE(key, ''), content, tags, category, source, created_by, created_at, updated_at`,
		p.ProjectID, p.Key, p.Content, tags, category, source)
	return scanInsight(row)
}

func insertInsightTx(ctx context.Context, db dbtx, p store.WriteInsightParams) (*store.Insight, error) {
	tags := p.Tags
	if tags == nil {
		tags = []string{}
	}
	category := p.Category
	if category == "" {
		category = "general"
	}
	source := p.Source
	if source == "" {
		source = "user"
	}
	var createdAt, updatedAt *time.Time
	if !p.CreatedAt.IsZero() {
		createdAt = &p.CreatedAt
	}
	if !p.UpdatedAt.IsZero() {
		updatedAt = &p.UpdatedAt
	}
	row := db.QueryRow(ctx, `
		INSERT INTO insights (project_id, key, content, tags, category, source, created_by, created_at, updated_at)
		VALUES ($1, NULLIF($2, ''), $3, $4, $5, $6, $7, COALESCE($8, now()), COALESCE($9, now()))
		RETURNING id, project_id, COALESCE(key, ''), content, tags, category, source, created_by, created_at, updated_at`,
		p.ProjectID, p.Key, p.Content, tags, category, source, p.CreatedBy, createdAt, updatedAt)
	return scanInsight(row)
}

// ImportProjects ensures each project (by slug) exists under orgID and writes its insights,
// attributing everything to createdBy, all within a single transaction so a failure partway
// through — e.g. an invalid category on one insight — leaves no partial data behind.
func (s *Store) ImportProjects(ctx context.Context, orgID, createdBy uuid.UUID, projects []store.ImportProject) (projectCount, insightCount int, err error) {
	s.logger(ctx).DebugContext(ctx, "importing projects", slog.String("org_id", orgID.String()), slog.String("created_by", createdBy.String()), slog.Int("project_count", len(projects)))

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return 0, 0, fmt.Errorf("begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	for _, ip := range projects {
		project, err := ensureProjectTx(ctx, tx, orgID, createdBy, ip.Slug, ip.Name)
		if err != nil {
			return 0, 0, fmt.Errorf("ensure project %q: %w", ip.Slug, err)
		}

		for _, ii := range ip.Insights {
			_, err := writeInsightTx(ctx, tx, store.WriteInsightParams{
				ProjectID: project.ID,
				Key:       ii.Key,
				Content:   ii.Content,
				Tags:      ii.Tags,
				Category:  ii.Category,
				Source:    ii.Source,
				CreatedBy: createdBy,
				CreatedAt: ii.CreatedAt,
				UpdatedAt: ii.UpdatedAt,
			})
			if err != nil {
				return 0, 0, fmt.Errorf("write insight in project %q: %w", ip.Slug, err)
			}
			insightCount++
		}
		projectCount++
	}

	if err := tx.Commit(ctx); err != nil {
		return 0, 0, fmt.Errorf("commit tx: %w", err)
	}
	return projectCount, insightCount, nil
}

// SearchInsights runs a full-text search over live insights in a project.
func (s *Store) SearchInsights(ctx context.Context, projectID uuid.UUID, query string, queryMode store.SearchQueryMode, tags []string, tagMode store.SearchTagMode, limit int) ([]*store.Insight, error) {
	s.logger(ctx).DebugContext(ctx, "searching insights", slog.String("project_id", projectID.String()), slog.Int("query_length", len(query)), slog.String("query_mode", string(queryMode)), slog.Int("tag_count", len(tags)), slog.String("tag_mode", string(tagMode)), slog.Int("limit", limit))

	rows, err := s.pool.Query(ctx, `
		SELECT id, project_id, COALESCE(key, ''), content, tags, category, source, created_by, created_at, updated_at
		FROM insights
		WHERE project_id = $1
		  AND deleted_at IS NULL
		  AND search_vector @@ CASE
			WHEN $3 = 'web' THEN websearch_to_tsquery('english', $2)
			ELSE plainto_tsquery('english', $2)
		  END
		  AND (COALESCE(cardinality($4::text[]), 0) = 0 OR CASE
			WHEN $5 = 'any' THEN tags && $4
			ELSE tags @> $4
		  END)
		ORDER BY ts_rank(search_vector, CASE
			WHEN $3 = 'web' THEN websearch_to_tsquery('english', $2)
			ELSE plainto_tsquery('english', $2)
		END) DESC
		LIMIT $6`,
		projectID, query, queryMode, tags, tagMode, limit)
	if err != nil {
		return nil, fmt.Errorf("search insights: %w", err)
	}
	defer rows.Close()
	return scanInsights(rows)
}

func (s *Store) ListInsights(ctx context.Context, p store.ListInsightsParams) (*store.InsightPage, error) {
	s.logger(ctx).DebugContext(ctx, "listing insights", slog.String("project_id", p.ProjectID.String()), slog.Bool("tag_filter", p.Tag != ""), slog.Int("limit", p.Limit), slog.Bool("after_cursor", p.After != nil))

	if p.Limit <= 0 {
		return &store.InsightPage{}, nil
	}

	query := `
		SELECT id, project_id, COALESCE(key, ''), content, tags, category, source, created_by, created_at, updated_at
		FROM insights
		WHERE project_id = $1 AND deleted_at IS NULL`
	args := []any{p.ProjectID}
	if p.Tag != "" {
		args = append(args, p.Tag)
		query += fmt.Sprintf(" AND tags @> ARRAY[$%d::text]", len(args))
	}
	if p.After != nil {
		args = append(args, p.After.UpdatedAt, p.After.ID)
		query += fmt.Sprintf(" AND (updated_at, id) < ($%d, $%d)", len(args)-1, len(args))
	}
	args = append(args, p.Limit+1)
	query += fmt.Sprintf(" ORDER BY updated_at DESC, id DESC LIMIT $%d", len(args))

	rows, err := s.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list insights: %w", err)
	}
	defer rows.Close()

	insights, err := scanInsights(rows)
	if err != nil {
		return nil, err
	}
	page := &store.InsightPage{Insights: insights}
	if len(page.Insights) > p.Limit {
		page.Insights = page.Insights[:p.Limit]
		last := page.Insights[len(page.Insights)-1]
		page.NextCursor = &store.InsightListCursor{UpdatedAt: last.UpdatedAt, ID: last.ID}
	}
	return page, nil
}

// ListTags returns tags for a project ordered by usage frequency.
func (s *Store) ListTags(ctx context.Context, projectID uuid.UUID, limit int) ([]store.TagCount, error) {
	s.logger(ctx).DebugContext(ctx, "listing tags", slog.String("project_id", projectID.String()), slog.Int("limit", limit))

	rows, err := s.pool.Query(ctx, `
		SELECT unnest(tags) AS tag, count(*) AS cnt
		FROM insights
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

// GetProjectDashboard returns aggregate read models for the project dashboard.
func (s *Store) GetProjectDashboard(ctx context.Context, projectID uuid.UUID) (*store.ProjectDashboard, error) {
	s.logger(ctx).DebugContext(ctx, "loading project dashboard", slog.String("project_id", projectID.String()))

	out := &store.ProjectDashboard{}
	if err := s.pool.QueryRow(ctx, `
		SELECT count(*)
		FROM insights
		WHERE project_id = $1 AND deleted_at IS NULL`,
		projectID).Scan(&out.TotalInsights); err != nil {
		return nil, fmt.Errorf("count insights: %w", err)
	}

	var err error
	out.CategoryCounts, err = s.countBuckets(ctx, `
		SELECT category, count(*)
		FROM insights
		WHERE project_id = $1 AND deleted_at IS NULL
		GROUP BY category
		ORDER BY count(*) DESC, category`, projectID)
	if err != nil {
		return nil, fmt.Errorf("category counts: %w", err)
	}

	out.SourceCounts, err = s.countBuckets(ctx, `
		SELECT source, count(*)
		FROM insights
		WHERE project_id = $1 AND deleted_at IS NULL
		GROUP BY source
		ORDER BY count(*) DESC, source`, projectID)
	if err != nil {
		return nil, fmt.Errorf("source counts: %w", err)
	}

	out.TopTags, err = s.countBuckets(ctx, `
		SELECT unnest(tags) AS tag, count(*) AS cnt
		FROM insights
		WHERE project_id = $1 AND deleted_at IS NULL
		GROUP BY tag
		ORDER BY cnt DESC, tag
		LIMIT 12`, projectID)
	if err != nil {
		return nil, fmt.Errorf("top tags: %w", err)
	}

	out.RecentActivity, err = s.activityBuckets(ctx, projectID)
	if err != nil {
		return nil, fmt.Errorf("recent activity: %w", err)
	}

	recent, err := s.ListInsights(ctx, store.ListInsightsParams{ProjectID: projectID, Limit: 12})
	if err != nil {
		return nil, fmt.Errorf("recent insights: %w", err)
	}
	out.RecentInsights = recent.Insights

	return out, nil
}

func (s *Store) countBuckets(ctx context.Context, query string, projectID uuid.UUID) ([]store.CountBucket, error) {
	rows, err := s.pool.Query(ctx, query, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []store.CountBucket
	for rows.Next() {
		var b store.CountBucket
		if err := rows.Scan(&b.Name, &b.Count); err != nil {
			return nil, fmt.Errorf("scan count bucket: %w", err)
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

func (s *Store) activityBuckets(ctx context.Context, projectID uuid.UUID) ([]store.ActivityBucket, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT to_char(days.day::date, 'YYYY-MM-DD') AS day, COALESCE(counts.cnt, 0)::int
		FROM generate_series(current_date - interval '13 days', current_date, interval '1 day') AS days(day)
		LEFT JOIN (
		  SELECT date_trunc('day', updated_at)::date AS day, count(*) AS cnt
		  FROM insights
		  WHERE project_id = $1
		    AND deleted_at IS NULL
		    AND updated_at >= current_date - interval '13 days'
		  GROUP BY date_trunc('day', updated_at)::date
		) counts ON counts.day = days.day::date
		ORDER BY days.day`,
		projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []store.ActivityBucket
	for rows.Next() {
		var b store.ActivityBucket
		if err := rows.Scan(&b.Date, &b.Count); err != nil {
			return nil, fmt.Errorf("scan activity bucket: %w", err)
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

// UpdateInsight patches content and/or tags on an existing live insight, scoped to orgID.
func (s *Store) UpdateInsight(ctx context.Context, p store.UpdateInsightParams) (*store.Insight, error) {
	s.logger(ctx).DebugContext(ctx, "updating insight", slog.String("insight_id", p.InsightID.String()), slog.Int("tag_count", len(p.Tags)), slog.String("org_id", p.OrgID.String()))

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	tags := p.Tags
	if tags == nil {
		tags = []string{}
	}
	row := tx.QueryRow(ctx, `
		UPDATE insights SET
		  content    = CASE WHEN $2 <> '' THEN $2 ELSE content END,
		  tags       = CASE WHEN $3 THEN $4 ELSE tags END,
		  updated_at = now()
		WHERE id = $1 AND deleted_at IS NULL
		  AND project_id IN (SELECT id FROM projects WHERE org_id = $5)
		RETURNING id, project_id, COALESCE(key, ''), content, tags, category, source, created_by, created_at, updated_at`,
		p.InsightID, p.Content, p.Tags != nil, tags, p.OrgID)
	insight, err := scanInsight(row)
	if errors.Is(err, store.ErrNotFound) {
		return nil, store.ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	if p.Content != "" {
		insight, err = syncInsightContentTx(ctx, tx, insight)
		if err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit tx: %w", err)
	}
	return insight, nil
}

// DeleteInsight soft-deletes an insight, scoped to orgID. Returns ErrNotFound if it does not exist, is already deleted, or belongs to a different org.
func (s *Store) DeleteInsight(ctx context.Context, orgID, insightID uuid.UUID) error {
	s.logger(ctx).DebugContext(ctx, "deleting insight", slog.String("insight_id", insightID.String()), slog.String("org_id", orgID.String()))

	ct, err := s.pool.Exec(ctx, `
		UPDATE insights SET deleted_at = now()
		WHERE id = $1 AND deleted_at IS NULL
		  AND project_id IN (SELECT id FROM projects WHERE org_id = $2)`,
		insightID, orgID)
	if err != nil {
		return fmt.Errorf("delete insight: %w", err)
	}
	if ct.RowsAffected() == 0 {
		return store.ErrNotFound
	}
	return nil
}

func scanInsight(row pgx.Row) (*store.Insight, error) {
	var idStr, projectIDStr, createdByStr string
	f := &store.Insight{}
	err := row.Scan(&idStr, &projectIDStr, &f.Key, &f.Content, &f.Tags, &f.Category, &f.Source, &createdByStr, &f.CreatedAt, &f.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, store.ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("scan insight: %w", err)
	}
	var parseErr error
	if f.ID, parseErr = uuid.Parse(idStr); parseErr != nil {
		return nil, fmt.Errorf("parse insight id: %w", parseErr)
	}
	if f.ProjectID, parseErr = uuid.Parse(projectIDStr); parseErr != nil {
		return nil, fmt.Errorf("parse project id: %w", parseErr)
	}
	if f.CreatedBy, parseErr = uuid.Parse(createdByStr); parseErr != nil {
		return nil, fmt.Errorf("parse created_by: %w", parseErr)
	}
	return f, nil
}

func scanInsights(rows pgx.Rows) ([]*store.Insight, error) {
	var insights []*store.Insight
	for rows.Next() {
		var idStr, projectIDStr, createdByStr string
		f := &store.Insight{}
		if err := rows.Scan(&idStr, &projectIDStr, &f.Key, &f.Content, &f.Tags, &f.Category, &f.Source, &createdByStr, &f.CreatedAt, &f.UpdatedAt); err != nil {
			return nil, fmt.Errorf("scan insight: %w", err)
		}
		var parseErr error
		if f.ID, parseErr = uuid.Parse(idStr); parseErr != nil {
			return nil, fmt.Errorf("parse insight id: %w", parseErr)
		}
		if f.ProjectID, parseErr = uuid.Parse(projectIDStr); parseErr != nil {
			return nil, fmt.Errorf("parse project id: %w", parseErr)
		}
		if f.CreatedBy, parseErr = uuid.Parse(createdByStr); parseErr != nil {
			return nil, fmt.Errorf("parse created_by: %w", parseErr)
		}
		insights = append(insights, f)
	}
	return insights, rows.Err()
}

// SaveClient persists a new OAuth2 client registration.
func (s *Store) GetClient(ctx context.Context, clientID string) (*store.OAuthClient, error) {
	var c store.OAuthClient
	err := s.pool.QueryRow(ctx, `
		SELECT client_id, client_name, redirect_uris, grant_types, response_types,
		       token_endpoint_auth_method, scope, issued_at, last_used_at, expires_at
		FROM oauth_clients
		WHERE client_id = $1 AND (expires_at IS NULL OR expires_at > now())`,
		clientID).Scan(
		&c.ClientID, &c.ClientName, &c.RedirectURIs, &c.GrantTypes, &c.ResponseTypes,
		&c.TokenEndpointAuthMethod, &c.Scope, &c.IssuedAt, &c.LastUsedAt, &c.ExpiresAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, store.ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get oauth client: %w", err)
	}
	return &c, nil
}

func (s *Store) SaveClient(ctx context.Context, c store.OAuthClient) error {
	s.logger(ctx).DebugContext(ctx, "saving oauth client", slog.String("client_id", c.ClientID), slog.Int("redirect_uri_count", len(c.RedirectURIs)), slog.Int("grant_type_count", len(c.GrantTypes)), slog.Int("response_type_count", len(c.ResponseTypes)), slog.String("token_endpoint_auth_method", c.TokenEndpointAuthMethod), slog.Time("issued_at", c.IssuedAt), slog.Any("expires_at", c.ExpiresAt))

	lastUsedAt := c.LastUsedAt
	if lastUsedAt.IsZero() {
		lastUsedAt = c.IssuedAt
	}
	_, err := s.pool.Exec(ctx, `
		INSERT INTO oauth_clients
			(client_id, client_name, redirect_uris, grant_types, response_types,
			 token_endpoint_auth_method, scope, issued_at, last_used_at, expires_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)`,
		c.ClientID, c.ClientName, c.RedirectURIs, c.GrantTypes, c.ResponseTypes,
		c.TokenEndpointAuthMethod, c.Scope, c.IssuedAt, lastUsedAt, c.ExpiresAt)
	if err != nil {
		return fmt.Errorf("save oauth client: %w", err)
	}
	return nil
}

// UpsertClient refreshes first-party metadata without changing recorded activity.
func (s *Store) UpsertClient(ctx context.Context, c store.OAuthClient) error {
	s.logger(ctx).DebugContext(ctx, "upserting oauth client", slog.String("client_id", c.ClientID), slog.Int("redirect_uri_count", len(c.RedirectURIs)), slog.Any("expires_at", c.ExpiresAt))

	_, err := s.pool.Exec(ctx, `
		INSERT INTO oauth_clients
			(client_id, client_name, redirect_uris, grant_types, response_types,
			 token_endpoint_auth_method, scope, issued_at, last_used_at, expires_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
		ON CONFLICT (client_id) DO UPDATE SET
			client_name = EXCLUDED.client_name,
			redirect_uris = EXCLUDED.redirect_uris,
			grant_types = EXCLUDED.grant_types,
			response_types = EXCLUDED.response_types,
			token_endpoint_auth_method = EXCLUDED.token_endpoint_auth_method,
			scope = EXCLUDED.scope,
			expires_at = EXCLUDED.expires_at`,
		c.ClientID, c.ClientName, c.RedirectURIs, c.GrantTypes, c.ResponseTypes,
		c.TokenEndpointAuthMethod, c.Scope, c.IssuedAt, c.IssuedAt, c.ExpiresAt)
	if err != nil {
		return fmt.Errorf("upsert oauth client: %w", err)
	}
	return nil
}

// TouchClient records recent use without writing more than once per day.
func (s *Store) TouchClient(ctx context.Context, clientID string) error {
	_, err := s.pool.Exec(ctx, `
		UPDATE oauth_clients
		SET last_used_at = now()
		WHERE client_id = $1
		  AND last_used_at < now() - interval '24 hours'`, clientID)
	if err != nil {
		return fmt.Errorf("touch oauth client: %w", err)
	}
	return nil
}

// ListAuditLog returns audit_log entries newest-first, with optional filtering by table,
// operation, and a minimum timestamp.
func (s *Store) ListAuditLog(ctx context.Context, filter store.AuditLogFilter) ([]*store.AuditLogEntry, error) {
	limit := filter.Limit
	if limit <= 0 {
		limit = 100
	}
	if limit > 500 {
		limit = 500
	}

	q := strings.Builder{}
	q.WriteString(`SELECT id, table_name, operation, old_data, new_data, changed_at FROM audit_log WHERE TRUE`)
	args := []any{}
	n := 1

	if filter.TableName != "" {
		fmt.Fprintf(&q, ` AND table_name = $%d`, n)
		args = append(args, filter.TableName)
		n++
	}
	if filter.Operation != "" {
		fmt.Fprintf(&q, ` AND operation = $%d`, n)
		args = append(args, filter.Operation)
		n++
	}
	if !filter.Since.IsZero() {
		fmt.Fprintf(&q, ` AND changed_at > $%d`, n)
		args = append(args, filter.Since)
		n++
	}
	fmt.Fprintf(&q, ` ORDER BY changed_at DESC LIMIT $%d`, n)
	args = append(args, limit)

	rows, err := s.pool.Query(ctx, q.String(), args...)
	if err != nil {
		return nil, fmt.Errorf("list audit log: %w", err)
	}
	defer rows.Close()

	var entries []*store.AuditLogEntry
	for rows.Next() {
		e := &store.AuditLogEntry{}
		if err := rows.Scan(&e.ID, &e.TableName, &e.Operation, &e.OldData, &e.NewData, &e.ChangedAt); err != nil {
			return nil, fmt.Errorf("scan audit log: %w", err)
		}
		entries = append(entries, e)
	}
	return entries, rows.Err()
}

func (s *Store) logger(ctx context.Context) *slog.Logger {
	return ctxlog.LoggerFrom(ctx).With(slog.String("component", "store"))
}
