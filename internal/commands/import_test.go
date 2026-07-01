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

func TestImportCmd_PreservesInsightTimestamps(t *testing.T) {
	st, dsn := newTestStoreAndDSN(t)
	dstUser, dstOrg, _ := seedUserOrgProject(t, st, 2006, "import-timestamps", "unrelated")

	sourceCreated := time.Date(2020, 1, 15, 10, 0, 0, 0, time.UTC)
	sourceUpdated := time.Date(2021, 6, 1, 8, 30, 0, 0, time.UTC)
	doc := singleProjectDocument{
		ExportedAt: time.Now().UTC(),
		Project: exportProject{
			Slug: "proj",
			Name: "Project",
			Insights: []exportInsight{
				{Content: "old news", Category: "fact", Source: "user", CreatedAt: sourceCreated, UpdatedAt: sourceUpdated},
			},
		},
	}
	input := filepath.Join(t.TempDir(), "in.json")
	writeJSON(t, input, doc)

	importCmd := &ImportCmd{DatabaseURL: dsn, Input: input, OrgID: dstOrg.ID.String(), CreatedBy: dstUser.ID.String()}
	require.NoError(t, importCmd.Run(t.Context(), testGlobals()))

	project, err := st.GetProjectBySlug(t.Context(), dstOrg.ID, "proj")
	require.NoError(t, err)
	insights, err := st.ListInsights(t.Context(), project.ID, "", 100)
	require.NoError(t, err)
	require.Len(t, insights, 1)
	require.True(t, sourceCreated.Equal(insights[0].CreatedAt), "created_at must be preserved from the export, not reset to now")
	require.True(t, sourceUpdated.Equal(insights[0].UpdatedAt), "updated_at must be preserved from the export, not reset to now")
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

func TestImportCmd_RejectsDuplicateSlugAcrossOrgs(t *testing.T) {
	st, dsn := newTestStoreAndDSN(t)
	dstUser, dstOrg, _ := seedUserOrgProject(t, st, 2007, "import-collide", "unrelated")

	doc := allOrgsDocument{
		ExportedAt: time.Now().UTC(),
		Orgs: []exportOrg{
			{
				Slug: "org-a", Name: "Org A", Kind: "shared",
				Projects: []exportProject{
					{Slug: "shared-slug", Name: "From A", Insights: []exportInsight{
						{Key: "same-key", Content: "from a", Category: "fact", Source: "user"},
					}},
				},
			},
			{
				Slug: "org-b", Name: "Org B", Kind: "shared",
				Projects: []exportProject{
					{Slug: "shared-slug", Name: "From B", Insights: []exportInsight{
						{Key: "same-key", Content: "from b", Category: "fact", Source: "user"},
					}},
				},
			},
		},
	}
	input := filepath.Join(t.TempDir(), "in.json")
	writeJSON(t, input, doc)

	importCmd := &ImportCmd{DatabaseURL: dsn, Input: input, OrgID: dstOrg.ID.String(), CreatedBy: dstUser.ID.String()}
	require.ErrorContains(t, importCmd.Run(t.Context(), testGlobals()), "refusing to merge")

	_, err := st.GetProjectBySlug(t.Context(), dstOrg.ID, "shared-slug")
	require.ErrorIs(t, err, store.ErrNotFound)
}

func TestImportCmd_AtomicOnFailure(t *testing.T) {
	st, dsn := newTestStoreAndDSN(t)
	dstUser, dstOrg, _ := seedUserOrgProject(t, st, 2005, "import-atomic", "unrelated")

	doc := allOrgsDocument{
		ExportedAt: time.Now().UTC(),
		Orgs: []exportOrg{
			{
				Slug: "org-a", Name: "Org A", Kind: "shared",
				Projects: []exportProject{
					{Slug: "proj-ok", Name: "Project OK", Insights: []exportInsight{
						{Content: "should not survive", Category: "fact", Source: "user"},
					}},
				},
			},
			{
				Slug: "org-b", Name: "Org B", Kind: "shared",
				Projects: []exportProject{
					// invalid category violates the DB check constraint, failing partway through the import.
					{Slug: "proj-bad", Name: "Project Bad", Insights: []exportInsight{
						{Content: "bad insight", Category: "not-a-real-category", Source: "user"},
					}},
				},
			},
		},
	}
	input := filepath.Join(t.TempDir(), "in.json")
	writeJSON(t, input, doc)

	importCmd := &ImportCmd{DatabaseURL: dsn, Input: input, OrgID: dstOrg.ID.String(), CreatedBy: dstUser.ID.String()}
	require.Error(t, importCmd.Run(t.Context(), testGlobals()))

	_, err := st.GetProjectBySlug(t.Context(), dstOrg.ID, "proj-ok")
	require.ErrorIs(t, err, store.ErrNotFound, "a project processed before the failing one must not survive a rolled-back import")

	_, err = st.GetProjectBySlug(t.Context(), dstOrg.ID, "proj-bad")
	require.ErrorIs(t, err, store.ErrNotFound)
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
