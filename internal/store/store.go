package store

import (
	"context"
	"crypto/sha256"
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

	UpsertUser(ctx context.Context, profile GitHubProfile) (*User, error)
	GetUserByGitHubID(ctx context.Context, githubID int64) (*User, error)
	GetUserByID(ctx context.Context, id uuid.UUID) (*User, error)
	CreateWebSession(ctx context.Context, session WebSession) (*WebSession, error)
	GetWebSessionByTokenHash(ctx context.Context, tokenHash []byte) (*WebSession, error)
	TouchWebSession(ctx context.Context, id uuid.UUID, lastSeenAt, idleExpiresAt time.Time) error
	RevokeWebSessionByTokenHash(ctx context.Context, tokenHash []byte) error

	GetPersonalOrgByUserID(ctx context.Context, userID uuid.UUID) (*Org, error)
	ListOrgs(ctx context.Context) ([]*Org, error)

	EnsureProject(ctx context.Context, orgID, createdBy uuid.UUID, slug, name string) (*Project, error)
	ListProjects(ctx context.Context, orgID uuid.UUID) ([]*Project, error)
	GetProjectBySlug(ctx context.Context, orgID uuid.UUID, slug string) (*Project, error)

	UpsertGrant(ctx context.Context, g Grant) error
	GetGrant(ctx context.Context, jti string) (*Grant, error)
	GetGrantByRefreshToken(ctx context.Context, token string) (*Grant, error)
	GetRetiredRefreshToken(ctx context.Context, tokenHash []byte) (*RetiredRefreshToken, error)
	RotateGrant(ctx context.Context, oldToken, oldJTI string, oldJWTExpiry time.Time, g Grant, retired *RetiredRefreshToken) (*Grant, error)
	DeleteGrant(ctx context.Context, jti string, retired *RetiredRefreshToken) error

	StorePendingAuth(ctx context.Context, state string, p PendingAuth) error
	ConsumePendingAuth(ctx context.Context, state string) (*PendingAuth, error)
	StoreAuthCode(ctx context.Context, code string, c AuthCode) error
	ConsumeAuthCode(ctx context.Context, code string) (*AuthCode, error)

	RevokeToken(ctx context.Context, jti string, expiresAt time.Time) error
	IsTokenRevoked(ctx context.Context, jti string) (bool, error)

	WriteInsight(ctx context.Context, p WriteInsightParams) (*Insight, error)
	GetInsight(ctx context.Context, p GetInsightParams) (*InsightDetail, error)
	ImportProjects(ctx context.Context, orgID, createdBy uuid.UUID, projects []ImportProject) (projectCount, insightCount int, err error)
	UpdateInsight(ctx context.Context, p UpdateInsightParams) (*Insight, error)
	DeleteInsight(ctx context.Context, orgID, insightID uuid.UUID) error
	SearchInsights(ctx context.Context, projectID uuid.UUID, query string, queryMode SearchQueryMode, tags []string, tagMode SearchTagMode, limit int) ([]*Insight, error)
	ListInsights(ctx context.Context, projectID uuid.UUID, tag string, limit int) ([]*Insight, error)
	ListTags(ctx context.Context, projectID uuid.UUID, limit int) ([]TagCount, error)
	GetProjectDashboard(ctx context.Context, projectID uuid.UUID) (*ProjectDashboard, error)

	SaveClient(ctx context.Context, c OAuthClient) error
	UpsertClient(ctx context.Context, c OAuthClient) error
	GetClient(ctx context.Context, clientID string) (*OAuthClient, error)
	TouchClient(ctx context.Context, clientID string) error

	ListAuditLog(ctx context.Context, filter AuditLogFilter) ([]*AuditLogEntry, error)
}

// User is a GitHub-authenticated user stored in the database.
type User struct {
	ID               uuid.UUID
	GitHubID         int64
	Email            string
	Login            string
	DisplayName      string
	AvatarURL        string
	ProfileURL       string
	Bio              string
	ProfileUpdatedAt time.Time
	CreatedAt        time.Time
	UpdatedAt        time.Time
}

type GitHubProfile struct {
	GitHubID    int64
	Email       string
	Login       string
	DisplayName string
	AvatarURL   string
	ProfileURL  string
	Bio         string
}

type WebSession struct {
	ID            uuid.UUID
	TokenHash     []byte
	UserID        uuid.UUID
	CreatedAt     time.Time
	LastSeenAt    time.Time
	IdleExpiresAt time.Time
	ExpiresAt     time.Time
	RevokedAt     time.Time
}

func HashSessionToken(token string) []byte {
	sum := sha256.Sum256([]byte(token))
	return sum[:]
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
	ID           uuid.UUID
	ProjectID    uuid.UUID
	Key          string // empty string when no key
	Content      string
	Tags         []string
	Category     string
	Source       string
	CreatedBy    uuid.UUID
	CreatedAt    time.Time
	UpdatedAt    time.Time
	LinkWarnings []InsightLinkWarning
}

type InsightLinkWarning struct {
	Code      string
	TargetKey string
}

type GetInsightParams struct {
	ProjectID     uuid.UUID
	InsightID     uuid.UUID
	Key           string
	RelationLimit int
}

type InsightLinkReference struct {
	TargetKey string
	Resolved  bool
	ID        uuid.UUID
	Category  string
	UpdatedAt time.Time
}

type InsightBacklink struct {
	ID        uuid.UUID
	Key       string
	Category  string
	UpdatedAt time.Time
}

type InsightDetail struct {
	Insight            *Insight
	Links              []InsightLinkReference
	Backlinks          []InsightBacklink
	LinkCount          int
	BacklinkCount      int
	LinksTruncated     bool
	BacklinksTruncated bool
}

const (
	InsightLinkWarningUnresolved = "unresolved_insight_link"
	InsightLinkWarningSelf       = "self_insight_link"
)

type SearchQueryMode string

const (
	SearchQueryModeAll SearchQueryMode = "all"
	SearchQueryModeWeb SearchQueryMode = "web"
)

type SearchTagMode string

const (
	SearchTagModeAll SearchTagMode = "all"
	SearchTagModeAny SearchTagMode = "any"
)

// WriteInsightParams holds the inputs for Store.WriteInsight.
type WriteInsightParams struct {
	ProjectID uuid.UUID
	Key       string // empty = no stable key
	Content   string
	Tags      []string
	Category  string
	Source    string
	CreatedBy uuid.UUID
	// CreatedAt and UpdatedAt let a caller preserve timestamps from another
	// source (e.g. Store.ImportProjects). Zero value = use the DB default (now()).
	// Only honoured on insert; an update-by-key write always sets updated_at to now().
	CreatedAt time.Time
	UpdatedAt time.Time
}

// ImportInsight is a single insight to import into a project, scoped to that
// project and attributed to the importing user by Store.ImportProjects.
type ImportInsight struct {
	Key       string
	Content   string
	Tags      []string
	Category  string
	Source    string
	CreatedAt time.Time
	UpdatedAt time.Time
}

// ImportProject is a project plus its insights to import as a unit via Store.ImportProjects.
type ImportProject struct {
	Slug     string
	Name     string
	Insights []ImportInsight
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

// CountBucket holds a named aggregate count.
type CountBucket struct {
	Name  string
	Count int
}

// ActivityBucket holds a single day's insight update count.
type ActivityBucket struct {
	Date  string
	Count int
}

// ProjectDashboard holds read-optimized aggregate data for the project dashboard.
type ProjectDashboard struct {
	TotalInsights  int
	CategoryCounts []CountBucket
	SourceCounts   []CountBucket
	TopTags        []CountBucket
	RecentActivity []ActivityBucket
	RecentInsights []*Insight
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

const (
	RetiredRefreshTokenReasonRotated              = "rotated"
	RetiredRefreshTokenReasonGitHubExpired        = "github_expired"
	RetiredRefreshTokenReasonGitHubInvalid        = "github_invalid"
	RetiredRefreshTokenReasonGitHubMissingRefresh = "github_missing_refresh" //nolint:gosec // reason label, not a credential
	RetiredRefreshTokenReasonGrantDeleted         = "grant_deleted"
)

// RetiredRefreshToken records hashed refresh tokens after rotation or teardown.
type RetiredRefreshToken struct {
	TokenHash      []byte
	Reason         string
	UserID         uuid.UUID
	ClientID       string
	OldJTI         string
	ReplacementJTI string
	GraceExpiresAt time.Time
	RetainedUntil  time.Time
	CreatedAt      time.Time
}

func HashRefreshToken(token string) []byte {
	sum := sha256.Sum256([]byte(token))
	return sum[:]
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
	LastUsedAt              time.Time
	ExpiresAt               *time.Time
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
