package store_test

import (
	"context"
	"log/slog"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"
	"github.com/wolfeidau/starlogz/internal/store"
)

// newTestStore starts a postgres container, runs migrations, and returns a Store.
// The container is terminated when t finishes.
func newTestStore(t *testing.T) *store.Store {
	t.Helper()
	ctx := context.Background()

	ctr, err := postgres.Run(ctx,
		"postgres:18-alpine",
		postgres.WithDatabase("testdb"),
		postgres.WithUsername("test"),
		postgres.WithPassword("test"),
		testcontainers.WithWaitStrategy(
			wait.ForLog("database system is ready to accept connections").
				WithOccurrence(2),
		),
	)
	require.NoError(t, err)
	t.Cleanup(func() { _ = ctr.Terminate(ctx) })

	dsn, err := ctr.ConnectionString(ctx, "sslmode=disable")
	require.NoError(t, err)

	st, err := store.New(ctx, dsn)
	require.NoError(t, err)
	t.Cleanup(st.Close)

	require.NoError(t, st.Migrate(ctx, slog.Default()))

	return st
}

func TestPing(t *testing.T) {
	st := newTestStore(t)
	require.NoError(t, st.Ping(context.Background()))
}

func TestUpsertUser_NewAndUpdate(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()

	require.NoError(t, st.UpsertUser(ctx, 12345, "alice@example.com", "alice"))

	u, err := st.GetUserByGitHubID(ctx, 12345)
	require.NoError(t, err)
	require.Equal(t, int64(12345), u.GitHubID)
	require.Equal(t, "alice@example.com", u.Email)
	require.Equal(t, "alice", u.Login)
	require.NotEqual(t, uuid.Nil, u.ID)

	// Update email and login on re-upsert.
	require.NoError(t, st.UpsertUser(ctx, 12345, "alice2@example.com", "alice2"))
	u2, err := st.GetUserByGitHubID(ctx, 12345)
	require.NoError(t, err)
	require.Equal(t, u.ID, u2.ID, "ID must not change on upsert")
	require.Equal(t, "alice2@example.com", u2.Email)
	require.Equal(t, "alice2", u2.Login)
}

func TestGetUserByGitHubID_NotFound(t *testing.T) {
	st := newTestStore(t)
	_, err := st.GetUserByGitHubID(context.Background(), 999999)
	require.ErrorIs(t, err, store.ErrNotFound)
}

func TestEnsureProject_CreateAndIdempotent(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()

	require.NoError(t, st.UpsertUser(ctx, 1, "bob@example.com", "bob"))
	u, err := st.GetUserByGitHubID(ctx, 1)
	require.NoError(t, err)

	p, err := st.EnsureProject(ctx, u.ID, "my-proj", "My Project")
	require.NoError(t, err)
	require.Equal(t, "my-proj", p.Slug)
	require.Equal(t, "My Project", p.Name)
	require.NotEqual(t, uuid.Nil, p.ID)

	// Idempotent call with new name — name should update.
	p2, err := st.EnsureProject(ctx, u.ID, "my-proj", "My Project Renamed")
	require.NoError(t, err)
	require.Equal(t, p.ID, p2.ID, "ID must not change")
	require.Equal(t, "My Project Renamed", p2.Name)
}

func TestGetProjectBySlug_NotFound(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()

	require.NoError(t, st.UpsertUser(ctx, 2, "c@example.com", "c"))
	u, err := st.GetUserByGitHubID(ctx, 2)
	require.NoError(t, err)

	_, err = st.GetProjectBySlug(ctx, u.ID, "no-such-project")
	require.ErrorIs(t, err, store.ErrNotFound)
}

func testUserAndProject(t *testing.T, st *store.Store, githubID int64) (*store.User, *store.Project) {
	t.Helper()
	ctx := context.Background()
	require.NoError(t, st.UpsertUser(ctx, githubID, "u@example.com", "u"))
	u, err := st.GetUserByGitHubID(ctx, githubID)
	require.NoError(t, err)
	p, err := st.EnsureProject(ctx, u.ID, "proj", "Project")
	require.NoError(t, err)
	return u, p
}

func TestWriteFact_InsertAndUpsertByKey(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	u, p := testUserAndProject(t, st, 10)

	// Insert a keyed fact.
	f, err := st.WriteFact(ctx, store.WriteFactParams{
		ProjectID:  p.ID,
		Key:        "api-version",
		Content:    "v1",
		Tags:       []string{"meta"},
		SourceType: "human",
		CreatedBy:  u.ID,
	})
	require.NoError(t, err)
	require.NotEqual(t, uuid.Nil, f.ID)
	require.Equal(t, "api-version", f.Key)
	require.Equal(t, "v1", f.Content)

	// Upsert the same key — should update content, same ID.
	f2, err := st.WriteFact(ctx, store.WriteFactParams{
		ProjectID:  p.ID,
		Key:        "api-version",
		Content:    "v2",
		Tags:       []string{"meta"},
		SourceType: "human",
		CreatedBy:  u.ID,
	})
	require.NoError(t, err)
	require.Equal(t, f.ID, f2.ID, "upsert must return same ID")
	require.Equal(t, "v2", f2.Content)
}

func TestWriteFact_InsertWithoutKey(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	u, p := testUserAndProject(t, st, 20)

	f1, err := st.WriteFact(ctx, store.WriteFactParams{ProjectID: p.ID, Content: "first", Tags: []string{}, SourceType: "agent", CreatedBy: u.ID})
	require.NoError(t, err)

	f2, err := st.WriteFact(ctx, store.WriteFactParams{ProjectID: p.ID, Content: "second", Tags: []string{}, SourceType: "agent", CreatedBy: u.ID})
	require.NoError(t, err)

	require.NotEqual(t, f1.ID, f2.ID, "keyless facts get distinct IDs")
	require.Equal(t, "", f1.Key)
	require.Equal(t, "", f2.Key)
}

func TestSearchFacts(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	u, p := testUserAndProject(t, st, 30)

	_, err := st.WriteFact(ctx, store.WriteFactParams{ProjectID: p.ID, Content: "PostgreSQL is a relational database", Tags: []string{"db"}, SourceType: "agent", CreatedBy: u.ID})
	require.NoError(t, err)
	_, err = st.WriteFact(ctx, store.WriteFactParams{ProjectID: p.ID, Content: "Redis is an in-memory store", Tags: []string{"cache"}, SourceType: "agent", CreatedBy: u.ID})
	require.NoError(t, err)

	results, err := st.SearchFacts(ctx, p.ID, "relational database", nil, 10)
	require.NoError(t, err)
	require.Len(t, results, 1)
	require.Contains(t, results[0].Content, "PostgreSQL")

	// Tag filter should narrow results.
	tagged, err := st.SearchFacts(ctx, p.ID, "database", []string{"cache"}, 10)
	require.NoError(t, err)
	require.Empty(t, tagged, "cache tag should exclude PostgreSQL result")
}

func TestListFacts(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	u, p := testUserAndProject(t, st, 40)

	for _, content := range []string{"fact one", "fact two", "fact three"} {
		_, err := st.WriteFact(ctx, store.WriteFactParams{ProjectID: p.ID, Content: content, Tags: []string{"x"}, SourceType: "agent", CreatedBy: u.ID})
		require.NoError(t, err)
	}

	all, err := st.ListFacts(ctx, p.ID, "", 10)
	require.NoError(t, err)
	require.Len(t, all, 3)

	byTag, err := st.ListFacts(ctx, p.ID, "x", 10)
	require.NoError(t, err)
	require.Len(t, byTag, 3)

	noMatch, err := st.ListFacts(ctx, p.ID, "y", 10)
	require.NoError(t, err)
	require.Empty(t, noMatch)
}

func TestDeleteFact(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	u, p := testUserAndProject(t, st, 50)

	f, err := st.WriteFact(ctx, store.WriteFactParams{ProjectID: p.ID, Content: "to delete", Tags: []string{}, SourceType: "agent", CreatedBy: u.ID})
	require.NoError(t, err)

	require.NoError(t, st.DeleteFact(ctx, f.ID))

	// Deleted fact must not appear in list.
	facts, err := st.ListFacts(ctx, p.ID, "", 10)
	require.NoError(t, err)
	require.Empty(t, facts)

	// Double-delete returns ErrNotFound.
	require.ErrorIs(t, st.DeleteFact(ctx, f.ID), store.ErrNotFound)
}

func TestDeleteFact_NotFound(t *testing.T) {
	st := newTestStore(t)
	err := st.DeleteFact(context.Background(), uuid.New())
	require.ErrorIs(t, err, store.ErrNotFound)
}
