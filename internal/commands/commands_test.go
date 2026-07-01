package commands

import (
	"log/slog"
	"os"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/wolfeidau/starlogz/internal/store"
	"github.com/wolfeidau/starlogz/internal/store/postgres"
	"github.com/wolfeidau/starlogz/internal/testutil/postgrestest"
)

var testDB = postgrestest.New("starlogz_commands_template", "starlogz_commands")

func TestMain(m *testing.M) {
	os.Exit(testDB.Run(m))
}

// newTestStoreAndDSN clones the migrated template database and returns both a
// Store for seeding fixtures and the DSN for commands that open their own connection.
func newTestStoreAndDSN(t *testing.T) (*postgres.Store, string) {
	t.Helper()
	dsn := testDB.NewDSN(t)

	st, err := postgres.New(t.Context(), dsn, store.NewEncryptor([32]byte{}))
	require.NoError(t, err)
	t.Cleanup(st.Close)

	return st, dsn
}

func testGlobals() *Globals {
	return &Globals{Logger: slog.New(slog.DiscardHandler)}
}

// seedUserOrgProject creates a user (and its implicit personal org) plus one project.
func seedUserOrgProject(t *testing.T, st *postgres.Store, githubID int64, login, projectSlug string) (*store.User, *store.Org, *store.Project) {
	t.Helper()
	ctx := t.Context()

	u, err := st.UpsertUser(ctx, githubID, login+"@example.com", login)
	require.NoError(t, err)
	org, err := st.GetPersonalOrgByUserID(ctx, u.ID)
	require.NoError(t, err)
	p, err := st.EnsureProject(ctx, org.ID, u.ID, projectSlug, projectSlug+" Project")
	require.NoError(t, err)

	return u, org, p
}
