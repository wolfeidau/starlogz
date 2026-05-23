package store

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/google/uuid"
)

// ErrNotFound is returned when a queried row does not exist.
var ErrNotFound = errors.New("not found")

// Store is the interface satisfied by the postgres.Store implementation.
type Store interface {
	Ping(ctx context.Context) error
	Migrate(ctx context.Context, logger *slog.Logger) error
	Close()

	UpsertUser(ctx context.Context, githubID int64, email, login string) (*User, error)
	GetUserByGitHubID(ctx context.Context, githubID int64) (*User, error)
	GetUserByID(ctx context.Context, id uuid.UUID) (*User, error)

	GetPersonalOrgByUserID(ctx context.Context, userID uuid.UUID) (*Org, error)

	EnsureProject(ctx context.Context, orgID, createdBy uuid.UUID, slug, name string) (*Project, error)
	ListProjects(ctx context.Context, orgID uuid.UUID) ([]*Project, error)
	GetProjectBySlug(ctx context.Context, orgID uuid.UUID, slug string) (*Project, error)

	UpsertGrant(ctx context.Context, g Grant) error
	GetGrant(ctx context.Context, jti string) (*Grant, error)
	GetGrantByRefreshToken(ctx context.Context, token string) (*Grant, error)
	RotateGrant(ctx context.Context, oldToken, oldJTI string, oldJWTExpiry time.Time, g Grant) (*Grant, error)
	DeleteGrant(ctx context.Context, jti string) error

	StorePendingAuth(ctx context.Context, state string, p PendingAuth) error
	ConsumePendingAuth(ctx context.Context, state string) (*PendingAuth, error)
	StoreAuthCode(ctx context.Context, code string, c AuthCode) error
	ConsumeAuthCode(ctx context.Context, code string) (*AuthCode, error)

	RevokeToken(ctx context.Context, jti string, expiresAt time.Time) error
	IsTokenRevoked(ctx context.Context, jti string) (bool, error)

	WriteInsight(ctx context.Context, p WriteInsightParams) (*Insight, error)
	UpdateInsight(ctx context.Context, p UpdateInsightParams) (*Insight, error)
	DeleteInsight(ctx context.Context, orgID, insightID uuid.UUID) error
	SearchInsights(ctx context.Context, projectID uuid.UUID, query string, tags []string, limit int) ([]*Insight, error)
	ListInsights(ctx context.Context, projectID uuid.UUID, tag string, limit int) ([]*Insight, error)
	ListTags(ctx context.Context, projectID uuid.UUID, limit int) ([]TagCount, error)

	SaveClient(ctx context.Context, c OAuthClient) error
	GetClient(ctx context.Context, clientID string) (*OAuthClient, error)

	ListAuditLog(ctx context.Context, filter AuditLogFilter) ([]*AuditLogEntry, error)
}

// User is a GitHub-authenticated user stored in the database.
type User struct {
	ID        uuid.UUID
	GitHubID  int64
	Email     string
	Login     string
	CreatedAt time.Time
	UpdatedAt time.Time
}

// Org is a tenant boundary; every project belongs to exactly one org.
type Org struct {
	ID        uuid.UUID
	Slug      string
	Name      string
	Kind      string // "personal" or "shared"
	CreatedAt time.Time
}

// Project is a named container for facts owned by an org.
type Project struct {
	ID        uuid.UUID
	OrgID     uuid.UUID
	CreatedBy uuid.UUID
	Slug      string
	Name      string
	CreatedAt time.Time
}

// Insight is a text assertion stored against a project.
type Insight struct {
	ID        uuid.UUID
	ProjectID uuid.UUID
	Key       string // empty string when no key
	Content   string
	Tags      []string
	Category  string
	Source    string
	CreatedBy uuid.UUID
	CreatedAt time.Time
	UpdatedAt time.Time
}

// WriteInsightParams holds the inputs for Store.WriteInsight.
type WriteInsightParams struct {
	ProjectID uuid.UUID
	Key       string // empty = no stable key
	Content   string
	Tags      []string
	Category  string
	Source    string
	CreatedBy uuid.UUID
}

// UpdateInsightParams holds the inputs for Store.UpdateInsight.
// Empty Content means no change. Nil Tags means no change; non-nil (including empty) replaces tags.
type UpdateInsightParams struct {
	OrgID     uuid.UUID
	InsightID uuid.UUID
	Content   string
	Tags      []string
}

// TagCount holds a tag name and its usage frequency within a project.
type TagCount struct {
	Name  string
	Count int
}

// Grant holds a single authorization grant with the associated GitHub App tokens.
// Tokens are stored encrypted at rest; this struct carries plaintext values.
type Grant struct {
	JTI                string
	UserID             uuid.UUID
	OurRefreshToken    string
	ClientID           string
	Scope              string
	AccessToken        string
	RefreshToken       string
	AccessTokenExpiry  time.Time
	RefreshTokenExpiry time.Time
	JWTExpiry          time.Time
	UpdatedAt          time.Time
}

// OAuthClient is a registered OAuth2 client stored in the database.
type OAuthClient struct {
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

// PendingAuth holds client PKCE and redirect params across the GitHub OAuth2 redirect leg.
type PendingAuth struct {
	ClientID      string
	RedirectURI   string
	Scope         string
	CodeChallenge string
	ClientState   string
}

// AuditLogEntry is a single row from the audit_log table.
type AuditLogEntry struct {
	ID        int64
	TableName string
	Operation string // INSERT, UPDATE, DELETE
	OldData   []byte // raw JSONB; nil for INSERT
	NewData   []byte // raw JSONB; nil for DELETE
	ChangedAt time.Time
}

// AuditLogFilter controls which audit_log rows are returned by ListAuditLog.
// Zero values for string fields and a zero Since mean "no filter on that field".
type AuditLogFilter struct {
	TableName string    // optional
	Operation string    // optional: INSERT, UPDATE, DELETE
	Since     time.Time // optional: only entries after this time
	Limit     int       // 0 → default 100; capped at 500
}

// AuthCode holds identity and GitHub tokens between the GitHub callback and token exchange.
// Tokens are stored encrypted at rest; this struct carries plaintext values.
type AuthCode struct {
	Sub                string // internal user UUID (JWT sub)
	GitHubID           int64  // GitHub numeric ID (for logging)
	Email              string
	Scope              string
	CodeChallenge      string
	RedirectURI        string
	ClientID           string
	AccessToken        string
	RefreshToken       string
	AccessTokenExpiry  time.Time
	RefreshTokenExpiry time.Time
}
