package insightlinks_test

import (
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/wolfeidau/starlogz/internal/insightlinks"
	"github.com/yuin/goldmark/ast"
)

func TestTargets(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		content string
		want    []string
	}{
		{name: "plain", content: "See [[insight:workflow]].", want: []string{"workflow"}},
		{name: "label and horizontal trim", content: "See [[insight:\t workflow \t| the workflow ]].", want: []string{"workflow"}},
		{name: "multiple sorted and deduplicated", content: "[[insight:z]] [[insight:a]] [[insight:z|again]]", want: []string{"a", "z"}},
		{name: "case sensitive", content: "[[insight:Key]] [[insight:key]]", want: []string{"Key", "key"}},
		{name: "empty key", content: "[[insight:]] [[insight:valid]]", want: []string{"valid"}},
		{name: "empty trimmed key", content: "[[insight: \t ]] [[insight:valid]]", want: []string{"valid"}},
		{name: "single closing bracket", content: "[[insight:bad] then [[insight:valid]]", want: []string{"valid"}},
		{name: "unclosed", content: "[[insight:missing", want: []string{}},
		{name: "unclosed before next line", content: "[[insight:missing\n[[insight:valid]]", want: []string{"valid"}},
		{name: "code span", content: "`[[insight:no]]` [[insight:yes]]", want: []string{"yes"}},
		{name: "fenced code", content: "```\n[[insight:no]]\n```\n[[insight:yes]]", want: []string{"yes"}},
		{name: "indented code", content: "    [[insight:no]]\n\n[[insight:yes]]", want: []string{"yes"}},
		{name: "raw HTML block", content: "<div>\n[[insight:no]]\n</div>\n\n[[insight:yes]]", want: []string{"yes"}},
		{name: "raw HTML tag", content: `<span data-link="[[insight:no]]">x</span> [[insight:yes]]`, want: []string{"yes"}},
		{name: "Markdown link", content: "[see [[insight:no]]](https://example.com) [[insight:yes]]", want: []string{"yes"}},
		{name: "Markdown image", content: "![see [[insight:no]]](image.png) [[insight:yes]]", want: []string{"yes"}},
		{name: "emphasis", content: "*see [[insight:yes|this]]*", want: []string{"yes"}},
		{name: "CRLF", content: "[[insight:first]]\r\n[[insight:second]]", want: []string{"first", "second"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			require.Equal(t, tt.want, insightlinks.Targets(tt.content))
		})
	}
}

func TestInsightLinkNode(t *testing.T) {
	t.Parallel()

	var links []*insightlinks.InsightLink
	err := ast.Walk(insightlinks.Parse([]byte("[[insight:target|label | detail]] [[insight:fallback| \t]]")), func(node ast.Node, entering bool) (ast.WalkStatus, error) {
		if entering && node.Kind() == insightlinks.KindInsightLink {
			links = append(links, node.(*insightlinks.InsightLink))
		}
		return ast.WalkContinue, nil
	})
	require.NoError(t, err)
	require.Len(t, links, 2)
	require.Equal(t, "target", links[0].TargetKey)
	require.Equal(t, "label | detail", links[0].Label)
	require.Equal(t, "fallback", links[1].TargetKey)
	require.Equal(t, "fallback", links[1].Label)
}

func TestRender(t *testing.T) {
	t.Parallel()

	content := "# Title\n\nFollow [[insight:a&b c|<choose>]] and [docs](https://example.com)."
	html, err := insightlinks.Render(content, "my project")
	require.NoError(t, err)
	require.Contains(t, html, "<h1>Title</h1>")
	require.Contains(t, html, `href="?project=my+project&amp;insight_key=a%26b+c"`)
	require.Contains(t, html, `class="insight-link"`)
	require.Contains(t, html, `data-starlogz-action="open-insight"`)
	require.Contains(t, html, `data-insight-key="a&amp;b c"`)
	require.Contains(t, html, `&lt;choose&gt;</a>`)
	require.Contains(t, html, `<a href="https://example.com">docs</a>`)
	require.Equal(t, insightlinks.Targets(content), []string{"a&b c"})
}

func TestRenderSanitizesUnsafeMarkdown(t *testing.T) {
	t.Parallel()

	html, err := insightlinks.Render("<script>alert(1)</script>\n\n[bad](javascript:alert(1))\n\n<object>embedded</object>", "project")
	require.NoError(t, err)
	for _, unsafe := range []string{"<script", "javascript:", "<object", "alert(1)"} {
		require.NotContains(t, strings.ToLower(html), unsafe)
	}
}

func TestRenderConcurrentProjects(t *testing.T) {
	t.Parallel()

	var wg sync.WaitGroup
	errs := make(chan error, 20)
	for i := range 20 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			project := fmt.Sprintf("project-%d", i)
			html, err := insightlinks.Render("[[insight:target]]", project)
			if err != nil {
				errs <- err
				return
			}
			if !strings.Contains(html, "project="+project+"&amp;insight_key=target") {
				errs <- fmt.Errorf("rendered wrong project: %s", html)
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		require.NoError(t, err)
	}
}

func FuzzTargets(f *testing.F) {
	f.Add("[[insight:target|label]]")
	f.Add("[[insight:]] [[insight:valid]]")
	f.Add("`[[insight:code]]`")

	f.Fuzz(func(t *testing.T, content string) {
		_ = insightlinks.Targets(content)
	})
}

func FuzzRender(f *testing.F) {
	f.Add("[[insight:target|label]]", "project")
	f.Add("<script>alert(1)</script>", "project")

	f.Fuzz(func(t *testing.T, content, project string) {
		_, _ = insightlinks.Render(content, project)
	})
}
