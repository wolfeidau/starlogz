package postgres_test

import (
	"encoding/json"
	"log/slog"
	"os"
	"sort"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
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
	st, _ := newTestStoreWithEncAndDSN(t, enc)
	return st
}

func newTestStoreAndDSN(t *testing.T) (*postgres.Store, string) {
	t.Helper()
	return newTestStoreWithEncAndDSN(t, nil)
}

func newTestStoreWithEncAndDSN(t *testing.T, enc *store.Encryptor) (*postgres.Store, string) {
	t.Helper()
	ctx := t.Context()
	dsn := testDB.NewDSN(t)

	st, err := postgres.New(ctx, dsn, enc)
	require.NoError(t, err)
	t.Cleanup(st.Close)

	return st, dsn
}

func TestPing(t *testing.T) {
	st := newTestStore(t)
	require.NoError(t, st.Ping(t.Context()))
}

func TestMigrateRepairsInvalidConcurrentIndex(t *testing.T) {
	st, dsn := newTestStoreAndDSN(t)
	ctx := t.Context()
	u, p := testUserAndProject(t, st, 2)
	for range 2 {
		_, err := st.WriteInsight(ctx, store.WriteInsightParams{ProjectID: p.ID, Content: "duplicate", CreatedBy: u.ID})
		require.NoError(t, err)
	}

	pool, err := pgxpool.New(ctx, dsn)
	require.NoError(t, err)
	t.Cleanup(pool.Close)
	_, err = pool.Exec(ctx, `DELETE FROM schema_migrations WHERE version = 18`)
	require.NoError(t, err)
	_, err = pool.Exec(ctx, `DROP INDEX insights_project_updated_live`)
	require.NoError(t, err)
	_, err = pool.Exec(ctx, `CREATE UNIQUE INDEX CONCURRENTLY insights_project_updated_live ON insights (content)`)
	require.Error(t, err)

	var valid bool
	err = pool.QueryRow(ctx, `SELECT indisvalid FROM pg_catalog.pg_index WHERE indexrelid = to_regclass('insights_project_updated_live')`).Scan(&valid)
	require.NoError(t, err)
	require.False(t, valid)

	require.NoError(t, st.Migrate(ctx, slog.New(slog.DiscardHandler)))
	var definition string
	err = pool.QueryRow(ctx, `SELECT indisvalid, pg_get_indexdef(indexrelid) FROM pg_catalog.pg_index WHERE indexrelid = to_regclass('insights_project_updated_live')`).Scan(&valid, &definition)
	require.NoError(t, err)
	require.True(t, valid)
	require.Contains(t, definition, "project_id, updated_at DESC, id DESC")
	require.Contains(t, definition, "WHERE (deleted_at IS NULL)")

	var indexOID uint32
	require.NoError(t, pool.QueryRow(ctx, `SELECT to_regclass('insights_project_updated_live')::oid`).Scan(&indexOID))
	_, err = pool.Exec(ctx, `DELETE FROM schema_migrations WHERE version = 18`)
	require.NoError(t, err)
	require.NoError(t, st.Migrate(ctx, slog.New(slog.DiscardHandler)))
	var reusedIndexOID uint32
	require.NoError(t, pool.QueryRow(ctx, `SELECT to_regclass('insights_project_updated_live')::oid`).Scan(&reusedIndexOID))
	require.Equal(t, indexOID, reusedIndexOID)
}

func TestUpsertUser_NewAndUpdate(t *testing.T) {
	st := newTestStore(t)
	ctx := t.Context()

	_, err := st.UpsertUser(ctx, store.GitHubProfile{
		GitHubID: 12345, Email: "alice@example.com", Login: "alice", DisplayName: "Alice",
		AvatarURL: "https://avatars.example/alice", ProfileURL: "https://example/alice", Bio: "Builder",
	})
	require.NoError(t, err)

	u, err := st.GetUserByGitHubID(ctx, 12345)
	require.NoError(t, err)
	require.Equal(t, int64(12345), u.GitHubID)
	require.Equal(t, "alice@example.com", u.Email)
	require.Equal(t, "alice", u.Login)
	require.Equal(t, "Alice", u.DisplayName)
	require.Equal(t, "https://avatars.example/alice", u.AvatarURL)
	require.Equal(t, "https://example/alice", u.ProfileURL)
	require.Equal(t, "Builder", u.Bio)
	require.NotEqual(t, uuid.Nil, u.ID)

	// Update email and login on re-upsert.
	_, err = st.UpsertUser(ctx, store.GitHubProfile{
		GitHubID: 12345, Email: "alice2@example.com", Login: "alice2", DisplayName: "Alice Updated",
	})
	require.NoError(t, err)
	u2, err := st.GetUserByGitHubID(ctx, 12345)
	require.NoError(t, err)
	require.Equal(t, u.ID, u2.ID, "ID must not change on upsert")
	require.Equal(t, "alice2@example.com", u2.Email)
	require.Equal(t, "alice2", u2.Login)
	require.Equal(t, "Alice Updated", u2.DisplayName)
	require.Empty(t, u2.AvatarURL)
}

func TestWebSessionLifecycle(t *testing.T) {
	st := newTestStore(t)
	ctx := t.Context()
	user, err := st.UpsertUser(ctx, store.GitHubProfile{GitHubID: 12346, Email: "web@example.com", Login: "web"})
	require.NoError(t, err)

	rawToken := "browser-session-token"
	now := time.Now()
	created, err := st.CreateWebSession(ctx, store.WebSession{
		TokenHash: store.HashSessionToken(rawToken), UserID: user.ID,
		IdleExpiresAt: now.Add(7 * 24 * time.Hour), ExpiresAt: now.Add(30 * 24 * time.Hour),
	})
	require.NoError(t, err)
	require.NotEqual(t, uuid.Nil, created.ID)
	audit, err := st.ListAuditLog(ctx, store.AuditLogFilter{TableName: "web_sessions"})
	require.NoError(t, err)
	require.Len(t, audit, 1)
	require.NotContains(t, string(audit[0].NewData), "token_hash")

	got, err := st.GetWebSessionByTokenHash(ctx, store.HashSessionToken(rawToken))
	require.NoError(t, err)
	require.Equal(t, user.ID, got.UserID)
	_, err = st.GetWebSessionByTokenHash(ctx, []byte(rawToken))
	require.ErrorIs(t, err, store.ErrNotFound)

	newSeen := now.Add(time.Hour)
	require.NoError(t, st.TouchWebSession(ctx, got.ID, newSeen, now.Add(8*24*time.Hour)))
	touched, err := st.GetWebSessionByTokenHash(ctx, store.HashSessionToken(rawToken))
	require.NoError(t, err)
	require.WithinDuration(t, newSeen, touched.LastSeenAt, time.Second)
	audit, err = st.ListAuditLog(ctx, store.AuditLogFilter{TableName: "web_sessions"})
	require.NoError(t, err)
	require.Len(t, audit, 1, "activity touches must not create audit churn")

	require.NoError(t, st.RevokeWebSessionByTokenHash(ctx, store.HashSessionToken(rawToken)))
	audit, err = st.ListAuditLog(ctx, store.AuditLogFilter{TableName: "web_sessions"})
	require.NoError(t, err)
	require.Len(t, audit, 2)
	require.Equal(t, "UPDATE", audit[0].Operation)
	require.NotContains(t, string(audit[0].OldData), "token_hash")
	require.NotContains(t, string(audit[0].NewData), "token_hash")
	_, err = st.GetWebSessionByTokenHash(ctx, store.HashSessionToken(rawToken))
	require.ErrorIs(t, err, store.ErrNotFound)
}

func TestWebSessionExpiredNotFound(t *testing.T) {
	st := newTestStore(t)
	ctx := t.Context()
	user, err := st.UpsertUser(ctx, store.GitHubProfile{GitHubID: 12347, Email: "expired@example.com", Login: "expired"})
	require.NoError(t, err)

	_, err = st.CreateWebSession(ctx, store.WebSession{
		TokenHash: store.HashSessionToken("expired-token"), UserID: user.ID,
		IdleExpiresAt: time.Now().Add(-time.Hour), ExpiresAt: time.Now().Add(time.Hour),
	})
	require.NoError(t, err)
	_, err = st.GetWebSessionByTokenHash(ctx, store.HashSessionToken("expired-token"))
	require.ErrorIs(t, err, store.ErrNotFound)
}

func TestGetUserByGitHubID_NotFound(t *testing.T) {
	st := newTestStore(t)
	_, err := st.GetUserByGitHubID(t.Context(), 999999)
	require.ErrorIs(t, err, store.ErrNotFound)
}

func TestEnsureProject_CreateAndIdempotent(t *testing.T) {
	st := newTestStore(t)
	ctx := t.Context()

	_, err := st.UpsertUser(ctx, store.GitHubProfile{GitHubID: 1, Email: "bob@example.com", Login: "bob"})
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
	ctx := t.Context()

	_, err := st.UpsertUser(ctx, store.GitHubProfile{GitHubID: 2, Email: "c@example.com", Login: "c"})
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
	ctx := t.Context()
	u, err := st.UpsertUser(ctx, store.GitHubProfile{GitHubID: githubID, Email: "u@example.com", Login: "u"})
	require.NoError(t, err)
	org, err := st.GetPersonalOrgByUserID(ctx, u.ID)
	require.NoError(t, err)
	p, err := st.EnsureProject(ctx, org.ID, u.ID, "proj", "Project")
	require.NoError(t, err)
	return u, p
}

func TestWriteInsight_InsertAndUpsertByKey(t *testing.T) {
	st := newTestStore(t)
	ctx := t.Context()
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
	ctx := t.Context()
	u, p := testUserAndProject(t, st, 20)

	f1, err := st.WriteInsight(ctx, store.WriteInsightParams{ProjectID: p.ID, Content: "first", Tags: []string{}, CreatedBy: u.ID})
	require.NoError(t, err)

	f2, err := st.WriteInsight(ctx, store.WriteInsightParams{ProjectID: p.ID, Content: "second", Tags: []string{}, CreatedBy: u.ID})
	require.NoError(t, err)

	require.NotEqual(t, f1.ID, f2.ID, "keyless insights get distinct IDs")
	require.Empty(t, f1.Key)
	require.Empty(t, f2.Key)
}

func TestInsightLinks_WarningsAndSynchronization(t *testing.T) {
	st := newTestStore(t)
	ctx := t.Context()
	u, p := testUserAndProject(t, st, 25)
	baseline := insightLinkAuditOperations(t, st)

	_, err := st.WriteInsight(ctx, store.WriteInsightParams{
		ProjectID: p.ID, Key: "target-a", Content: "target", CreatedBy: u.ID,
	})
	require.NoError(t, err)
	source, err := st.WriteInsight(ctx, store.WriteInsightParams{
		ProjectID: p.ID,
		Key:       "source",
		Content:   "[[insight:target-a]] [[insight:missing]] [[insight:source]] [[insight:missing|again]]",
		CreatedBy: u.ID,
	})
	require.NoError(t, err)
	require.Equal(t, []store.InsightLinkWarning{
		{Code: store.InsightLinkWarningUnresolved, TargetKey: "missing"},
		{Code: store.InsightLinkWarningSelf, TargetKey: "source"},
	}, source.LinkWarnings)
	require.Equal(t, map[string]int{"INSERT": 2}, operationDelta(baseline, insightLinkAuditOperations(t, st)))

	_, err = st.WriteInsight(ctx, store.WriteInsightParams{
		ProjectID: p.ID,
		Key:       "source",
		Content:   source.Content,
		CreatedBy: u.ID,
	})
	require.NoError(t, err)
	require.Equal(t, map[string]int{"INSERT": 2}, operationDelta(baseline, insightLinkAuditOperations(t, st)), "unchanged links must be preserved")

	targetPage, err := st.ListInsights(ctx, store.ListInsightsParams{ProjectID: p.ID, Limit: 10})
	require.NoError(t, err)
	var targetID uuid.UUID
	for _, insight := range targetPage.Insights {
		if insight.Key == "target-a" {
			targetID = insight.ID
		}
	}
	require.NotEqual(t, uuid.Nil, targetID)
	require.NoError(t, st.DeleteInsight(ctx, p.OrgID, targetID))

	source, err = st.WriteInsight(ctx, store.WriteInsightParams{
		ProjectID: p.ID,
		Key:       "source",
		Content:   source.Content,
		CreatedBy: u.ID,
	})
	require.NoError(t, err)
	require.Equal(t, []store.InsightLinkWarning{
		{Code: store.InsightLinkWarningUnresolved, TargetKey: "missing"},
		{Code: store.InsightLinkWarningSelf, TargetKey: "source"},
		{Code: store.InsightLinkWarningUnresolved, TargetKey: "target-a"},
	}, source.LinkWarnings)

	_, err = st.WriteInsight(ctx, store.WriteInsightParams{
		ProjectID: p.ID, Key: "target-a", Content: "recreated", CreatedBy: u.ID,
	})
	require.NoError(t, err)
	source, err = st.UpdateInsight(ctx, store.UpdateInsightParams{
		OrgID: p.OrgID, InsightID: source.ID, Content: "[[insight:target-a]]",
	})
	require.NoError(t, err)
	require.Empty(t, source.LinkWarnings)
	require.Equal(t, map[string]int{"INSERT": 2, "DELETE": 1}, operationDelta(baseline, insightLinkAuditOperations(t, st)))

	_, err = st.UpdateInsight(ctx, store.UpdateInsightParams{
		OrgID: p.OrgID, InsightID: source.ID, Tags: []string{"preserved"},
	})
	require.NoError(t, err)
	require.Equal(t, map[string]int{"INSERT": 2, "DELETE": 1}, operationDelta(baseline, insightLinkAuditOperations(t, st)), "tag-only updates must not touch links")

	_, err = st.UpdateInsight(ctx, store.UpdateInsightParams{
		OrgID: p.OrgID, InsightID: source.ID, Content: "no links",
	})
	require.NoError(t, err)
	require.Equal(t, map[string]int{"INSERT": 2, "DELETE": 2}, operationDelta(baseline, insightLinkAuditOperations(t, st)))
}

func TestInsightLinks_KeylessSourceAndProjectIsolation(t *testing.T) {
	st := newTestStore(t)
	ctx := t.Context()
	u, p := testUserAndProject(t, st, 26)
	baseline := insightLinkAuditOperations(t, st)
	other, err := st.EnsureProject(ctx, p.OrgID, u.ID, "other", "Other")
	require.NoError(t, err)
	_, err = st.WriteInsight(ctx, store.WriteInsightParams{
		ProjectID: other.ID, Key: "other-target", Content: "target", CreatedBy: u.ID,
	})
	require.NoError(t, err)

	source, err := st.WriteInsight(ctx, store.WriteInsightParams{
		ProjectID: p.ID, Content: "[[insight:other-target]]", CreatedBy: u.ID,
	})
	require.NoError(t, err)
	require.Empty(t, source.Key)
	require.Equal(t, []store.InsightLinkWarning{{
		Code: store.InsightLinkWarningUnresolved, TargetKey: "other-target",
	}}, source.LinkWarnings)
	require.Equal(t, map[string]int{"INSERT": 1}, operationDelta(baseline, insightLinkAuditOperations(t, st)))
}

func TestGetInsight_Relationships(t *testing.T) {
	st := newTestStore(t)
	ctx := t.Context()
	u, p := testUserAndProject(t, st, 29)
	base := time.Date(2026, time.July, 14, 10, 0, 0, 0, time.UTC)

	root, err := st.WriteInsight(ctx, store.WriteInsightParams{
		ProjectID: p.ID, Key: "root", Content: "root", CreatedBy: u.ID,
		CreatedAt: base, UpdatedAt: base,
	})
	require.NoError(t, err)
	alpha, err := st.WriteInsight(ctx, store.WriteInsightParams{
		ProjectID: p.ID, Key: "alpha", Content: "alpha", Category: "fact", CreatedBy: u.ID,
		CreatedAt: base, UpdatedAt: base,
	})
	require.NoError(t, err)
	beta, err := st.WriteInsight(ctx, store.WriteInsightParams{
		ProjectID: p.ID, Key: "beta", Content: "beta", Category: "decision", CreatedBy: u.ID,
		CreatedAt: base, UpdatedAt: base,
	})
	require.NoError(t, err)

	source, err := st.WriteInsight(ctx, store.WriteInsightParams{
		ProjectID: p.ID, Key: "source", Content: "[[insight:missing]] [[insight:beta]] [[insight:alpha]] [[insight:root]]", CreatedBy: u.ID,
		CreatedAt: base.Add(time.Minute), UpdatedAt: base.Add(time.Minute),
	})
	require.NoError(t, err)
	keyless, err := st.WriteInsight(ctx, store.WriteInsightParams{
		ProjectID: p.ID, Content: "[[insight:root]]", CreatedBy: u.ID,
		CreatedAt: base.Add(2 * time.Minute), UpdatedAt: base.Add(2 * time.Minute),
	})
	require.NoError(t, err)
	newest, err := st.WriteInsight(ctx, store.WriteInsightParams{
		ProjectID: p.ID, Key: "newest", Content: "[[insight:root]]", CreatedBy: u.ID,
		CreatedAt: base.Add(3 * time.Minute), UpdatedAt: base.Add(3 * time.Minute),
	})
	require.NoError(t, err)

	detail, err := st.GetInsight(ctx, store.GetInsightParams{ProjectID: p.ID, Key: "source", RelationLimit: 2})
	require.NoError(t, err)
	require.Equal(t, source.ID, detail.Insight.ID)
	require.Equal(t, 4, detail.LinkCount)
	require.True(t, detail.LinksTruncated)
	require.Equal(t, []store.InsightLinkReference{
		{TargetKey: "alpha", Resolved: true, ID: alpha.ID, Category: "fact", UpdatedAt: alpha.UpdatedAt},
		{TargetKey: "beta", Resolved: true, ID: beta.ID, Category: "decision", UpdatedAt: beta.UpdatedAt},
	}, detail.Links)
	require.Zero(t, detail.BacklinkCount)
	require.Empty(t, detail.Backlinks)

	detail, err = st.GetInsight(ctx, store.GetInsightParams{ProjectID: p.ID, InsightID: source.ID, RelationLimit: 10})
	require.NoError(t, err)
	require.Equal(t, []string{"alpha", "beta", "missing", "root"}, []string{
		detail.Links[0].TargetKey, detail.Links[1].TargetKey, detail.Links[2].TargetKey, detail.Links[3].TargetKey,
	})
	require.False(t, detail.Links[2].Resolved)
	require.Equal(t, uuid.Nil, detail.Links[2].ID)

	detail, err = st.GetInsight(ctx, store.GetInsightParams{ProjectID: p.ID, Key: "root", RelationLimit: 2})
	require.NoError(t, err)
	require.Equal(t, root.ID, detail.Insight.ID)
	require.Equal(t, 3, detail.BacklinkCount)
	require.True(t, detail.BacklinksTruncated)
	require.Equal(t, []uuid.UUID{newest.ID, keyless.ID}, []uuid.UUID{detail.Backlinks[0].ID, detail.Backlinks[1].ID})
	require.Equal(t, "", detail.Backlinks[1].Key)

	require.NoError(t, st.DeleteInsight(ctx, p.OrgID, alpha.ID))
	detail, err = st.GetInsight(ctx, store.GetInsightParams{ProjectID: p.ID, Key: "source", RelationLimit: 10})
	require.NoError(t, err)
	require.False(t, detail.Links[0].Resolved)
	_, err = st.WriteInsight(ctx, store.WriteInsightParams{ProjectID: p.ID, Key: "alpha", Content: "recreated", CreatedBy: u.ID})
	require.NoError(t, err)
	detail, err = st.GetInsight(ctx, store.GetInsightParams{ProjectID: p.ID, Key: "source", RelationLimit: 10})
	require.NoError(t, err)
	require.True(t, detail.Links[0].Resolved)

	require.NoError(t, st.DeleteInsight(ctx, p.OrgID, root.ID))
	_, err = st.GetInsight(ctx, store.GetInsightParams{ProjectID: p.ID, InsightID: root.ID, RelationLimit: 10})
	require.ErrorIs(t, err, store.ErrNotFound)
}

func TestImportProjects_SynchronizesInsightLinks(t *testing.T) {
	st := newTestStore(t)
	ctx := t.Context()
	u, p := testUserAndProject(t, st, 27)
	baseline := insightLinkAuditOperations(t, st)

	projectCount, insightCount, err := st.ImportProjects(ctx, p.OrgID, u.ID, []store.ImportProject{{
		Slug: "imported",
		Name: "Imported",
		Insights: []store.ImportInsight{
			{Key: "source", Content: "[[insight:target]]"},
			{Key: "target", Content: "target"},
		},
	}})
	require.NoError(t, err)
	require.Equal(t, 1, projectCount)
	require.Equal(t, 2, insightCount)
	require.Equal(t, map[string]int{"INSERT": 1}, operationDelta(baseline, insightLinkAuditOperations(t, st)))
}

func TestImportProjects_RollsBackInsightLinks(t *testing.T) {
	st := newTestStore(t)
	ctx := t.Context()
	u, p := testUserAndProject(t, st, 28)
	baseline := insightLinkAuditOperations(t, st)

	_, _, err := st.ImportProjects(ctx, p.OrgID, u.ID, []store.ImportProject{{
		Slug: "rollback-links",
		Name: "Rollback Links",
		Insights: []store.ImportInsight{
			{Key: "source", Content: "[[insight:target]]"},
			{Key: "invalid", Content: "invalid", Category: "not-a-category"},
		},
	}})
	require.Error(t, err)
	require.Empty(t, operationDelta(baseline, insightLinkAuditOperations(t, st)))
	_, err = st.GetProjectBySlug(ctx, p.OrgID, "rollback-links")
	require.ErrorIs(t, err, store.ErrNotFound)
}

func operationDelta(before, after map[string]int) map[string]int {
	delta := make(map[string]int)
	for operation, count := range after {
		if count -= before[operation]; count != 0 {
			delta[operation] = count
		}
	}
	return delta
}

func insightLinkAuditOperations(t *testing.T, st *postgres.Store) map[string]int {
	t.Helper()
	entries, err := st.ListAuditLog(t.Context(), store.AuditLogFilter{TableName: "insight_links", Limit: 500})
	require.NoError(t, err)

	operations := make(map[string]int)
	for _, entry := range entries {
		operations[entry.Operation]++
		data := entry.NewData
		if entry.Operation == "DELETE" {
			data = entry.OldData
		}
		var row struct {
			TargetKey string `json:"target_key"`
		}
		require.NoError(t, json.Unmarshal(data, &row))
		require.NotEmpty(t, row.TargetKey)
	}
	return operations
}

func TestSearchInsights(t *testing.T) {
	st := newTestStore(t)
	ctx := t.Context()
	u, p := testUserAndProject(t, st, 30)

	_, err := st.WriteInsight(ctx, store.WriteInsightParams{ProjectID: p.ID, Content: "PostgreSQL is a relational database", Tags: []string{"db"}, CreatedBy: u.ID})
	require.NoError(t, err)
	_, err = st.WriteInsight(ctx, store.WriteInsightParams{ProjectID: p.ID, Content: "Redis is an in-memory store", Tags: []string{"cache"}, CreatedBy: u.ID})
	require.NoError(t, err)

	results, err := st.SearchInsights(ctx, p.ID, "relational database", store.SearchQueryModeAll, nil, store.SearchTagModeAll, 10)
	require.NoError(t, err)
	require.Len(t, results, 1)
	require.Contains(t, results[0].Content, "PostgreSQL")

	// Tag filter should narrow results.
	tagged, err := st.SearchInsights(ctx, p.ID, "database", store.SearchQueryModeAll, []string{"cache"}, store.SearchTagModeAll, 10)
	require.NoError(t, err)
	require.Empty(t, tagged, "cache tag should exclude PostgreSQL result")

	// Tag names should be searchable even when absent from content.
	byTagName, err := st.SearchInsights(ctx, p.ID, "cache", store.SearchQueryModeAll, nil, store.SearchTagModeAll, 10)
	require.NoError(t, err)
	require.Len(t, byTagName, 1, "searching by tag name should find insights tagged with that word")
	require.Contains(t, byTagName[0].Content, "Redis")

	web, err := st.SearchInsights(ctx, p.ID, "relational OR redis", store.SearchQueryModeWeb, nil, store.SearchTagModeAll, 10)
	require.NoError(t, err)
	require.Len(t, web, 2)

	_, err = st.WriteInsight(ctx, store.WriteInsightParams{ProjectID: p.ID, Content: "PostgreSQL replication", Tags: []string{"db", "operations"}, CreatedBy: u.ID})
	require.NoError(t, err)

	allTags, err := st.SearchInsights(ctx, p.ID, "postgresql", store.SearchQueryModeAll, []string{"db", "operations"}, store.SearchTagModeAll, 10)
	require.NoError(t, err)
	require.Len(t, allTags, 1)
	require.Contains(t, allTags[0].Content, "replication")

	anyTags, err := st.SearchInsights(ctx, p.ID, "postgresql", store.SearchQueryModeAll, []string{"db", "operations"}, store.SearchTagModeAny, 10)
	require.NoError(t, err)
	require.Len(t, anyTags, 2)
}

func TestListInsights(t *testing.T) {
	st := newTestStore(t)
	ctx := t.Context()
	u, p := testUserAndProject(t, st, 40)

	for _, content := range []string{"insight one", "insight two", "insight three"} {
		_, err := st.WriteInsight(ctx, store.WriteInsightParams{ProjectID: p.ID, Content: content, Tags: []string{"x"}, CreatedBy: u.ID})
		require.NoError(t, err)
	}

	all, err := st.ListInsights(ctx, store.ListInsightsParams{ProjectID: p.ID, Limit: 10})
	require.NoError(t, err)
	require.Len(t, all.Insights, 3)

	byTag, err := st.ListInsights(ctx, store.ListInsightsParams{ProjectID: p.ID, Tag: "x", Limit: 10})
	require.NoError(t, err)
	require.Len(t, byTag.Insights, 3)

	noMatch, err := st.ListInsights(ctx, store.ListInsightsParams{ProjectID: p.ID, Tag: "y", Limit: 10})
	require.NoError(t, err)
	require.Empty(t, noMatch.Insights)
	require.Nil(t, noMatch.NextCursor)

	exact, err := st.ListInsights(ctx, store.ListInsightsParams{ProjectID: p.ID, Limit: 3})
	require.NoError(t, err)
	require.Len(t, exact.Insights, 3)
	require.Nil(t, exact.NextCursor)

	limitPlusOne, err := st.ListInsights(ctx, store.ListInsightsParams{ProjectID: p.ID, Limit: 2})
	require.NoError(t, err)
	require.Len(t, limitPlusOne.Insights, 2)
	require.NotNil(t, limitPlusOne.NextCursor)
	require.Nil(t, all.NextCursor)
}

func TestListInsightsCursorUsesUpdatedAtAndID(t *testing.T) {
	st := newTestStore(t)
	ctx := t.Context()
	u, p := testUserAndProject(t, st, 41)
	updatedAt := time.Date(2026, 7, 18, 12, 0, 0, 123456000, time.UTC)

	var written []*store.Insight
	for _, content := range []string{"insight one", "insight two", "insight three"} {
		insight, err := st.WriteInsight(ctx, store.WriteInsightParams{
			ProjectID: p.ID,
			Content:   content,
			Tags:      []string{"x"},
			CreatedBy: u.ID,
			CreatedAt: updatedAt,
			UpdatedAt: updatedAt,
		})
		require.NoError(t, err)
		written = append(written, insight)
	}
	sort.Slice(written, func(i, j int) bool {
		return written[i].ID.String() > written[j].ID.String()
	})

	first, err := st.ListInsights(ctx, store.ListInsightsParams{ProjectID: p.ID, Tag: "x", Limit: 2})
	require.NoError(t, err)
	require.Len(t, first.Insights, 2)
	require.Equal(t, written[0].ID, first.Insights[0].ID)
	require.Equal(t, written[1].ID, first.Insights[1].ID)
	require.NotNil(t, first.NextCursor)
	require.Equal(t, first.Insights[1].ID, first.NextCursor.ID)
	require.Equal(t, updatedAt.UnixMicro(), first.NextCursor.UpdatedAt.UnixMicro())

	second, err := st.ListInsights(ctx, store.ListInsightsParams{ProjectID: p.ID, Tag: "x", Limit: 2, After: first.NextCursor})
	require.NoError(t, err)
	require.Len(t, second.Insights, 1)
	require.Equal(t, written[2].ID, second.Insights[0].ID)
	require.Nil(t, second.NextCursor)
}

func TestDeleteInsight(t *testing.T) {
	st := newTestStore(t)
	ctx := t.Context()
	u, p := testUserAndProject(t, st, 50)

	f, err := st.WriteInsight(ctx, store.WriteInsightParams{ProjectID: p.ID, Content: "to delete", Tags: []string{}, CreatedBy: u.ID})
	require.NoError(t, err)

	require.NoError(t, st.DeleteInsight(ctx, p.OrgID, f.ID))

	// Deleted insight must not appear in list.
	insights, err := st.ListInsights(ctx, store.ListInsightsParams{ProjectID: p.ID, Limit: 10})
	require.NoError(t, err)
	require.Empty(t, insights.Insights)

	// Double-delete returns ErrNotFound.
	require.ErrorIs(t, st.DeleteInsight(ctx, p.OrgID, f.ID), store.ErrNotFound)
}

func TestDeleteInsight_NotFound(t *testing.T) {
	st := newTestStore(t)
	err := st.DeleteInsight(t.Context(), uuid.New(), uuid.New())
	require.ErrorIs(t, err, store.ErrNotFound)
}

func TestListProjects(t *testing.T) {
	st := newTestStore(t)
	ctx := t.Context()

	_, err := st.UpsertUser(ctx, store.GitHubProfile{GitHubID: 60, Email: "d@example.com", Login: "d"})
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

func TestListOrgs(t *testing.T) {
	st := newTestStore(t)
	ctx := t.Context()

	_, err := st.UpsertUser(ctx, store.GitHubProfile{GitHubID: 61, Email: "zed@example.com", Login: "zed"})
	require.NoError(t, err)
	_, err = st.UpsertUser(ctx, store.GitHubProfile{GitHubID: 62, Email: "amy@example.com", Login: "amy"})
	require.NoError(t, err)

	orgs, err := st.ListOrgs(ctx)
	require.NoError(t, err)

	var slugs []string
	for _, o := range orgs {
		require.Equal(t, "personal", o.Kind)
		slugs = append(slugs, o.Slug)
	}
	require.Contains(t, slugs, "zed")
	require.Contains(t, slugs, "amy")

	// Ordered by kind then name: amy's personal org must sort before zed's.
	amyIdx, zedIdx := -1, -1
	for i, s := range slugs {
		switch s {
		case "amy":
			amyIdx = i
		case "zed":
			zedIdx = i
		}
	}
	require.Less(t, amyIdx, zedIdx)
}

func TestListTags(t *testing.T) {
	st := newTestStore(t)
	ctx := t.Context()
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

func TestGetProjectDashboard(t *testing.T) {
	st := newTestStore(t)
	ctx := t.Context()
	u, p := testUserAndProject(t, st, 75)

	_, err := st.WriteInsight(ctx, store.WriteInsightParams{
		ProjectID: p.ID,
		Content:   "Postgres stores facts",
		Tags:      []string{"db", "backend"},
		Category:  "fact",
		Source:    "agent",
		CreatedBy: u.ID,
	})
	require.NoError(t, err)
	_, err = st.WriteInsight(ctx, store.WriteInsightParams{
		ProjectID: p.ID,
		Content:   "Prefer Connect RPC for UI APIs",
		Tags:      []string{"api"},
		Category:  "preference",
		Source:    "user",
		CreatedBy: u.ID,
	})
	require.NoError(t, err)
	deleted, err := st.WriteInsight(ctx, store.WriteInsightParams{
		ProjectID: p.ID,
		Content:   "deleted insight",
		Tags:      []string{"deleted"},
		Category:  "fact",
		Source:    "agent",
		CreatedBy: u.ID,
	})
	require.NoError(t, err)
	require.NoError(t, st.DeleteInsight(ctx, p.OrgID, deleted.ID))

	dashboard, err := st.GetProjectDashboard(ctx, p.ID)
	require.NoError(t, err)
	require.Equal(t, 2, dashboard.TotalInsights)
	require.Len(t, dashboard.RecentActivity, 14)
	require.Len(t, dashboard.RecentInsights, 2)

	categoryCounts := map[string]int{}
	for _, bucket := range dashboard.CategoryCounts {
		categoryCounts[bucket.Name] = bucket.Count
	}
	require.Equal(t, 1, categoryCounts["fact"])
	require.Equal(t, 1, categoryCounts["preference"])

	sourceCounts := map[string]int{}
	for _, bucket := range dashboard.SourceCounts {
		sourceCounts[bucket.Name] = bucket.Count
	}
	require.Equal(t, 1, sourceCounts["agent"])
	require.Equal(t, 1, sourceCounts["user"])

	tagCounts := map[string]int{}
	for _, bucket := range dashboard.TopTags {
		tagCounts[bucket.Name] = bucket.Count
	}
	require.Equal(t, 1, tagCounts["db"])
	require.Equal(t, 1, tagCounts["backend"])
	require.Equal(t, 1, tagCounts["api"])
	require.Zero(t, tagCounts["deleted"])
}

func TestUpdateInsight(t *testing.T) {
	st := newTestStore(t)
	ctx := t.Context()
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
	ctx := t.Context()

	u, err := st.UpsertUser(ctx, store.GitHubProfile{GitHubID: 100, Email: "grantuser@example.com", Login: "grantuser"})
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
	ctx := t.Context()

	u, err := st.UpsertUser(ctx, store.GitHubProfile{GitHubID: 200, Email: "pruneuser@example.com", Login: "pruneuser"})
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

	_, err := st.GetGrant(t.Context(), "no-such-jti")
	require.ErrorIs(t, err, store.ErrNotFound)
}

func TestRotateGrant_RotatesAndPreservesScope(t *testing.T) {
	st := newTestStoreWithEnc(t, store.NewEncryptor(testEncKey))
	ctx := t.Context()

	u, err := st.UpsertUser(ctx, store.GitHubProfile{GitHubID: 400, Email: "rotate@example.com", Login: "rotateuser"})
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
	ctx := t.Context()

	u, err := st.UpsertUser(ctx, store.GitHubProfile{GitHubID: 300, Email: "nokey@example.com", Login: "nokey"})
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
	ctx := t.Context()

	now := time.Now().UTC().Truncate(time.Second)
	expiresAt := now.Add(90 * 24 * time.Hour)
	c := store.OAuthClient{
		ClientID:                "test-client-id-001",
		ClientName:              "Test Client",
		RedirectURIs:            []string{"https://client.example.com/callback"},
		GrantTypes:              []string{"authorization_code"},
		ResponseTypes:           []string{"code"},
		TokenEndpointAuthMethod: "none",
		Scope:                   "insights:read",
		IssuedAt:                now,
		ExpiresAt:               &expiresAt,
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
	require.WithinDuration(t, c.IssuedAt, got.LastUsedAt, time.Second)
	require.NotNil(t, got.ExpiresAt)
	require.WithinDuration(t, *c.ExpiresAt, *got.ExpiresAt, time.Second)
}

func TestTouchClient(t *testing.T) {
	st := newTestStore(t)
	ctx := t.Context()
	lastUsedAt := time.Now().UTC().Add(-48 * time.Hour).Truncate(time.Second)
	c := store.OAuthClient{
		ClientID:                "touched-client",
		RedirectURIs:            []string{"https://client.example.com/callback"},
		GrantTypes:              []string{"authorization_code", "refresh_token"},
		ResponseTypes:           []string{"code"},
		TokenEndpointAuthMethod: "none",
		IssuedAt:                lastUsedAt,
		LastUsedAt:              lastUsedAt,
	}
	require.NoError(t, st.SaveClient(ctx, c))

	require.NoError(t, st.TouchClient(ctx, c.ClientID))
	got, err := st.GetClient(ctx, c.ClientID)
	require.NoError(t, err)
	require.Greater(t, got.LastUsedAt, lastUsedAt)
}

func TestTouchClient_ThrottlesRecentUpdates(t *testing.T) {
	st := newTestStore(t)
	ctx := t.Context()
	lastUsedAt := time.Now().UTC().Truncate(time.Second)
	c := store.OAuthClient{
		ClientID:                "recent-client",
		RedirectURIs:            []string{"https://client.example.com/callback"},
		GrantTypes:              []string{"authorization_code", "refresh_token"},
		ResponseTypes:           []string{"code"},
		TokenEndpointAuthMethod: "none",
		IssuedAt:                lastUsedAt,
		LastUsedAt:              lastUsedAt,
	}
	require.NoError(t, st.SaveClient(ctx, c))

	require.NoError(t, st.TouchClient(ctx, c.ClientID))
	got, err := st.GetClient(ctx, c.ClientID)
	require.NoError(t, err)
	require.True(t, lastUsedAt.Equal(got.LastUsedAt))
}

func TestSaveClient_Permanent(t *testing.T) {
	st := newTestStore(t)
	ctx := t.Context()

	c := store.OAuthClient{
		ClientID:                "permanent-client-id",
		RedirectURIs:            []string{"https://client.example.com/callback"},
		GrantTypes:              []string{"authorization_code", "refresh_token"},
		ResponseTypes:           []string{"code"},
		TokenEndpointAuthMethod: "none",
		IssuedAt:                time.Now().UTC(),
	}

	require.NoError(t, st.SaveClient(ctx, c))
	got, err := st.GetClient(ctx, c.ClientID)
	require.NoError(t, err)
	require.Nil(t, got.ExpiresAt)
}

func TestGetClient_ExpiredTemporaryClient(t *testing.T) {
	st := newTestStore(t)
	ctx := t.Context()
	expiresAt := time.Now().Add(-time.Minute)
	c := store.OAuthClient{
		ClientID:                "expired-temporary-client",
		RedirectURIs:            []string{"https://client.example.com/callback"},
		GrantTypes:              []string{"authorization_code"},
		ResponseTypes:           []string{"code"},
		TokenEndpointAuthMethod: "none",
		IssuedAt:                time.Now().Add(-time.Hour),
		ExpiresAt:               &expiresAt,
	}

	require.NoError(t, st.SaveClient(ctx, c))
	_, err := st.GetClient(ctx, c.ClientID)
	require.ErrorIs(t, err, store.ErrNotFound)
}

func TestSaveClient_DuplicateClientID(t *testing.T) {
	st := newTestStore(t)
	ctx := t.Context()

	now := time.Now().UTC()
	expiresAt := now.Add(90 * 24 * time.Hour)
	c := store.OAuthClient{
		ClientID:                "duplicate-client-id",
		ClientName:              "First",
		RedirectURIs:            []string{"https://a.example.com/cb"},
		GrantTypes:              []string{"authorization_code"},
		ResponseTypes:           []string{"code"},
		TokenEndpointAuthMethod: "none",
		IssuedAt:                now,
		ExpiresAt:               &expiresAt,
	}

	require.NoError(t, st.SaveClient(ctx, c))

	c.ClientName = "Second"
	err := st.SaveClient(ctx, c)
	require.Error(t, err, "saving a duplicate client_id must return an error")
}

func TestUpsertClient_UpdatesExistingClient(t *testing.T) {
	st := newTestStore(t)
	ctx := t.Context()

	now := time.Now().UTC().Truncate(time.Second)
	expiresAt := now.Add(90 * 24 * time.Hour)
	c := store.OAuthClient{
		ClientID:                "ui-client",
		ClientName:              "First",
		RedirectURIs:            []string{"https://a.example.com/cb"},
		GrantTypes:              []string{"authorization_code"},
		ResponseTypes:           []string{"code"},
		TokenEndpointAuthMethod: "none",
		Scope:                   "insights:read",
		IssuedAt:                now,
		ExpiresAt:               &expiresAt,
	}

	require.NoError(t, st.UpsertClient(ctx, c))

	c.ClientName = "Second"
	c.RedirectURIs = []string{"https://b.example.com/cb"}
	c.Scope = "insights:read insights:write"
	c.IssuedAt = now.Add(time.Hour)
	require.NoError(t, st.UpsertClient(ctx, c))

	got, err := st.GetClient(ctx, c.ClientID)
	require.NoError(t, err)
	require.Equal(t, "Second", got.ClientName)
	require.Equal(t, []string{"https://b.example.com/cb"}, got.RedirectURIs)
	require.Equal(t, "insights:read insights:write", got.Scope)
	require.True(t, got.LastUsedAt.Equal(now), "metadata upsert must preserve client activity")
}

// --- GetUserByID ---

func TestGetUserByID_Success(t *testing.T) {
	st := newTestStore(t)
	ctx := t.Context()

	upserted, err := st.UpsertUser(ctx, store.GitHubProfile{GitHubID: 900, Email: "id@example.com", Login: "iduser"})
	require.NoError(t, err)

	got, err := st.GetUserByID(ctx, upserted.ID)
	require.NoError(t, err)
	require.Equal(t, upserted.ID, got.ID)
	require.Equal(t, int64(900), got.GitHubID)
	require.Equal(t, "id@example.com", got.Email)
}

func TestGetUserByID_NotFound(t *testing.T) {
	st := newTestStore(t)
	_, err := st.GetUserByID(t.Context(), uuid.New())
	require.ErrorIs(t, err, store.ErrNotFound)
}

// --- GetPersonalOrgByUserID ---

func TestGetPersonalOrgByUserID_CreatedOnFirstLogin(t *testing.T) {
	st := newTestStore(t)
	ctx := t.Context()

	u, err := st.UpsertUser(ctx, store.GitHubProfile{GitHubID: 910, Email: "org@example.com", Login: "orguser"})
	require.NoError(t, err)

	org, err := st.GetPersonalOrgByUserID(ctx, u.ID)
	require.NoError(t, err)
	require.Equal(t, "orguser", org.Slug)
	require.Equal(t, "personal", org.Kind)
	require.NotEqual(t, uuid.Nil, org.ID)
}

func TestGetPersonalOrgByUserID_NotFound(t *testing.T) {
	st := newTestStore(t)
	_, err := st.GetPersonalOrgByUserID(t.Context(), uuid.New())
	require.ErrorIs(t, err, store.ErrNotFound)
}

// --- UpsertUser slug collision ---

func TestUpsertUser_SameLoginSlugAllowedForMultiplePersonalOrgs(t *testing.T) {
	// Personal org slugs are display-only; two users with the same GitHub login
	// (possible after a username transfer) can both hold that slug without conflict.
	st := newTestStore(t)
	ctx := t.Context()

	u1, err := st.UpsertUser(ctx, store.GitHubProfile{GitHubID: 920, Email: "first@example.com", Login: "sharedlogin"})
	require.NoError(t, err)

	u2, err := st.UpsertUser(ctx, store.GitHubProfile{GitHubID: 921, Email: "second@example.com", Login: "sharedlogin"})
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
	ctx := t.Context()

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
	_, err := st.ConsumePendingAuth(t.Context(), "no-such-state")
	require.ErrorIs(t, err, store.ErrNotFound)
}

func TestConsumePendingAuth_SingleUse(t *testing.T) {
	st := newTestStore(t)
	ctx := t.Context()

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
	ctx := t.Context()

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
	ctx := t.Context()

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
	ctx := t.Context()

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
	_, err := st.ConsumeAuthCode(t.Context(), "no-such-code")
	require.ErrorIs(t, err, store.ErrNotFound)
}

func TestConsumeAuthCode_SingleUse(t *testing.T) {
	st := newTestStoreWithEnc(t, store.NewEncryptor(testEncKey))
	ctx := t.Context()

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
	ctx := t.Context()

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
	ctx := t.Context()

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
	ctx := t.Context()

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
	ctx := t.Context()

	u, err := st.UpsertUser(ctx, store.GitHubProfile{GitHubID: 1100, Email: "rftoken@example.com", Login: "rfuser"})
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
	_, err := st.GetGrantByRefreshToken(t.Context(), "no-such-token")
	require.ErrorIs(t, err, store.ErrNotFound)
}

func TestRotateGrant_Success(t *testing.T) {
	st := newTestStoreWithEnc(t, store.NewEncryptor(testEncKey))
	ctx := t.Context()

	u, err := st.UpsertUser(ctx, store.GitHubProfile{GitHubID: 1200, Email: "rotate@example.com", Login: "rotateuser"})
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
	ctx := t.Context()

	u, err := st.UpsertUser(ctx, store.GitHubProfile{GitHubID: 1250, Email: "retired@example.com", Login: "retireduser"})
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
	ctx := t.Context()

	u, err := st.UpsertUser(ctx, store.GitHubProfile{GitHubID: 1260, Email: "rollback@example.com", Login: "rollbackuser"})
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

func TestGetRetiredRefreshToken_IgnoresExpiredRetention(t *testing.T) {
	st := newTestStoreWithEnc(t, store.NewEncryptor(testEncKey))
	ctx := t.Context()

	u, err := st.UpsertUser(ctx, store.GitHubProfile{GitHubID: 1270, Email: "pruneretired@example.com", Login: "pruneretired"})
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
	ctx := t.Context()

	u, err := st.UpsertUser(ctx, store.GitHubProfile{GitHubID: 1300, Email: "race@example.com", Login: "raceuser"})
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
	ctx := t.Context()

	u, err := st.UpsertUser(ctx, store.GitHubProfile{GitHubID: 1400, Email: "del@example.com", Login: "deluser"})
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
	err := st.DeleteGrant(t.Context(), "no-such-jti", nil)
	require.ErrorIs(t, err, store.ErrNotFound)
}
