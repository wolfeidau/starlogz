package integration_test

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/lestrrat-go/jwx/v3/jwk"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/require"
	"github.com/wolfeidau/starlogz/internal/clientclass"
	"github.com/wolfeidau/starlogz/internal/oidc"
	"github.com/wolfeidau/starlogz/internal/server"
	"github.com/wolfeidau/starlogz/internal/store"
	"github.com/wolfeidau/starlogz/internal/store/postgres"
	"github.com/wolfeidau/starlogz/internal/testutil/postgrestest"
)

var testDB = postgrestest.New("starlogz_tools_template", "starlogz_tools")

func TestMain(m *testing.M) {
	os.Exit(testDB.Run(m))
}

type memAuthState struct {
	pending map[string]store.PendingAuth
	codes   map[string]store.AuthCode
}

func newMemAuthState() *memAuthState {
	return &memAuthState{pending: map[string]store.PendingAuth{}, codes: map[string]store.AuthCode{}}
}

func (m *memAuthState) StorePendingAuth(_ context.Context, state string, p store.PendingAuth) error {
	m.pending[state] = p
	return nil
}

func (m *memAuthState) ConsumePendingAuth(_ context.Context, state string) (*store.PendingAuth, error) {
	p, ok := m.pending[state]
	if !ok {
		return nil, store.ErrNotFound
	}
	delete(m.pending, state)
	return &p, nil
}

func (m *memAuthState) StoreAuthCode(_ context.Context, code string, c store.AuthCode) error {
	m.codes[code] = c
	return nil
}

func (m *memAuthState) ConsumeAuthCode(_ context.Context, code string) (*store.AuthCode, error) {
	c, ok := m.codes[code]
	if !ok {
		return nil, store.ErrNotFound
	}
	delete(m.codes, code)
	return &c, nil
}

type memRevocation struct{ revoked map[string]struct{} }

func newMemRevocation() *memRevocation { return &memRevocation{revoked: map[string]struct{}{}} }

func (m *memRevocation) RevokeToken(_ context.Context, jti string, _ time.Time) error {
	m.revoked[jti] = struct{}{}
	return nil
}

func (m *memRevocation) IsTokenRevoked(_ context.Context, jti string) (bool, error) {
	_, ok := m.revoked[jti]
	return ok, nil
}

// toolFixture wires a real Postgres-backed server so MCP tool calls can be
// invoked end-to-end via the streamable HTTP transport with bearer auth.
type toolFixture struct {
	ts      *httptest.Server
	oidcSrv *oidc.Server
	store   *postgres.Store
}

// newToolFixture clones a migrated Postgres template database, builds the
// server with a shared signing key, and returns a fixture ready for tool calls.
func newToolFixture(t *testing.T) *toolFixture {
	t.Helper()
	ctx := t.Context()

	st, err := postgres.New(ctx, testDB.NewDSN(t), nil)
	require.NoError(t, err)
	t.Cleanup(st.Close)

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
	u, err := f.store.UpsertUser(ctx, store.GitHubProfile{GitHubID: id, Email: login + "@example.com", Login: login})
	require.NoError(t, err)
	return u
}

// tokenFor issues a JWT whose sub is the given user UUID and whose scope claim
// is the supplied space-separated string.
func (f *toolFixture) tokenFor(t *testing.T, userID uuid.UUID, scopes string) string {
	t.Helper()
	tok, err := f.oidcSrv.IssueJWT(userID.String(), scopes, uuid.New().String(), time.Now().Add(time.Hour), clientclass.Unknown())
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

func TestUIWebSession(t *testing.T) {
	f := newToolFixture(t)
	user := f.makeUser(t, t.Context(), "web-user")
	rawToken := "opaque-browser-session"
	now := time.Now()
	_, err := f.store.CreateWebSession(t.Context(), store.WebSession{
		TokenHash: store.HashSessionToken(rawToken), UserID: user.ID,
		IdleExpiresAt: now.Add(7 * 24 * time.Hour), ExpiresAt: now.Add(30 * 24 * time.Hour),
	})
	require.NoError(t, err)

	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost,
		f.ts.URL+"/starlogz.v1.UIService/GetSession", strings.NewReader("{}"))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(&http.Cookie{Name: "starlogz_session", Value: rawToken})
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	require.Equal(t, http.StatusOK, resp.StatusCode)

	var body map[string]any
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&body))
	require.Equal(t, user.ID.String(), body["userId"])
	require.Equal(t, "web-user", body["login"])
}

func TestUIGetInsightAndRenderedHTML(t *testing.T) {
	ctx := t.Context()
	f := newToolFixture(t)
	user := f.makeUser(t, ctx, "render-user")
	org, err := f.store.GetPersonalOrgByUserID(ctx, user.ID)
	require.NoError(t, err)
	project, err := f.store.EnsureProject(ctx, org.ID, user.ID, "render-project", "Render Project")
	require.NoError(t, err)
	target, err := f.store.WriteInsight(ctx, store.WriteInsightParams{
		ProjectID: project.ID, Key: "target", Content: "Target", Category: "fact", Source: "repo", CreatedBy: user.ID,
	})
	require.NoError(t, err)
	source, err := f.store.WriteInsight(ctx, store.WriteInsightParams{
		ProjectID: project.ID, Key: "source",
		Content:  "# Source\n\nFollow [[insight:target|Target <safe>]]. <script>alert(1)</script>",
		Category: "context", Source: "agent", CreatedBy: user.ID,
	})
	require.NoError(t, err)
	_, err = f.store.WriteInsight(ctx, store.WriteInsightParams{
		ProjectID: project.ID, Key: "backlink", Content: "[[insight:source]]", CreatedBy: user.ID,
	})
	require.NoError(t, err)

	rawToken := "render-browser-session"
	now := time.Now()
	_, err = f.store.CreateWebSession(ctx, store.WebSession{
		TokenHash: store.HashSessionToken(rawToken), UserID: user.ID,
		IdleExpiresAt: now.Add(time.Hour), ExpiresAt: now.Add(2 * time.Hour),
	})
	require.NoError(t, err)

	type protoInsight struct {
		ID           string `json:"id"`
		Content      string `json:"content"`
		RenderedHTML string `json:"renderedHtml"`
	}
	var got struct {
		Insight protoInsight `json:"insight"`
		Links   []struct {
			TargetKey string `json:"targetKey"`
			Resolved  bool   `json:"resolved"`
			ID        string `json:"id"`
		} `json:"links"`
		Backlinks     []map[string]any `json:"backlinks"`
		LinkCount     int              `json:"linkCount"`
		BacklinkCount int              `json:"backlinkCount"`
	}
	resp := f.uiRequest(t, rawToken, "/starlogz.v1.UIService/GetInsight", map[string]any{
		"project": "render-project", "key": "source", "relationLimit": 10,
	})
	defer func() { _ = resp.Body.Close() }()
	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&got))
	require.Equal(t, source.ID.String(), got.Insight.ID)
	require.Equal(t, source.Content, got.Insight.Content)
	require.Contains(t, got.Insight.RenderedHTML, "<h1>Source</h1>")
	require.Contains(t, got.Insight.RenderedHTML, `href="?project=render-project&amp;insight_key=target"`)
	require.Contains(t, got.Insight.RenderedHTML, "Target &lt;safe&gt;")
	require.NotContains(t, got.Insight.RenderedHTML, "<script")
	require.Equal(t, 1, got.LinkCount)
	require.Equal(t, 1, got.BacklinkCount)
	require.Equal(t, target.ID.String(), got.Links[0].ID)
	require.True(t, got.Links[0].Resolved)
	require.Len(t, got.Backlinks, 1)

	resp = f.uiRequest(t, rawToken, "/starlogz.v1.UIService/GetInsight", map[string]any{
		"project": "render-project", "id": source.ID.String(),
	})
	_ = resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)

	for path, input := range map[string]map[string]any{
		"/starlogz.v1.UIService/GetProjectDashboard": {"project": "render-project"},
		"/starlogz.v1.UIService/ListInsights":        {"project": "render-project", "limit": 10},
		"/starlogz.v1.UIService/SearchInsights":      {"project": "render-project", "query": "Source", "limit": 10},
	} {
		resp = f.uiRequest(t, rawToken, path, input)
		require.Equal(t, http.StatusOK, resp.StatusCode, path)
		var body map[string]any
		require.NoError(t, json.NewDecoder(resp.Body).Decode(&body))
		_ = resp.Body.Close()
		require.Contains(t, fmt.Sprint(body), "renderedHtml", path)
	}

	for _, input := range []map[string]any{
		{"project": "render-project"},
		{"project": "render-project", "key": ""},
		{"project": "render-project", "id": "not-a-uuid"},
		{"project": "render-project", "key": "source", "relationLimit": 0},
		{"project": "render-project", "key": "source", "relationLimit": 101},
	} {
		resp = f.uiRequest(t, rawToken, "/starlogz.v1.UIService/GetInsight", input)
		_ = resp.Body.Close()
		require.Equal(t, http.StatusBadRequest, resp.StatusCode, input)
	}
	for _, key := range []string{"missing", " source "} {
		resp = f.uiRequest(t, rawToken, "/starlogz.v1.UIService/GetInsight", map[string]any{
			"project": "render-project", "key": key,
		})
		_ = resp.Body.Close()
		require.Equal(t, http.StatusNotFound, resp.StatusCode)
	}
}

func TestUIWebSessionRejectsInvalidCookieAndClearsIt(t *testing.T) {
	f := newToolFixture(t)
	user := f.makeUser(t, t.Context(), "invalid-session-user")
	now := time.Now()

	for _, tc := range []struct {
		name     string
		rawToken string
		session  store.WebSession
		revoke   bool
	}{
		{
			name: "expired", rawToken: "expired-browser-session",
			session: store.WebSession{UserID: user.ID, IdleExpiresAt: now.Add(-time.Hour), ExpiresAt: now.Add(time.Hour)},
		},
		{
			name: "revoked", rawToken: "revoked-browser-session", revoke: true,
			session: store.WebSession{UserID: user.ID, IdleExpiresAt: now.Add(time.Hour), ExpiresAt: now.Add(2 * time.Hour)},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tc.session.TokenHash = store.HashSessionToken(tc.rawToken)
			_, err := f.store.CreateWebSession(t.Context(), tc.session)
			require.NoError(t, err)
			if tc.revoke {
				require.NoError(t, f.store.RevokeWebSessionByTokenHash(t.Context(), tc.session.TokenHash))
			}

			resp := f.uiSessionRequest(t, tc.rawToken)
			defer func() { _ = resp.Body.Close() }()
			require.Equal(t, http.StatusUnauthorized, resp.StatusCode)
			requireSessionCookieCleared(t, resp)
		})
	}
}

func TestUILogoutRevokesSession(t *testing.T) {
	f := newToolFixture(t)
	user := f.makeUser(t, t.Context(), "logout-user")
	rawToken := "logout-browser-session"
	now := time.Now()
	_, err := f.store.CreateWebSession(t.Context(), store.WebSession{
		TokenHash: store.HashSessionToken(rawToken), UserID: user.ID,
		IdleExpiresAt: now.Add(time.Hour), ExpiresAt: now.Add(2 * time.Hour),
	})
	require.NoError(t, err)

	client := &http.Client{CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }}
	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, f.ts.URL+"/logout", nil)
	require.NoError(t, err)
	req.AddCookie(&http.Cookie{Name: "starlogz_session", Value: rawToken})
	resp, err := client.Do(req)
	require.NoError(t, err)
	_ = resp.Body.Close()
	require.Equal(t, http.StatusFound, resp.StatusCode)
	requireSessionCookieCleared(t, resp)

	after := f.uiSessionRequest(t, rawToken)
	defer func() { _ = after.Body.Close() }()
	require.Equal(t, http.StatusUnauthorized, after.StatusCode)
}

func TestUIWebSessionTouchIsThrottled(t *testing.T) {
	f := newToolFixture(t)
	user := f.makeUser(t, t.Context(), "touch-user")
	rawToken := "touch-browser-session"
	now := time.Now()
	initialSeen := now.Add(-2 * time.Hour)
	_, err := f.store.CreateWebSession(t.Context(), store.WebSession{
		TokenHash: store.HashSessionToken(rawToken), UserID: user.ID, LastSeenAt: initialSeen,
		IdleExpiresAt: now.Add(time.Hour), ExpiresAt: now.Add(30 * 24 * time.Hour),
	})
	require.NoError(t, err)

	resp := f.uiSessionRequest(t, rawToken)
	_ = resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)
	touched, err := f.store.GetWebSessionByTokenHash(t.Context(), store.HashSessionToken(rawToken))
	require.NoError(t, err)
	require.True(t, touched.LastSeenAt.After(initialSeen))
	require.True(t, touched.IdleExpiresAt.After(now.Add(6*24*time.Hour)))

	resp = f.uiSessionRequest(t, rawToken)
	_ = resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)
	unchanged, err := f.store.GetWebSessionByTokenHash(t.Context(), store.HashSessionToken(rawToken))
	require.NoError(t, err)
	require.Equal(t, touched.LastSeenAt, unchanged.LastSeenAt)
}

func TestUIWebSessionStoreFailureKeepsCookie(t *testing.T) {
	f := newToolFixture(t)
	user := f.makeUser(t, t.Context(), "store-failure-user")
	rawToken := "store-failure-browser-session"
	now := time.Now()
	_, err := f.store.CreateWebSession(t.Context(), store.WebSession{
		TokenHash: store.HashSessionToken(rawToken), UserID: user.ID,
		IdleExpiresAt: now.Add(time.Hour), ExpiresAt: now.Add(2 * time.Hour),
	})
	require.NoError(t, err)
	f.store.Close()

	resp := f.uiSessionRequest(t, rawToken)
	defer func() { _ = resp.Body.Close() }()
	require.Equal(t, http.StatusInternalServerError, resp.StatusCode)
	for _, cookie := range resp.Cookies() {
		require.NotEqual(t, "starlogz_session", cookie.Name, "infrastructure errors must not clear the session cookie")
	}
}

func (f *toolFixture) uiSessionRequest(t *testing.T, rawToken string) *http.Response {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost,
		f.ts.URL+"/starlogz.v1.UIService/GetSession", strings.NewReader("{}"))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(&http.Cookie{Name: "starlogz_session", Value: rawToken})
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	return resp
}

func (f *toolFixture) uiRequest(t *testing.T, rawToken, path string, body any) *http.Response {
	t.Helper()
	payload, err := json.Marshal(body)
	require.NoError(t, err)
	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, f.ts.URL+path, strings.NewReader(string(payload)))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(&http.Cookie{Name: "starlogz_session", Value: rawToken})
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	return resp
}

func requireSessionCookieCleared(t *testing.T, resp *http.Response) {
	t.Helper()
	for _, cookie := range resp.Cookies() {
		if cookie.Name == "starlogz_session" {
			require.Less(t, cookie.MaxAge, 0)
			return
		}
	}
	require.Fail(t, "response did not clear starlogz_session")
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

// insightWriteArgs builds insight_write arguments; category and source are required by the tool schema.
func insightWriteArgs(project, content string, extra map[string]any) map[string]any {
	args := map[string]any{
		"project":  project,
		"content":  content,
		"key":      "",
		"tags":     []string{},
		"category": "fact",
		"source":   "agent",
	}
	for k, v := range extra {
		args[k] = v
	}
	return args
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

func inputSchemaMap(t *testing.T, tools []*mcp.Tool, name string) map[string]any {
	t.Helper()
	for _, tool := range tools {
		if tool.Name != name {
			continue
		}
		raw, err := json.Marshal(tool.InputSchema)
		require.NoError(t, err)
		var schema map[string]any
		require.NoError(t, json.Unmarshal(raw, &schema))
		return schema
	}
	require.FailNowf(t, "tool not found", "tool %q not found", name)
	return nil
}

func schemaProperty(t *testing.T, schema map[string]any, name string) map[string]any {
	t.Helper()
	properties, ok := schema["properties"].(map[string]any)
	require.True(t, ok, "schema properties missing")
	property, ok := properties[name].(map[string]any)
	require.True(t, ok, "schema property %q missing", name)
	return property
}

func schemaRequired(t *testing.T, schema map[string]any) []string {
	t.Helper()
	raw, ok := schema["required"].([]any)
	require.True(t, ok, "schema required missing")
	out := make([]string, len(raw))
	for i, v := range raw {
		out[i], ok = v.(string)
		require.True(t, ok, "required entry must be a string")
	}
	return out
}

func requireSchemaNumber(t *testing.T, expected float64, actual any) {
	t.Helper()
	require.InDelta(t, expected, actual, 0)
}

// --- Cross-org isolation regression (review #1) ---

// TestInsightDelete_CrossOrg_Forbidden proves that an insight written by user A's
// session cannot be deleted by user B's session, even with a valid JWT and the
// insights:write scope. Regression for the v0.1 leak called out in tools_review.md.
func TestInsightDelete_CrossOrg_Forbidden(t *testing.T) {
	ctx := t.Context()
	f := newToolFixture(t)

	alice := f.makeUser(t, ctx, "alice")
	bob := f.makeUser(t, ctx, "bob")

	aliceSess := f.connect(t, ctx, f.tokenFor(t, alice.ID, "insights:read insights:write"))
	bobSess := f.connect(t, ctx, f.tokenFor(t, bob.ID, "insights:read insights:write"))

	writeRes := callTool(t, ctx, aliceSess, "insight_write", insightWriteArgs("demo", "alice's secret", nil))
	require.False(t, writeRes.IsError, "insight_write failed: %s", resultText(t, writeRes))

	var written struct {
		ID string `json:"id"`
	}
	require.NoError(t, json.Unmarshal([]byte(resultText(t, writeRes)), &written))
	require.NotEmpty(t, written.ID)

	delRes := callTool(t, ctx, bobSess, "insight_delete", map[string]any{"id": written.ID})
	require.True(t, delRes.IsError, "insight_delete must reject cross-org delete; got %s", resultText(t, delRes))
	require.Contains(t, resultText(t, delRes), "not found or already deleted")

	// Sanity: alice can still see her fact, proving the delete attempt did not succeed.
	listRes := callTool(t, ctx, aliceSess, "insight_list", map[string]any{"project": "demo", "tag": "", "limit": 0})
	require.False(t, listRes.IsError, "insight_list failed: %s", resultText(t, listRes))
	require.Contains(t, resultText(t, listRes), "alice's secret")
}

// --- whoami ---

func TestWhoami_ReturnsIdentityAndScopes(t *testing.T) {
	ctx := t.Context()
	f := newToolFixture(t)

	user := f.makeUser(t, ctx, "alice")
	sess := f.connect(t, ctx, f.tokenFor(t, user.ID, "insights:read insights:write"))

	res := callTool(t, ctx, sess, "whoami", map[string]any{})
	require.False(t, res.IsError, "whoami failed: %s", resultText(t, res))

	var got struct {
		UserID string   `json:"user_id"`
		Scopes []string `json:"scopes"`
	}
	require.NoError(t, json.Unmarshal([]byte(resultText(t, res)), &got))
	require.Equal(t, user.ID.String(), got.UserID)
	require.ElementsMatch(t, []string{"insights:read", "insights:write"}, got.Scopes)
}

func TestToolInputSchemas_AdvertiseValidationHints(t *testing.T) {
	ctx := t.Context()
	f := newToolFixture(t)

	user := f.makeUser(t, ctx, "alice")
	sess := f.connect(t, ctx, f.tokenFor(t, user.ID, "insights:read insights:write"))

	list, err := sess.ListTools(ctx, nil)
	require.NoError(t, err)

	projectEnsure := inputSchemaMap(t, list.Tools, "project_ensure")
	require.ElementsMatch(t, []string{"slug"}, schemaRequired(t, projectEnsure))
	requireSchemaNumber(t, 1, schemaProperty(t, projectEnsure, "slug")["minLength"])

	insightWrite := inputSchemaMap(t, list.Tools, "insight_write")
	require.ElementsMatch(t, []string{"project", "content", "category", "source"}, schemaRequired(t, insightWrite))
	requireSchemaNumber(t, 1, schemaProperty(t, insightWrite, "project")["minLength"])
	requireSchemaNumber(t, 1, schemaProperty(t, insightWrite, "content")["minLength"])
	require.ElementsMatch(t, []any{"fact", "decision", "insight", "preference", "context", "general"}, schemaProperty(t, insightWrite, "category")["enum"])
	require.ElementsMatch(t, []any{"user", "repo", "agent", "command"}, schemaProperty(t, insightWrite, "source")["enum"])

	insightGet := inputSchemaMap(t, list.Tools, "insight_get")
	require.ElementsMatch(t, []string{"project"}, schemaRequired(t, insightGet))
	requireSchemaNumber(t, 1, schemaProperty(t, insightGet, "project")["minLength"])
	requireSchemaNumber(t, 1, schemaProperty(t, insightGet, "id")["minLength"])
	require.Equal(t, "uuid", schemaProperty(t, insightGet, "id")["format"])
	requireSchemaNumber(t, 1, schemaProperty(t, insightGet, "key")["minLength"])
	requireSchemaNumber(t, 1, schemaProperty(t, insightGet, "relation_limit")["minimum"])
	requireSchemaNumber(t, 100, schemaProperty(t, insightGet, "relation_limit")["maximum"])
	require.Len(t, insightGet["oneOf"], 2)

	insightSearch := inputSchemaMap(t, list.Tools, "insight_search")
	require.ElementsMatch(t, []string{"project", "query"}, schemaRequired(t, insightSearch))
	requireSchemaNumber(t, 1, schemaProperty(t, insightSearch, "project")["minLength"])
	requireSchemaNumber(t, 1, schemaProperty(t, insightSearch, "query")["minLength"])
	require.ElementsMatch(t, []any{"all", "web"}, schemaProperty(t, insightSearch, "query_mode")["enum"])
	require.ElementsMatch(t, []any{"all", "any"}, schemaProperty(t, insightSearch, "tag_mode")["enum"])
	requireSchemaNumber(t, 0, schemaProperty(t, insightSearch, "limit")["minimum"])
	requireSchemaNumber(t, 100, schemaProperty(t, insightSearch, "limit")["maximum"])

	insightList := inputSchemaMap(t, list.Tools, "insight_list")
	require.ElementsMatch(t, []string{"project"}, schemaRequired(t, insightList))
	requireSchemaNumber(t, 1, schemaProperty(t, insightList, "project")["minLength"])
	requireSchemaNumber(t, 0, schemaProperty(t, insightList, "limit")["minimum"])
	requireSchemaNumber(t, 200, schemaProperty(t, insightList, "limit")["maximum"])

	insightDelete := inputSchemaMap(t, list.Tools, "insight_delete")
	require.ElementsMatch(t, []string{"id"}, schemaRequired(t, insightDelete))
	requireSchemaNumber(t, 1, schemaProperty(t, insightDelete, "id")["minLength"])
	require.Equal(t, "uuid", schemaProperty(t, insightDelete, "id")["format"])

	insightListTags := inputSchemaMap(t, list.Tools, "insight_list_tags")
	require.ElementsMatch(t, []string{"project"}, schemaRequired(t, insightListTags))
	requireSchemaNumber(t, 1, schemaProperty(t, insightListTags, "project")["minLength"])
	requireSchemaNumber(t, 0, schemaProperty(t, insightListTags, "limit")["minimum"])
	requireSchemaNumber(t, 200, schemaProperty(t, insightListTags, "limit")["maximum"])

	insightUpdate := inputSchemaMap(t, list.Tools, "insight_update")
	require.ElementsMatch(t, []string{"id"}, schemaRequired(t, insightUpdate))
	requireSchemaNumber(t, 1, schemaProperty(t, insightUpdate, "id")["minLength"])
	require.Equal(t, "uuid", schemaProperty(t, insightUpdate, "id")["format"])
}

// --- project_ensure ---

func TestProjectEnsure_CreatesUnderPersonalOrg(t *testing.T) {
	ctx := t.Context()
	f := newToolFixture(t)

	user := f.makeUser(t, ctx, "alice")
	sess := f.connect(t, ctx, f.tokenFor(t, user.ID, "insights:read insights:write"))

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
	ctx := t.Context()
	f := newToolFixture(t)

	user := f.makeUser(t, ctx, "alice")
	sess := f.connect(t, ctx, f.tokenFor(t, user.ID, "insights:read insights:write"))

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
	ctx := t.Context()
	f := newToolFixture(t)

	user := f.makeUser(t, ctx, "alice")
	sess := f.connect(t, ctx, f.tokenFor(t, user.ID, "insights:read insights:write"))

	res := callTool(t, ctx, sess, "project_ensure", map[string]any{"slug": "my-slug", "name": ""})
	require.False(t, res.IsError)

	var got struct {
		Slug string `json:"slug"`
		Name string `json:"name"`
	}
	require.NoError(t, json.Unmarshal([]byte(resultText(t, res)), &got))
	require.Equal(t, got.Slug, got.Name)
}

// --- insight_write ---

func TestInsightWrite_AutoCreatesProject(t *testing.T) {
	ctx := t.Context()
	f := newToolFixture(t)

	user := f.makeUser(t, ctx, "alice")
	sess := f.connect(t, ctx, f.tokenFor(t, user.ID, "insights:read insights:write"))

	res := callTool(t, ctx, sess, "insight_write", insightWriteArgs("auto-project", "hello", nil))
	require.False(t, res.IsError, "insight_write failed: %s", resultText(t, res))

	listRes := callTool(t, ctx, sess, "project_list", map[string]any{})
	require.False(t, listRes.IsError)
	require.Contains(t, resultText(t, listRes), "auto-project")
}

func TestInsightWrite_RequiresWriteScope(t *testing.T) {
	ctx := t.Context()
	f := newToolFixture(t)

	user := f.makeUser(t, ctx, "alice")
	sess := f.connect(t, ctx, f.tokenFor(t, user.ID, "insights:read"))

	res := callTool(t, ctx, sess, "insight_write", insightWriteArgs("demo", "should fail", nil))
	require.True(t, res.IsError)
	require.Contains(t, resultText(t, res), "missing required scope")
}

func TestInsightWrite_RejectsInvalidCategory(t *testing.T) {
	ctx := t.Context()
	f := newToolFixture(t)

	user := f.makeUser(t, ctx, "alice")
	sess := f.connect(t, ctx, f.tokenFor(t, user.ID, "insights:read insights:write"))

	res := callTool(t, ctx, sess, "insight_write", insightWriteArgs("demo", "content", map[string]any{"category": "bogus"}))
	require.True(t, res.IsError)

	// Confirm no project was auto-created as a side effect.
	listRes := callTool(t, ctx, sess, "project_list", map[string]any{})
	require.False(t, listRes.IsError)
	var listed struct {
		Projects []any `json:"projects"`
	}
	require.NoError(t, json.Unmarshal([]byte(resultText(t, listRes)), &listed))
	require.Empty(t, listed.Projects, "invalid write must not create the project")
}

func TestInsightWrite_RejectsInvalidSource(t *testing.T) {
	ctx := t.Context()
	f := newToolFixture(t)

	user := f.makeUser(t, ctx, "alice")
	sess := f.connect(t, ctx, f.tokenFor(t, user.ID, "insights:read insights:write"))

	res := callTool(t, ctx, sess, "insight_write", insightWriteArgs("demo", "content", map[string]any{"source": "keyboard"}))
	require.True(t, res.IsError)
}

func TestInsightWrite_RejectsEmptyContent(t *testing.T) {
	ctx := t.Context()
	f := newToolFixture(t)

	user := f.makeUser(t, ctx, "alice")
	sess := f.connect(t, ctx, f.tokenFor(t, user.ID, "insights:read insights:write"))

	res := callTool(t, ctx, sess, "insight_write", insightWriteArgs("demo", "", nil))
	require.True(t, res.IsError)
}

func TestInsightWrite_RejectsEmptyProject(t *testing.T) {
	ctx := t.Context()
	f := newToolFixture(t)

	user := f.makeUser(t, ctx, "alice")
	sess := f.connect(t, ctx, f.tokenFor(t, user.ID, "insights:read insights:write"))

	res := callTool(t, ctx, sess, "insight_write", insightWriteArgs("", "content", nil))
	require.True(t, res.IsError)
}

func TestInsightWrite_NormalisesTags(t *testing.T) {
	ctx := t.Context()
	f := newToolFixture(t)

	user := f.makeUser(t, ctx, "alice")
	sess := f.connect(t, ctx, f.tokenFor(t, user.ID, "insights:read insights:write"))

	wr := callTool(t, ctx, sess, "insight_write", insightWriteArgs("demo", "content", map[string]any{"tags": []string{"Go", "HTTP2"}}))
	require.False(t, wr.IsError, "insight_write failed: %s", resultText(t, wr))

	var written struct {
		ID string `json:"id"`
	}
	require.NoError(t, json.Unmarshal([]byte(resultText(t, wr)), &written))

	listRes := callTool(t, ctx, sess, "insight_list", map[string]any{"project": "demo", "tag": "", "limit": 10})
	require.False(t, listRes.IsError, "insight_list failed: %s", resultText(t, listRes))
	var listed struct {
		Insights []struct {
			Tags []string `json:"tags"`
		} `json:"insights"`
	}
	require.NoError(t, json.Unmarshal([]byte(resultText(t, listRes)), &listed))
	require.Len(t, listed.Insights, 1)
	require.Equal(t, []string{"go", "http2"}, listed.Insights[0].Tags)
}

func TestInsightSearch_RejectsEmptyQuery(t *testing.T) {
	ctx := t.Context()
	f := newToolFixture(t)

	user := f.makeUser(t, ctx, "alice")
	sess := f.connect(t, ctx, f.tokenFor(t, user.ID, "insights:read insights:write"))

	res := callTool(t, ctx, sess, "insight_search", map[string]any{"project": "demo", "query": "", "tags": []string{}, "limit": 10})
	require.True(t, res.IsError)
}

func TestInsightWrite_UpsertsByKey(t *testing.T) {
	ctx := t.Context()
	f := newToolFixture(t)

	user := f.makeUser(t, ctx, "alice")
	sess := f.connect(t, ctx, f.tokenFor(t, user.ID, "insights:read insights:write"))

	var result1, result2 struct {
		ID      string `json:"id"`
		Updated bool   `json:"updated"`
	}

	res1 := callTool(t, ctx, sess, "insight_write", insightWriteArgs("demo", "original", map[string]any{"key": "stable-key"}))
	require.False(t, res1.IsError, "first write failed: %s", resultText(t, res1))
	require.NoError(t, json.Unmarshal([]byte(resultText(t, res1)), &result1))
	require.False(t, result1.Updated, "first write should not be an update")

	res2 := callTool(t, ctx, sess, "insight_write", insightWriteArgs("demo", "updated", map[string]any{"key": "stable-key"}))
	require.False(t, res2.IsError, "second write failed: %s", resultText(t, res2))
	require.NoError(t, json.Unmarshal([]byte(resultText(t, res2)), &result2))
	require.Equal(t, result1.ID, result2.ID, "upsert must return the same ID")
	require.True(t, result2.Updated, "second write with same key should be an update")
}

func TestInsightWriteAndUpdate_ReturnLinkWarnings(t *testing.T) {
	ctx := t.Context()
	f := newToolFixture(t)

	user := f.makeUser(t, ctx, "alice")
	sess := f.connect(t, ctx, f.tokenFor(t, user.ID, "insights:read insights:write"))

	type warning struct {
		Code      string `json:"code"`
		TargetKey string `json:"target_key"`
	}
	var written struct {
		ID       string    `json:"id"`
		Warnings []warning `json:"warnings"`
	}
	res := callTool(t, ctx, sess, "insight_write", insightWriteArgs(
		"demo",
		"[[insight:missing]] [[insight:source]]",
		map[string]any{"key": "source"},
	))
	require.False(t, res.IsError, "insight_write failed: %s", resultText(t, res))
	require.NoError(t, json.Unmarshal([]byte(resultText(t, res)), &written))
	require.Equal(t, []warning{
		{Code: "unresolved_insight_link", TargetKey: "missing"},
		{Code: "self_insight_link", TargetKey: "source"},
	}, written.Warnings)

	upd := callTool(t, ctx, sess, "insight_update", map[string]any{
		"id":      written.ID,
		"content": "[[insight:still-missing]]",
	})
	require.False(t, upd.IsError, "insight_update failed: %s", resultText(t, upd))
	var updated struct {
		Warnings []warning `json:"warnings"`
	}
	require.NoError(t, json.Unmarshal([]byte(resultText(t, upd)), &updated))
	require.Equal(t, []warning{{Code: "unresolved_insight_link", TargetKey: "still-missing"}}, updated.Warnings)

	tagOnly := callTool(t, ctx, sess, "insight_update", map[string]any{
		"id":   written.ID,
		"tags": []string{"tag-only"},
	})
	require.False(t, tagOnly.IsError, "insight_update failed: %s", resultText(t, tagOnly))
	require.NotContains(t, resultText(t, tagOnly), `"warnings"`)
}

func TestInsightGet_ReturnsBoundedRelationships(t *testing.T) {
	ctx := t.Context()
	f := newToolFixture(t)
	user := f.makeUser(t, ctx, "alice")
	sess := f.connect(t, ctx, f.tokenFor(t, user.ID, "insights:read insights:write"))

	var target struct {
		ID string `json:"id"`
	}
	res := callTool(t, ctx, sess, "insight_write", insightWriteArgs("demo", "target", map[string]any{"key": "target"}))
	require.False(t, res.IsError, "insight_write failed: %s", resultText(t, res))
	require.NoError(t, json.Unmarshal([]byte(resultText(t, res)), &target))

	var source struct {
		ID string `json:"id"`
	}
	res = callTool(t, ctx, sess, "insight_write", insightWriteArgs(
		"demo", "[[insight:target]] [[insight:missing]]", map[string]any{"key": "source"},
	))
	require.False(t, res.IsError, "insight_write failed: %s", resultText(t, res))
	require.NoError(t, json.Unmarshal([]byte(resultText(t, res)), &source))

	res = callTool(t, ctx, sess, "insight_write", insightWriteArgs("demo", "[[insight:source]]", map[string]any{"key": "backlink"}))
	require.False(t, res.IsError, "insight_write failed: %s", resultText(t, res))

	type reference struct {
		TargetKey string `json:"target_key"`
		Resolved  bool   `json:"resolved"`
		ID        string `json:"id"`
		Key       string `json:"key"`
	}
	var got struct {
		Insight struct {
			ID      string `json:"id"`
			Key     string `json:"key"`
			Content string `json:"content"`
		} `json:"insight"`
		Links              []reference `json:"links"`
		Backlinks          []reference `json:"backlinks"`
		LinkCount          int         `json:"link_count"`
		BacklinkCount      int         `json:"backlink_count"`
		LinksTruncated     bool        `json:"links_truncated"`
		BacklinksTruncated bool        `json:"backlinks_truncated"`
	}
	res = callTool(t, ctx, sess, "insight_get", map[string]any{"project": "demo", "key": "source", "relation_limit": 1})
	require.False(t, res.IsError, "insight_get failed: %s", resultText(t, res))
	require.NoError(t, json.Unmarshal([]byte(resultText(t, res)), &got))
	require.Equal(t, source.ID, got.Insight.ID)
	require.Equal(t, "source", got.Insight.Key)
	require.Equal(t, "[[insight:target]] [[insight:missing]]", got.Insight.Content)
	require.Equal(t, 2, got.LinkCount)
	require.Equal(t, 1, got.BacklinkCount)
	require.True(t, got.LinksTruncated)
	require.False(t, got.BacklinksTruncated)
	require.Equal(t, []reference{{TargetKey: "missing"}}, got.Links)
	require.Len(t, got.Backlinks, 1)
	require.Equal(t, "backlink", got.Backlinks[0].Key)
	readOnly := f.connect(t, ctx, f.tokenFor(t, user.ID, "insights:read"))
	res = callTool(t, ctx, readOnly, "insight_get", map[string]any{"project": "demo", "key": "source"})
	require.False(t, res.IsError, "read-scoped insight_get failed: %s", resultText(t, res))

	res = callTool(t, ctx, sess, "insight_get", map[string]any{"project": "demo", "id": source.ID})
	require.False(t, res.IsError, "insight_get by ID failed: %s", resultText(t, res))
	require.NoError(t, json.Unmarshal([]byte(resultText(t, res)), &got))
	require.Len(t, got.Links, 2)
	require.Equal(t, reference{TargetKey: "target", Resolved: true, ID: target.ID}, got.Links[1])

	res = callTool(t, ctx, sess, "insight_delete", map[string]any{"id": target.ID})
	require.False(t, res.IsError, "insight_delete failed: %s", resultText(t, res))
	res = callTool(t, ctx, sess, "insight_get", map[string]any{"project": "demo", "key": "source"})
	require.False(t, res.IsError, "insight_get failed: %s", resultText(t, res))
	got.Links = nil
	require.NoError(t, json.Unmarshal([]byte(resultText(t, res)), &got))
	require.False(t, got.Links[1].Resolved)
	require.Empty(t, got.Links[1].ID)

	for _, input := range []map[string]any{
		{"project": "demo"},
		{"project": "demo", "id": source.ID, "key": "source"},
		{"project": "demo", "id": "not-a-uuid"},
		{"project": "demo", "key": "source", "relation_limit": 0},
		{"project": "demo", "key": "source", "relation_limit": 101},
	} {
		res = callTool(t, ctx, sess, "insight_get", input)
		require.True(t, res.IsError, "invalid insight_get input succeeded: %#v", input)
	}

	bob := f.makeUser(t, ctx, "bob")
	bobSess := f.connect(t, ctx, f.tokenFor(t, bob.ID, "insights:read"))
	res = callTool(t, ctx, bobSess, "insight_get", map[string]any{"project": "demo", "key": "source"})
	require.True(t, res.IsError)
	require.NotContains(t, resultText(t, res), source.ID)
}

// --- insight_list / insight_search / insight_list_tags org scoping ---

func TestInsightList_ScopedToCallerOrg(t *testing.T) {
	ctx := t.Context()
	f := newToolFixture(t)

	alice := f.makeUser(t, ctx, "alice")
	bob := f.makeUser(t, ctx, "bob")

	aliceSess := f.connect(t, ctx, f.tokenFor(t, alice.ID, "insights:read insights:write"))
	bobSess := f.connect(t, ctx, f.tokenFor(t, bob.ID, "insights:read"))

	wr := callTool(t, ctx, aliceSess, "insight_write", insightWriteArgs("alice-proj", "alice data", nil))
	require.False(t, wr.IsError)

	res := callTool(t, ctx, bobSess, "insight_list", map[string]any{
		"project": "alice-proj",
		"tag":     "",
		"limit":   0,
	})
	require.True(t, res.IsError)
	require.Contains(t, resultText(t, res), "project")
	require.Contains(t, resultText(t, res), "not found")
}

func TestInsightSearch_ScopedToCallerOrg(t *testing.T) {
	ctx := t.Context()
	f := newToolFixture(t)

	alice := f.makeUser(t, ctx, "alice")
	bob := f.makeUser(t, ctx, "bob")

	aliceSess := f.connect(t, ctx, f.tokenFor(t, alice.ID, "insights:read insights:write"))
	bobSess := f.connect(t, ctx, f.tokenFor(t, bob.ID, "insights:read"))

	wr := callTool(t, ctx, aliceSess, "insight_write", insightWriteArgs("alice-proj", "alice data", nil))
	require.False(t, wr.IsError)

	res := callTool(t, ctx, bobSess, "insight_search", map[string]any{
		"project": "alice-proj",
		"query":   "alice",
		"tags":    []string{},
		"limit":   0,
	})
	require.True(t, res.IsError)
	require.Contains(t, resultText(t, res), "project")
	require.Contains(t, resultText(t, res), "not found")
}

func TestInsightSearch_ReturnsMatchingInsights(t *testing.T) {
	ctx := t.Context()
	f := newToolFixture(t)

	user := f.makeUser(t, ctx, "alice")
	sess := f.connect(t, ctx, f.tokenFor(t, user.ID, "insights:read insights:write"))

	for _, content := range []string{"golang concurrency patterns", "python asyncio basics"} {
		wr := callTool(t, ctx, sess, "insight_write", insightWriteArgs("search-test", content, nil))
		require.False(t, wr.IsError, "insight_write failed: %s", resultText(t, wr))
	}

	res := callTool(t, ctx, sess, "insight_search", map[string]any{
		"project": "search-test",
		"query":   "concurrency",
		"tags":    []string{},
		"limit":   0,
	})
	require.False(t, res.IsError, "insight_search failed: %s", resultText(t, res))
	text := resultText(t, res)
	require.Contains(t, text, "golang concurrency patterns")
	require.NotContains(t, text, "python asyncio basics")
}

func TestInsightSearch_WebQueryAndTagModes(t *testing.T) {
	ctx := t.Context()
	f := newToolFixture(t)

	user := f.makeUser(t, ctx, "alice")
	sess := f.connect(t, ctx, f.tokenFor(t, user.ID, "insights:read insights:write"))

	writes := []struct {
		content string
		tags    []string
	}{
		{content: "OAuth client registration", tags: []string{"auth", "api"}},
		{content: "MCP server transport", tags: []string{"mcp", "api"}},
		{content: "deprecated OAuth callback", tags: []string{"auth", "legacy"}},
	}
	for _, write := range writes {
		wr := callTool(t, ctx, sess, "insight_write", insightWriteArgs("search-modes", write.content, map[string]any{"tags": write.tags}))
		require.False(t, wr.IsError, "insight_write failed: %s", resultText(t, wr))
	}

	web := callTool(t, ctx, sess, "insight_search", map[string]any{
		"project":    "search-modes",
		"query":      "\"OAuth client\" OR MCP -deprecated",
		"query_mode": "web",
		"limit":      10,
	})
	require.False(t, web.IsError, "insight_search failed: %s", resultText(t, web))
	webText := resultText(t, web)
	require.Contains(t, webText, "OAuth client registration")
	require.Contains(t, webText, "MCP server transport")
	require.NotContains(t, webText, "deprecated OAuth callback")

	allTags := callTool(t, ctx, sess, "insight_search", map[string]any{
		"project":    "search-modes",
		"query":      "OAuth OR MCP",
		"tags":       []string{"auth", "api"},
		"tag_mode":   "all",
		"query_mode": "web",
		"limit":      10,
	})
	require.False(t, allTags.IsError, "insight_search failed: %s", resultText(t, allTags))
	require.Contains(t, resultText(t, allTags), "OAuth client registration")
	require.NotContains(t, resultText(t, allTags), "MCP server transport")

	anyTags := callTool(t, ctx, sess, "insight_search", map[string]any{
		"project":    "search-modes",
		"query":      "OAuth OR MCP",
		"tags":       []string{"auth", "mcp"},
		"tag_mode":   "any",
		"query_mode": "web",
		"limit":      10,
	})
	require.False(t, anyTags.IsError, "insight_search failed: %s", resultText(t, anyTags))
	require.Contains(t, resultText(t, anyTags), "OAuth client registration")
	require.Contains(t, resultText(t, anyTags), "MCP server transport")
}

func TestInsightListTags_ScopedToCallerOrg(t *testing.T) {
	ctx := t.Context()
	f := newToolFixture(t)

	alice := f.makeUser(t, ctx, "alice")
	bob := f.makeUser(t, ctx, "bob")

	aliceSess := f.connect(t, ctx, f.tokenFor(t, alice.ID, "insights:read insights:write"))
	bobSess := f.connect(t, ctx, f.tokenFor(t, bob.ID, "insights:read"))

	wr := callTool(t, ctx, aliceSess, "insight_write", insightWriteArgs("alice-proj", "alice data", map[string]any{"tags": []string{"go"}}))
	require.False(t, wr.IsError)

	res := callTool(t, ctx, bobSess, "insight_list_tags", map[string]any{
		"project": "alice-proj",
		"limit":   0,
	})
	require.True(t, res.IsError)
	require.Contains(t, resultText(t, res), "project")
	require.Contains(t, resultText(t, res), "not found")
}

func TestInsightListTags_ReturnsByFrequency(t *testing.T) {
	ctx := t.Context()
	f := newToolFixture(t)

	user := f.makeUser(t, ctx, "alice")
	sess := f.connect(t, ctx, f.tokenFor(t, user.ID, "insights:read insights:write"))

	// "go" appears 3 times, "concurrency" once — go must sort first.
	for _, tags := range [][]string{{"go", "concurrency"}, {"go"}, {"go"}} {
		wr := callTool(t, ctx, sess, "insight_write", insightWriteArgs("tags-test", "content", map[string]any{"tags": tags}))
		require.False(t, wr.IsError, "insight_write failed: %s", resultText(t, wr))
	}

	res := callTool(t, ctx, sess, "insight_list_tags", map[string]any{
		"project": "tags-test",
		"limit":   0,
	})
	require.False(t, res.IsError, "insight_list_tags failed: %s", resultText(t, res))

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

func TestInsightList_RespectsTagFilter(t *testing.T) {
	ctx := t.Context()
	f := newToolFixture(t)

	user := f.makeUser(t, ctx, "alice")
	sess := f.connect(t, ctx, f.tokenFor(t, user.ID, "insights:read insights:write"))

	for _, w := range []struct {
		content string
		tags    []string
	}{
		{"go fact", []string{"go"}},
		{"python fact", []string{"python"}},
		{"both fact", []string{"go", "python"}},
	} {
		wr := callTool(t, ctx, sess, "insight_write", insightWriteArgs("tagged", w.content, map[string]any{"tags": w.tags}))
		require.False(t, wr.IsError, "insight_write failed: %s", resultText(t, wr))
	}

	res := callTool(t, ctx, sess, "insight_list", map[string]any{
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

// --- insight_delete / insight_update: scope enforcement and error surfaces ---

func TestInsightDelete_RequiresWriteScope(t *testing.T) {
	ctx := t.Context()
	f := newToolFixture(t)

	user := f.makeUser(t, ctx, "alice")
	sess := f.connect(t, ctx, f.tokenFor(t, user.ID, "insights:read"))

	res := callTool(t, ctx, sess, "insight_delete", map[string]any{"id": uuid.New().String()})
	require.True(t, res.IsError)
	require.Contains(t, resultText(t, res), "missing required scope")
}

func TestInsightUpdate_RequiresWriteScope(t *testing.T) {
	ctx := t.Context()
	f := newToolFixture(t)

	user := f.makeUser(t, ctx, "alice")
	sess := f.connect(t, ctx, f.tokenFor(t, user.ID, "insights:read"))

	res := callTool(t, ctx, sess, "insight_update", map[string]any{
		"id":      uuid.New().String(),
		"content": "x",
		"tags":    []string{},
	})
	require.True(t, res.IsError)
	require.Contains(t, resultText(t, res), "missing required scope")
}

func TestInsightDelete_UnknownID_NotFound(t *testing.T) {
	ctx := t.Context()
	f := newToolFixture(t)

	user := f.makeUser(t, ctx, "alice")
	sess := f.connect(t, ctx, f.tokenFor(t, user.ID, "insights:read insights:write"))

	res := callTool(t, ctx, sess, "insight_delete", map[string]any{"id": uuid.New().String()})
	require.True(t, res.IsError)
	require.Contains(t, resultText(t, res), "not found or already deleted")
}

func TestInsightUpdate_UnknownID_NotFound(t *testing.T) {
	ctx := t.Context()
	f := newToolFixture(t)

	user := f.makeUser(t, ctx, "alice")
	sess := f.connect(t, ctx, f.tokenFor(t, user.ID, "insights:read insights:write"))

	res := callTool(t, ctx, sess, "insight_update", map[string]any{
		"id":      uuid.New().String(),
		"content": "x",
		"tags":    []string{},
	})
	require.True(t, res.IsError)
	require.Contains(t, resultText(t, res), "not found or already deleted")
}

func TestInsightDelete_InvalidUUID_StructuredError(t *testing.T) {
	ctx := t.Context()
	f := newToolFixture(t)

	user := f.makeUser(t, ctx, "alice")
	sess := f.connect(t, ctx, f.tokenFor(t, user.ID, "insights:read insights:write"))

	res := callTool(t, ctx, sess, "insight_delete", map[string]any{"id": "not-a-uuid"})
	require.True(t, res.IsError)
	require.Contains(t, resultText(t, res), "invalid insight ID")
}

func TestInsightDelete_RemovesInsight(t *testing.T) {
	ctx := t.Context()
	f := newToolFixture(t)

	user := f.makeUser(t, ctx, "alice")
	sess := f.connect(t, ctx, f.tokenFor(t, user.ID, "insights:read insights:write"))

	wr := callTool(t, ctx, sess, "insight_write", insightWriteArgs("delete-test", "to be deleted", nil))
	require.False(t, wr.IsError, "insight_write failed: %s", resultText(t, wr))
	var written struct {
		ID string `json:"id"`
	}
	require.NoError(t, json.Unmarshal([]byte(resultText(t, wr)), &written))

	listBefore := callTool(t, ctx, sess, "insight_list", map[string]any{"project": "delete-test", "tag": "", "limit": 0})
	require.False(t, listBefore.IsError)
	require.Contains(t, resultText(t, listBefore), "to be deleted")

	delRes := callTool(t, ctx, sess, "insight_delete", map[string]any{"id": written.ID})
	require.False(t, delRes.IsError, "insight_delete failed: %s", resultText(t, delRes))

	listAfter := callTool(t, ctx, sess, "insight_list", map[string]any{"project": "delete-test", "tag": "", "limit": 0})
	require.False(t, listAfter.IsError)
	require.NotContains(t, resultText(t, listAfter), "to be deleted")
}

// --- project_list ---

func TestProjectList_EmptyForFreshUser(t *testing.T) {
	ctx := t.Context()
	f := newToolFixture(t)

	user := f.makeUser(t, ctx, "alice")
	sess := f.connect(t, ctx, f.tokenFor(t, user.ID, "insights:read"))

	res := callTool(t, ctx, sess, "project_list", map[string]any{})
	require.False(t, res.IsError)

	var got struct {
		Projects []any `json:"projects"`
	}
	require.NoError(t, json.Unmarshal([]byte(resultText(t, res)), &got))
	require.Empty(t, got.Projects)
}

func TestProjectList_ReturnsOnlyOwnOrg(t *testing.T) {
	ctx := t.Context()
	f := newToolFixture(t)

	alice := f.makeUser(t, ctx, "alice")
	bob := f.makeUser(t, ctx, "bob")

	aliceSess := f.connect(t, ctx, f.tokenFor(t, alice.ID, "insights:read insights:write"))
	bobSess := f.connect(t, ctx, f.tokenFor(t, bob.ID, "insights:read"))

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
	ctx := t.Context()
	f := newToolFixture(t)

	// JWT sub is a valid UUID but has no corresponding user row in the database.
	token := f.tokenFor(t, uuid.New(), "insights:read insights:write")
	sess := f.connect(t, ctx, token)

	res := callTool(t, ctx, sess, "project_list", map[string]any{})
	require.True(t, res.IsError)
	require.Contains(t, resultText(t, res), "user not found")
}

// TestFactUpdate_CrossOrg_Forbidden is the analogous regression for insight_update.
func TestInsightUpdate_CrossOrg_Forbidden(t *testing.T) {
	ctx := t.Context()
	f := newToolFixture(t)

	alice := f.makeUser(t, ctx, "alice")
	bob := f.makeUser(t, ctx, "bob")

	aliceSess := f.connect(t, ctx, f.tokenFor(t, alice.ID, "insights:read insights:write"))
	bobSess := f.connect(t, ctx, f.tokenFor(t, bob.ID, "insights:read insights:write"))

	writeRes := callTool(t, ctx, aliceSess, "insight_write", insightWriteArgs("demo", "alice's original", nil))
	require.False(t, writeRes.IsError, "insight_write failed: %s", resultText(t, writeRes))

	var written struct {
		ID string `json:"id"`
	}
	require.NoError(t, json.Unmarshal([]byte(resultText(t, writeRes)), &written))
	require.NotEmpty(t, written.ID)

	updRes := callTool(t, ctx, bobSess, "insight_update", map[string]any{
		"id":      written.ID,
		"content": "tampered by bob",
		"tags":    []string{},
	})
	require.True(t, updRes.IsError, "insight_update must reject cross-org update; got %s", resultText(t, updRes))
	require.Contains(t, resultText(t, updRes), "not found or already deleted")

	// Sanity: alice's original content is intact.
	listRes := callTool(t, ctx, aliceSess, "insight_list", map[string]any{"project": "demo", "tag": "", "limit": 0})
	require.False(t, listRes.IsError, "insight_list failed: %s", resultText(t, listRes))
	require.Contains(t, resultText(t, listRes), "alice's original")
	require.NotContains(t, resultText(t, listRes), "tampered")
}

func TestInsightUpdate_ChangesContentAndTags(t *testing.T) {
	ctx := t.Context()
	f := newToolFixture(t)

	user := f.makeUser(t, ctx, "alice")
	sess := f.connect(t, ctx, f.tokenFor(t, user.ID, "insights:read insights:write"))

	wr := callTool(t, ctx, sess, "insight_write", insightWriteArgs("update-test", "original content", map[string]any{"tags": []string{"old-tag"}}))
	require.False(t, wr.IsError, "insight_write failed: %s", resultText(t, wr))
	var written struct {
		ID string `json:"id"`
	}
	require.NoError(t, json.Unmarshal([]byte(resultText(t, wr)), &written))

	upd := callTool(t, ctx, sess, "insight_update", map[string]any{
		"id":      written.ID,
		"content": "updated content",
		"tags":    []string{"new-tag"},
	})
	require.False(t, upd.IsError, "insight_update failed: %s", resultText(t, upd))

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
	listRes := callTool(t, ctx, sess, "insight_list", map[string]any{"project": "update-test", "tag": "", "limit": 0})
	require.False(t, listRes.IsError)
	text := resultText(t, listRes)
	require.Contains(t, text, "updated content")
	require.NotContains(t, text, "original content")
}
