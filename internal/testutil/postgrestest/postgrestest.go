package postgrestest

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	postgrescont "github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"
	"github.com/wolfeidau/starlogz/internal/store/postgres"
)

var databaseNamePattern = regexp.MustCompile(`^[a-zA-Z_][a-zA-Z0-9_]*$`)

// Template owns a Postgres testcontainer and a migrated template database.
type Template struct {
	templateDBName string
	dbNamePrefix   string
	adminDSN       string
}

// New returns a reusable Postgres template fixture for a test package.
func New(templateDBName, dbNamePrefix string) *Template {
	validateDatabaseName(templateDBName)
	validateDatabaseName(dbNamePrefix)
	return &Template{templateDBName: templateDBName, dbNamePrefix: dbNamePrefix}
}

// Run starts Postgres, migrates the template database, runs the package tests,
// and tears the container down. Call it from TestMain.
func (p *Template) Run(m *testing.M) int {
	ctx := context.Background()

	ctr, err := postgrescont.Run(ctx,
		"postgres:18-alpine",
		postgrescont.WithDatabase("testdb"),
		postgrescont.WithUsername("test"),
		postgrescont.WithPassword("test"),
		testcontainers.WithWaitStrategy(
			wait.ForLog("database system is ready to accept connections").
				WithOccurrence(2),
		),
	)
	if err != nil {
		fmt.Fprintf(os.Stderr, "start postgres container: %v\n", err)
		return 1
	}
	defer func() { _ = ctr.Terminate(ctx) }()

	p.adminDSN, err = ctr.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		fmt.Fprintf(os.Stderr, "postgres connection string: %v\n", err)
		return 1
	}

	if err := p.createTemplateDB(ctx); err != nil {
		fmt.Fprintf(os.Stderr, "create template database: %v\n", err)
		return 1
	}

	return m.Run()
}

// NewDSN clones the migrated template database and returns a DSN for the clone.
// The cloned database is dropped when t finishes.
func (p *Template) NewDSN(t *testing.T) string {
	t.Helper()
	ctx := context.Background()

	dbName := p.createTestDB(t, ctx)
	t.Cleanup(func() { p.dropTestDB(t, ctx, dbName) })
	return p.dsnForDB(dbName)
}

func (p *Template) createTemplateDB(ctx context.Context) error {
	adminPool, err := pgxpool.New(ctx, p.adminDSN)
	if err != nil {
		return fmt.Errorf("connect admin database: %w", err)
	}
	defer adminPool.Close()

	if _, err := adminPool.Exec(ctx, "CREATE DATABASE "+p.templateDBName); err != nil {
		return fmt.Errorf("create database: %w", err)
	}

	st, err := postgres.New(ctx, p.dsnForDB(p.templateDBName), nil)
	if err != nil {
		return fmt.Errorf("connect template database: %w", err)
	}
	defer st.Close()

	if err := st.Migrate(ctx, slog.Default()); err != nil {
		return fmt.Errorf("migrate template database: %w", err)
	}
	return nil
}

func (p *Template) createTestDB(t *testing.T, ctx context.Context) string {
	t.Helper()

	dbName := p.dbNamePrefix + "_" + strings.ReplaceAll(uuid.NewString(), "-", "_")
	adminPool, err := pgxpool.New(ctx, p.adminDSN)
	require.NoError(t, err)
	defer adminPool.Close()

	_, err = adminPool.Exec(ctx, "CREATE DATABASE "+dbName+" WITH TEMPLATE "+p.templateDBName)
	require.NoError(t, err)
	return dbName
}

func (p *Template) dropTestDB(t *testing.T, ctx context.Context, dbName string) {
	t.Helper()

	adminPool, err := pgxpool.New(ctx, p.adminDSN)
	require.NoError(t, err)
	defer adminPool.Close()

	_, err = adminPool.Exec(ctx, "DROP DATABASE IF EXISTS "+dbName+" WITH (FORCE)")
	require.NoError(t, err)
}

func (p *Template) dsnForDB(dbName string) string {
	config, err := pgxpool.ParseConfig(p.adminDSN)
	if err != nil {
		panic(fmt.Sprintf("parse test dsn: %v", err))
	}
	config.ConnConfig.Database = dbName
	return config.ConnString()
}

func validateDatabaseName(name string) {
	if !databaseNamePattern.MatchString(name) {
		panic(fmt.Sprintf("invalid postgres test database name %q", name))
	}
}
