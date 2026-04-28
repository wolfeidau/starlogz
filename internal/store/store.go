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

	UpsertUser(ctx context.Context, githubID int64, email, login string) error
	GetUserByGitHubID(ctx context.Context, githubID int64) (*User, error)

	EnsureProject(ctx context.Context, ownerID uuid.UUID, slug, name string) (*Project, error)
	ListProjects(ctx context.Context, ownerID uuid.UUID) ([]*Project, error)
	GetProjectBySlug(ctx context.Context, ownerID uuid.UUID, slug string) (*Project, error)

	UpsertGrant(ctx context.Context, g Grant) error
	GetGrant(ctx context.Context, jti string) (*Grant, error)

	WriteFact(ctx context.Context, p WriteFactParams) (*Fact, error)
	UpdateFact(ctx context.Context, p UpdateFactParams) (*Fact, error)
	DeleteFact(ctx context.Context, factID uuid.UUID) error
	SearchFacts(ctx context.Context, projectID uuid.UUID, query string, tags []string, limit int) ([]*Fact, error)
	ListFacts(ctx context.Context, projectID uuid.UUID, tag string, limit int) ([]*Fact, error)
	ListTags(ctx context.Context, projectID uuid.UUID, limit int) ([]TagCount, error)

	SaveOAuthClient(ctx context.Context, c OAuthClient) error
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

// Project is a named container for facts owned by a single user.
type Project struct {
	ID        uuid.UUID
	OwnerID   uuid.UUID
	Slug      string
	Name      string
	CreatedAt time.Time
}

// Fact is a text assertion stored against a project.
type Fact struct {
	ID         uuid.UUID
	ProjectID  uuid.UUID
	Key        string // empty string when no key
	Content    string
	Tags       []string
	SourceType string
	CreatedBy  uuid.UUID
	CreatedAt  time.Time
	UpdatedAt  time.Time
}

// WriteFactParams holds the inputs for Store.WriteFact.
type WriteFactParams struct {
	ProjectID  uuid.UUID
	Key        string // empty = no stable key
	Content    string
	Tags       []string
	SourceType string
	CreatedBy  uuid.UUID
}

// UpdateFactParams holds the inputs for Store.UpdateFact.
// Empty Content means no change. Nil Tags means no change; non-nil (including empty) replaces tags.
type UpdateFactParams struct {
	FactID  uuid.UUID
	Content string
	Tags    []string
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
	GitHubID           int64
	AccessToken        string
	RefreshToken       string
	AccessTokenExpiry  time.Time
	RefreshTokenExpiry time.Time
	JWTExpiry          time.Time
	UpdatedAt          time.Time
}

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
