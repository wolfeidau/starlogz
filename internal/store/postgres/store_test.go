package postgres_test

import (
	"context"
	"log/slog"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	postgrescont "github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"
	"github.com/wolfeidau/starlogz/internal/store"
	"github.com/wolfeidau/starlogz/internal/store/postgres"
)

// testEncKey is a fixed key used in grant tests that require encryption.
var testEncKey = func() [32]byte {
	var k [32]byte
	copy(k[:], "test-key-0123456789abcdefghijklm")
	return k
}()

// newTestStore starts a postgres container, runs migrations, and returns a Store.
// The container is terminated when t finishes.
func newTestStore(t *testing.T) *postgres.Store {
	t.Helper()
	return newTestStoreWithEnc(t, nil)
}

// newTestStoreWithEnc is like newTestStore but configures an encryptor at construction time.
func newTestStoreWithEnc(t *testing.T, enc *store.Encryptor) *postgres.Store {
	t.Helper()
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
	require.NoError(t, err)
	t.Cleanup(func() { _ = ctr.Terminate(ctx) })

	dsn, err := ctr.ConnectionString(ctx, "sslmode=disable")
	require.NoError(t, err)

	st, err := postgres.New(ctx, dsn, enc)
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

func testUserAndProject(t *testing.T, st *postgres.Store, githubID int64) (*store.User, *store.Project) {
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
	require.Empty(t, f1.Key)
	require.Empty(t, f2.Key)
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

func TestListProjects(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()

	require.NoError(t, st.UpsertUser(ctx, 60, "d@example.com", "d"))
	u, err := st.GetUserByGitHubID(ctx, 60)
	require.NoError(t, err)

	// No projects yet.
	projects, err := st.ListProjects(ctx, u.ID)
	require.NoError(t, err)
	require.Empty(t, projects)

	_, err = st.EnsureProject(ctx, u.ID, "beta", "Beta")
	require.NoError(t, err)
	_, err = st.EnsureProject(ctx, u.ID, "alpha", "Alpha")
	require.NoError(t, err)

	projects, err = st.ListProjects(ctx, u.ID)
	require.NoError(t, err)
	require.Len(t, projects, 2)
	// Ordered by name ascending.
	require.Equal(t, "alpha", projects[0].Slug)
	require.Equal(t, "beta", projects[1].Slug)
}

func TestListTags(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	u, p := testUserAndProject(t, st, 70)

	write := func(tags []string) {
		_, err := st.WriteFact(ctx, store.WriteFactParams{ProjectID: p.ID, Content: "x", Tags: tags, SourceType: "agent", CreatedBy: u.ID})
		require.NoError(t, err)
	}
	write([]string{"auth", "api"})
	write([]string{"auth", "db"})
	write([]string{"auth"})

	tags, err := st.ListTags(ctx, p.ID, 10)
	require.NoError(t, err)
	require.Len(t, tags, 3)
	// auth appears 3 times — must be first.
	require.Equal(t, "auth", tags[0].Name)
	require.Equal(t, 3, tags[0].Count)

	// Deleted facts must not contribute to counts.
	f, err := st.WriteFact(ctx, store.WriteFactParams{ProjectID: p.ID, Content: "gone", Tags: []string{"orphan"}, SourceType: "agent", CreatedBy: u.ID})
	require.NoError(t, err)
	require.NoError(t, st.DeleteFact(ctx, f.ID))

	tags, err = st.ListTags(ctx, p.ID, 10)
	require.NoError(t, err)
	for _, tc := range tags {
		require.NotEqual(t, "orphan", tc.Name, "deleted fact tags must not appear")
	}
}

func TestUpdateFact(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	u, p := testUserAndProject(t, st, 80)

	f, err := st.WriteFact(ctx, store.WriteFactParams{
		ProjectID:  p.ID,
		Content:    "original content",
		Tags:       []string{"v1"},
		SourceType: "human",
		CreatedBy:  u.ID,
	})
	require.NoError(t, err)

	// Update content only — tags should be unchanged.
	updated, err := st.UpdateFact(ctx, store.UpdateFactParams{FactID: f.ID, Content: "updated content"})
	require.NoError(t, err)
	require.Equal(t, "updated content", updated.Content)
	require.Equal(t, []string{"v1"}, updated.Tags)

	// Update tags only — content should be unchanged.
	updated, err = st.UpdateFact(ctx, store.UpdateFactParams{FactID: f.ID, Tags: []string{"v2", "patched"}})
	require.NoError(t, err)
	require.Equal(t, "updated content", updated.Content)
	require.Equal(t, []string{"v2", "patched"}, updated.Tags)

	// Clear tags by passing an empty (non-nil) slice.
	updated, err = st.UpdateFact(ctx, store.UpdateFactParams{FactID: f.ID, Tags: []string{}})
	require.NoError(t, err)
	require.Empty(t, updated.Tags)

	// ErrNotFound on a missing fact.
	require.ErrorIs(t, func() error {
		_, err := st.UpdateFact(ctx, store.UpdateFactParams{FactID: uuid.New(), Content: "x"})
		return err
	}(), store.ErrNotFound)

	// ErrNotFound after soft-delete.
	require.NoError(t, st.DeleteFact(ctx, f.ID))
	_, err = st.UpdateFact(ctx, store.UpdateFactParams{FactID: f.ID, Content: "too late"})
	require.ErrorIs(t, err, store.ErrNotFound)
}

// --- Grants ---

func TestUpsertGrant_StoresAndRetrievesEncryptedTokens(t *testing.T) {
	st := newTestStoreWithEnc(t, store.NewEncryptor(testEncKey))
	ctx := context.Background()

	require.NoError(t, st.UpsertUser(ctx, 100, "grantuser@example.com", "grantuser"))

	now := time.Now().UTC().Truncate(time.Second)
	g := store.Grant{
		JTI:                "test-jti-001",
		GitHubID:           100,
		AccessToken:        "gha_accesstoken123",
		RefreshToken:       "ghr_refreshtoken456",
		AccessTokenExpiry:  now.Add(8 * time.Hour),
		RefreshTokenExpiry: now.Add(6 * 30 * 24 * time.Hour),
		JWTExpiry:          now.Add(7 * 24 * time.Hour),
	}

	require.NoError(t, st.UpsertGrant(ctx, g))

	got, err := st.GetGrant(ctx, "test-jti-001")
	require.NoError(t, err)
	require.Equal(t, g.JTI, got.JTI)
	require.Equal(t, g.GitHubID, got.GitHubID)
	require.Equal(t, g.AccessToken, got.AccessToken)
	require.Equal(t, g.RefreshToken, got.RefreshToken)
	require.WithinDuration(t, g.AccessTokenExpiry, got.AccessTokenExpiry, time.Second)
	require.WithinDuration(t, g.RefreshTokenExpiry, got.RefreshTokenExpiry, time.Second)
}

func TestUpsertGrant_PrunesExpiredGrants(t *testing.T) {
	st := newTestStoreWithEnc(t, store.NewEncryptor(testEncKey))
	ctx := context.Background()

	require.NoError(t, st.UpsertUser(ctx, 200, "pruneuser@example.com", "pruneuser"))

	now := time.Now().UTC()
	expired := store.Grant{
		JTI:                "expired-jti",
		GitHubID:           200,
		AccessToken:        "old-access",
		RefreshToken:       "old-refresh",
		AccessTokenExpiry:  now.Add(-10 * time.Hour),
		RefreshTokenExpiry: now.Add(-1 * time.Hour),
		JWTExpiry:          now.Add(-1 * time.Second), // already expired
	}
	require.NoError(t, st.UpsertGrant(ctx, expired))

	// Confirm expired grant was inserted.
	_, err := st.GetGrant(ctx, "expired-jti")
	require.NoError(t, err)

	// Upsert a new grant for the same user — triggers lazy prune of the expired one.
	fresh := store.Grant{
		JTI:                "fresh-jti",
		GitHubID:           200,
		AccessToken:        "new-access",
		RefreshToken:       "new-refresh",
		AccessTokenExpiry:  now.Add(8 * time.Hour),
		RefreshTokenExpiry: now.Add(180 * 24 * time.Hour),
		JWTExpiry:          now.Add(7 * 24 * time.Hour),
	}
	require.NoError(t, st.UpsertGrant(ctx, fresh))

	_, err = st.GetGrant(ctx, "expired-jti")
	require.ErrorIs(t, err, store.ErrNotFound, "expired grant must be pruned")

	_, err = st.GetGrant(ctx, "fresh-jti")
	require.NoError(t, err, "fresh grant must still exist")
}

func TestGetGrant_NotFound(t *testing.T) {
	st := newTestStoreWithEnc(t, store.NewEncryptor(testEncKey))

	_, err := st.GetGrant(context.Background(), "no-such-jti")
	require.ErrorIs(t, err, store.ErrNotFound)
}

func TestUpsertGrant_NoEncryptionKey(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()

	require.NoError(t, st.UpsertUser(ctx, 300, "nokey@example.com", "nokey"))

	err := st.UpsertGrant(ctx, store.Grant{
		JTI:      "no-key-jti",
		GitHubID: 300,
	})
	require.Error(t, err, "UpsertGrant without encryption key must fail")
}

// --- OAuth clients ---

func TestSaveClient(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()

	now := time.Now().UTC().Truncate(time.Second)
	c := store.OAuthClient{
		ClientID:                "test-client-id-001",
		ClientName:              "Test Client",
		RedirectURIs:            []string{"https://client.example.com/callback"},
		GrantTypes:              []string{"authorization_code"},
		ResponseTypes:           []string{"code"},
		TokenEndpointAuthMethod: "none",
		Scope:                   "facts:read",
		IssuedAt:                now,
		ExpiresAt:               now.Add(90 * 24 * time.Hour),
	}

	require.NoError(t, st.SaveClient(ctx, c))
}

func TestSaveClient_DuplicateClientID(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()

	now := time.Now().UTC()
	c := store.OAuthClient{
		ClientID:                "duplicate-client-id",
		ClientName:              "First",
		RedirectURIs:            []string{"https://a.example.com/cb"},
		GrantTypes:              []string{"authorization_code"},
		ResponseTypes:           []string{"code"},
		TokenEndpointAuthMethod: "none",
		IssuedAt:                now,
		ExpiresAt:               now.Add(90 * 24 * time.Hour),
	}

	require.NoError(t, st.SaveClient(ctx, c))

	c.ClientName = "Second"
	err := st.SaveClient(ctx, c)
	require.Error(t, err, "saving a duplicate client_id must return an error")
}
