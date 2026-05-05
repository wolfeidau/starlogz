package server_test

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/lestrrat-go/jwx/v3/jwk"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	postgrescont "github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"
	"github.com/wolfeidau/starlogz/internal/oidc"
	"github.com/wolfeidau/starlogz/internal/server"
	"github.com/wolfeidau/starlogz/internal/store"
	"github.com/wolfeidau/starlogz/internal/store/postgres"
)

// toolFixture wires a real Postgres-backed server so MCP tool calls can be
// invoked end-to-end via the streamable HTTP transport with bearer auth.
type toolFixture struct {
	ts      *httptest.Server
	oidcSrv *oidc.Server
	store   *postgres.Store
}

// newToolFixture spins up a postgres testcontainer, builds the server with a
// shared signing key, and returns a fixture ready for tool calls. The
// container, server, and DB pool are torn down on test cleanup.
func newToolFixture(t *testing.T) *toolFixture {
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

	st, err := postgres.New(ctx, dsn, nil)
	require.NoError(t, err)
	t.Cleanup(st.Close)
	require.NoError(t, st.Migrate(ctx, slog.Default()))

	priv, err := ecdsa.GenerateKey(elliptic.P384(), rand.Reader)
	require.NoError(t, err)
	raw, err := jwk.Import(priv)
	require.NoError(t, err)

	// BaseURL controls the iss/aud claims; it must match between IssueJWT and
	// VerifyJWT but does not need to match the httptest listener.
	const baseURL = "http://localhost"

	oidcSrv, err := oidc.NewServer(oidc.Config{
		BaseURL:    baseURL,
		AuthState:  newMemAuthState(),
		Revocation: newMemRevocation(),
	}, raw)
	require.NoError(t, err)

	srv, err := server.New(server.Config{
		BaseURL:    baseURL,
		PrivKey:    raw,
		Logger:     slog.Default(),
		Store:      st,
		AuthState:  newMemAuthState(),
		Revocation: newMemRevocation(),
	})
	require.NoError(t, err)

	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)

	return &toolFixture{ts: ts, oidcSrv: oidcSrv, store: st}
}

var ghIDSeq atomic.Int64

// makeUser inserts a user (and its personal org) and returns the row.
// Each call uses a unique synthetic GitHub ID so users never collide within a test.
func (f *toolFixture) makeUser(t *testing.T, ctx context.Context, login string) *store.User {
	t.Helper()
	id := ghIDSeq.Add(1)
	u, err := f.store.UpsertUser(ctx, id, login+"@example.com", login)
	require.NoError(t, err)
	return u
}

// tokenFor issues a JWT whose sub is the given user UUID and whose scope claim
// is the supplied space-separated string.
func (f *toolFixture) tokenFor(t *testing.T, userID uuid.UUID, scopes string) string {
	t.Helper()
	tok, err := f.oidcSrv.IssueJWT(userID.String(), "user@example.com", scopes, uuid.New().String(), time.Now().Add(time.Hour))
	require.NoError(t, err)
	return tok
}

// connect returns a connected MCP client session that injects the given bearer
// token on every request. The session is closed on test cleanup.
func (f *toolFixture) connect(t *testing.T, ctx context.Context, token string) *mcp.ClientSession {
	t.Helper()
	httpClient := &http.Client{Transport: &bearerTransport{token: token, base: http.DefaultTransport}}
	transport := &mcp.StreamableClientTransport{
		Endpoint:             f.ts.URL + "/mcp",
		HTTPClient:           httpClient,
		DisableStandaloneSSE: true,
	}
	client := mcp.NewClient(&mcp.Implementation{Name: "starlogz-tools-test"}, nil)
	sess, err := client.Connect(ctx, transport, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = sess.Close() })
	return sess
}

type bearerTransport struct {
	token string
	base  http.RoundTripper
}

func (b *bearerTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	r := req.Clone(req.Context())
	r.Header.Set("Authorization", "Bearer "+b.token)
	return b.base.RoundTrip(r)
}

// resultText returns the concatenated text content of a tool result, or the
// empty string when there is none. Tool handlers in this package always return
// a single TextContent, so callers can treat the return as the full payload.
func resultText(t *testing.T, res *mcp.CallToolResult) string {
	t.Helper()
	require.NotNil(t, res)
	if len(res.Content) == 0 {
		return ""
	}
	tc, ok := res.Content[0].(*mcp.TextContent)
	require.True(t, ok, "expected TextContent, got %T", res.Content[0])
	return tc.Text
}

// callTool invokes a tool with the given JSON-marshalable arguments and asserts
// the RPC itself succeeded. The returned result may still carry IsError=true,
// which the caller is expected to inspect.
func callTool(t *testing.T, ctx context.Context, sess *mcp.ClientSession, name string, args any) *mcp.CallToolResult {
	t.Helper()
	raw, err := json.Marshal(args)
	require.NoError(t, err)
	res, err := sess.CallTool(ctx, &mcp.CallToolParams{
		Name:      name,
		Arguments: json.RawMessage(raw),
	})
	require.NoError(t, err)
	return res
}

// --- Cross-org isolation regression (review #1) ---

// TestFactDelete_CrossOrg_Forbidden proves that a fact written by user A's
// session cannot be deleted by user B's session, even with a valid JWT and the
// facts:write scope. Regression for the v0.1 leak called out in tools_review.md.
func TestFactDelete_CrossOrg_Forbidden(t *testing.T) {
	ctx := context.Background()
	f := newToolFixture(t)

	alice := f.makeUser(t, ctx, "alice")
	bob := f.makeUser(t, ctx, "bob")

	aliceSess := f.connect(t, ctx, f.tokenFor(t, alice.ID, "facts:read facts:write"))
	bobSess := f.connect(t, ctx, f.tokenFor(t, bob.ID, "facts:read facts:write"))

	writeRes := callTool(t, ctx, aliceSess, "fact_write", map[string]any{
		"project": "demo",
		"content": "alice's secret",
		"key":     "",
		"tags":    []string{},
	})
	require.False(t, writeRes.IsError, "fact_write failed: %s", resultText(t, writeRes))

	var written struct {
		ID string `json:"id"`
	}
	require.NoError(t, json.Unmarshal([]byte(resultText(t, writeRes)), &written))
	require.NotEmpty(t, written.ID)

	delRes := callTool(t, ctx, bobSess, "fact_delete", map[string]any{"id": written.ID})
	require.True(t, delRes.IsError, "fact_delete must reject cross-org delete; got %s", resultText(t, delRes))
	require.Contains(t, resultText(t, delRes), "not found or already deleted")

	// Sanity: alice can still see her fact, proving the delete attempt did not succeed.
	listRes := callTool(t, ctx, aliceSess, "fact_list", map[string]any{"project": "demo", "tag": "", "limit": 0})
	require.False(t, listRes.IsError, "fact_list failed: %s", resultText(t, listRes))
	require.Contains(t, resultText(t, listRes), "alice's secret")
}

// --- whoami ---

func TestWhoami_ReturnsIdentityAndScopes(t *testing.T) {
	ctx := context.Background()
	f := newToolFixture(t)

	user := f.makeUser(t, ctx, "alice")
	sess := f.connect(t, ctx, f.tokenFor(t, user.ID, "facts:read facts:write"))

	res := callTool(t, ctx, sess, "whoami", map[string]any{})
	require.False(t, res.IsError, "whoami failed: %s", resultText(t, res))

	var got struct {
		UserID string   `json:"user_id"`
		Scopes []string `json:"scopes"`
	}
	require.NoError(t, json.Unmarshal([]byte(resultText(t, res)), &got))
	require.Equal(t, user.ID.String(), got.UserID)
	require.ElementsMatch(t, []string{"facts:read", "facts:write"}, got.Scopes)
}

// --- project_ensure ---

func TestProjectEnsure_CreatesUnderPersonalOrg(t *testing.T) {
	ctx := context.Background()
	f := newToolFixture(t)

	user := f.makeUser(t, ctx, "alice")
	sess := f.connect(t, ctx, f.tokenFor(t, user.ID, "facts:read facts:write"))

	res := callTool(t, ctx, sess, "project_ensure", map[string]any{"slug": "my-project", "name": "My Project"})
	require.False(t, res.IsError, "project_ensure failed: %s", resultText(t, res))

	var got struct {
		ID   string `json:"id"`
		Slug string `json:"slug"`
		Name string `json:"name"`
	}
	require.NoError(t, json.Unmarshal([]byte(resultText(t, res)), &got))
	require.Equal(t, "my-project", got.Slug)
	require.Equal(t, "My Project", got.Name)
	require.NotEmpty(t, got.ID)

	listRes := callTool(t, ctx, sess, "project_list", map[string]any{})
	require.False(t, listRes.IsError)
	require.Contains(t, resultText(t, listRes), "my-project")
}

func TestProjectEnsure_IdempotentOnDuplicateSlug(t *testing.T) {
	ctx := context.Background()
	f := newToolFixture(t)

	user := f.makeUser(t, ctx, "alice")
	sess := f.connect(t, ctx, f.tokenFor(t, user.ID, "facts:read facts:write"))

	res1 := callTool(t, ctx, sess, "project_ensure", map[string]any{"slug": "dup", "name": "Dup"})
	require.False(t, res1.IsError)
	var got1 struct {
		ID string `json:"id"`
	}
	require.NoError(t, json.Unmarshal([]byte(resultText(t, res1)), &got1))

	res2 := callTool(t, ctx, sess, "project_ensure", map[string]any{"slug": "dup", "name": "Dup"})
	require.False(t, res2.IsError)
	var got2 struct {
		ID string `json:"id"`
	}
	require.NoError(t, json.Unmarshal([]byte(resultText(t, res2)), &got2))

	require.Equal(t, got1.ID, got2.ID)
}

func TestProjectEnsure_DefaultsNameToSlug(t *testing.T) {
	ctx := context.Background()
	f := newToolFixture(t)

	user := f.makeUser(t, ctx, "alice")
	sess := f.connect(t, ctx, f.tokenFor(t, user.ID, "facts:read facts:write"))

	res := callTool(t, ctx, sess, "project_ensure", map[string]any{"slug": "my-slug", "name": ""})
	require.False(t, res.IsError)

	var got struct {
		Slug string `json:"slug"`
		Name string `json:"name"`
	}
	require.NoError(t, json.Unmarshal([]byte(resultText(t, res)), &got))
	require.Equal(t, got.Slug, got.Name)
}

// --- fact_write ---

func TestFactWrite_AutoCreatesProject(t *testing.T) {
	ctx := context.Background()
	f := newToolFixture(t)

	user := f.makeUser(t, ctx, "alice")
	sess := f.connect(t, ctx, f.tokenFor(t, user.ID, "facts:read facts:write"))

	res := callTool(t, ctx, sess, "fact_write", map[string]any{
		"project": "auto-project",
		"content": "hello",
		"key":     "",
		"tags":    []string{},
	})
	require.False(t, res.IsError, "fact_write failed: %s", resultText(t, res))

	listRes := callTool(t, ctx, sess, "project_list", map[string]any{})
	require.False(t, listRes.IsError)
	require.Contains(t, resultText(t, listRes), "auto-project")
}

func TestFactWrite_RequiresWriteScope(t *testing.T) {
	ctx := context.Background()
	f := newToolFixture(t)

	user := f.makeUser(t, ctx, "alice")
	sess := f.connect(t, ctx, f.tokenFor(t, user.ID, "facts:read"))

	res := callTool(t, ctx, sess, "fact_write", map[string]any{
		"project": "demo",
		"content": "should fail",
		"key":     "",
		"tags":    []string{},
	})
	require.True(t, res.IsError)
	require.Contains(t, resultText(t, res), "missing required scope")
}

func TestFactWrite_UpsertsByKey(t *testing.T) {
	ctx := context.Background()
	f := newToolFixture(t)

	user := f.makeUser(t, ctx, "alice")
	sess := f.connect(t, ctx, f.tokenFor(t, user.ID, "facts:read facts:write"))

	var result1, result2 struct {
		ID      string `json:"id"`
		Updated bool   `json:"updated"`
	}

	res1 := callTool(t, ctx, sess, "fact_write", map[string]any{
		"project": "demo",
		"content": "original",
		"key":     "stable-key",
		"tags":    []string{},
	})
	require.False(t, res1.IsError, "first write failed: %s", resultText(t, res1))
	require.NoError(t, json.Unmarshal([]byte(resultText(t, res1)), &result1))
	require.False(t, result1.Updated, "first write should not be an update")

	res2 := callTool(t, ctx, sess, "fact_write", map[string]any{
		"project": "demo",
		"content": "updated",
		"key":     "stable-key",
		"tags":    []string{},
	})
	require.False(t, res2.IsError, "second write failed: %s", resultText(t, res2))
	require.NoError(t, json.Unmarshal([]byte(resultText(t, res2)), &result2))
	require.Equal(t, result1.ID, result2.ID, "upsert must return the same ID")
	require.True(t, result2.Updated, "second write with same key should be an update")
}

// --- fact_list / fact_search / fact_list_tags org scoping ---

func TestFactList_ScopedToCallerOrg(t *testing.T) {
	ctx := context.Background()
	f := newToolFixture(t)

	alice := f.makeUser(t, ctx, "alice")
	bob := f.makeUser(t, ctx, "bob")

	aliceSess := f.connect(t, ctx, f.tokenFor(t, alice.ID, "facts:read facts:write"))
	bobSess := f.connect(t, ctx, f.tokenFor(t, bob.ID, "facts:read"))

	wr := callTool(t, ctx, aliceSess, "fact_write", map[string]any{
		"project": "alice-proj",
		"content": "alice data",
		"key":     "",
		"tags":    []string{},
	})
	require.False(t, wr.IsError)

	res := callTool(t, ctx, bobSess, "fact_list", map[string]any{
		"project": "alice-proj",
		"tag":     "",
		"limit":   0,
	})
	require.True(t, res.IsError)
	require.Contains(t, resultText(t, res), "project")
	require.Contains(t, resultText(t, res), "not found")
}

func TestFactSearch_ScopedToCallerOrg(t *testing.T) {
	ctx := context.Background()
	f := newToolFixture(t)

	alice := f.makeUser(t, ctx, "alice")
	bob := f.makeUser(t, ctx, "bob")

	aliceSess := f.connect(t, ctx, f.tokenFor(t, alice.ID, "facts:read facts:write"))
	bobSess := f.connect(t, ctx, f.tokenFor(t, bob.ID, "facts:read"))

	wr := callTool(t, ctx, aliceSess, "fact_write", map[string]any{
		"project": "alice-proj",
		"content": "alice data",
		"key":     "",
		"tags":    []string{},
	})
	require.False(t, wr.IsError)

	res := callTool(t, ctx, bobSess, "fact_search", map[string]any{
		"project": "alice-proj",
		"query":   "alice",
		"tags":    []string{},
		"limit":   0,
	})
	require.True(t, res.IsError)
	require.Contains(t, resultText(t, res), "project")
	require.Contains(t, resultText(t, res), "not found")
}

func TestFactSearch_ReturnsMatchingFacts(t *testing.T) {
	ctx := context.Background()
	f := newToolFixture(t)

	user := f.makeUser(t, ctx, "alice")
	sess := f.connect(t, ctx, f.tokenFor(t, user.ID, "facts:read facts:write"))

	for _, content := range []string{"golang concurrency patterns", "python asyncio basics"} {
		wr := callTool(t, ctx, sess, "fact_write", map[string]any{
			"project": "search-test",
			"content": content,
			"key":     "",
			"tags":    []string{},
		})
		require.False(t, wr.IsError, "fact_write failed: %s", resultText(t, wr))
	}

	res := callTool(t, ctx, sess, "fact_search", map[string]any{
		"project": "search-test",
		"query":   "concurrency",
		"tags":    []string{},
		"limit":   0,
	})
	require.False(t, res.IsError, "fact_search failed: %s", resultText(t, res))
	text := resultText(t, res)
	require.Contains(t, text, "golang concurrency patterns")
	require.NotContains(t, text, "python asyncio basics")
}

func TestFactListTags_ScopedToCallerOrg(t *testing.T) {
	ctx := context.Background()
	f := newToolFixture(t)

	alice := f.makeUser(t, ctx, "alice")
	bob := f.makeUser(t, ctx, "bob")

	aliceSess := f.connect(t, ctx, f.tokenFor(t, alice.ID, "facts:read facts:write"))
	bobSess := f.connect(t, ctx, f.tokenFor(t, bob.ID, "facts:read"))

	wr := callTool(t, ctx, aliceSess, "fact_write", map[string]any{
		"project": "alice-proj",
		"content": "alice data",
		"key":     "",
		"tags":    []string{"go"},
	})
	require.False(t, wr.IsError)

	res := callTool(t, ctx, bobSess, "fact_list_tags", map[string]any{
		"project": "alice-proj",
		"limit":   0,
	})
	require.True(t, res.IsError)
	require.Contains(t, resultText(t, res), "project")
	require.Contains(t, resultText(t, res), "not found")
}

func TestFactListTags_ReturnsByFrequency(t *testing.T) {
	ctx := context.Background()
	f := newToolFixture(t)

	user := f.makeUser(t, ctx, "alice")
	sess := f.connect(t, ctx, f.tokenFor(t, user.ID, "facts:read facts:write"))

	// "go" appears 3 times, "concurrency" once — go must sort first.
	for _, tags := range [][]string{{"go", "concurrency"}, {"go"}, {"go"}} {
		wr := callTool(t, ctx, sess, "fact_write", map[string]any{
			"project": "tags-test",
			"content": "content",
			"key":     "",
			"tags":    tags,
		})
		require.False(t, wr.IsError, "fact_write failed: %s", resultText(t, wr))
	}

	res := callTool(t, ctx, sess, "fact_list_tags", map[string]any{
		"project": "tags-test",
		"limit":   0,
	})
	require.False(t, res.IsError, "fact_list_tags failed: %s", resultText(t, res))

	var got struct {
		Tags []struct {
			Name  string `json:"name"`
			Count int    `json:"count"`
		} `json:"tags"`
	}
	require.NoError(t, json.Unmarshal([]byte(resultText(t, res)), &got))
	require.NotEmpty(t, got.Tags)
	require.Equal(t, "go", got.Tags[0].Name)
	require.Equal(t, 3, got.Tags[0].Count)
}

func TestFactList_RespectsTagFilter(t *testing.T) {
	ctx := context.Background()
	f := newToolFixture(t)

	user := f.makeUser(t, ctx, "alice")
	sess := f.connect(t, ctx, f.tokenFor(t, user.ID, "facts:read facts:write"))

	for _, w := range []struct {
		content string
		tags    []string
	}{
		{"go fact", []string{"go"}},
		{"python fact", []string{"python"}},
		{"both fact", []string{"go", "python"}},
	} {
		wr := callTool(t, ctx, sess, "fact_write", map[string]any{
			"project": "tagged",
			"content": w.content,
			"key":     "",
			"tags":    w.tags,
		})
		require.False(t, wr.IsError, "fact_write failed: %s", resultText(t, wr))
	}

	res := callTool(t, ctx, sess, "fact_list", map[string]any{
		"project": "tagged",
		"tag":     "go",
		"limit":   0,
	})
	require.False(t, res.IsError)
	text := resultText(t, res)
	require.Contains(t, text, "go fact")
	require.Contains(t, text, "both fact")
	require.NotContains(t, text, "python fact")
}

// --- fact_delete / fact_update: scope enforcement and error surfaces ---

func TestFactDelete_RequiresWriteScope(t *testing.T) {
	ctx := context.Background()
	f := newToolFixture(t)

	user := f.makeUser(t, ctx, "alice")
	sess := f.connect(t, ctx, f.tokenFor(t, user.ID, "facts:read"))

	res := callTool(t, ctx, sess, "fact_delete", map[string]any{"id": uuid.New().String()})
	require.True(t, res.IsError)
	require.Contains(t, resultText(t, res), "missing required scope")
}

func TestFactUpdate_RequiresWriteScope(t *testing.T) {
	ctx := context.Background()
	f := newToolFixture(t)

	user := f.makeUser(t, ctx, "alice")
	sess := f.connect(t, ctx, f.tokenFor(t, user.ID, "facts:read"))

	res := callTool(t, ctx, sess, "fact_update", map[string]any{
		"id":      uuid.New().String(),
		"content": "x",
		"tags":    []string{},
	})
	require.True(t, res.IsError)
	require.Contains(t, resultText(t, res), "missing required scope")
}

func TestFactDelete_UnknownID_NotFound(t *testing.T) {
	ctx := context.Background()
	f := newToolFixture(t)

	user := f.makeUser(t, ctx, "alice")
	sess := f.connect(t, ctx, f.tokenFor(t, user.ID, "facts:read facts:write"))

	res := callTool(t, ctx, sess, "fact_delete", map[string]any{"id": uuid.New().String()})
	require.True(t, res.IsError)
	require.Contains(t, resultText(t, res), "not found or already deleted")
}

func TestFactUpdate_UnknownID_NotFound(t *testing.T) {
	ctx := context.Background()
	f := newToolFixture(t)

	user := f.makeUser(t, ctx, "alice")
	sess := f.connect(t, ctx, f.tokenFor(t, user.ID, "facts:read facts:write"))

	res := callTool(t, ctx, sess, "fact_update", map[string]any{
		"id":      uuid.New().String(),
		"content": "x",
		"tags":    []string{},
	})
	require.True(t, res.IsError)
	require.Contains(t, resultText(t, res), "not found or already deleted")
}

func TestFactDelete_InvalidUUID_StructuredError(t *testing.T) {
	ctx := context.Background()
	f := newToolFixture(t)

	user := f.makeUser(t, ctx, "alice")
	sess := f.connect(t, ctx, f.tokenFor(t, user.ID, "facts:read facts:write"))

	res := callTool(t, ctx, sess, "fact_delete", map[string]any{"id": "not-a-uuid"})
	require.True(t, res.IsError)
	require.Contains(t, resultText(t, res), "invalid fact ID")
}

func TestFactDelete_RemovesFact(t *testing.T) {
	ctx := context.Background()
	f := newToolFixture(t)

	user := f.makeUser(t, ctx, "alice")
	sess := f.connect(t, ctx, f.tokenFor(t, user.ID, "facts:read facts:write"))

	wr := callTool(t, ctx, sess, "fact_write", map[string]any{
		"project": "delete-test",
		"content": "to be deleted",
		"key":     "",
		"tags":    []string{},
	})
	require.False(t, wr.IsError, "fact_write failed: %s", resultText(t, wr))
	var written struct {
		ID string `json:"id"`
	}
	require.NoError(t, json.Unmarshal([]byte(resultText(t, wr)), &written))

	listBefore := callTool(t, ctx, sess, "fact_list", map[string]any{"project": "delete-test", "tag": "", "limit": 0})
	require.False(t, listBefore.IsError)
	require.Contains(t, resultText(t, listBefore), "to be deleted")

	delRes := callTool(t, ctx, sess, "fact_delete", map[string]any{"id": written.ID})
	require.False(t, delRes.IsError, "fact_delete failed: %s", resultText(t, delRes))

	listAfter := callTool(t, ctx, sess, "fact_list", map[string]any{"project": "delete-test", "tag": "", "limit": 0})
	require.False(t, listAfter.IsError)
	require.NotContains(t, resultText(t, listAfter), "to be deleted")
}

// --- project_list ---

func TestProjectList_EmptyForFreshUser(t *testing.T) {
	ctx := context.Background()
	f := newToolFixture(t)

	user := f.makeUser(t, ctx, "alice")
	sess := f.connect(t, ctx, f.tokenFor(t, user.ID, "facts:read"))

	res := callTool(t, ctx, sess, "project_list", map[string]any{})
	require.False(t, res.IsError)

	var got struct {
		Projects []any `json:"projects"`
	}
	require.NoError(t, json.Unmarshal([]byte(resultText(t, res)), &got))
	require.Empty(t, got.Projects)
}

func TestProjectList_ReturnsOnlyOwnOrg(t *testing.T) {
	ctx := context.Background()
	f := newToolFixture(t)

	alice := f.makeUser(t, ctx, "alice")
	bob := f.makeUser(t, ctx, "bob")

	aliceSess := f.connect(t, ctx, f.tokenFor(t, alice.ID, "facts:read facts:write"))
	bobSess := f.connect(t, ctx, f.tokenFor(t, bob.ID, "facts:read"))

	wr := callTool(t, ctx, aliceSess, "project_ensure", map[string]any{"slug": "alice-proj", "name": "Alice"})
	require.False(t, wr.IsError)

	res := callTool(t, ctx, bobSess, "project_list", map[string]any{})
	require.False(t, res.IsError)

	var got struct {
		Projects []any `json:"projects"`
	}
	require.NoError(t, json.Unmarshal([]byte(resultText(t, res)), &got))
	require.Empty(t, got.Projects)
}

// --- resolveUserAndOrg ---

func TestResolveUser_UnknownSub_CleanError(t *testing.T) {
	ctx := context.Background()
	f := newToolFixture(t)

	// JWT sub is a valid UUID but has no corresponding user row in the database.
	token := f.tokenFor(t, uuid.New(), "facts:read facts:write")
	sess := f.connect(t, ctx, token)

	res := callTool(t, ctx, sess, "project_list", map[string]any{})
	require.True(t, res.IsError)
	require.Contains(t, resultText(t, res), "user not found")
}

// TestFactUpdate_CrossOrg_Forbidden is the analogous regression for fact_update.
func TestFactUpdate_CrossOrg_Forbidden(t *testing.T) {
	ctx := context.Background()
	f := newToolFixture(t)

	alice := f.makeUser(t, ctx, "alice")
	bob := f.makeUser(t, ctx, "bob")

	aliceSess := f.connect(t, ctx, f.tokenFor(t, alice.ID, "facts:read facts:write"))
	bobSess := f.connect(t, ctx, f.tokenFor(t, bob.ID, "facts:read facts:write"))

	writeRes := callTool(t, ctx, aliceSess, "fact_write", map[string]any{
		"project": "demo",
		"content": "alice's original",
		"key":     "",
		"tags":    []string{},
	})
	require.False(t, writeRes.IsError, "fact_write failed: %s", resultText(t, writeRes))

	var written struct {
		ID string `json:"id"`
	}
	require.NoError(t, json.Unmarshal([]byte(resultText(t, writeRes)), &written))
	require.NotEmpty(t, written.ID)

	updRes := callTool(t, ctx, bobSess, "fact_update", map[string]any{
		"id":      written.ID,
		"content": "tampered by bob",
		"tags":    []string{},
	})
	require.True(t, updRes.IsError, "fact_update must reject cross-org update; got %s", resultText(t, updRes))
	require.Contains(t, resultText(t, updRes), "not found or already deleted")

	// Sanity: alice's original content is intact.
	listRes := callTool(t, ctx, aliceSess, "fact_list", map[string]any{"project": "demo", "tag": "", "limit": 0})
	require.False(t, listRes.IsError, "fact_list failed: %s", resultText(t, listRes))
	require.Contains(t, resultText(t, listRes), "alice's original")
	require.NotContains(t, resultText(t, listRes), "tampered")
}

func TestFactUpdate_ChangesContentAndTags(t *testing.T) {
	ctx := context.Background()
	f := newToolFixture(t)

	user := f.makeUser(t, ctx, "alice")
	sess := f.connect(t, ctx, f.tokenFor(t, user.ID, "facts:read facts:write"))

	wr := callTool(t, ctx, sess, "fact_write", map[string]any{
		"project": "update-test",
		"content": "original content",
		"key":     "",
		"tags":    []string{"old-tag"},
	})
	require.False(t, wr.IsError, "fact_write failed: %s", resultText(t, wr))
	var written struct {
		ID string `json:"id"`
	}
	require.NoError(t, json.Unmarshal([]byte(resultText(t, wr)), &written))

	upd := callTool(t, ctx, sess, "fact_update", map[string]any{
		"id":      written.ID,
		"content": "updated content",
		"tags":    []string{"new-tag"},
	})
	require.False(t, upd.IsError, "fact_update failed: %s", resultText(t, upd))

	var updResult struct {
		ID      string   `json:"id"`
		Content string   `json:"content"`
		Tags    []string `json:"tags"`
	}
	require.NoError(t, json.Unmarshal([]byte(resultText(t, upd)), &updResult))
	require.Equal(t, written.ID, updResult.ID)
	require.Equal(t, "updated content", updResult.Content)
	require.Equal(t, []string{"new-tag"}, updResult.Tags)

	// Verify the change is persisted, not just returned from the mutation.
	listRes := callTool(t, ctx, sess, "fact_list", map[string]any{"project": "update-test", "tag": "", "limit": 0})
	require.False(t, listRes.IsError)
	text := resultText(t, listRes)
	require.Contains(t, text, "updated content")
	require.NotContains(t, text, "original content")
}
