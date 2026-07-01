package commands

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/wolfeidau/starlogz/internal/store"
)

func TestExportCmd_SingleProject(t *testing.T) {
	st, dsn := newTestStoreAndDSN(t)
	u, org, p := seedUserOrgProject(t, st, 1001, "export-single", "proj")

	_, err := st.WriteInsight(t.Context(), store.WriteInsightParams{
		ProjectID: p.ID,
		Key:       "preferred-language",
		Content:   "Go",
		Tags:      []string{"lang"},
		Category:  "preference",
		Source:    "user",
		CreatedBy: u.ID,
	})
	require.NoError(t, err)
	_, err = st.WriteInsight(t.Context(), store.WriteInsightParams{
		ProjectID: p.ID,
		Content:   "shipped v1",
		Category:  "fact",
		Source:    "agent",
		CreatedBy: u.ID,
	})
	require.NoError(t, err)

	output := filepath.Join(t.TempDir(), "export.json")
	cmd := &ExportCmd{
		DatabaseURL: dsn,
		OrgID:       org.ID.String(),
		ProjectSlug: p.Slug,
		Output:      output,
	}
	require.NoError(t, cmd.Run(t.Context(), testGlobals()))

	raw, err := os.ReadFile(output)
	require.NoError(t, err)

	require.NotContains(t, string(raw), org.ID.String())
	require.NotContains(t, string(raw), u.ID.String())
	require.NotContains(t, string(raw), "created_by")

	var doc singleProjectDocument
	require.NoError(t, json.Unmarshal(raw, &doc))
	require.Equal(t, p.Slug, doc.Project.Slug)
	require.Len(t, doc.Project.Insights, 2)

	var keys []string
	for _, in := range doc.Project.Insights {
		keys = append(keys, in.Key)
	}
	require.Contains(t, keys, "preferred-language")
	require.Contains(t, keys, "")
}

func TestExportCmd_All(t *testing.T) {
	st, dsn := newTestStoreAndDSN(t)
	_, orgA, projA := seedUserOrgProject(t, st, 1002, "export-all-a", "proj-a")
	uB, orgB, projB := seedUserOrgProject(t, st, 1003, "export-all-b", "proj-b")

	_, err := st.WriteInsight(t.Context(), store.WriteInsightParams{
		ProjectID: projB.ID, Content: "b insight", Category: "fact", Source: "user", CreatedBy: uB.ID,
	})
	require.NoError(t, err)

	output := filepath.Join(t.TempDir(), "export-all.json")
	cmd := &ExportCmd{DatabaseURL: dsn, All: true, Output: output}
	require.NoError(t, cmd.Run(t.Context(), testGlobals()))

	raw, err := os.ReadFile(output)
	require.NoError(t, err)

	var doc allOrgsDocument
	require.NoError(t, json.Unmarshal(raw, &doc))

	found := map[string]exportOrg{}
	for _, o := range doc.Orgs {
		found[o.Slug] = o
	}

	orgAResult, ok := found[orgA.Slug]
	require.True(t, ok, "org %s must be present in export", orgA.Slug)
	require.Len(t, orgAResult.Projects, 1)
	require.Equal(t, projA.Slug, orgAResult.Projects[0].Slug)
	require.Empty(t, orgAResult.Projects[0].Insights)

	orgBResult, ok := found[orgB.Slug]
	require.True(t, ok, "org %s must be present in export", orgB.Slug)
	require.Len(t, orgBResult.Projects, 1)
	require.Len(t, orgBResult.Projects[0].Insights, 1)
	require.Equal(t, "b insight", orgBResult.Projects[0].Insights[0].Content)
}

func TestExportCmd_ValidatesFlags(t *testing.T) {
	_, dsn := newTestStoreAndDSN(t)
	output := filepath.Join(t.TempDir(), "out.json")

	all := &ExportCmd{DatabaseURL: dsn, All: true, OrgID: "some-org", Output: output}
	require.Error(t, all.Run(t.Context(), testGlobals()))

	missing := &ExportCmd{DatabaseURL: dsn, Output: output}
	require.Error(t, missing.Run(t.Context(), testGlobals()))
}

func TestExportCmd_ProjectNotFound(t *testing.T) {
	st, dsn := newTestStoreAndDSN(t)
	_, org, _ := seedUserOrgProject(t, st, 1004, "export-missing", "proj")

	cmd := &ExportCmd{
		DatabaseURL: dsn,
		OrgID:       org.ID.String(),
		ProjectSlug: "no-such-project",
		Output:      filepath.Join(t.TempDir(), "out.json"),
	}
	require.Error(t, cmd.Run(t.Context(), testGlobals()))
}
