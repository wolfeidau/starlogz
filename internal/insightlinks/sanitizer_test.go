package insightlinks

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestSanitizerRejectsUnknownActionsAndExecutableContent(t *testing.T) {
	t.Parallel()

	html := htmlPolicy.Sanitize(`<a href="javascript:alert(1)" onclick="alert(1)" data-starlogz-action="unknown" data-insight-key="target">target</a><iframe src="https://example.com"></iframe><style>body{display:none}</style>`)
	for _, unsafe := range []string{"javascript:", "onclick", "unknown", "iframe", "style", "display:none"} {
		require.NotContains(t, strings.ToLower(html), unsafe)
	}
	require.Contains(t, html, ">target</a>")
}
