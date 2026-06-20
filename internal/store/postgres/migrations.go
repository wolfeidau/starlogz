package postgres

import (
	"context"
	"embed"
	"fmt"
	"hash/fnv"
	"log/slog"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

//go:embed migrations/*.sql
var migrationsFS embed.FS

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

	type migration struct {
		version int
		name    string
		content string
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

		migrations = append(migrations, migration{version: version, name: entry.Name(), content: string(content)})
	}

	sort.Slice(migrations, func(i, j int) bool { return migrations[i].version < migrations[j].version })

	for _, m := range migrations {
		if err := runMigration(ctx, pool, logger, m.version, m.name, m.content); err != nil {
			return fmt.Errorf("migration %s: %w", m.name, err)
		}
	}

	logger.InfoContext(ctx, "migrations complete", slog.Int("count", len(migrations)))
	return nil
}

func runMigration(ctx context.Context, pool *pgxpool.Pool, logger *slog.Logger, version int, name, content string) error {
	var applied bool
	err := pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM schema_migrations WHERE version = $1)`, version).Scan(&applied)
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

	logger.InfoContext(ctx, "applying migration", slog.Int("version", version), slog.String("name", name))

	tx, err := pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin transaction: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if _, err := tx.Exec(ctx, content); err != nil {
		return fmt.Errorf("execute SQL: %w", err)
	}

	return tx.Commit(ctx)
}
