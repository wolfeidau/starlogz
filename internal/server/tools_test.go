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
	require.Contains(t, resultText(t, delRes), "not found")

	// Sanity: alice can still see her fact, proving the delete attempt did not succeed.
	listRes := callTool(t, ctx, aliceSess, "fact_list", map[string]any{"project": "demo", "tag": "", "limit": 0})
	require.False(t, listRes.IsError, "fact_list failed: %s", resultText(t, listRes))
	require.Contains(t, resultText(t, listRes), "alice's secret")
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
	require.Contains(t, resultText(t, updRes), "not found")

	// Sanity: alice's original content is intact.
	listRes := callTool(t, ctx, aliceSess, "fact_list", map[string]any{"project": "demo", "tag": "", "limit": 0})
	require.False(t, listRes.IsError, "fact_list failed: %s", resultText(t, listRes))
	require.Contains(t, resultText(t, listRes), "alice's original")
	require.NotContains(t, resultText(t, listRes), "tampered")
}
