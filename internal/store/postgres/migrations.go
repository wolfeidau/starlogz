package postgres

import (
	"context"
	"embed"
	"errors"
	"fmt"
	"hash/fnv"
	"log/slog"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

//go:embed migrations/*.sql
var migrationsFS embed.FS

const concurrentIndexDirective = "-- starlogz:concurrent-index "

type migration struct {
	version         int
	name            string
	content         string
	concurrentIndex string
}

// migrationLockKey is the Postgres advisory lock key used to serialise concurrent migration runs.
// Derived from fnv64a("starlogz-migrations") so it is unique to this project.
var migrationLockKey = func() int64 {
	h := fnv.New64a()
	_, _ = h.Write([]byte("starlogz-migrations"))
	return int64(h.Sum64())
}()

// RunMigrations executes all pending SQL migrations in version order under an advisory lock.
// Only one process holds the lock at a time; others block until migrations finish.
// The lock acquisition times out after 60 seconds to prevent indefinite blocking.
func RunMigrations(ctx context.Context, pool *pgxpool.Pool, logger *slog.Logger) error {
	logger.InfoContext(ctx, "running database migrations")

	lockCtx, lockCancel := context.WithTimeout(ctx, 60*time.Second)
	defer lockCancel()

	conn, err := pool.Acquire(lockCtx)
	if err != nil {
		return fmt.Errorf("acquire connection for migration lock: %w", err)
	}
	defer conn.Release()

	if _, err := conn.Exec(lockCtx, "SELECT pg_advisory_lock($1)", migrationLockKey); err != nil {
		return fmt.Errorf("acquire migration lock: %w", err)
	}
	defer func() {
		unlockCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		_, _ = conn.Exec(unlockCtx, "SELECT pg_advisory_unlock($1)", migrationLockKey)
	}()

	entries, err := migrationsFS.ReadDir("migrations")
	if err != nil {
		return fmt.Errorf("read migrations dir: %w", err)
	}

	var migrations []migration
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".sql") {
			continue
		}

		parts := strings.SplitN(entry.Name(), "_", 2)
		if len(parts) < 2 {
			logger.WarnContext(ctx, "skipping migration with unexpected filename", slog.String("file", entry.Name()))
			continue
		}

		version, err := strconv.Atoi(parts[0])
		if err != nil {
			logger.WarnContext(ctx, "skipping migration with non-numeric prefix", slog.String("file", entry.Name()))
			continue
		}

		content, err := migrationsFS.ReadFile("migrations/" + entry.Name())
		if err != nil {
			return fmt.Errorf("read migration %s: %w", entry.Name(), err)
		}

		indexName, migrationContent, err := parseMigrationContent(string(content))
		if err != nil {
			return fmt.Errorf("parse migration %s: %w", entry.Name(), err)
		}

		migrations = append(migrations, migration{version: version, name: entry.Name(), content: migrationContent, concurrentIndex: indexName})
	}

	sort.Slice(migrations, func(i, j int) bool { return migrations[i].version < migrations[j].version })

	for _, m := range migrations {
		if err := runMigration(ctx, conn, logger, m); err != nil {
			return fmt.Errorf("migration %s: %w", m.name, err)
		}
	}

	logger.InfoContext(ctx, "migrations complete", slog.Int("count", len(migrations)))
	return nil
}

func parseMigrationContent(content string) (indexName, migrationContent string, err error) {
	firstLine, rest, found := strings.Cut(content, "\n")
	if !strings.HasPrefix(firstLine, concurrentIndexDirective) {
		return "", content, nil
	}
	if !found {
		return "", "", fmt.Errorf("concurrent index migration has no SQL")
	}
	indexName = strings.TrimSpace(strings.TrimPrefix(firstLine, concurrentIndexDirective))
	if indexName == "" {
		return "", "", fmt.Errorf("concurrent index migration has no index name")
	}
	migrationContent = strings.TrimSpace(rest)
	if migrationContent == "" {
		return "", "", fmt.Errorf("concurrent index migration has no SQL")
	}
	return indexName, migrationContent, nil
}

func runMigration(ctx context.Context, conn *pgxpool.Conn, logger *slog.Logger, m migration) error {
	var applied bool
	err := conn.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM schema_migrations WHERE version = $1)`, m.version).Scan(&applied)
	if err != nil {
		// schema_migrations doesn't exist yet — first migration has not run
		if strings.Contains(err.Error(), "does not exist") {
			applied = false
		} else {
			return fmt.Errorf("check migration status: %w", err)
		}
	}

	if applied {
		return nil
	}

	logger.InfoContext(ctx, "applying migration", slog.Int("version", m.version), slog.String("name", m.name))

	if m.concurrentIndex != "" {
		return runConcurrentIndexMigration(ctx, conn, m)
	}

	tx, err := conn.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin transaction: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if _, err := tx.Exec(ctx, m.content); err != nil {
		return fmt.Errorf("execute SQL: %w", err)
	}

	return tx.Commit(ctx)
}

func runConcurrentIndexMigration(ctx context.Context, conn *pgxpool.Conn, m migration) error {
	exists, valid, err := indexState(ctx, conn, m.concurrentIndex)
	if err != nil {
		return err
	}
	if exists && !valid {
		if _, err := conn.Exec(ctx, "DROP INDEX CONCURRENTLY "+pgx.Identifier{m.concurrentIndex}.Sanitize()); err != nil {
			return fmt.Errorf("drop invalid concurrent index: %w", err)
		}
		exists = false
	}
	if !exists {
		if _, err := conn.Exec(ctx, m.content); err != nil {
			return fmt.Errorf("execute concurrent index SQL: %w", err)
		}
	}

	exists, valid, err = indexState(ctx, conn, m.concurrentIndex)
	if err != nil {
		return err
	}
	if !exists || !valid {
		return fmt.Errorf("concurrent index %q is not valid", m.concurrentIndex)
	}
	if _, err := conn.Exec(ctx, `INSERT INTO schema_migrations (version) VALUES ($1) ON CONFLICT DO NOTHING`, m.version); err != nil {
		return fmt.Errorf("record concurrent index migration: %w", err)
	}
	return nil
}

func indexState(ctx context.Context, conn *pgxpool.Conn, indexName string) (exists, valid bool, err error) {
	err = conn.QueryRow(ctx, `
		SELECT indisvalid
		FROM pg_catalog.pg_index
		WHERE indexrelid = to_regclass($1)`, indexName).Scan(&valid)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, false, nil
	}
	if err != nil {
		return false, false, fmt.Errorf("inspect concurrent index: %w", err)
	}
	return true, valid, nil
}
