package postgres_test

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"sort"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

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

func databaseNow(t *testing.T, dsn string) time.Time {
	t.Helper()
	pool, err := pgxpool.New(t.Context(), dsn)
	require.NoError(t, err)
	t.Cleanup(pool.Close)

	var now time.Time
	require.NoError(t, pool.QueryRow(t.Context(), `SELECT now()`).Scan(&now))
	return now
}

type insightTimestamps struct {
	insight              *store.Insight
	createdAt, updatedAt time.Time
}

func setInsightTimestamps(t *testing.T, dsn string, values ...insightTimestamps) {
	t.Helper()
	pool, err := pgxpool.New(t.Context(), dsn)
	require.NoError(t, err)
	t.Cleanup(pool.Close)

	for _, value := range values {
		_, err := pool.Exec(t.Context(), `UPDATE insights SET created_at = $2, updated_at = $3 WHERE id = $1`,
			value.insight.ID, value.createdAt, value.updatedAt)
		require.NoError(t, err)
		// pgx returns timestamptz in the connection's local location; keep the
		// fixture object in the same representation for structural assertions.
		value.insight.CreatedAt = value.createdAt.In(time.Local)
		value.insight.UpdatedAt = value.updatedAt.In(time.Local)
	}
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

func TestMigrateAddsInsightRevisionBaselines(t *testing.T) {
	st, dsn := newTestStoreAndDSN(t)
	ctx := t.Context()
	u, p := testUserAndProject(t, st, 3)

	liveUpdatedAt := time.Date(2026, time.July, 17, 10, 11, 12, 0, time.UTC)
	live, err := st.WriteInsight(ctx, store.WriteInsightParams{
		ProjectID: p.ID,
		Key:       "live",
		Content:   "live content",
		Tags:      []string{"one", "two"},
		Category:  "fact",
		Source:    "repo",
		CreatedBy: u.ID,
	})
	require.NoError(t, err)

	deleted, err := st.WriteInsight(ctx, store.WriteInsightParams{
		ProjectID: p.ID,
		Key:       "deleted",
		Content:   "deleted content",
		Tags:      []string{"three"},
		Category:  "decision",
		Source:    "user",
		CreatedBy: u.ID,
	})
	require.NoError(t, err)
	keyless, err := st.WriteInsight(ctx, store.WriteInsightParams{
		ProjectID: p.ID, Content: "keyless content", Tags: []string{"append-only"}, CreatedBy: u.ID,
	})
	require.NoError(t, err)
	_, err = st.DeleteInsight(ctx, store.DeleteInsightParams{OrgID: p.OrgID, InsightID: deleted.ID, ChangedBy: u.ID})
	require.NoError(t, err)
	setInsightTimestamps(t, dsn,
		insightTimestamps{insight: live, createdAt: liveUpdatedAt.Add(-time.Hour), updatedAt: liveUpdatedAt},
		insightTimestamps{insight: deleted, createdAt: liveUpdatedAt.Add(-2 * time.Hour), updatedAt: liveUpdatedAt.Add(-time.Hour)},
	)

	pool, err := pgxpool.New(ctx, dsn)
	require.NoError(t, err)
	t.Cleanup(pool.Close)
	var deletedAt time.Time
	require.NoError(t, pool.QueryRow(ctx, `SELECT deleted_at FROM insights WHERE id = $1`, deleted.ID).Scan(&deletedAt))

	_, err = pool.Exec(ctx, `DROP TABLE insight_revisions`)
	require.NoError(t, err)
	_, err = pool.Exec(ctx, `ALTER TABLE insights DROP COLUMN revision`)
	require.NoError(t, err)
	_, err = pool.Exec(ctx, `DELETE FROM schema_migrations WHERE version = 19`)
	require.NoError(t, err)

	require.NoError(t, st.Migrate(ctx, slog.New(slog.DiscardHandler)))

	type baseline struct {
		CurrentRevision int
		Revision        int
		Operation       string
		Key             string
		Content         string
		Tags            []string
		Category        string
		Source          string
		DeletedAt       *time.Time
		ChangedBy       *uuid.UUID
		ChangedAt       time.Time
	}
	readBaseline := func(insightID uuid.UUID) baseline {
		t.Helper()
		var got baseline
		err := pool.QueryRow(ctx, `
			SELECT i.revision, r.revision, r.operation, COALESCE(r.key, ''), r.content,
			       r.tags, r.category, r.source, r.deleted_at, r.changed_by, r.changed_at
			FROM insights i
			JOIN insight_revisions r ON r.insight_id = i.id AND r.revision = i.revision
			WHERE i.id = $1`, insightID).Scan(
			&got.CurrentRevision,
			&got.Revision,
			&got.Operation,
			&got.Key,
			&got.Content,
			&got.Tags,
			&got.Category,
			&got.Source,
			&got.DeletedAt,
			&got.ChangedBy,
			&got.ChangedAt,
		)
		require.NoError(t, err)
		return got
	}

	liveBaseline := readBaseline(live.ID)
	require.Equal(t, 1, liveBaseline.CurrentRevision)
	require.Equal(t, 1, liveBaseline.Revision)
	require.Equal(t, "baseline", liveBaseline.Operation)
	require.Equal(t, "live", liveBaseline.Key)
	require.Equal(t, "live content", liveBaseline.Content)
	require.Equal(t, []string{"one", "two"}, liveBaseline.Tags)
	require.Equal(t, "fact", liveBaseline.Category)
	require.Equal(t, "repo", liveBaseline.Source)
	require.Nil(t, liveBaseline.DeletedAt)
	require.Nil(t, liveBaseline.ChangedBy)
	require.True(t, liveUpdatedAt.Equal(liveBaseline.ChangedAt))

	deletedBaseline := readBaseline(deleted.ID)
	require.Equal(t, "baseline", deletedBaseline.Operation)
	require.Equal(t, "deleted", deletedBaseline.Key)
	require.Equal(t, "deleted content", deletedBaseline.Content)
	require.Equal(t, []string{"three"}, deletedBaseline.Tags)
	require.Equal(t, "decision", deletedBaseline.Category)
	require.Equal(t, "user", deletedBaseline.Source)
	require.NotNil(t, deletedBaseline.DeletedAt)
	require.Equal(t, deletedAt, *deletedBaseline.DeletedAt)
	require.Nil(t, deletedBaseline.ChangedBy)
	require.Equal(t, deletedAt, deletedBaseline.ChangedAt)
	var keyIsNull bool
	require.NoError(t, pool.QueryRow(ctx, `
		SELECT key IS NULL FROM insight_revisions WHERE insight_id = $1 AND revision = 1`, keyless.ID).Scan(&keyIsNull))
	require.True(t, keyIsNull)
	history, err := st.ListInsightHistory(ctx, store.ListInsightHistoryParams{
		ProjectID: p.ID, InsightID: deleted.ID, Limit: 20,
	})
	require.NoError(t, err)
	require.Equal(t, 1, history.CurrentRevision)
	require.NotNil(t, history.DeletedAt)
	require.Len(t, history.Revisions, 1)
	require.Equal(t, "baseline", history.Revisions[0].Operation)
	require.Nil(t, history.Revisions[0].ChangedBy)

	_, err = pool.Exec(ctx, `UPDATE insights SET revision = 0 WHERE id = $1`, live.ID)
	require.Error(t, err)
	_, err = pool.Exec(ctx, `
		INSERT INTO insight_revisions (
			insight_id, revision, operation, key, content, tags, category, source, changed_at
		)
		VALUES ($1, 0, 'baseline', 'invalid', 'invalid', '{}', 'fact', 'repo', now())`, live.ID)
	require.Error(t, err)

	_, err = pool.Exec(ctx, `DELETE FROM insights WHERE id = $1`, live.ID)
	require.NoError(t, err)
	var revisionCount int
	require.NoError(t, pool.QueryRow(ctx, `SELECT count(*) FROM insight_revisions WHERE insight_id = $1`, live.ID).Scan(&revisionCount))
	require.Zero(t, revisionCount)
}

func TestMigrateDropsInsightAuditTrigger(t *testing.T) {
	st, dsn := newTestStoreAndDSN(t)
	ctx := t.Context()

	pool, err := pgxpool.New(ctx, dsn)
	require.NoError(t, err)
	t.Cleanup(pool.Close)

	var triggerExists bool
	require.NoError(t, pool.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1
			FROM pg_trigger
			WHERE tgrelid = 'insights'::regclass
			  AND tgname = 'audit_insights'
		)`).Scan(&triggerExists))
	require.False(t, triggerExists)

	// Recreate the pre-migration state and verify the migration is the operation
	// that removes the trigger, rather than relying only on the template state.
	_, err = pool.Exec(ctx, `
		CREATE TRIGGER audit_insights
		AFTER INSERT OR UPDATE OR DELETE ON insights
		FOR EACH ROW EXECUTE FUNCTION audit_trigger_func();
		DELETE FROM schema_migrations WHERE version = 20`)
	require.NoError(t, err)

	require.NoError(t, st.Migrate(ctx, slog.New(slog.DiscardHandler)))
	require.NoError(t, pool.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1
			FROM pg_trigger
			WHERE tgrelid = 'insights'::regclass
			  AND tgname = 'audit_insights'
		)`).Scan(&triggerExists))
	require.False(t, triggerExists)
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

type testInsightRevision struct {
	Revision  int
	Operation string
	Key       *string
	Content   string
	Tags      []string
	Category  string
	Source    string
	DeletedAt *time.Time
	ChangedBy uuid.UUID
}

func readInsightRevisions(t *testing.T, pool *pgxpool.Pool, insightID uuid.UUID) []testInsightRevision {
	t.Helper()
	rows, err := pool.Query(t.Context(), `
		SELECT revision, operation, key, content, tags, category, source, deleted_at,
		       COALESCE(changed_by::text, '')
		FROM insight_revisions
		WHERE insight_id = $1
		ORDER BY revision`, insightID)
	require.NoError(t, err)
	defer rows.Close()

	var revisions []testInsightRevision
	for rows.Next() {
		var revision testInsightRevision
		var changedBy string
		require.NoError(t, rows.Scan(
			&revision.Revision, &revision.Operation, &revision.Key, &revision.Content,
			&revision.Tags, &revision.Category, &revision.Source, &revision.DeletedAt, &changedBy,
		))
		if changedBy != "" {
			revision.ChangedBy = uuid.MustParse(changedBy)
		}
		revisions = append(revisions, revision)
	}
	require.NoError(t, rows.Err())
	return revisions
}

func TestInsightRevisionMutationSequence(t *testing.T) {
	st, dsn := newTestStoreAndDSN(t)
	ctx := t.Context()
	u, p := testUserAndProject(t, st, 9)
	pool, err := pgxpool.New(ctx, dsn)
	require.NoError(t, err)
	t.Cleanup(pool.Close)

	keyless, err := st.WriteInsight(ctx, store.WriteInsightParams{
		ProjectID: p.ID, Content: "append-only", Tags: []string{"log"}, CreatedBy: u.ID,
	})
	require.NoError(t, err)
	require.Equal(t, 1, keyless.Revision)
	keylessRevisions := readInsightRevisions(t, pool, keyless.ID)
	require.Len(t, keylessRevisions, 1)
	require.Nil(t, keylessRevisions[0].Key)
	require.Equal(t, "create", keylessRevisions[0].Operation)

	expectedZero := 0
	current, err := st.WriteInsight(ctx, store.WriteInsightParams{
		ProjectID: p.ID, Key: "stable", Content: "v1", Tags: []string{"one"},
		Category: "decision", Source: "user", CreatedBy: u.ID, ExpectedRevision: &expectedZero,
	})
	require.NoError(t, err)
	require.Equal(t, 1, current.Revision)

	expectedOne := 1
	current, err = st.WriteInsight(ctx, store.WriteInsightParams{
		ProjectID: p.ID, Key: "stable", Content: "v2", Tags: []string{"one"},
		Category: "decision", Source: "user", CreatedBy: u.ID, ExpectedRevision: &expectedOne,
	})
	require.NoError(t, err)
	require.Equal(t, 2, current.Revision)
	updatedAt := current.UpdatedAt

	expectedTwo := 2
	noOp, err := st.WriteInsight(ctx, store.WriteInsightParams{
		ProjectID: p.ID, Key: "stable", Content: "v2", Tags: []string{"one"},
		Category: "decision", Source: "user", CreatedBy: u.ID, ExpectedRevision: &expectedTwo,
	})
	require.NoError(t, err)
	require.Equal(t, 2, noOp.Revision)
	require.True(t, updatedAt.Equal(noOp.UpdatedAt))

	current, err = st.UpdateInsight(ctx, store.UpdateInsightParams{
		OrgID: p.OrgID, InsightID: current.ID, Tags: []string{"two"},
		ChangedBy: u.ID, ExpectedRevision: &expectedTwo,
	})
	require.NoError(t, err)
	require.Equal(t, 3, current.Revision)

	expectedThree := 3
	deletedRevision, err := st.DeleteInsight(ctx, store.DeleteInsightParams{
		OrgID: p.OrgID, InsightID: current.ID, ChangedBy: u.ID, ExpectedRevision: &expectedThree,
	})
	require.NoError(t, err)
	require.Equal(t, 4, deletedRevision)

	revisions := readInsightRevisions(t, pool, current.ID)
	require.Len(t, revisions, 4)
	require.Equal(t, []string{"create", "update", "update", "delete"}, []string{
		revisions[0].Operation, revisions[1].Operation, revisions[2].Operation, revisions[3].Operation,
	})
	require.Equal(t, "v1", revisions[0].Content)
	require.Equal(t, "v2", revisions[1].Content)
	require.Equal(t, []string{"two"}, revisions[2].Tags)
	require.NotNil(t, revisions[3].DeletedAt)
	for _, revision := range revisions {
		require.Equal(t, u.ID, revision.ChangedBy)
	}
}

func TestListInsightHistoryPaginatesSoftDeletedInsight(t *testing.T) {
	st := newTestStore(t)
	ctx := t.Context()
	u, p := testUserAndProject(t, st, 36)

	current, err := st.WriteInsight(ctx, store.WriteInsightParams{
		ProjectID: p.ID, Key: "history", Content: "v1", Tags: []string{"one"},
		Category: "decision", Source: "user", CreatedBy: u.ID,
	})
	require.NoError(t, err)
	expected := current.Revision
	current, err = st.UpdateInsight(ctx, store.UpdateInsightParams{
		OrgID: p.OrgID, InsightID: current.ID, Content: "v2", ChangedBy: u.ID,
		ExpectedRevision: &expected,
	})
	require.NoError(t, err)
	expected = current.Revision
	current, err = st.UpdateInsight(ctx, store.UpdateInsightParams{
		OrgID: p.OrgID, InsightID: current.ID, Tags: []string{"two"}, ChangedBy: u.ID,
		ExpectedRevision: &expected,
	})
	require.NoError(t, err)
	expected = current.Revision
	deletedRevision, err := st.DeleteInsight(ctx, store.DeleteInsightParams{
		OrgID: p.OrgID, InsightID: current.ID, ChangedBy: u.ID, ExpectedRevision: &expected,
	})
	require.NoError(t, err)
	require.Equal(t, 4, deletedRevision)

	first, err := st.ListInsightHistory(ctx, store.ListInsightHistoryParams{
		ProjectID: p.ID, InsightID: current.ID, Limit: 2,
	})
	require.NoError(t, err)
	require.Equal(t, current.ID, first.InsightID)
	require.Equal(t, "history", first.Key)
	require.Equal(t, 4, first.CurrentRevision)
	require.NotNil(t, first.DeletedAt)
	require.Equal(t, []int{4, 3}, []int{first.Revisions[0].Revision, first.Revisions[1].Revision})
	require.Equal(t, []string{"delete", "update"}, []string{first.Revisions[0].Operation, first.Revisions[1].Operation})
	require.NotNil(t, first.Revisions[0].DeletedAt)
	require.Nil(t, first.Revisions[1].DeletedAt)
	require.Equal(t, "v2", first.Revisions[1].Content)
	require.Equal(t, []string{"two"}, first.Revisions[1].Tags)
	require.Equal(t, &store.InsightHistoryCursor{Revision: 3}, first.NextCursor)

	second, err := st.ListInsightHistory(ctx, store.ListInsightHistoryParams{
		ProjectID: p.ID, InsightID: current.ID, Limit: 2, After: first.NextCursor,
	})
	require.NoError(t, err)
	require.Equal(t, []int{2, 1}, []int{second.Revisions[0].Revision, second.Revisions[1].Revision})
	require.Equal(t, []string{"update", "create"}, []string{second.Revisions[0].Operation, second.Revisions[1].Operation})
	require.Equal(t, "v2", second.Revisions[0].Content)
	require.Equal(t, "v1", second.Revisions[1].Content)
	require.Nil(t, second.NextCursor)
	for _, revision := range append(first.Revisions, second.Revisions...) {
		require.Equal(t, current.ID, revision.InsightID)
		require.Equal(t, u.ID, *revision.ChangedBy)
		require.False(t, revision.ChangedAt.IsZero())
	}

	empty, err := st.ListInsightHistory(ctx, store.ListInsightHistoryParams{
		ProjectID: p.ID, InsightID: current.ID, Limit: 2,
		After: &store.InsightHistoryCursor{Revision: 1},
	})
	require.NoError(t, err)
	require.Equal(t, current.ID, empty.InsightID)
	require.Equal(t, 4, empty.CurrentRevision)
	require.Empty(t, empty.Revisions)
	require.Nil(t, empty.NextCursor)

	otherProject, err := st.EnsureProject(ctx, p.OrgID, u.ID, "other-history", "Other History")
	require.NoError(t, err)
	_, err = st.ListInsightHistory(ctx, store.ListInsightHistoryParams{
		ProjectID: otherProject.ID, InsightID: current.ID, Limit: 20,
	})
	require.ErrorIs(t, err, store.ErrNotFound)
	_, err = st.ListInsightHistory(ctx, store.ListInsightHistoryParams{
		ProjectID: p.ID, InsightID: uuid.New(), Limit: 20,
	})
	require.ErrorIs(t, err, store.ErrNotFound)
}

func TestListInsightHistoryCursorKeepsOlderContinuationStable(t *testing.T) {
	st := newTestStore(t)
	ctx := t.Context()
	u, p := testUserAndProject(t, st, 37)

	current, err := st.WriteInsight(ctx, store.WriteInsightParams{
		ProjectID: p.ID, Key: "concurrent-history", Content: "v1", CreatedBy: u.ID,
	})
	require.NoError(t, err)
	for _, content := range []string{"v2", "v3"} {
		expected := current.Revision
		current, err = st.UpdateInsight(ctx, store.UpdateInsightParams{
			OrgID: p.OrgID, InsightID: current.ID, Content: content,
			ChangedBy: u.ID, ExpectedRevision: &expected,
		})
		require.NoError(t, err)
	}

	first, err := st.ListInsightHistory(ctx, store.ListInsightHistoryParams{
		ProjectID: p.ID, InsightID: current.ID, Limit: 1,
	})
	require.NoError(t, err)
	require.Equal(t, []int{3}, []int{first.Revisions[0].Revision})
	require.Equal(t, &store.InsightHistoryCursor{Revision: 3}, first.NextCursor)

	expected := current.Revision
	current, err = st.UpdateInsight(ctx, store.UpdateInsightParams{
		OrgID: p.OrgID, InsightID: current.ID, Content: "v4",
		ChangedBy: u.ID, ExpectedRevision: &expected,
	})
	require.NoError(t, err)
	require.Equal(t, 4, current.Revision)

	continuation, err := st.ListInsightHistory(ctx, store.ListInsightHistoryParams{
		ProjectID: p.ID, InsightID: current.ID, Limit: 10, After: first.NextCursor,
	})
	require.NoError(t, err)
	require.Equal(t, 4, continuation.CurrentRevision)
	require.Equal(t, []int{2, 1}, []int{continuation.Revisions[0].Revision, continuation.Revisions[1].Revision})
}

func TestRestoreInsightCreatesLiveRevisionAndSuppressesLiveNoOp(t *testing.T) {
	st, dsn := newTestStoreAndDSN(t)
	ctx := t.Context()
	u, p := testUserAndProject(t, st, 38)
	pool, err := pgxpool.New(ctx, dsn)
	require.NoError(t, err)
	t.Cleanup(pool.Close)

	_, err = st.WriteInsight(ctx, store.WriteInsightParams{
		ProjectID: p.ID, Key: "target-a", Content: "target", CreatedBy: u.ID,
	})
	require.NoError(t, err)
	current, err := st.WriteInsight(ctx, store.WriteInsightParams{
		ProjectID: p.ID, Key: "restore-source", Content: "[[insight:target-a]] [[insight:missing]]",
		Tags: []string{"v1"}, Category: "decision", Source: "repo", CreatedBy: u.ID,
	})
	require.NoError(t, err)
	expected := current.Revision
	current, err = st.WriteInsight(ctx, store.WriteInsightParams{
		ProjectID: p.ID, Key: "restore-source", Content: "[[insight:changed]]", Tags: []string{"v2"},
		Category: "context", Source: "agent", CreatedBy: u.ID, ExpectedRevision: &expected,
	})
	require.NoError(t, err)
	expected = current.Revision
	deletedRevision, err := st.DeleteInsight(ctx, store.DeleteInsightParams{
		OrgID: p.OrgID, InsightID: current.ID, ChangedBy: u.ID, ExpectedRevision: &expected,
	})
	require.NoError(t, err)
	require.Equal(t, 3, deletedRevision)

	restored, err := st.RestoreInsight(ctx, store.RestoreInsightParams{
		ProjectID: p.ID, InsightID: current.ID, TargetRevision: 1,
		ExpectedRevision: deletedRevision, ChangedBy: u.ID,
	})
	require.NoError(t, err)
	require.Equal(t, 4, restored.Revision)
	require.Equal(t, "restore-source", restored.Key)
	require.Equal(t, "[[insight:target-a]] [[insight:missing]]", restored.Content)
	require.Equal(t, []string{"v1"}, restored.Tags)
	require.Equal(t, "decision", restored.Category)
	require.Equal(t, "repo", restored.Source)
	require.Equal(t, []store.InsightLinkWarning{{Code: store.InsightLinkWarningUnresolved, TargetKey: "missing"}}, restored.LinkWarnings)

	detail, err := st.GetInsight(ctx, store.GetInsightParams{ProjectID: p.ID, InsightID: current.ID, RelationLimit: 10})
	require.NoError(t, err)
	require.Equal(t, []string{"missing", "target-a"}, []string{detail.Links[0].TargetKey, detail.Links[1].TargetKey})
	require.False(t, detail.Links[0].Resolved)
	require.True(t, detail.Links[1].Resolved)
	revisions := readInsightRevisions(t, pool, current.ID)
	require.Equal(t, []string{"create", "update", "delete", "restore"}, []string{
		revisions[0].Operation, revisions[1].Operation, revisions[2].Operation, revisions[3].Operation,
	})
	require.Nil(t, revisions[3].DeletedAt)
	require.Equal(t, u.ID, revisions[3].ChangedBy)

	_, err = pool.Exec(ctx, `DELETE FROM insight_links WHERE source_insight_id = $1`, current.ID)
	require.NoError(t, err)
	noOp, err := st.RestoreInsight(ctx, store.RestoreInsightParams{
		ProjectID: p.ID, InsightID: current.ID, TargetRevision: 4, ExpectedRevision: 4, ChangedBy: u.ID,
	})
	require.NoError(t, err)
	require.Equal(t, 4, noOp.Revision)
	require.Equal(t, restored.UpdatedAt, noOp.UpdatedAt)
	require.Len(t, noOp.LinkWarnings, 1)
	require.Len(t, readInsightRevisions(t, pool, current.ID), 4)
	detail, err = st.GetInsight(ctx, store.GetInsightParams{ProjectID: p.ID, InsightID: current.ID, RelationLimit: 10})
	require.NoError(t, err)
	require.Len(t, detail.Links, 2)

	changed, err := st.RestoreInsight(ctx, store.RestoreInsightParams{
		ProjectID: p.ID, InsightID: current.ID, TargetRevision: 2, ExpectedRevision: 4, ChangedBy: u.ID,
	})
	require.NoError(t, err)
	require.Equal(t, 5, changed.Revision)
	require.Equal(t, "[[insight:changed]]", changed.Content)
	require.Equal(t, []string{"v2"}, changed.Tags)
	require.Equal(t, "context", changed.Category)
	require.Equal(t, "agent", changed.Source)

	_, err = st.RestoreInsight(ctx, store.RestoreInsightParams{
		ProjectID: p.ID, InsightID: current.ID, TargetRevision: 99, ExpectedRevision: 5, ChangedBy: u.ID,
	})
	require.ErrorIs(t, err, store.ErrInsightRevisionNotFound)
	other, err := st.EnsureProject(ctx, p.OrgID, u.ID, "restore-other", "Restore Other")
	require.NoError(t, err)
	_, err = st.RestoreInsight(ctx, store.RestoreInsightParams{
		ProjectID: other.ID, InsightID: current.ID, TargetRevision: 1, ExpectedRevision: 5, ChangedBy: u.ID,
	})
	require.ErrorIs(t, err, store.ErrNotFound)
	_, err = st.RestoreInsight(ctx, store.RestoreInsightParams{
		ProjectID: p.ID, InsightID: current.ID, TargetRevision: 0, ExpectedRevision: 5, ChangedBy: u.ID,
	})
	require.ErrorIs(t, err, store.ErrInvalidTargetRevision)
	_, err = st.RestoreInsight(ctx, store.RestoreInsightParams{
		ProjectID: p.ID, InsightID: current.ID, TargetRevision: 1, ExpectedRevision: 0, ChangedBy: u.ID,
	})
	require.ErrorIs(t, err, store.ErrInvalidExpectedRevision)
}

func TestRestoreInsightMapsUniqueKeyConflictWithoutMutation(t *testing.T) {
	st, dsn := newTestStoreAndDSN(t)
	ctx := t.Context()
	u, p := testUserAndProject(t, st, 39)
	pool, err := pgxpool.New(ctx, dsn)
	require.NoError(t, err)
	t.Cleanup(pool.Close)

	original, err := st.WriteInsight(ctx, store.WriteInsightParams{
		ProjectID: p.ID, Key: "reused", Content: "original", CreatedBy: u.ID,
	})
	require.NoError(t, err)
	deletedRevision, err := st.DeleteInsight(ctx, store.DeleteInsightParams{
		OrgID: p.OrgID, InsightID: original.ID, ChangedBy: u.ID,
	})
	require.NoError(t, err)
	_, err = st.WriteInsight(ctx, store.WriteInsightParams{
		ProjectID: p.ID, Key: "reused", Content: "claimant", CreatedBy: u.ID,
	})
	require.NoError(t, err)

	_, err = st.RestoreInsight(ctx, store.RestoreInsightParams{
		ProjectID: p.ID, InsightID: original.ID, TargetRevision: 1,
		ExpectedRevision: deletedRevision, ChangedBy: u.ID,
	})
	require.ErrorIs(t, err, store.ErrInsightKeyConflict)
	require.Len(t, readInsightRevisions(t, pool, original.ID), 2)
	history, err := st.ListInsightHistory(ctx, store.ListInsightHistoryParams{
		ProjectID: p.ID, InsightID: original.ID, Limit: 10,
	})
	require.NoError(t, err)
	require.Equal(t, 2, history.CurrentRevision)
	require.NotNil(t, history.DeletedAt)
}

func TestRestoreInsightDeletedSameStateStillCreatesLiveRevision(t *testing.T) {
	st, dsn := newTestStoreAndDSN(t)
	ctx := t.Context()
	u, p := testUserAndProject(t, st, 43)
	pool, err := pgxpool.New(ctx, dsn)
	require.NoError(t, err)
	t.Cleanup(pool.Close)

	current, err := st.WriteInsight(ctx, store.WriteInsightParams{
		ProjectID: p.ID, Key: "same-state", Content: "unchanged", CreatedBy: u.ID,
	})
	require.NoError(t, err)
	deletedRevision, err := st.DeleteInsight(ctx, store.DeleteInsightParams{
		OrgID: p.OrgID, InsightID: current.ID, ChangedBy: u.ID,
	})
	require.NoError(t, err)
	restored, err := st.RestoreInsight(ctx, store.RestoreInsightParams{
		ProjectID: p.ID, InsightID: current.ID, TargetRevision: deletedRevision,
		ExpectedRevision: deletedRevision, ChangedBy: u.ID,
	})
	require.NoError(t, err)
	require.Equal(t, 3, restored.Revision)
	require.Equal(t, "unchanged", restored.Content)
	revisions := readInsightRevisions(t, pool, current.ID)
	require.Equal(t, []string{"create", "delete", "restore"}, []string{
		revisions[0].Operation, revisions[1].Operation, revisions[2].Operation,
	})
	require.NotNil(t, revisions[1].DeletedAt)
	require.Nil(t, revisions[2].DeletedAt)
}

func TestRestoreInsightConcurrentPreconditionAllowsOneWriter(t *testing.T) {
	st, dsn := newTestStoreAndDSN(t)
	ctx := t.Context()
	u, p := testUserAndProject(t, st, 40)
	pool, err := pgxpool.New(ctx, dsn)
	require.NoError(t, err)
	t.Cleanup(pool.Close)

	current, err := st.WriteInsight(ctx, store.WriteInsightParams{
		ProjectID: p.ID, Key: "restore-race", Content: "v1", CreatedBy: u.ID,
	})
	require.NoError(t, err)
	expected := current.Revision
	current, err = st.UpdateInsight(ctx, store.UpdateInsightParams{
		OrgID: p.OrgID, InsightID: current.ID, Content: "v2", ChangedBy: u.ID, ExpectedRevision: &expected,
	})
	require.NoError(t, err)

	start := make(chan struct{})
	results := make(chan error, 2)
	for range 2 {
		go func() {
			<-start
			_, restoreErr := st.RestoreInsight(ctx, store.RestoreInsightParams{
				ProjectID: p.ID, InsightID: current.ID, TargetRevision: 1,
				ExpectedRevision: 2, ChangedBy: u.ID,
			})
			results <- restoreErr
		}()
	}
	close(start)
	var successes, conflicts int
	for range 2 {
		resultErr := <-results
		if resultErr == nil {
			successes++
			continue
		}
		var conflict *store.RevisionConflictError
		require.ErrorAs(t, resultErr, &conflict)
		require.Equal(t, 3, conflict.Current)
		conflicts++
	}
	require.Equal(t, 1, successes)
	require.Equal(t, 1, conflicts)
	require.Len(t, readInsightRevisions(t, pool, current.ID), 3)
}

func TestRestoreInsightRollsBackDerivedStateAndRevision(t *testing.T) {
	tests := []struct {
		name       string
		failureSQL string
	}{
		{
			name: "link synchronization",
			failureSQL: `
				CREATE FUNCTION reject_restore_link() RETURNS trigger LANGUAGE plpgsql AS $$
				BEGIN RAISE EXCEPTION 'reject restore link'; END $$;
				CREATE TRIGGER reject_restore_link BEFORE INSERT ON insight_links
				FOR EACH ROW WHEN (NEW.target_key = 'original') EXECUTE FUNCTION reject_restore_link()`,
		},
		{
			name: "revision insertion",
			failureSQL: `
				CREATE FUNCTION reject_restore_revision() RETURNS trigger LANGUAGE plpgsql AS $$
				BEGIN RAISE EXCEPTION 'reject restore revision'; END $$;
				CREATE TRIGGER reject_restore_revision BEFORE INSERT ON insight_revisions
				FOR EACH ROW WHEN (NEW.operation = 'restore') EXECUTE FUNCTION reject_restore_revision()`,
		},
	}
	for i, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			st, dsn := newTestStoreAndDSN(t)
			ctx := t.Context()
			u, p := testUserAndProject(t, st, int64(41+i))
			pool, err := pgxpool.New(ctx, dsn)
			require.NoError(t, err)
			t.Cleanup(pool.Close)
			current, err := st.WriteInsight(ctx, store.WriteInsightParams{
				ProjectID: p.ID, Key: "restore-rollback", Content: "[[insight:original]]", CreatedBy: u.ID,
			})
			require.NoError(t, err)
			expected := current.Revision
			current, err = st.UpdateInsight(ctx, store.UpdateInsightParams{
				OrgID: p.OrgID, InsightID: current.ID, Content: "[[insight:changed]]",
				ChangedBy: u.ID, ExpectedRevision: &expected,
			})
			require.NoError(t, err)
			_, err = pool.Exec(ctx, tc.failureSQL)
			require.NoError(t, err)

			_, err = st.RestoreInsight(ctx, store.RestoreInsightParams{
				ProjectID: p.ID, InsightID: current.ID, TargetRevision: 1,
				ExpectedRevision: 2, ChangedBy: u.ID,
			})
			require.Error(t, err)
			detail, err := st.GetInsight(ctx, store.GetInsightParams{
				ProjectID: p.ID, InsightID: current.ID, RelationLimit: 10,
			})
			require.NoError(t, err)
			require.Equal(t, "[[insight:changed]]", detail.Insight.Content)
			require.Equal(t, 2, detail.Insight.Revision)
			require.Len(t, detail.Links, 1)
			require.Equal(t, "changed", detail.Links[0].TargetKey)
			require.Len(t, readInsightRevisions(t, pool, current.ID), 2)
		})
	}
}

func TestInsightRevisionPreconditions(t *testing.T) {
	st, dsn := newTestStoreAndDSN(t)
	ctx := t.Context()
	u, p := testUserAndProject(t, st, 19)
	pool, err := pgxpool.New(ctx, dsn)
	require.NoError(t, err)
	t.Cleanup(pool.Close)

	expectedThree := 3
	_, err = st.WriteInsight(ctx, store.WriteInsightParams{
		ProjectID: p.ID, Key: "missing", Content: "no", CreatedBy: u.ID, ExpectedRevision: &expectedThree,
	})
	var conflict *store.RevisionConflictError
	require.ErrorAs(t, err, &conflict)
	require.Equal(t, &store.RevisionConflictError{Expected: 3, Current: 0}, conflict)

	_, err = st.WriteInsight(ctx, store.WriteInsightParams{
		ProjectID: p.ID, Content: "keyless", CreatedBy: u.ID, ExpectedRevision: &expectedThree,
	})
	require.ErrorIs(t, err, store.ErrInvalidExpectedRevision)

	expectedZero := 0
	keyless, err := st.WriteInsight(ctx, store.WriteInsightParams{
		ProjectID: p.ID, Content: "keyless zero", CreatedBy: u.ID, ExpectedRevision: &expectedZero,
	})
	require.NoError(t, err)
	require.Equal(t, 1, keyless.Revision)
	_, err = st.UpdateInsight(ctx, store.UpdateInsightParams{
		OrgID: p.OrgID, InsightID: keyless.ID, Content: "invalid", ExpectedRevision: &expectedZero,
	})
	require.ErrorIs(t, err, store.ErrInvalidExpectedRevision)
	_, err = st.DeleteInsight(ctx, store.DeleteInsightParams{
		OrgID: p.OrgID, InsightID: keyless.ID, ExpectedRevision: &expectedZero,
	})
	require.ErrorIs(t, err, store.ErrInvalidExpectedRevision)
	current, err := st.WriteInsight(ctx, store.WriteInsightParams{
		ProjectID: p.ID, Key: "guarded", Content: "v1", CreatedBy: u.ID, ExpectedRevision: &expectedZero,
	})
	require.NoError(t, err)
	_, err = st.WriteInsight(ctx, store.WriteInsightParams{
		ProjectID: p.ID, Key: "guarded", Content: "replace", CreatedBy: u.ID, ExpectedRevision: &expectedZero,
	})
	conflict = nil
	require.ErrorAs(t, err, &conflict)
	require.Equal(t, &store.RevisionConflictError{Expected: 0, Current: 1}, conflict)

	expectedOne := 1
	current, err = st.UpdateInsight(ctx, store.UpdateInsightParams{
		OrgID: p.OrgID, InsightID: current.ID, Content: "v2", ChangedBy: u.ID, ExpectedRevision: &expectedOne,
	})
	require.NoError(t, err)
	require.Equal(t, 2, current.Revision)
	_, err = st.UpdateInsight(ctx, store.UpdateInsightParams{
		OrgID: p.OrgID, InsightID: current.ID, Content: "stale", ChangedBy: u.ID, ExpectedRevision: &expectedOne,
	})
	conflict = nil
	require.ErrorAs(t, err, &conflict)
	require.Equal(t, &store.RevisionConflictError{Expected: 1, Current: 2}, conflict)
	_, err = st.DeleteInsight(ctx, store.DeleteInsightParams{
		OrgID: p.OrgID, InsightID: current.ID, ChangedBy: u.ID, ExpectedRevision: &expectedOne,
	})
	conflict = nil
	require.ErrorAs(t, err, &conflict)
	require.Equal(t, &store.RevisionConflictError{Expected: 1, Current: 2}, conflict)
	require.Len(t, readInsightRevisions(t, pool, current.ID), 2)

	concurrent, err := st.WriteInsight(ctx, store.WriteInsightParams{
		ProjectID: p.ID, Key: "concurrent", Content: "start", CreatedBy: u.ID,
	})
	require.NoError(t, err)
	start := make(chan struct{})
	results := make(chan error, 2)
	for _, content := range []string{"first", "second"} {
		go func() {
			<-start
			_, updateErr := st.UpdateInsight(ctx, store.UpdateInsightParams{
				OrgID: p.OrgID, InsightID: concurrent.ID, Content: content,
				ChangedBy: u.ID, ExpectedRevision: &expectedOne,
			})
			results <- updateErr
		}()
	}
	close(start)
	var successes, conflicts int
	for range 2 {
		resultErr := <-results
		if resultErr == nil {
			successes++
			continue
		}
		conflict = nil
		require.ErrorAs(t, resultErr, &conflict)
		require.Equal(t, 2, conflict.Current)
		conflicts++
	}
	require.Equal(t, 1, successes)
	require.Equal(t, 1, conflicts)
	require.Len(t, readInsightRevisions(t, pool, concurrent.ID), 2)
}

func TestInsightRevisionMutationRollback(t *testing.T) {
	tests := []struct {
		name       string
		failureSQL string
		content    string
	}{
		{
			name: "link synchronization",
			failureSQL: `
				CREATE FUNCTION reject_insight_link() RETURNS trigger LANGUAGE plpgsql AS $$
				BEGIN RAISE EXCEPTION 'reject link'; END $$;
				CREATE TRIGGER reject_insight_link BEFORE INSERT ON insight_links
				FOR EACH ROW WHEN (NEW.target_key = 'reject') EXECUTE FUNCTION reject_insight_link()`,
			content: "[[insight:reject]]",
		},
		{
			name: "revision insertion",
			failureSQL: `
				CREATE FUNCTION reject_insight_revision() RETURNS trigger LANGUAGE plpgsql AS $$
				BEGIN RAISE EXCEPTION 'reject revision'; END $$;
				CREATE TRIGGER reject_insight_revision BEFORE INSERT ON insight_revisions
				FOR EACH ROW WHEN (NEW.operation = 'update') EXECUTE FUNCTION reject_insight_revision()`,
			content: "[[insight:changed]]",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			st, dsn := newTestStoreAndDSN(t)
			ctx := t.Context()
			u, p := testUserAndProject(t, st, 29)
			current, err := st.WriteInsight(ctx, store.WriteInsightParams{
				ProjectID: p.ID, Key: "source", Content: "[[insight:original]]", CreatedBy: u.ID,
			})
			require.NoError(t, err)
			pool, err := pgxpool.New(ctx, dsn)
			require.NoError(t, err)
			t.Cleanup(pool.Close)
			_, err = pool.Exec(ctx, tc.failureSQL)
			require.NoError(t, err)

			_, err = st.UpdateInsight(ctx, store.UpdateInsightParams{
				OrgID: p.OrgID, InsightID: current.ID, Content: tc.content, ChangedBy: u.ID,
			})
			require.Error(t, err)
			detail, err := st.GetInsight(ctx, store.GetInsightParams{
				ProjectID: p.ID, InsightID: current.ID, RelationLimit: 10,
			})
			require.NoError(t, err)
			require.Equal(t, "[[insight:original]]", detail.Insight.Content)
			require.Equal(t, 1, detail.Insight.Revision)
			require.Len(t, detail.Links, 1)
			require.Equal(t, "original", detail.Links[0].TargetKey)
			require.Len(t, readInsightRevisions(t, pool, current.ID), 1)
		})
	}
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
	_, err = st.DeleteInsight(ctx, store.DeleteInsightParams{OrgID: p.OrgID, InsightID: targetID, ChangedBy: u.ID})
	require.NoError(t, err)

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
	}, source.LinkWarnings, "semantic no-op must refresh derived link warnings")

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

func TestInsightLinks_NoOpRepairsLegacyProjection(t *testing.T) {
	st, dsn := newTestStoreAndDSN(t)
	ctx := t.Context()
	u, p := testUserAndProject(t, st, 35)

	_, err := st.WriteInsight(ctx, store.WriteInsightParams{
		ProjectID: p.ID, Key: "target", Content: "target", CreatedBy: u.ID,
	})
	require.NoError(t, err)
	source, err := st.WriteInsight(ctx, store.WriteInsightParams{
		ProjectID: p.ID, Key: "source", Content: "[[insight:target]] [[insight:missing]]", CreatedBy: u.ID,
	})
	require.NoError(t, err)

	pool, err := pgxpool.New(ctx, dsn)
	require.NoError(t, err)
	t.Cleanup(pool.Close)
	_, err = pool.Exec(ctx, `DELETE FROM insight_links WHERE source_insight_id = $1`, source.ID)
	require.NoError(t, err)

	expectedRevision := source.Revision
	repaired, err := st.WriteInsight(ctx, store.WriteInsightParams{
		ProjectID: p.ID, Key: source.Key, Content: source.Content, CreatedBy: u.ID,
		ExpectedRevision: &expectedRevision,
	})
	require.NoError(t, err)
	require.Equal(t, source.Revision, repaired.Revision)
	require.True(t, source.UpdatedAt.Equal(repaired.UpdatedAt))
	require.False(t, repaired.ContentChanged)
	require.Equal(t, []store.InsightLinkWarning{
		{Code: store.InsightLinkWarningUnresolved, TargetKey: "missing"},
	}, repaired.LinkWarnings)
	require.Len(t, readInsightRevisions(t, pool, source.ID), 1)

	detail, err := st.GetInsight(ctx, store.GetInsightParams{
		ProjectID: p.ID, InsightID: source.ID, RelationLimit: 10,
	})
	require.NoError(t, err)
	require.Equal(t, 2, detail.LinkCount)
	require.Equal(t, "missing", detail.Links[0].TargetKey)
	require.False(t, detail.Links[0].Resolved)
	require.Equal(t, "target", detail.Links[1].TargetKey)
	require.True(t, detail.Links[1].Resolved)

	_, err = pool.Exec(ctx, `DELETE FROM insight_links WHERE source_insight_id = $1`, source.ID)
	require.NoError(t, err)
	repaired, err = st.UpdateInsight(ctx, store.UpdateInsightParams{
		OrgID: p.OrgID, InsightID: source.ID, Content: source.Content,
		ChangedBy: u.ID, ExpectedRevision: &expectedRevision,
	})
	require.NoError(t, err)
	require.Equal(t, source.Revision, repaired.Revision)
	require.True(t, source.UpdatedAt.Equal(repaired.UpdatedAt))
	require.False(t, repaired.ContentChanged)
	require.Len(t, readInsightRevisions(t, pool, source.ID), 1)

	detail, err = st.GetInsight(ctx, store.GetInsightParams{
		ProjectID: p.ID, InsightID: source.ID, RelationLimit: 10,
	})
	require.NoError(t, err)
	require.Equal(t, 2, detail.LinkCount)

	_, err = pool.Exec(ctx, `DELETE FROM insight_links WHERE source_insight_id = $1`, source.ID)
	require.NoError(t, err)
	repaired, err = st.UpdateInsight(ctx, store.UpdateInsightParams{
		OrgID: p.OrgID, InsightID: source.ID, Content: source.Content, Tags: []string{"updated"},
		ChangedBy: u.ID, ExpectedRevision: &expectedRevision,
	})
	require.NoError(t, err)
	require.Equal(t, source.Revision+1, repaired.Revision)
	require.False(t, repaired.ContentChanged)
	require.Len(t, readInsightRevisions(t, pool, source.ID), 2)

	detail, err = st.GetInsight(ctx, store.GetInsightParams{
		ProjectID: p.ID, InsightID: source.ID, RelationLimit: 10,
	})
	require.NoError(t, err)
	require.Equal(t, 2, detail.LinkCount)
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
	st, dsn := newTestStoreAndDSN(t)
	ctx := t.Context()
	u, p := testUserAndProject(t, st, 29)
	base := time.Date(2026, time.July, 14, 10, 0, 0, 0, time.UTC)

	root, err := st.WriteInsight(ctx, store.WriteInsightParams{
		ProjectID: p.ID, Key: "root", Content: "root", CreatedBy: u.ID,
	})
	require.NoError(t, err)
	alpha, err := st.WriteInsight(ctx, store.WriteInsightParams{
		ProjectID: p.ID, Key: "alpha", Content: "alpha", Category: "fact", CreatedBy: u.ID,
	})
	require.NoError(t, err)
	beta, err := st.WriteInsight(ctx, store.WriteInsightParams{
		ProjectID: p.ID, Key: "beta", Content: "beta", Category: "decision", CreatedBy: u.ID,
	})
	require.NoError(t, err)

	source, err := st.WriteInsight(ctx, store.WriteInsightParams{
		ProjectID: p.ID, Key: "source", Content: "[[insight:missing]] [[insight:beta]] [[insight:alpha]] [[insight:root]]", CreatedBy: u.ID,
	})
	require.NoError(t, err)
	keyless, err := st.WriteInsight(ctx, store.WriteInsightParams{
		ProjectID: p.ID, Content: "[[insight:root]]", CreatedBy: u.ID,
	})
	require.NoError(t, err)
	newest, err := st.WriteInsight(ctx, store.WriteInsightParams{
		ProjectID: p.ID, Key: "newest", Content: "[[insight:root]]", CreatedBy: u.ID,
	})
	require.NoError(t, err)
	setInsightTimestamps(t, dsn,
		insightTimestamps{insight: root, createdAt: base, updatedAt: base},
		insightTimestamps{insight: alpha, createdAt: base, updatedAt: base},
		insightTimestamps{insight: beta, createdAt: base, updatedAt: base},
		insightTimestamps{insight: source, createdAt: base.Add(time.Minute), updatedAt: base.Add(time.Minute)},
		insightTimestamps{insight: keyless, createdAt: base.Add(2 * time.Minute), updatedAt: base.Add(2 * time.Minute)},
		insightTimestamps{insight: newest, createdAt: base.Add(3 * time.Minute), updatedAt: base.Add(3 * time.Minute)},
	)

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

	_, err = st.DeleteInsight(ctx, store.DeleteInsightParams{OrgID: p.OrgID, InsightID: alpha.ID, ChangedBy: u.ID})
	require.NoError(t, err)
	detail, err = st.GetInsight(ctx, store.GetInsightParams{ProjectID: p.ID, Key: "source", RelationLimit: 10})
	require.NoError(t, err)
	require.False(t, detail.Links[0].Resolved)
	_, err = st.WriteInsight(ctx, store.WriteInsightParams{ProjectID: p.ID, Key: "alpha", Content: "recreated", CreatedBy: u.ID})
	require.NoError(t, err)
	detail, err = st.GetInsight(ctx, store.GetInsightParams{ProjectID: p.ID, Key: "source", RelationLimit: 10})
	require.NoError(t, err)
	require.True(t, detail.Links[0].Resolved)

	_, err = st.DeleteInsight(ctx, store.DeleteInsightParams{OrgID: p.OrgID, InsightID: root.ID, ChangedBy: u.ID})
	require.NoError(t, err)
	_, err = st.GetInsight(ctx, store.GetInsightParams{ProjectID: p.ID, InsightID: root.ID, RelationLimit: 10})
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

	results, err := st.SearchInsights(ctx, store.SearchInsightsParams{ProjectID: p.ID, Query: "relational database", QueryMode: store.SearchQueryModeAll, TagMode: store.SearchTagModeAll, Limit: 10})
	require.NoError(t, err)
	require.Len(t, results.Hits, 1)
	require.Contains(t, results.Hits[0].Insight.Content, "PostgreSQL")

	// Tag filter should narrow results.
	tagged, err := st.SearchInsights(ctx, store.SearchInsightsParams{ProjectID: p.ID, Query: "database", QueryMode: store.SearchQueryModeAll, Tags: []string{"cache"}, TagMode: store.SearchTagModeAll, Limit: 10})
	require.NoError(t, err)
	require.Empty(t, tagged.Hits, "cache tag should exclude PostgreSQL result")

	// Tag names should be searchable even when absent from content.
	byTagName, err := st.SearchInsights(ctx, store.SearchInsightsParams{ProjectID: p.ID, Query: "cache", QueryMode: store.SearchQueryModeAll, TagMode: store.SearchTagModeAll, Limit: 10})
	require.NoError(t, err)
	require.Len(t, byTagName.Hits, 1, "searching by tag name should find insights tagged with that word")
	require.Contains(t, byTagName.Hits[0].Insight.Content, "Redis")

	web, err := st.SearchInsights(ctx, store.SearchInsightsParams{ProjectID: p.ID, Query: "relational OR redis", QueryMode: store.SearchQueryModeWeb, TagMode: store.SearchTagModeAll, Limit: 10})
	require.NoError(t, err)
	require.Len(t, web.Hits, 2)

	_, err = st.WriteInsight(ctx, store.WriteInsightParams{ProjectID: p.ID, Content: "PostgreSQL replication", Tags: []string{"db", "operations"}, CreatedBy: u.ID})
	require.NoError(t, err)

	allTags, err := st.SearchInsights(ctx, store.SearchInsightsParams{ProjectID: p.ID, Query: "postgresql", QueryMode: store.SearchQueryModeAll, Tags: []string{"db", "operations"}, TagMode: store.SearchTagModeAll, Limit: 10})
	require.NoError(t, err)
	require.Len(t, allTags.Hits, 1)
	require.Contains(t, allTags.Hits[0].Insight.Content, "replication")

	anyTags, err := st.SearchInsights(ctx, store.SearchInsightsParams{ProjectID: p.ID, Query: "postgresql", QueryMode: store.SearchQueryModeAll, Tags: []string{"db", "operations"}, TagMode: store.SearchTagModeAny, Limit: 10})
	require.NoError(t, err)
	require.Len(t, anyTags.Hits, 2)
}

func TestSearchInsightsCompactProjection(t *testing.T) {
	st := newTestStore(t)
	ctx := t.Context()
	u, p := testUserAndProject(t, st, 33)
	content := strings.Repeat("prefix ", 50) + "compactneedle " + strings.Repeat("suffix ", 50)

	_, err := st.WriteInsight(ctx, store.WriteInsightParams{
		ProjectID: p.ID, Content: content, Tags: []string{"search"}, CreatedBy: u.ID,
	})
	require.NoError(t, err)
	_, err = st.WriteInsight(ctx, store.WriteInsightParams{
		ProjectID: p.ID, Content: strings.Repeat("tag content ", 50), Tags: []string{"tagneedle"}, CreatedBy: u.ID,
	})
	require.NoError(t, err)
	longWord := strings.Repeat("é", 1000)
	_, err = st.WriteInsight(ctx, store.WriteInsightParams{
		ProjectID: p.ID, Content: "oversizedneedle " + strings.Repeat(longWord+" ", 39), Tags: []string{"search"}, CreatedBy: u.ID,
	})
	require.NoError(t, err)

	full, err := st.SearchInsights(ctx, store.SearchInsightsParams{
		ProjectID: p.ID, Query: "compactneedle", QueryMode: store.SearchQueryModeAll,
		TagMode: store.SearchTagModeAll, Limit: 5,
	})
	require.NoError(t, err)
	require.Len(t, full.Hits, 1)
	require.Equal(t, content, full.Hits[0].Insight.Content)
	require.Empty(t, full.Hits[0].Snippet)

	compact, err := st.SearchInsights(ctx, store.SearchInsightsParams{
		ProjectID: p.ID, Query: "compactneedle", QueryMode: store.SearchQueryModeAll,
		TagMode: store.SearchTagModeAll, Limit: 5, Compact: true,
	})
	require.NoError(t, err)
	require.Len(t, compact.Hits, 1)
	require.Empty(t, compact.Hits[0].Insight.Content)
	require.Contains(t, compact.Hits[0].Snippet, "compactneedle")
	require.LessOrEqual(t, len(strings.Fields(compact.Hits[0].Snippet)), 40)
	require.NotContains(t, compact.Hits[0].Snippet, "<b>")

	tagOnly, err := st.SearchInsights(ctx, store.SearchInsightsParams{
		ProjectID: p.ID, Query: "tagneedle", QueryMode: store.SearchQueryModeAll,
		TagMode: store.SearchTagModeAll, Limit: 5, Compact: true,
	})
	require.NoError(t, err)
	require.Len(t, tagOnly.Hits, 1)
	require.Empty(t, tagOnly.Hits[0].Insight.Content)
	require.NotEmpty(t, tagOnly.Hits[0].Snippet)
	require.LessOrEqual(t, len(strings.Fields(tagOnly.Hits[0].Snippet)), 40)

	oversized, err := st.SearchInsights(ctx, store.SearchInsightsParams{
		ProjectID: p.ID, Query: "oversizedneedle", QueryMode: store.SearchQueryModeAll,
		TagMode: store.SearchTagModeAll, Limit: 5, Compact: true,
	})
	require.NoError(t, err)
	require.Len(t, oversized.Hits, 1)
	require.LessOrEqual(t, len(oversized.Hits[0].Snippet), store.MaxInsightSearchSnippetBytes)
	require.True(t, utf8.ValidString(oversized.Hits[0].Snippet))
	require.True(t, strings.HasSuffix(oversized.Hits[0].Snippet, "…"))
}

func TestSearchInsightsCursorPaginationAcrossRankAndIDBoundaries(t *testing.T) {
	st, dsn := newTestStoreAndDSN(t)
	ctx := t.Context()
	u, p := testUserAndProject(t, st, 31)
	updatedAt := time.Date(2026, 7, 18, 12, 0, 0, 123456000, time.UTC)
	searchTag := uuid.NewString()

	contents := []string{
		"pagination pagination pagination",
		"pagination pagination",
		"pagination token alpha",
		"pagination token beta",
	}
	var written []*store.Insight
	for _, content := range contents {
		insight, err := st.WriteInsight(ctx, store.WriteInsightParams{
			ProjectID: p.ID, Content: content, Tags: []string{searchTag}, CreatedBy: u.ID,
		})
		require.NoError(t, err)
		require.NotEqual(t, uuid.Nil, insight.ID)
		written = append(written, insight)
	}
	for _, insight := range written {
		setInsightTimestamps(t, dsn, insightTimestamps{insight: insight, createdAt: updatedAt, updatedAt: updatedAt})
	}

	base := store.SearchInsightsParams{
		ProjectID: p.ID, Query: "pagination", QueryMode: store.SearchQueryModeAll,
		Tags: []string{searchTag}, TagMode: store.SearchTagModeAll,
	}
	all := base
	all.Limit = 10
	allPage, err := st.SearchInsights(ctx, all)
	require.NoError(t, err)
	require.Len(t, allPage.Hits, 4)
	require.Equal(t, contents[0], allPage.Hits[0].Insight.Content)
	require.Equal(t, contents[1], allPage.Hits[1].Insight.Content)
	equalRankIDs := []string{allPage.Hits[2].Insight.ID.String(), allPage.Hits[3].Insight.ID.String()}
	require.True(t, sort.IsSorted(sort.Reverse(sort.StringSlice(equalRankIDs))))
	expected := make([]string, len(allPage.Hits))
	for i, hit := range allPage.Hits {
		expected[i] = hit.Insight.ID.String()
	}

	zero := base
	zero.Limit = 0
	page, err := st.SearchInsights(ctx, zero)
	require.NoError(t, err)
	require.Empty(t, page.Hits)
	require.Nil(t, page.NextCursor)

	exact := base
	exact.Limit = 4
	page, err = st.SearchInsights(ctx, exact)
	require.NoError(t, err)
	require.Len(t, page.Hits, 4)
	require.Nil(t, page.NextCursor)

	limitPlusOne := base
	limitPlusOne.Limit = 3
	page, err = st.SearchInsights(ctx, limitPlusOne)
	require.NoError(t, err)
	require.Len(t, page.Hits, 3)
	require.NotNil(t, page.NextCursor)

	var seen []string
	var boundaryRanks []float32
	after := (*store.InsightSearchCursor)(nil)
	for {
		request := base
		request.Limit = 1
		request.After = after
		page, err = st.SearchInsights(ctx, request)
		require.NoError(t, err)
		for _, hit := range page.Hits {
			seen = append(seen, hit.Insight.ID.String())
		}
		if page.NextCursor == nil {
			break
		}
		boundaryRanks = append(boundaryRanks, page.NextCursor.Rank)
		after = page.NextCursor
	}
	require.Equal(t, expected, seen)
	require.Len(t, map[string]struct{}{seen[0]: {}, seen[1]: {}, seen[2]: {}, seen[3]: {}}, 4)
	require.Len(t, boundaryRanks, 3)
	require.Greater(t, boundaryRanks[0], boundaryRanks[1])
	require.Greater(t, boundaryRanks[1], boundaryRanks[2])
}

func TestSearchInsightsCursorDocumentsConcurrentMutationLimitation(t *testing.T) {
	st, dsn := newTestStoreAndDSN(t)
	ctx := t.Context()
	u, p := testUserAndProject(t, st, 32)
	updatedAt := time.Date(2025, 7, 18, 12, 0, 0, 0, time.UTC)
	searchTag := uuid.NewString()

	var written []*store.Insight
	for range 3 {
		insight, err := st.WriteInsight(ctx, store.WriteInsightParams{
			ProjectID: p.ID, Content: "pagination token", Tags: []string{searchTag}, CreatedBy: u.ID,
		})
		require.NoError(t, err)
		written = append(written, insight)
	}
	for _, insight := range written {
		setInsightTimestamps(t, dsn, insightTimestamps{insight: insight, createdAt: updatedAt, updatedAt: updatedAt})
	}

	base := store.SearchInsightsParams{
		ProjectID: p.ID, Query: "pagination", QueryMode: store.SearchQueryModeAll,
		Tags: []string{searchTag}, TagMode: store.SearchTagModeAll,
	}
	baseline := base
	baseline.Limit = 10
	baselinePage, err := st.SearchInsights(ctx, baseline)
	require.NoError(t, err)
	require.Len(t, baselinePage.Hits, 3)

	first := base
	first.Limit = 1
	firstPage, err := st.SearchInsights(ctx, first)
	require.NoError(t, err)
	require.NotNil(t, firstPage.NextCursor)
	require.Equal(t, baselinePage.Hits[0].Insight.ID, firstPage.Hits[0].Insight.ID)

	moved := baselinePage.Hits[1].Insight
	_, err = st.UpdateInsight(ctx, store.UpdateInsightParams{
		OrgID: p.OrgID, InsightID: moved.ID, Tags: []string{searchTag, "moved"},
	})
	require.NoError(t, err)

	continuation := base
	continuation.Limit = 10
	continuation.After = firstPage.NextCursor
	remaining, err := st.SearchInsights(ctx, continuation)
	require.NoError(t, err)
	require.Len(t, remaining.Hits, 1)
	require.Equal(t, baselinePage.Hits[2].Insight.ID, remaining.Hits[0].Insight.ID)
	require.NotEqual(t, moved.ID, remaining.Hits[0].Insight.ID)
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
	st, dsn := newTestStoreAndDSN(t)
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
		})
		require.NoError(t, err)
		written = append(written, insight)
	}
	for _, insight := range written {
		setInsightTimestamps(t, dsn, insightTimestamps{insight: insight, createdAt: updatedAt, updatedAt: updatedAt})
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

	revision, err := st.DeleteInsight(ctx, store.DeleteInsightParams{OrgID: p.OrgID, InsightID: f.ID, ChangedBy: u.ID})
	require.NoError(t, err)
	require.Equal(t, 2, revision)

	// Deleted insight must not appear in list.
	insights, err := st.ListInsights(ctx, store.ListInsightsParams{ProjectID: p.ID, Limit: 10})
	require.NoError(t, err)
	require.Empty(t, insights.Insights)

	// Double-delete returns ErrNotFound.
	_, err = st.DeleteInsight(ctx, store.DeleteInsightParams{OrgID: p.OrgID, InsightID: f.ID, ChangedBy: u.ID})
	require.ErrorIs(t, err, store.ErrNotFound)
}

func TestDeleteInsight_NotFound(t *testing.T) {
	st := newTestStore(t)
	_, err := st.DeleteInsight(t.Context(), store.DeleteInsightParams{OrgID: uuid.New(), InsightID: uuid.New()})
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
	_, err = st.DeleteInsight(ctx, store.DeleteInsightParams{OrgID: p.OrgID, InsightID: f.ID, ChangedBy: u.ID})
	require.NoError(t, err)

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
	_, err = st.DeleteInsight(ctx, store.DeleteInsightParams{OrgID: p.OrgID, InsightID: deleted.ID, ChangedBy: u.ID})
	require.NoError(t, err)

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
	_, err = st.DeleteInsight(ctx, store.DeleteInsightParams{OrgID: p.OrgID, InsightID: f.ID, ChangedBy: u.ID})
	require.NoError(t, err)
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
	st, dsn := newTestStoreWithEncAndDSN(t, store.NewEncryptor(testEncKey))
	ctx := t.Context()

	u, err := st.UpsertUser(ctx, store.GitHubProfile{GitHubID: 200, Email: "pruneuser@example.com", Login: "pruneuser"})
	require.NoError(t, err)

	now := databaseNow(t, dsn).UTC()
	expired := store.Grant{
		JTI:                "expired-jti",
		UserID:             u.ID,
		ClientID:           "client-A",
		AccessToken:        "old-access",
		RefreshToken:       "old-refresh",
		AccessTokenExpiry:  now.Add(-10 * time.Hour),
		RefreshTokenExpiry: now.Add(-1 * time.Hour),
		JWTExpiry:          now.Add(-time.Minute), // already expired
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
		JWTExpiry:          now.Add(-time.Minute),
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
	st, dsn := newTestStoreAndDSN(t)
	ctx := t.Context()
	now := databaseNow(t, dsn)
	expiresAt := now.Add(-time.Minute)
	c := store.OAuthClient{
		ClientID:                "expired-temporary-client",
		RedirectURIs:            []string{"https://client.example.com/callback"},
		GrantTypes:              []string{"authorization_code"},
		ResponseTypes:           []string{"code"},
		TokenEndpointAuthMethod: "none",
		IssuedAt:                now.Add(-time.Hour),
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
		ClientID:             "client-abc",
		ClientName:           "Example client",
		ClientKind:           store.OAuthClientKindCIMD,
		RedirectURI:          "https://client.example.com/callback",
		Scope:                "insights:read",
		CodeChallenge:        "challenge-xyz",
		ClientState:          "opaque-state",
		RefreshAllowed:       true,
		ConfirmationRequired: true,
	}
	require.NoError(t, st.StorePendingAuth(ctx, "state-001", p))

	got, err := st.ConsumePendingAuth(ctx, "state-001")
	require.NoError(t, err)
	require.Equal(t, p.ClientID, got.ClientID)
	require.Equal(t, p.ClientName, got.ClientName)
	require.Equal(t, p.ClientKind, got.ClientKind)
	require.Equal(t, p.RedirectURI, got.RedirectURI)
	require.Equal(t, p.Scope, got.Scope)
	require.Equal(t, p.CodeChallenge, got.CodeChallenge)
	require.Equal(t, p.ClientState, got.ClientState)
	require.Equal(t, p.RefreshAllowed, got.RefreshAllowed)
	require.Equal(t, p.ConfirmationRequired, got.ConfirmationRequired)
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
	require.Equal(t, store.OAuthClientKindRegistered, got.ClientKind)
}

func TestPendingAuthMigrationDefaultsAllowLegacyRows(t *testing.T) {
	st, dsn := newTestStoreAndDSN(t)
	pool, err := pgxpool.New(t.Context(), dsn)
	require.NoError(t, err)
	t.Cleanup(pool.Close)
	_, err = pool.Exec(t.Context(), `
		INSERT INTO pending_auths (state, redirect_uri, scope, code_challenge, expires_at)
		VALUES ('legacy-state', 'https://client.example.com/callback', 'insights:read', 'challenge', now() + interval '1 minute')`)
	require.NoError(t, err)

	got, err := st.ConsumePendingAuth(t.Context(), "legacy-state")
	require.NoError(t, err)
	require.Empty(t, got.ClientName)
	require.Equal(t, store.OAuthClientKindRegistered, got.ClientKind)
	require.False(t, got.ConfirmationRequired)
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
		RefreshAllowed:     true,
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
	require.Equal(t, c.RefreshAllowed, got.RefreshAllowed)
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

// --- Authorization confirmations ---

func TestAuthorizationConfirmationApprovalIsAtomicAndEncrypted(t *testing.T) {
	st, dsn := newTestStoreWithEncAndDSN(t, store.NewEncryptor(testEncKey))
	ctx := t.Context()
	pool, err := pgxpool.New(ctx, dsn)
	require.NoError(t, err)
	t.Cleanup(pool.Close)
	tokenHash := store.HashSessionToken("confirmation-token")
	now := time.Now().UTC().Truncate(time.Second)
	c := store.AuthorizationConfirmation{
		AuthCode: store.AuthCode{
			Sub: "user-uuid", GitHubID: 1004, Email: "user@example.com", Scope: "insights:read",
			CodeChallenge: "challenge", RedirectURI: "https://client.example.com/callback",
			ClientID: "client", RefreshAllowed: true, AccessToken: "gha_confirmation_secret", RefreshToken: "ghr_confirmation_secret",
			AccessTokenExpiry: now.Add(time.Hour), RefreshTokenExpiry: now.Add(24 * time.Hour),
		},
		ClientName: "Example", ClientState: "state-value",
	}
	require.NoError(t, st.StoreAuthorizationConfirmation(ctx, tokenHash, c))

	var encAccess, encRefresh []byte
	require.NoError(t, pool.QueryRow(ctx, `SELECT access_token, refresh_token FROM authorization_confirmations WHERE token_hash = $1`, tokenHash).Scan(&encAccess, &encRefresh))
	require.NotContains(t, string(encAccess), c.AccessToken)
	require.NotContains(t, string(encRefresh), c.RefreshToken)

	result, err := st.CompleteAuthorizationConfirmation(ctx, tokenHash, true, "approved-code")
	require.NoError(t, err)
	require.Equal(t, c.RedirectURI, result.RedirectURI)
	require.Equal(t, c.ClientState, result.ClientState)

	got, err := st.ConsumeAuthCode(ctx, "approved-code")
	require.NoError(t, err)
	require.True(t, got.RefreshAllowed)
	require.Equal(t, c.AccessToken, got.AccessToken)
	require.Equal(t, c.RefreshToken, got.RefreshToken)
	require.WithinDuration(t, c.AccessTokenExpiry, got.AccessTokenExpiry, time.Second)

	_, err = st.CompleteAuthorizationConfirmation(ctx, tokenHash, true, "replay-code")
	require.ErrorIs(t, err, store.ErrNotFound)
	_, err = st.ConsumeAuthCode(ctx, "replay-code")
	require.ErrorIs(t, err, store.ErrNotFound)
}

func TestAuthorizationConfirmationDenialAndExpiryCreateNoCode(t *testing.T) {
	st, dsn := newTestStoreWithEncAndDSN(t, store.NewEncryptor(testEncKey))
	ctx := t.Context()
	pool, err := pgxpool.New(ctx, dsn)
	require.NoError(t, err)
	t.Cleanup(pool.Close)
	base := store.AuthorizationConfirmation{AuthCode: store.AuthCode{
		Sub: "user-uuid", GitHubID: 1005, Email: "user@example.com", Scope: "insights:read",
		CodeChallenge: "challenge", RedirectURI: "https://client.example.com/callback",
	}}

	denyHash := store.HashSessionToken("deny-token")
	require.NoError(t, st.StoreAuthorizationConfirmation(ctx, denyHash, base))
	_, err = st.CompleteAuthorizationConfirmation(ctx, denyHash, false, "")
	require.NoError(t, err)
	_, err = st.ConsumeAuthCode(ctx, "")
	require.ErrorIs(t, err, store.ErrNotFound)

	expiredHash := store.HashSessionToken("expired-token")
	require.NoError(t, st.StoreAuthorizationConfirmation(ctx, expiredHash, base))
	_, err = pool.Exec(ctx, `UPDATE authorization_confirmations SET expires_at = now() - interval '1 second' WHERE token_hash = $1`, expiredHash)
	require.NoError(t, err)
	_, err = st.CompleteAuthorizationConfirmation(ctx, expiredHash, true, "expired-code")
	require.ErrorIs(t, err, store.ErrNotFound)
	_, err = st.ConsumeAuthCode(ctx, "expired-code")
	require.ErrorIs(t, err, store.ErrNotFound)
}

func TestAuthorizationConfirmationConcurrentApprovalHasOneWinner(t *testing.T) {
	st := newTestStoreWithEnc(t, store.NewEncryptor(testEncKey))
	ctx := t.Context()
	tokenHash := store.HashSessionToken("concurrent-token")
	require.NoError(t, st.StoreAuthorizationConfirmation(ctx, tokenHash, store.AuthorizationConfirmation{AuthCode: store.AuthCode{
		Sub: "user-uuid", GitHubID: 1006, Email: "user@example.com", Scope: "insights:read",
		CodeChallenge: "challenge", RedirectURI: "https://client.example.com/callback",
	}}))

	errs := make(chan error, 2)
	for i := range 2 {
		go func(code string) {
			_, err := st.CompleteAuthorizationConfirmation(ctx, tokenHash, true, code)
			errs <- err
		}(fmt.Sprintf("concurrent-code-%d", i))
	}
	got := []error{<-errs, <-errs}
	var successes, notFound int
	for _, err := range got {
		if err == nil {
			successes++
		} else if errors.Is(err, store.ErrNotFound) {
			notFound++
		}
	}
	require.Equal(t, 1, successes)
	require.Equal(t, 1, notFound)
}

func TestAuthorizationConfirmationApprovalFailurePreservesConfirmation(t *testing.T) {
	st, dsn := newTestStoreWithEncAndDSN(t, store.NewEncryptor(testEncKey))
	ctx := t.Context()
	pool, err := pgxpool.New(ctx, dsn)
	require.NoError(t, err)
	t.Cleanup(pool.Close)
	tokenHash := store.HashSessionToken("rollback-token")
	require.NoError(t, st.StoreAuthorizationConfirmation(ctx, tokenHash, store.AuthorizationConfirmation{AuthCode: store.AuthCode{
		Sub: "user-uuid", GitHubID: 1007, Email: "user@example.com", Scope: "insights:read",
		CodeChallenge: "challenge", RedirectURI: "https://client.example.com/callback",
	}}))
	_, err = pool.Exec(ctx, `DROP TABLE auth_codes`)
	require.NoError(t, err)

	_, err = st.CompleteAuthorizationConfirmation(ctx, tokenHash, true, "failed-code")
	require.Error(t, err)
	var count int
	require.NoError(t, pool.QueryRow(ctx, `SELECT count(*) FROM authorization_confirmations WHERE token_hash = $1`, tokenHash).Scan(&count))
	require.Equal(t, 1, count)
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
	st, dsn := newTestStoreAndDSN(t)
	ctx := t.Context()

	jti := uuid.New().String()
	// expires_at in the past — token would have expired naturally, not considered revoked.
	exp := databaseNow(t, dsn).Add(-time.Minute)

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
	st, dsn := newTestStoreWithEncAndDSN(t, store.NewEncryptor(testEncKey))
	ctx := t.Context()

	u, err := st.UpsertUser(ctx, store.GitHubProfile{GitHubID: 1270, Email: "pruneretired@example.com", Login: "pruneretired"})
	require.NoError(t, err)

	now := databaseNow(t, dsn).UTC().Truncate(time.Second)
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
		RetainedUntil:  now.Add(-time.Minute),
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
