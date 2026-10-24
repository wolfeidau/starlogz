package integration_test

import (
	"regexp"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/require"
)

var nonAlnum = regexp.MustCompile(`[^a-z0-9]+`)

// TestProtocolVersions guards against SDK upgrades that break older clients on the stateless server.
func TestProtocolVersions(t *testing.T) {
	versions := mcp.SupportedProtocolVersions()
	require.Contains(t, versions, "2025-11-25")
	require.Contains(t, versions, "2026-07-28")

	wantTools := []string{
		"whoami", "project_list", "project_ensure", "insight_write", "insight_get", "insight_history",
		"insight_restore", "insight_search", "insight_list", "insight_update", "insight_delete", "insight_list_tags",
	}

	f := newToolFixture(t)
	for _, v := range versions {
		t.Run(v, func(t *testing.T) {
			ctx := t.Context()
			safe := nonAlnum.ReplaceAllString(v, "-")
			user := f.makeUser(t, ctx, "proto-user-"+safe)
			sess := f.connectWithOptions(t, ctx, f.tokenFor(t, user.ID, "insights:read insights:write"),
				&mcp.ClientSessionOptions{ProtocolVersion: v})
			require.Equal(t, v, sess.InitializeResult().ProtocolVersion)

			tools, err := sess.ListTools(ctx, nil)
			require.NoError(t, err)
			var names []string
			for _, tool := range tools.Tools {
				names = append(names, tool.Name)
			}
			require.ElementsMatch(t, wantTools, names)

			slug := "proto-" + safe
			content := "protocol matrix content " + v
			res := callTool(t, ctx, sess, "insight_write", insightWriteArgs(slug, content, nil))
			require.False(t, res.IsError, resultText(t, res))

			res = callTool(t, ctx, sess, "insight_search", map[string]any{"project": slug, "query": "protocol matrix"})
			require.False(t, res.IsError, resultText(t, res))
			// Snippets are trimmed to the matched terms, so assert on the query phrase.
			require.Contains(t, resultText(t, res), "protocol matrix content")
		})
	}
}
