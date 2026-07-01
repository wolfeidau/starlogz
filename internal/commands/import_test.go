package commands

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/wolfeidau/starlogz/internal/store"
)

func TestImportCmd_SingleProjectRoundTrip(t *testing.T) {
	st, dsn := newTestStoreAndDSN(t)
	srcUser, srcOrg, srcProj := seedUserOrgProject(t, st, 2001, "import-src", "proj")
	_, err := st.WriteInsight(t.Context(), store.WriteInsightParams{
		ProjectID: srcProj.ID,
		Key:       "preferred-language",
		Content:   "Go",
		Tags:      []string{"lang"},
		Category:  "preference",
		Source:    "user",
		CreatedBy: srcUser.ID,
	})
	require.NoError(t, err)

	exportFile := filepath.Join(t.TempDir(), "export.json")
	exportCmd := &ExportCmd{
		DatabaseURL: dsn,
		OrgID:       srcOrg.ID.String(),
		ProjectSlug: srcProj.Slug,
		Output:      exportFile,
	}
	require.NoError(t, exportCmd.Run(t.Context(), testGlobals()))

	dstUser, dstOrg, _ := seedUserOrgProject(t, st, 2002, "import-dst", "unrelated")

	importCmd := &ImportCmd{
		DatabaseURL: dsn,
		Input:       exportFile,
		OrgID:       dstOrg.ID.String(),
		CreatedBy:   dstUser.ID.String(),
	}
	require.NoError(t, importCmd.Run(t.Context(), testGlobals()))

	imported, err := st.GetProjectBySlug(t.Context(), dstOrg.ID, srcProj.Slug)
	require.NoError(t, err)
	require.Equal(t, srcProj.Name, imported.Name)

	insights, err := st.ListInsights(t.Context(), imported.ID, "", 100)
	require.NoError(t, err)
	require.Len(t, insights, 1)
	require.Equal(t, "preferred-language", insights[0].Key)
	require.Equal(t, "Go", insights[0].Content)
	require.Equal(t, dstUser.ID, insights[0].CreatedBy, "imported rows must be attributed to the target user, not the source user")
}

func TestImportCmd_KeyedInsightsUpsertOnRerun(t *testing.T) {
	st, dsn := newTestStoreAndDSN(t)
	dstUser, dstOrg, _ := seedUserOrgProject(t, st, 2003, "import-rerun", "unrelated")

	doc := singleProjectDocument{
		ExportedAt: time.Now().UTC(),
		Project: exportProject{
			Slug: "proj",
			Name: "Project",
			Insights: []exportInsight{
				{Key: "k1", Content: "v1", Category: "fact", Source: "user"},
			},
		},
	}
	input := filepath.Join(t.TempDir(), "in.json")
	writeJSON(t, input, doc)

	importCmd := &ImportCmd{DatabaseURL: dsn, Input: input, OrgID: dstOrg.ID.String(), CreatedBy: dstUser.ID.String()}
	require.NoError(t, importCmd.Run(t.Context(), testGlobals()))
	require.NoError(t, importCmd.Run(t.Context(), testGlobals()))

	project, err := st.GetProjectBySlug(t.Context(), dstOrg.ID, "proj")
	require.NoError(t, err)
	insights, err := st.ListInsights(t.Context(), project.ID, "", 100)
	require.NoError(t, err)
	require.Len(t, insights, 1, "re-importing a keyed insight must upsert, not duplicate")
}

func TestImportCmd_AllOrgsShapeFlattensIntoTargetOrg(t *testing.T) {
	st, dsn := newTestStoreAndDSN(t)
	dstUser, dstOrg, _ := seedUserOrgProject(t, st, 2004, "import-flatten", "unrelated")

	doc := allOrgsDocument{
		ExportedAt: time.Now().UTC(),
		Orgs: []exportOrg{
			{
				Slug: "org-a", Name: "Org A", Kind: "shared",
				Projects: []exportProject{
					{Slug: "proj-a", Name: "Project A", Insights: []exportInsight{
						{Content: "from a", Category: "fact", Source: "user"},
					}},
				},
			},
			{
				Slug: "org-b", Name: "Org B", Kind: "shared",
				Projects: []exportProject{
					{Slug: "proj-b", Name: "Project B", Insights: []exportInsight{
						{Key: "k-b", Content: "from b", Category: "decision", Source: "agent"},
					}},
				},
			},
		},
	}
	input := filepath.Join(t.TempDir(), "in.json")
	writeJSON(t, input, doc)

	importCmd := &ImportCmd{DatabaseURL: dsn, Input: input, OrgID: dstOrg.ID.String(), CreatedBy: dstUser.ID.String()}
	require.NoError(t, importCmd.Run(t.Context(), testGlobals()))

	projA, err := st.GetProjectBySlug(t.Context(), dstOrg.ID, "proj-a")
	require.NoError(t, err)
	projB, err := st.GetProjectBySlug(t.Context(), dstOrg.ID, "proj-b")
	require.NoError(t, err)

	insightsA, err := st.ListInsights(t.Context(), projA.ID, "", 100)
	require.NoError(t, err)
	require.Len(t, insightsA, 1)
	require.Equal(t, "from a", insightsA[0].Content)

	insightsB, err := st.ListInsights(t.Context(), projB.ID, "", 100)
	require.NoError(t, err)
	require.Len(t, insightsB, 1)
	require.Equal(t, "from b", insightsB[0].Content)
}

func TestImportCmd_NoProjects(t *testing.T) {
	_, dsn := newTestStoreAndDSN(t)

	input := filepath.Join(t.TempDir(), "empty.json")
	writeJSON(t, input, allOrgsDocument{ExportedAt: time.Now().UTC()})

	importCmd := &ImportCmd{DatabaseURL: dsn, Input: input, OrgID: "00000000-0000-0000-0000-000000000000", CreatedBy: "00000000-0000-0000-0000-000000000000"}
	require.ErrorContains(t, importCmd.Run(t.Context(), testGlobals()), "no projects")
}

func TestImportCmd_InvalidIDs(t *testing.T) {
	_, dsn := newTestStoreAndDSN(t)
	input := filepath.Join(t.TempDir(), "in.json")
	writeJSON(t, input, singleProjectDocument{Project: exportProject{Slug: "p", Name: "P"}})

	badOrg := &ImportCmd{DatabaseURL: dsn, Input: input, OrgID: "not-a-uuid", CreatedBy: "00000000-0000-0000-0000-000000000000"}
	require.Error(t, badOrg.Run(t.Context(), testGlobals()))

	badUser := &ImportCmd{DatabaseURL: dsn, Input: input, OrgID: "00000000-0000-0000-0000-000000000000", CreatedBy: "not-a-uuid"}
	require.Error(t, badUser.Run(t.Context(), testGlobals()))
}

func writeJSON(t *testing.T, path string, v any) {
	t.Helper()
	raw, err := json.MarshalIndent(v, "", "  ")
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(path, raw, 0o600))
}
