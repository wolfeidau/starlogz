package store

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ErrNotFound is returned when a queried row does not exist.
var ErrNotFound = errors.New("not found")

// Store wraps a pgxpool and exposes domain-level query methods.
type Store struct {
	pool   *pgxpool.Pool
	encKey *[32]byte
}

// New connects to PostgreSQL and returns a Store. Call Migrate before first use.
func New(ctx context.Context, dsn string) (*Store, error) {
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		return nil, fmt.Errorf("connect: %w", err)
	}
	return &Store{pool: pool}, nil
}

// SetEncryptionKey configures the key used to encrypt and decrypt stored GitHub tokens.
// Must be called before any grant operations.
func (s *Store) SetEncryptionKey(key [32]byte) {
	s.encKey = &key
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
