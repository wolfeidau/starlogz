package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func writeSARIF(t *testing.T, content string) string {
	t.Helper()
	f, err := os.CreateTemp(t.TempDir(), "*.sarif")
	require.NoError(t, err)
	_, err = f.WriteString(content)
	require.NoError(t, err)
	require.NoError(t, f.Close())
	return f.Name()
}

func TestParseSARIF_Empty(t *testing.T) {
	path := writeSARIF(t, `{"runs":[]}`)
	findings, err := parseSARIF(path)
	require.NoError(t, err)
	require.Empty(t, findings)
}

func TestParseSARIF_Missing(t *testing.T) {
	_, err := parseSARIF(filepath.Join(t.TempDir(), "nofile.sarif"))
	require.Error(t, err)
}

func TestParseSARIF_Basic(t *testing.T) {
	sarif := `{
		"runs": [{
			"tool": {"driver": {"name": "mytool", "informationUri": "https://example.com", "rules": [
				{"id": "R001", "helpUri": "https://example.com/R001"}
			]}},
			"results": [{
				"ruleId": "R001",
				"level": "warning",
				"message": {"text": "something bad"},
				"locations": [{"physicalLocation": {"artifactLocation": {"uri": "foo/bar.go"}, "region": {"startLine": 42}}}]
			}]
		}]
	}`
	findings, err := parseSARIF(writeSARIF(t, sarif))
	require.NoError(t, err)
	require.Len(t, findings, 1)
	f := findings[0]
	require.Equal(t, "mytool", f.tool)
	require.Equal(t, "https://example.com", f.toolURI)
	require.Equal(t, "R001", f.ruleID)
	require.Equal(t, "https://example.com/R001", f.ruleURI)
	require.Equal(t, "warning", f.level)
	require.Equal(t, "something bad", f.message)
	require.Equal(t, "foo/bar.go", f.path)
	require.Equal(t, 42, f.line)
}

func TestParseSARIF_DefaultsLevelToWarning(t *testing.T) {
	sarif := `{"runs": [{"tool": {"driver": {"name": "t"}}, "results": [{"ruleId": "X", "message": {"text": "m"}}]}]}`
	findings, err := parseSARIF(writeSARIF(t, sarif))
	require.NoError(t, err)
	require.Equal(t, "warning", findings[0].level)
}

func TestParseSARIF_NoLocation(t *testing.T) {
	sarif := `{"runs": [{"tool": {"driver": {"name": "t"}}, "results": [{"ruleId": "X", "level": "error", "message": {"text": "m"}, "locations": []}]}]}`
	findings, err := parseSARIF(writeSARIF(t, sarif))
	require.NoError(t, err)
	require.Empty(t, findings[0].path)
	require.Zero(t, findings[0].line)
}

func TestBuildMarkdown_SingleRun(t *testing.T) {
	findings := []finding{
		{tool: "lint", toolURI: "https://lint.dev", ruleID: "R1", ruleURI: "https://lint.dev/R1", level: "error", message: "bad", path: "a.go", line: 1},
		{tool: "lint", toolURI: "https://lint.dev", ruleID: "R2", level: "warning", message: "meh", path: "b.go", line: 5},
	}
	out := buildMarkdown(findings)
	require.Contains(t, out, "[lint](https://lint.dev)")
	require.Contains(t, out, ":no_entry: 1 error")
	require.Contains(t, out, ":warning: 1 warning")
	require.Contains(t, out, "[R1](https://lint.dev/R1)")
	require.Contains(t, out, "R2")
	require.Contains(t, out, "`a.go:1`")
}

func TestBuildMarkdown_MultiRun(t *testing.T) {
	findings := []finding{
		{tool: "toolA", level: "warning", message: "a", ruleID: "A1"},
		{tool: "toolB", level: "error", message: "b", ruleID: "B1"},
	}
	out := buildMarkdown(findings)
	require.Contains(t, out, "### toolA")
	require.Contains(t, out, "### toolB")
	idxA := strings.Index(out, "### toolA")
	idxB := strings.Index(out, "### toolB")
	require.Less(t, idxA, idxB, "toolA section should appear before toolB")
}

func TestBuildMarkdown_PipeEscaped(t *testing.T) {
	findings := []finding{
		{tool: "t", level: "warning", message: "use a|b instead", ruleID: "X"},
	}
	out := buildMarkdown(findings)
	require.Contains(t, out, `a\|b`)
}
