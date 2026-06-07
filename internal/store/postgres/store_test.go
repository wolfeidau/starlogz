package postgres_test

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"github.com/wolfeidau/starlogz/internal/store"
	"github.com/wolfeidau/starlogz/internal/store/postgres"
	"github.com/wolfeidau/starlogz/internal/testutil/postgrestest"
)

// testEncKey is a fixed key used in grant tests that require encryption.
var testEncKey = func() [32]byte {
	var k [32]byte
	copy(k[:], "test-key-0123456789abcdefghijklm")
	return k
}()

var testDB = postgrestest.New("starlogz_store_template", "starlogz_store")

func TestMain(m *testing.M) {
	os.Exit(testDB.Run(m))
}

// newTestStore clones the migrated template database and returns a Store.
// The cloned database is dropped when t finishes.
func newTestStore(t *testing.T) *postgres.Store {
	t.Helper()
	return newTestStoreWithEnc(t, nil)
}

// newTestStoreWithEnc is like newTestStore but configures an encryptor at construction time.
func newTestStoreWithEnc(t *testing.T, enc *store.Encryptor) *postgres.Store {
	t.Helper()
	ctx := context.Background()

	st, err := postgres.New(ctx, testDB.NewDSN(t), enc)
	require.NoError(t, err)
	t.Cleanup(st.Close)

	return st
}

func TestPing(t *testing.T) {
	st := newTestStore(t)
	require.NoError(t, st.Ping(context.Background()))
}

func TestUpsertUser_NewAndUpdate(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()

	_, err := st.UpsertUser(ctx, 12345, "alice@example.com", "alice")
	require.NoError(t, err)

	u, err := st.GetUserByGitHubID(ctx, 12345)
	require.NoError(t, err)
	require.Equal(t, int64(12345), u.GitHubID)
	require.Equal(t, "alice@example.com", u.Email)
	require.Equal(t, "alice", u.Login)
	require.NotEqual(t, uuid.Nil, u.ID)

	// Update email and login on re-upsert.
	_, err = st.UpsertUser(ctx, 12345, "alice2@example.com", "alice2")
	require.NoError(t, err)
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

	_, err := st.UpsertUser(ctx, 1, "bob@example.com", "bob")
	require.NoError(t, err)
	u, err := st.GetUserByGitHubID(ctx, 1)
	require.NoError(t, err)
	org, err := st.GetPersonalOrgByUserID(ctx, u.ID)
	require.NoError(t, err)

	p, err := st.EnsureProject(ctx, org.ID, u.ID, "my-proj", "My Project")
	require.NoError(t, err)
	require.Equal(t, "my-proj", p.Slug)
	require.Equal(t, "My Project", p.Name)
	require.NotEqual(t, uuid.Nil, p.ID)

	// Idempotent call with new name — name should update.
	p2, err := st.EnsureProject(ctx, org.ID, u.ID, "my-proj", "My Project Renamed")
	require.NoError(t, err)
	require.Equal(t, p.ID, p2.ID, "ID must not change")
	require.Equal(t, "My Project Renamed", p2.Name)
}

func TestGetProjectBySlug_NotFound(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()

	_, err := st.UpsertUser(ctx, 2, "c@example.com", "c")
	require.NoError(t, err)
	u, err := st.GetUserByGitHubID(ctx, 2)
	require.NoError(t, err)
	org, err := st.GetPersonalOrgByUserID(ctx, u.ID)
	require.NoError(t, err)

	_, err = st.GetProjectBySlug(ctx, org.ID, "no-such-project")
	require.ErrorIs(t, err, store.ErrNotFound)
}

func testUserAndProject(t *testing.T, st *postgres.Store, githubID int64) (*store.User, *store.Project) {
	t.Helper()
	ctx := context.Background()
	u, err := st.UpsertUser(ctx, githubID, "u@example.com", "u")
	require.NoError(t, err)
	org, err := st.GetPersonalOrgByUserID(ctx, u.ID)
	require.NoError(t, err)
	p, err := st.EnsureProject(ctx, org.ID, u.ID, "proj", "Project")
	require.NoError(t, err)
	return u, p
}

func TestWriteInsight_InsertAndUpsertByKey(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	u, p := testUserAndProject(t, st, 10)

	// Insert a keyed insight.
	f, err := st.WriteInsight(ctx, store.WriteInsightParams{
		ProjectID: p.ID,
		Key:       "api-version",
		Content:   "v1",
		Tags:      []string{"meta"},
		Category:  "decision",
		Source:    "user",
		CreatedBy: u.ID,
	})
	require.NoError(t, err)
	require.NotEqual(t, uuid.Nil, f.ID)
	require.Equal(t, "api-version", f.Key)
	require.Equal(t, "v1", f.Content)

	// Upsert the same key — should update content, same ID.
	f2, err := st.WriteInsight(ctx, store.WriteInsightParams{
		ProjectID: p.ID,
		Key:       "api-version",
		Content:   "v2",
		Tags:      []string{"meta"},
		Category:  "decision",
		Source:    "user",
		CreatedBy: u.ID,
	})
	require.NoError(t, err)
	require.Equal(t, f.ID, f2.ID, "upsert must return same ID")
	require.Equal(t, "v2", f2.Content)
}

func TestWriteInsight_InsertWithoutKey(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	u, p := testUserAndProject(t, st, 20)

	f1, err := st.WriteInsight(ctx, store.WriteInsightParams{ProjectID: p.ID, Content: "first", Tags: []string{}, CreatedBy: u.ID})
	require.NoError(t, err)

	f2, err := st.WriteInsight(ctx, store.WriteInsightParams{ProjectID: p.ID, Content: "second", Tags: []string{}, CreatedBy: u.ID})
	require.NoError(t, err)

	require.NotEqual(t, f1.ID, f2.ID, "keyless insights get distinct IDs")
	require.Empty(t, f1.Key)
	require.Empty(t, f2.Key)
}

func TestSearchInsights(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	u, p := testUserAndProject(t, st, 30)

	_, err := st.WriteInsight(ctx, store.WriteInsightParams{ProjectID: p.ID, Content: "PostgreSQL is a relational database", Tags: []string{"db"}, CreatedBy: u.ID})
	require.NoError(t, err)
	_, err = st.WriteInsight(ctx, store.WriteInsightParams{ProjectID: p.ID, Content: "Redis is an in-memory store", Tags: []string{"cache"}, CreatedBy: u.ID})
	require.NoError(t, err)

	results, err := st.SearchInsights(ctx, p.ID, "relational database", nil, 10)
	require.NoError(t, err)
	require.Len(t, results, 1)
	require.Contains(t, results[0].Content, "PostgreSQL")

	// Tag filter should narrow results.
	tagged, err := st.SearchInsights(ctx, p.ID, "database", []string{"cache"}, 10)
	require.NoError(t, err)
	require.Empty(t, tagged, "cache tag should exclude PostgreSQL result")

	// Tag names should be searchable even when absent from content.
	byTagName, err := st.SearchInsights(ctx, p.ID, "cache", nil, 10)
	require.NoError(t, err)
	require.Len(t, byTagName, 1, "searching by tag name should find insights tagged with that word")
	require.Contains(t, byTagName[0].Content, "Redis")
}

func TestListInsights(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	u, p := testUserAndProject(t, st, 40)

	for _, content := range []string{"insight one", "insight two", "insight three"} {
		_, err := st.WriteInsight(ctx, store.WriteInsightParams{ProjectID: p.ID, Content: content, Tags: []string{"x"}, CreatedBy: u.ID})
		require.NoError(t, err)
	}

	all, err := st.ListInsights(ctx, p.ID, "", 10)
	require.NoError(t, err)
	require.Len(t, all, 3)

	byTag, err := st.ListInsights(ctx, p.ID, "x", 10)
	require.NoError(t, err)
	require.Len(t, byTag, 3)

	noMatch, err := st.ListInsights(ctx, p.ID, "y", 10)
	require.NoError(t, err)
	require.Empty(t, noMatch)
}

func TestDeleteInsight(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	u, p := testUserAndProject(t, st, 50)

	f, err := st.WriteInsight(ctx, store.WriteInsightParams{ProjectID: p.ID, Content: "to delete", Tags: []string{}, CreatedBy: u.ID})
	require.NoError(t, err)

	require.NoError(t, st.DeleteInsight(ctx, p.OrgID, f.ID))

	// Deleted insight must not appear in list.
	insights, err := st.ListInsights(ctx, p.ID, "", 10)
	require.NoError(t, err)
	require.Empty(t, insights)

	// Double-delete returns ErrNotFound.
	require.ErrorIs(t, st.DeleteInsight(ctx, p.OrgID, f.ID), store.ErrNotFound)
}

func TestDeleteInsight_NotFound(t *testing.T) {
	st := newTestStore(t)
	err := st.DeleteInsight(context.Background(), uuid.New(), uuid.New())
	require.ErrorIs(t, err, store.ErrNotFound)
}

func TestListProjects(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()

	_, err := st.UpsertUser(ctx, 60, "d@example.com", "d")
	require.NoError(t, err)
	u, err := st.GetUserByGitHubID(ctx, 60)
	require.NoError(t, err)
	org, err := st.GetPersonalOrgByUserID(ctx, u.ID)
	require.NoError(t, err)

	// No projects yet.
	projects, err := st.ListProjects(ctx, org.ID)
	require.NoError(t, err)
	require.Empty(t, projects)

	_, err = st.EnsureProject(ctx, org.ID, u.ID, "beta", "Beta")
	require.NoError(t, err)
	_, err = st.EnsureProject(ctx, org.ID, u.ID, "alpha", "Alpha")
	require.NoError(t, err)

	projects, err = st.ListProjects(ctx, org.ID)
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
		_, err := st.WriteInsight(ctx, store.WriteInsightParams{ProjectID: p.ID, Content: "x", Tags: tags, CreatedBy: u.ID})
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

	// Deleted insights must not contribute to counts.
	f, err := st.WriteInsight(ctx, store.WriteInsightParams{ProjectID: p.ID, Content: "gone", Tags: []string{"orphan"}, CreatedBy: u.ID})
	require.NoError(t, err)
	require.NoError(t, st.DeleteInsight(ctx, p.OrgID, f.ID))

	tags, err = st.ListTags(ctx, p.ID, 10)
	require.NoError(t, err)
	for _, tc := range tags {
		require.NotEqual(t, "orphan", tc.Name, "deleted insight tags must not appear")
	}
}

func TestUpdateInsight(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	u, p := testUserAndProject(t, st, 80)

	f, err := st.WriteInsight(ctx, store.WriteInsightParams{
		ProjectID: p.ID,
		Content:   "original content",
		Tags:      []string{"v1"},
		CreatedBy: u.ID,
	})
	require.NoError(t, err)

	// Update content only — tags should be unchanged.
	updated, err := st.UpdateInsight(ctx, store.UpdateInsightParams{OrgID: p.OrgID, InsightID: f.ID, Content: "updated content"})
	require.NoError(t, err)
	require.Equal(t, "updated content", updated.Content)
	require.Equal(t, []string{"v1"}, updated.Tags)

	// Update tags only — content should be unchanged.
	updated, err = st.UpdateInsight(ctx, store.UpdateInsightParams{OrgID: p.OrgID, InsightID: f.ID, Tags: []string{"v2", "patched"}})
	require.NoError(t, err)
	require.Equal(t, "updated content", updated.Content)
	require.Equal(t, []string{"v2", "patched"}, updated.Tags)

	// Clear tags by passing an empty (non-nil) slice.
	updated, err = st.UpdateInsight(ctx, store.UpdateInsightParams{OrgID: p.OrgID, InsightID: f.ID, Tags: []string{}})
	require.NoError(t, err)
	require.Empty(t, updated.Tags)

	// ErrNotFound on a missing insight.
	require.ErrorIs(t, func() error {
		_, err := st.UpdateInsight(ctx, store.UpdateInsightParams{OrgID: p.OrgID, InsightID: uuid.New(), Content: "x"})
		return err
	}(), store.ErrNotFound)

	// ErrNotFound after soft-delete.
	require.NoError(t, st.DeleteInsight(ctx, p.OrgID, f.ID))
	_, err = st.UpdateInsight(ctx, store.UpdateInsightParams{OrgID: p.OrgID, InsightID: f.ID, Content: "too late"})
	require.ErrorIs(t, err, store.ErrNotFound)
}

// --- Grants ---

func TestUpsertGrant_StoresAndRetrievesEncryptedTokens(t *testing.T) {
	st := newTestStoreWithEnc(t, store.NewEncryptor(testEncKey))
	ctx := context.Background()

	u, err := st.UpsertUser(ctx, 100, "grantuser@example.com", "grantuser")
	require.NoError(t, err)

	now := time.Now().UTC().Truncate(time.Second)
	g := store.Grant{
		JTI:                "test-jti-001",
		UserID:             u.ID,
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
	require.Equal(t, g.UserID, got.UserID)
	require.Equal(t, g.AccessToken, got.AccessToken)
	require.Equal(t, g.RefreshToken, got.RefreshToken)
	require.WithinDuration(t, g.AccessTokenExpiry, got.AccessTokenExpiry, time.Second)
	require.WithinDuration(t, g.RefreshTokenExpiry, got.RefreshTokenExpiry, time.Second)
}

func TestUpsertGrant_PrunesExpiredGrants(t *testing.T) {
	st := newTestStoreWithEnc(t, store.NewEncryptor(testEncKey))
	ctx := context.Background()

	u, err := st.UpsertUser(ctx, 200, "pruneuser@example.com", "pruneuser")
	require.NoError(t, err)

	now := time.Now().UTC()
	expired := store.Grant{
		JTI:                "expired-jti",
		UserID:             u.ID,
		ClientID:           "client-A",
		AccessToken:        "old-access",
		RefreshToken:       "old-refresh",
		AccessTokenExpiry:  now.Add(-10 * time.Hour),
		RefreshTokenExpiry: now.Add(-1 * time.Hour),
		JWTExpiry:          now.Add(-1 * time.Second), // already expired
	}
	require.NoError(t, st.UpsertGrant(ctx, expired))

	// Plant an expired grant for a different client — must not be pruned.
	expiredOtherClient := store.Grant{
		JTI:                "expired-other-client-jti",
		UserID:             u.ID,
		ClientID:           "client-B",
		AccessToken:        "other-access",
		RefreshToken:       "other-refresh",
		AccessTokenExpiry:  now.Add(-10 * time.Hour),
		RefreshTokenExpiry: now.Add(-1 * time.Hour),
		JWTExpiry:          now.Add(-1 * time.Second),
	}
	require.NoError(t, st.UpsertGrant(ctx, expiredOtherClient))

	// Confirm expired grants were inserted.
	_, err = st.GetGrant(ctx, "expired-jti")
	require.NoError(t, err)
	_, err = st.GetGrant(ctx, "expired-other-client-jti")
	require.NoError(t, err)

	// Upsert a new grant for client-A — triggers lazy prune scoped to (user, client-A).
	fresh := store.Grant{
		JTI:                "fresh-jti",
		UserID:             u.ID,
		ClientID:           "client-A",
		AccessToken:        "new-access",
		RefreshToken:       "new-refresh",
		AccessTokenExpiry:  now.Add(8 * time.Hour),
		RefreshTokenExpiry: now.Add(180 * 24 * time.Hour),
		JWTExpiry:          now.Add(7 * 24 * time.Hour),
	}
	require.NoError(t, st.UpsertGrant(ctx, fresh))

	_, err = st.GetGrant(ctx, "expired-jti")
	require.ErrorIs(t, err, store.ErrNotFound, "expired client-A grant must be pruned")

	_, err = st.GetGrant(ctx, "fresh-jti")
	require.NoError(t, err, "fresh grant must still exist")

	// client-B's expired grant must be untouched — prune is scoped to (user, client).
	_, err = st.GetGrant(ctx, "expired-other-client-jti")
	require.NoError(t, err, "expired grant for a different client must not be pruned")
}

func TestGetGrant_NotFound(t *testing.T) {
	st := newTestStoreWithEnc(t, store.NewEncryptor(testEncKey))

	_, err := st.GetGrant(context.Background(), "no-such-jti")
	require.ErrorIs(t, err, store.ErrNotFound)
}

func TestRotateGrant_RotatesAndPreservesScope(t *testing.T) {
	st := newTestStoreWithEnc(t, store.NewEncryptor(testEncKey))
	ctx := context.Background()

	u, err := st.UpsertUser(ctx, 400, "rotate@example.com", "rotateuser")
	require.NoError(t, err)

	now := time.Now().UTC().Truncate(time.Second)
	original := store.Grant{
		JTI:                "rotate-jti-old",
		UserID:             u.ID,
		OurRefreshToken:    "our-refresh-old",
		ClientID:           "client-A",
		Scope:              "insights:read insights:write",
		AccessToken:        "gha_old",
		RefreshToken:       "ghr_old",
		AccessTokenExpiry:  now.Add(8 * time.Hour),
		RefreshTokenExpiry: now.Add(180 * 24 * time.Hour),
		JWTExpiry:          now.Add(7 * 24 * time.Hour),
	}
	require.NoError(t, st.UpsertGrant(ctx, original))

	// Sanity-check the seeded grant round-trips, including scope.
	seeded, err := st.GetGrantByRefreshToken(ctx, "our-refresh-old")
	require.NoError(t, err)
	require.Equal(t, "insights:read insights:write", seeded.Scope)
	require.Equal(t, "client-A", seeded.ClientID)

	rotated := store.Grant{
		JTI:                "rotate-jti-new",
		UserID:             u.ID,
		OurRefreshToken:    "our-refresh-new",
		ClientID:           "client-A",
		Scope:              "insights:read insights:write",
		AccessToken:        "gha_new",
		RefreshToken:       "ghr_new",
		AccessTokenExpiry:  now.Add(16 * time.Hour),
		RefreshTokenExpiry: now.Add(184 * 24 * time.Hour),
		JWTExpiry:          now.Add(14 * 24 * time.Hour),
	}

	got, err := st.RotateGrant(ctx, "our-refresh-old", original.JTI, original.JWTExpiry, rotated, nil)
	require.NoError(t, err)
	require.Equal(t, "rotate-jti-new", got.JTI)
	require.Equal(t, "our-refresh-new", got.OurRefreshToken)
	require.Equal(t, "insights:read insights:write", got.Scope, "scope must round-trip through rotation")
	require.Equal(t, "gha_new", got.AccessToken)
	require.Equal(t, "ghr_new", got.RefreshToken)
	require.WithinDuration(t, rotated.JWTExpiry, got.JWTExpiry, time.Second)

	// Old jti must be revoked atomically with the rotation.
	revoked, err := st.IsTokenRevoked(ctx, "rotate-jti-old")
	require.NoError(t, err)
	require.True(t, revoked, "rotation must revoke the old jti in the same transaction")

	// Old refresh token is gone; new one is queryable.
	_, err = st.GetGrantByRefreshToken(ctx, "our-refresh-old")
	require.ErrorIs(t, err, store.ErrNotFound)

	fetched, err := st.GetGrantByRefreshToken(ctx, "our-refresh-new")
	require.NoError(t, err)
	require.Equal(t, "rotate-jti-new", fetched.JTI)
	require.Equal(t, "insights:read insights:write", fetched.Scope)

	// Old jti row no longer exists (UPDATE replaced the primary key).
	_, err = st.GetGrant(ctx, "rotate-jti-old")
	require.ErrorIs(t, err, store.ErrNotFound)
}

func TestUpsertGrant_NoEncryptionKey(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()

	u, err := st.UpsertUser(ctx, 300, "nokey@example.com", "nokey")
	require.NoError(t, err)

	err = st.UpsertGrant(ctx, store.Grant{
		JTI:    "no-key-jti",
		UserID: u.ID,
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
		Scope:                   "insights:read",
		IssuedAt:                now,
		ExpiresAt:               now.Add(90 * 24 * time.Hour),
	}

	require.NoError(t, st.SaveClient(ctx, c))

	got, err := st.GetClient(ctx, c.ClientID)
	require.NoError(t, err)
	require.Equal(t, c.ClientID, got.ClientID)
	require.Equal(t, c.ClientName, got.ClientName)
	require.Equal(t, c.RedirectURIs, got.RedirectURIs)
	require.Equal(t, c.GrantTypes, got.GrantTypes)
	require.Equal(t, c.ResponseTypes, got.ResponseTypes)
	require.Equal(t, c.TokenEndpointAuthMethod, got.TokenEndpointAuthMethod)
	require.Equal(t, c.Scope, got.Scope)
	require.WithinDuration(t, c.IssuedAt, got.IssuedAt, time.Second)
	require.WithinDuration(t, c.ExpiresAt, got.ExpiresAt, time.Second)
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

// --- GetUserByID ---

func TestGetUserByID_Success(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()

	upserted, err := st.UpsertUser(ctx, 900, "id@example.com", "iduser")
	require.NoError(t, err)

	got, err := st.GetUserByID(ctx, upserted.ID)
	require.NoError(t, err)
	require.Equal(t, upserted.ID, got.ID)
	require.Equal(t, int64(900), got.GitHubID)
	require.Equal(t, "id@example.com", got.Email)
}

func TestGetUserByID_NotFound(t *testing.T) {
	st := newTestStore(t)
	_, err := st.GetUserByID(context.Background(), uuid.New())
	require.ErrorIs(t, err, store.ErrNotFound)
}

// --- GetPersonalOrgByUserID ---

func TestGetPersonalOrgByUserID_CreatedOnFirstLogin(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()

	u, err := st.UpsertUser(ctx, 910, "org@example.com", "orguser")
	require.NoError(t, err)

	org, err := st.GetPersonalOrgByUserID(ctx, u.ID)
	require.NoError(t, err)
	require.Equal(t, "orguser", org.Slug)
	require.Equal(t, "personal", org.Kind)
	require.NotEqual(t, uuid.Nil, org.ID)
}

func TestGetPersonalOrgByUserID_NotFound(t *testing.T) {
	st := newTestStore(t)
	_, err := st.GetPersonalOrgByUserID(context.Background(), uuid.New())
	require.ErrorIs(t, err, store.ErrNotFound)
}

// --- UpsertUser slug collision ---

func TestUpsertUser_SameLoginSlugAllowedForMultiplePersonalOrgs(t *testing.T) {
	// Personal org slugs are display-only; two users with the same GitHub login
	// (possible after a username transfer) can both hold that slug without conflict.
	st := newTestStore(t)
	ctx := context.Background()

	u1, err := st.UpsertUser(ctx, 920, "first@example.com", "sharedlogin")
	require.NoError(t, err)

	u2, err := st.UpsertUser(ctx, 921, "second@example.com", "sharedlogin")
	require.NoError(t, err)

	org1, err := st.GetPersonalOrgByUserID(ctx, u1.ID)
	require.NoError(t, err)
	require.Equal(t, "sharedlogin", org1.Slug)

	org2, err := st.GetPersonalOrgByUserID(ctx, u2.ID)
	require.NoError(t, err)
	require.Equal(t, "sharedlogin", org2.Slug, "second user must also get the login slug without conflict")

	require.NotEqual(t, org1.ID, org2.ID, "each user must have their own personal org")
}

// --- StorePendingAuth / ConsumePendingAuth ---

func TestStorePendingAuth_ConsumeSuccess(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()

	p := store.PendingAuth{
		ClientID:      "client-abc",
		RedirectURI:   "https://client.example.com/callback",
		Scope:         "insights:read",
		CodeChallenge: "challenge-xyz",
		ClientState:   "opaque-state",
	}
	require.NoError(t, st.StorePendingAuth(ctx, "state-001", p))

	got, err := st.ConsumePendingAuth(ctx, "state-001")
	require.NoError(t, err)
	require.Equal(t, p.ClientID, got.ClientID)
	require.Equal(t, p.RedirectURI, got.RedirectURI)
	require.Equal(t, p.Scope, got.Scope)
	require.Equal(t, p.CodeChallenge, got.CodeChallenge)
	require.Equal(t, p.ClientState, got.ClientState)
}

func TestConsumePendingAuth_NotFound(t *testing.T) {
	st := newTestStore(t)
	_, err := st.ConsumePendingAuth(context.Background(), "no-such-state")
	require.ErrorIs(t, err, store.ErrNotFound)
}

func TestConsumePendingAuth_SingleUse(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()

	require.NoError(t, st.StorePendingAuth(ctx, "single-use-state", store.PendingAuth{
		RedirectURI:   "https://client.example.com/callback",
		Scope:         "insights:read",
		CodeChallenge: "challenge",
	}))

	_, err := st.ConsumePendingAuth(ctx, "single-use-state")
	require.NoError(t, err)

	_, err = st.ConsumePendingAuth(ctx, "single-use-state")
	require.ErrorIs(t, err, store.ErrNotFound, "second consume must return ErrNotFound")
}

func TestStorePendingAuth_EmptyOptionalFields(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()

	// client_id and client_state are optional — empty string stored as NULL.
	p := store.PendingAuth{
		RedirectURI:   "https://client.example.com/callback",
		Scope:         "insights:read",
		CodeChallenge: "challenge",
	}
	require.NoError(t, st.StorePendingAuth(ctx, "state-noopt", p))

	got, err := st.ConsumePendingAuth(ctx, "state-noopt")
	require.NoError(t, err)
	require.Empty(t, got.ClientID)
	require.Empty(t, got.ClientState)
}

// --- StoreAuthCode / ConsumeAuthCode ---

func TestStoreAuthCode_ConsumeWithTokens(t *testing.T) {
	st := newTestStoreWithEnc(t, store.NewEncryptor(testEncKey))
	ctx := context.Background()

	now := time.Now().UTC().Truncate(time.Second)
	c := store.AuthCode{
		Sub:                "user-uuid-abc",
		GitHubID:           1001,
		Email:              "code@example.com",
		Scope:              "insights:read insights:write",
		CodeChallenge:      "challenge-abc",
		RedirectURI:        "https://client.example.com/callback",
		ClientID:           "client-xyz",
		AccessToken:        "gha_access_test",
		RefreshToken:       "ghr_refresh_test",
		AccessTokenExpiry:  now.Add(8 * time.Hour),
		RefreshTokenExpiry: now.Add(180 * 24 * time.Hour),
	}
	require.NoError(t, st.StoreAuthCode(ctx, "auth-code-001", c))

	got, err := st.ConsumeAuthCode(ctx, "auth-code-001")
	require.NoError(t, err)
	require.Equal(t, c.Sub, got.Sub)
	require.Equal(t, c.GitHubID, got.GitHubID)
	require.Equal(t, c.Email, got.Email)
	require.Equal(t, c.Scope, got.Scope)
	require.Equal(t, c.CodeChallenge, got.CodeChallenge)
	require.Equal(t, c.RedirectURI, got.RedirectURI)
	require.Equal(t, c.ClientID, got.ClientID)
	require.Equal(t, c.AccessToken, got.AccessToken)
	require.Equal(t, c.RefreshToken, got.RefreshToken)
	require.WithinDuration(t, c.AccessTokenExpiry, got.AccessTokenExpiry, time.Second)
	require.WithinDuration(t, c.RefreshTokenExpiry, got.RefreshTokenExpiry, time.Second)
}

func TestStoreAuthCode_ConsumeWithoutTokens(t *testing.T) {
	// Verifies that empty tokens are stored as zero bytes (not NULL) and read back as empty strings.
	st := newTestStoreWithEnc(t, store.NewEncryptor(testEncKey))
	ctx := context.Background()

	c := store.AuthCode{
		Sub:           "user-uuid-notokens",
		GitHubID:      1002,
		Email:         "notokens@example.com",
		Scope:         "insights:read",
		CodeChallenge: "challenge",
		RedirectURI:   "https://client.example.com/callback",
	}
	require.NoError(t, st.StoreAuthCode(ctx, "auth-code-notokens", c))

	got, err := st.ConsumeAuthCode(ctx, "auth-code-notokens")
	require.NoError(t, err)
	require.Empty(t, got.AccessToken)
	require.Empty(t, got.RefreshToken)
	require.True(t, got.AccessTokenExpiry.IsZero())
	require.True(t, got.RefreshTokenExpiry.IsZero())
}

func TestConsumeAuthCode_NotFound(t *testing.T) {
	st := newTestStoreWithEnc(t, store.NewEncryptor(testEncKey))
	_, err := st.ConsumeAuthCode(context.Background(), "no-such-code")
	require.ErrorIs(t, err, store.ErrNotFound)
}

func TestConsumeAuthCode_SingleUse(t *testing.T) {
	st := newTestStoreWithEnc(t, store.NewEncryptor(testEncKey))
	ctx := context.Background()

	require.NoError(t, st.StoreAuthCode(ctx, "single-use-code", store.AuthCode{
		Sub:           "user-uuid",
		GitHubID:      1003,
		Email:         "u@example.com",
		Scope:         "insights:read",
		CodeChallenge: "challenge",
		RedirectURI:   "https://client.example.com/callback",
	}))

	_, err := st.ConsumeAuthCode(ctx, "single-use-code")
	require.NoError(t, err)

	_, err = st.ConsumeAuthCode(ctx, "single-use-code")
	require.ErrorIs(t, err, store.ErrNotFound, "second consume must return ErrNotFound")
}

// --- RevokeToken / IsTokenRevoked ---

func TestRevokeToken_RevokedTokenIsDetected(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()

	jti := uuid.New().String()
	exp := time.Now().Add(time.Hour)

	revoked, err := st.IsTokenRevoked(ctx, jti)
	require.NoError(t, err)
	require.False(t, revoked, "token must not be revoked before RevokeToken is called")

	require.NoError(t, st.RevokeToken(ctx, jti, exp))

	revoked, err = st.IsTokenRevoked(ctx, jti)
	require.NoError(t, err)
	require.True(t, revoked)
}

func TestRevokeToken_Idempotent(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()

	jti := uuid.New().String()
	exp := time.Now().Add(time.Hour)

	require.NoError(t, st.RevokeToken(ctx, jti, exp))
	require.NoError(t, st.RevokeToken(ctx, jti, exp), "second revocation of same jti must not error")

	revoked, err := st.IsTokenRevoked(ctx, jti)
	require.NoError(t, err)
	require.True(t, revoked)
}

func TestIsTokenRevoked_ExpiredEntryReturnsFalse(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()

	jti := uuid.New().String()
	// expires_at in the past — token would have expired naturally, not considered revoked.
	exp := time.Now().Add(-time.Second)

	require.NoError(t, st.RevokeToken(ctx, jti, exp))

	revoked, err := st.IsTokenRevoked(ctx, jti)
	require.NoError(t, err)
	require.False(t, revoked, "expired revocation entry must not count as revoked")
}

// --- GetGrantByRefreshToken / RotateGrant / DeleteGrant ---

func TestGetGrantByRefreshToken_Success(t *testing.T) {
	st := newTestStoreWithEnc(t, store.NewEncryptor(testEncKey))
	ctx := context.Background()

	u, err := st.UpsertUser(ctx, 1100, "rftoken@example.com", "rfuser")
	require.NoError(t, err)

	now := time.Now().UTC().Truncate(time.Second)
	g := store.Grant{
		JTI:                "jti-rftoken-001",
		UserID:             u.ID,
		OurRefreshToken:    "our-refresh-token-abc",
		ClientID:           "client-001",
		AccessToken:        "gha_access",
		RefreshToken:       "ghr_refresh",
		AccessTokenExpiry:  now.Add(8 * time.Hour),
		RefreshTokenExpiry: now.Add(180 * 24 * time.Hour),
		JWTExpiry:          now.Add(7 * 24 * time.Hour),
	}
	require.NoError(t, st.UpsertGrant(ctx, g))

	got, err := st.GetGrantByRefreshToken(ctx, "our-refresh-token-abc")
	require.NoError(t, err)
	require.Equal(t, g.JTI, got.JTI)
	require.Equal(t, g.UserID, got.UserID)
	require.Equal(t, g.OurRefreshToken, got.OurRefreshToken)
	require.Equal(t, g.ClientID, got.ClientID)
	require.Equal(t, g.AccessToken, got.AccessToken)
	require.Equal(t, g.RefreshToken, got.RefreshToken)
}

func TestGetGrantByRefreshToken_NotFound(t *testing.T) {
	st := newTestStoreWithEnc(t, store.NewEncryptor(testEncKey))
	_, err := st.GetGrantByRefreshToken(context.Background(), "no-such-token")
	require.ErrorIs(t, err, store.ErrNotFound)
}

func TestRotateGrant_Success(t *testing.T) {
	st := newTestStoreWithEnc(t, store.NewEncryptor(testEncKey))
	ctx := context.Background()

	u, err := st.UpsertUser(ctx, 1200, "rotate@example.com", "rotateuser")
	require.NoError(t, err)

	now := time.Now().UTC().Truncate(time.Second)
	original := store.Grant{
		JTI:                "jti-rotate-old",
		UserID:             u.ID,
		OurRefreshToken:    "old-refresh-token",
		ClientID:           "client-rotate",
		AccessToken:        "gha_old_access",
		RefreshToken:       "ghr_old_refresh",
		AccessTokenExpiry:  now.Add(8 * time.Hour),
		RefreshTokenExpiry: now.Add(180 * 24 * time.Hour),
		JWTExpiry:          now.Add(7 * 24 * time.Hour),
	}
	require.NoError(t, st.UpsertGrant(ctx, original))

	rotated := store.Grant{
		JTI:                "jti-rotate-new",
		UserID:             u.ID,
		OurRefreshToken:    "new-refresh-token",
		ClientID:           "client-rotate",
		AccessToken:        "gha_new_access",
		RefreshToken:       "ghr_new_refresh",
		AccessTokenExpiry:  now.Add(8 * time.Hour),
		RefreshTokenExpiry: now.Add(179 * 24 * time.Hour),
		JWTExpiry:          now.Add(7 * 24 * time.Hour),
	}

	got, err := st.RotateGrant(ctx, "old-refresh-token", original.JTI, original.JWTExpiry, rotated, nil)
	require.NoError(t, err)
	require.Equal(t, rotated.JTI, got.JTI)
	require.Equal(t, rotated.OurRefreshToken, got.OurRefreshToken)
	require.Equal(t, rotated.AccessToken, got.AccessToken)
	require.Equal(t, rotated.RefreshToken, got.RefreshToken)

	// Old token must no longer be findable.
	_, err = st.GetGrantByRefreshToken(ctx, "old-refresh-token")
	require.ErrorIs(t, err, store.ErrNotFound, "old refresh token must be gone after rotation")

	// New token must be findable.
	found, err := st.GetGrantByRefreshToken(ctx, "new-refresh-token")
	require.NoError(t, err)
	require.Equal(t, rotated.JTI, found.JTI)
}

func TestRotateGrant_RecordsRetiredRefreshToken(t *testing.T) {
	st := newTestStoreWithEnc(t, store.NewEncryptor(testEncKey))
	ctx := context.Background()

	u, err := st.UpsertUser(ctx, 1250, "retired@example.com", "retireduser")
	require.NoError(t, err)

	now := time.Now().UTC().Truncate(time.Second)
	original := store.Grant{
		JTI:                "jti-retired-old",
		UserID:             u.ID,
		OurRefreshToken:    "retired-old-refresh",
		ClientID:           "client-retired",
		AccessToken:        "gha_old",
		RefreshToken:       "ghr_old",
		AccessTokenExpiry:  now.Add(8 * time.Hour),
		RefreshTokenExpiry: now.Add(180 * 24 * time.Hour),
		JWTExpiry:          now.Add(7 * 24 * time.Hour),
	}
	require.NoError(t, st.UpsertGrant(ctx, original))

	rotated := store.Grant{
		JTI:                "jti-retired-new",
		UserID:             u.ID,
		OurRefreshToken:    "retired-new-refresh",
		ClientID:           "client-retired",
		AccessToken:        "gha_new",
		RefreshToken:       "ghr_new",
		AccessTokenExpiry:  now.Add(8 * time.Hour),
		RefreshTokenExpiry: now.Add(179 * 24 * time.Hour),
		JWTExpiry:          now.Add(7 * 24 * time.Hour),
	}
	graceExpires := now.Add(30 * time.Second)
	retainedUntil := now.Add(24 * time.Hour)
	_, err = st.RotateGrant(ctx, "retired-old-refresh", original.JTI, original.JWTExpiry, rotated, &store.RetiredRefreshToken{
		TokenHash:      store.HashRefreshToken("retired-old-refresh"),
		Reason:         store.RetiredRefreshTokenReasonRotated,
		UserID:         u.ID,
		ClientID:       original.ClientID,
		OldJTI:         original.JTI,
		ReplacementJTI: rotated.JTI,
		GraceExpiresAt: graceExpires,
		RetainedUntil:  retainedUntil,
	})
	require.NoError(t, err)

	retired, err := st.GetRetiredRefreshToken(ctx, store.HashRefreshToken("retired-old-refresh"))
	require.NoError(t, err)
	require.Equal(t, store.RetiredRefreshTokenReasonRotated, retired.Reason)
	require.Equal(t, u.ID, retired.UserID)
	require.Equal(t, original.ClientID, retired.ClientID)
	require.Equal(t, original.JTI, retired.OldJTI)
	require.Equal(t, rotated.JTI, retired.ReplacementJTI)
	require.WithinDuration(t, graceExpires, retired.GraceExpiresAt, time.Second)
	require.WithinDuration(t, retainedUntil, retired.RetainedUntil, time.Second)
}

func TestRotateGrant_RollsBackRetiredTokenFailure(t *testing.T) {
	st := newTestStoreWithEnc(t, store.NewEncryptor(testEncKey))
	ctx := context.Background()

	u, err := st.UpsertUser(ctx, 1260, "rollback@example.com", "rollbackuser")
	require.NoError(t, err)

	now := time.Now().UTC().Truncate(time.Second)
	original := store.Grant{
		JTI:                "jti-rollback-old",
		UserID:             u.ID,
		OurRefreshToken:    "rollback-old-refresh",
		ClientID:           "client-rollback",
		AccessToken:        "gha_old",
		RefreshToken:       "ghr_old",
		AccessTokenExpiry:  now.Add(8 * time.Hour),
		RefreshTokenExpiry: now.Add(180 * 24 * time.Hour),
		JWTExpiry:          now.Add(7 * 24 * time.Hour),
	}
	require.NoError(t, st.UpsertGrant(ctx, original))

	_, err = st.RotateGrant(ctx, "rollback-old-refresh", original.JTI, original.JWTExpiry, store.Grant{
		JTI:                "jti-rollback-new",
		UserID:             u.ID,
		OurRefreshToken:    "rollback-new-refresh",
		ClientID:           "client-rollback",
		AccessToken:        "gha_new",
		RefreshToken:       "ghr_new",
		AccessTokenExpiry:  now.Add(8 * time.Hour),
		RefreshTokenExpiry: now.Add(179 * 24 * time.Hour),
		JWTExpiry:          now.Add(7 * 24 * time.Hour),
	}, &store.RetiredRefreshToken{
		TokenHash: store.HashRefreshToken("rollback-old-refresh"),
		// Missing Reason should abort the transaction.
		RetainedUntil: now.Add(24 * time.Hour),
	})
	require.Error(t, err)

	_, err = st.GetGrantByRefreshToken(ctx, "rollback-old-refresh")
	require.NoError(t, err, "old grant must remain live after failed retired-token insert")
	_, err = st.GetGrantByRefreshToken(ctx, "rollback-new-refresh")
	require.ErrorIs(t, err, store.ErrNotFound)
	_, err = st.GetRetiredRefreshToken(ctx, store.HashRefreshToken("rollback-old-refresh"))
	require.ErrorIs(t, err, store.ErrNotFound)
}

func TestGetRetiredRefreshToken_PrunesExpiredRetention(t *testing.T) {
	st := newTestStoreWithEnc(t, store.NewEncryptor(testEncKey))
	ctx := context.Background()

	u, err := st.UpsertUser(ctx, 1270, "pruneretired@example.com", "pruneretired")
	require.NoError(t, err)

	now := time.Now().UTC().Truncate(time.Second)
	original := store.Grant{
		JTI:                "jti-retention-old",
		UserID:             u.ID,
		OurRefreshToken:    "retention-old-refresh",
		ClientID:           "client-retention",
		AccessToken:        "gha_old",
		RefreshToken:       "ghr_old",
		AccessTokenExpiry:  now.Add(8 * time.Hour),
		RefreshTokenExpiry: now.Add(180 * 24 * time.Hour),
		JWTExpiry:          now.Add(7 * 24 * time.Hour),
	}
	require.NoError(t, st.UpsertGrant(ctx, original))

	_, err = st.RotateGrant(ctx, "retention-old-refresh", original.JTI, original.JWTExpiry, store.Grant{
		JTI:                "jti-retention-new",
		UserID:             u.ID,
		OurRefreshToken:    "retention-new-refresh",
		ClientID:           "client-retention",
		AccessToken:        "gha_new",
		RefreshToken:       "ghr_new",
		AccessTokenExpiry:  now.Add(8 * time.Hour),
		RefreshTokenExpiry: now.Add(179 * 24 * time.Hour),
		JWTExpiry:          now.Add(7 * 24 * time.Hour),
	}, &store.RetiredRefreshToken{
		TokenHash:      store.HashRefreshToken("retention-old-refresh"),
		Reason:         store.RetiredRefreshTokenReasonRotated,
		UserID:         u.ID,
		ClientID:       original.ClientID,
		OldJTI:         original.JTI,
		ReplacementJTI: "jti-retention-new",
		RetainedUntil:  now.Add(-time.Second),
	})
	require.NoError(t, err)

	_, err = st.GetRetiredRefreshToken(ctx, store.HashRefreshToken("retention-old-refresh"))
	require.ErrorIs(t, err, store.ErrNotFound)
}

func TestRotateGrant_NotFoundOnConcurrentRace(t *testing.T) {
	st := newTestStoreWithEnc(t, store.NewEncryptor(testEncKey))
	ctx := context.Background()

	u, err := st.UpsertUser(ctx, 1300, "race@example.com", "raceuser")
	require.NoError(t, err)

	now := time.Now().UTC().Truncate(time.Second)
	require.NoError(t, st.UpsertGrant(ctx, store.Grant{
		JTI:                "jti-race",
		UserID:             u.ID,
		OurRefreshToken:    "race-token",
		AccessToken:        "gha_access",
		RefreshToken:       "ghr_refresh",
		AccessTokenExpiry:  now.Add(8 * time.Hour),
		RefreshTokenExpiry: now.Add(180 * 24 * time.Hour),
		JWTExpiry:          now.Add(7 * 24 * time.Hour),
	}))

	// First rotation succeeds.
	_, err = st.RotateGrant(ctx, "race-token", "jti-race", now.Add(7*24*time.Hour), store.Grant{
		JTI:             "jti-race-rotated",
		UserID:          u.ID,
		OurRefreshToken: "race-token-rotated",
		AccessToken:     "gha_new",
		RefreshToken:    "ghr_new",
		JWTExpiry:       now.Add(7 * 24 * time.Hour),
	}, nil)
	require.NoError(t, err)

	// Second rotation with the same old token simulates a concurrent race — must return ErrNotFound.
	_, err = st.RotateGrant(ctx, "race-token", "jti-race", now.Add(7*24*time.Hour), store.Grant{
		JTI:             "jti-race-rotated-2",
		UserID:          u.ID,
		OurRefreshToken: "race-token-rotated-2",
		AccessToken:     "gha_new2",
		RefreshToken:    "ghr_new2",
		JWTExpiry:       now.Add(7 * 24 * time.Hour),
	}, nil)
	require.ErrorIs(t, err, store.ErrNotFound, "concurrent rotation must return ErrNotFound")
}

func TestDeleteGrant_Success(t *testing.T) {
	st := newTestStoreWithEnc(t, store.NewEncryptor(testEncKey))
	ctx := context.Background()

	u, err := st.UpsertUser(ctx, 1400, "del@example.com", "deluser")
	require.NoError(t, err)

	now := time.Now().UTC().Truncate(time.Second)
	require.NoError(t, st.UpsertGrant(ctx, store.Grant{
		JTI:                "jti-delete",
		UserID:             u.ID,
		AccessToken:        "gha_access",
		RefreshToken:       "ghr_refresh",
		AccessTokenExpiry:  now.Add(8 * time.Hour),
		RefreshTokenExpiry: now.Add(180 * 24 * time.Hour),
		JWTExpiry:          now.Add(7 * 24 * time.Hour),
	}))

	require.NoError(t, st.DeleteGrant(ctx, "jti-delete", &store.RetiredRefreshToken{
		TokenHash:     store.HashRefreshToken("deleted-refresh-token"),
		Reason:        store.RetiredRefreshTokenReasonGitHubExpired,
		UserID:        u.ID,
		ClientID:      "client-delete",
		OldJTI:        "jti-delete",
		RetainedUntil: now.Add(24 * time.Hour),
	}))

	_, err = st.GetGrant(ctx, "jti-delete")
	require.ErrorIs(t, err, store.ErrNotFound, "deleted grant must not be retrievable")
	retired, err := st.GetRetiredRefreshToken(ctx, store.HashRefreshToken("deleted-refresh-token"))
	require.NoError(t, err)
	require.Equal(t, store.RetiredRefreshTokenReasonGitHubExpired, retired.Reason)
	require.Equal(t, "jti-delete", retired.OldJTI)
}

func TestDeleteGrant_NotFound(t *testing.T) {
	st := newTestStoreWithEnc(t, store.NewEncryptor(testEncKey))
	err := st.DeleteGrant(context.Background(), "no-such-jti", nil)
	require.ErrorIs(t, err, store.ErrNotFound)
}
