package commands

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"time"

	"github.com/google/uuid"
	"github.com/wolfeidau/starlogz/internal/store"
	"github.com/wolfeidau/starlogz/internal/store/postgres"
)

// maxExportInsights bounds a single project's export; well above any realistic project size.
const maxExportInsights = 1_000_000

type ExportCmd struct {
	DatabaseURL string `help:"PostgreSQL connection string." env:"DATABASE_URL" required:""`
	All         bool   `help:"Export every org and project instead of a single project."`
	OrgID       string `help:"ID of the org that owns the project. Required unless --all is set."`
	ProjectSlug string `help:"Slug of the project to export. Required unless --all is set."`
	Output      string `help:"Path to write the export JSON file." required:""`
}

// exportOrg, exportProject and exportInsight deliberately omit every
// auth-related field (org/project/user IDs, created_by) so the file carries
// no UUIDs tying it back to this instance's tenants or users. Note this is
// not full anonymization: a personal org's slug is the owning user's GitHub
// login, so a --all export still names every user on the instance.
type exportOrg struct {
	Slug     string          `json:"slug"`
	Name     string          `json:"name"`
	Kind     string          `json:"kind"`
	Projects []exportProject `json:"projects"`
}

type exportProject struct {
	Slug     string          `json:"slug"`
	Name     string          `json:"name"`
	Insights []exportInsight `json:"insights"`
}

type exportInsight struct {
	Key       string    `json:"key,omitempty"`
	Content   string    `json:"content"`
	Tags      []string  `json:"tags"`
	Category  string    `json:"category"`
	Source    string    `json:"source"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// singleProjectDocument is the export shape used for a single --org-id/--project-slug export.
type singleProjectDocument struct {
	ExportedAt time.Time     `json:"exported_at"`
	Project    exportProject `json:"project"`
}

// allOrgsDocument is the export shape used for a full --all export.
type allOrgsDocument struct {
	ExportedAt time.Time   `json:"exported_at"`
	Orgs       []exportOrg `json:"orgs"`
}

func (c *ExportCmd) Run(ctx context.Context, globals *Globals) error {
	if c.All && (c.OrgID != "" || c.ProjectSlug != "") {
		return fmt.Errorf("--all cannot be combined with --org-id or --project-slug")
	}
	if !c.All && (c.OrgID == "" || c.ProjectSlug == "") {
		return fmt.Errorf("--org-id and --project-slug are required unless --all is set")
	}

	st, err := postgres.New(ctx, c.DatabaseURL, store.NewEncryptor([32]byte{}))
	if err != nil {
		return fmt.Errorf("failed to connect to database: %w", err)
	}
	defer st.Close()

	if c.All {
		return c.runAll(ctx, st, globals.Logger)
	}
	return c.runSingleProject(ctx, st, globals.Logger)
}

func (c *ExportCmd) runSingleProject(ctx context.Context, st *postgres.Store, logger *slog.Logger) error {
	orgID, err := uuid.Parse(c.OrgID)
	if err != nil {
		return fmt.Errorf("failed to parse org id: %w", err)
	}

	project, err := st.GetProjectBySlug(ctx, orgID, c.ProjectSlug)
	if err != nil {
		return fmt.Errorf("failed to get project: %w", err)
	}

	insights, err := exportInsightsForProject(ctx, st, project.ID)
	if err != nil {
		return err
	}

	doc := singleProjectDocument{
		ExportedAt: time.Now().UTC(),
		Project: exportProject{
			Slug:     project.Slug,
			Name:     project.Name,
			Insights: insights,
		},
	}

	if err := writeExportFile(c.Output, doc); err != nil {
		return err
	}

	logger.InfoContext(ctx, "exported project",
		slog.String("project_slug", project.Slug),
		slog.Int("insight_count", len(insights)),
		slog.String("output", c.Output),
	)
	return nil
}

func (c *ExportCmd) runAll(ctx context.Context, st *postgres.Store, logger *slog.Logger) error {
	dbOrgs, err := st.ListOrgs(ctx)
	if err != nil {
		return fmt.Errorf("failed to list orgs: %w", err)
	}

	orgs := make([]exportOrg, len(dbOrgs))
	projectCount, insightCount := 0, 0
	for i, o := range dbOrgs {
		projects, err := st.ListProjects(ctx, o.ID)
		if err != nil {
			return fmt.Errorf("failed to list projects for org %s: %w", o.Slug, err)
		}

		exported := make([]exportProject, len(projects))
		for j, p := range projects {
			insights, err := exportInsightsForProject(ctx, st, p.ID)
			if err != nil {
				return err
			}
			exported[j] = exportProject{
				Slug:     p.Slug,
				Name:     p.Name,
				Insights: insights,
			}
			insightCount += len(insights)
		}
		projectCount += len(projects)

		orgs[i] = exportOrg{
			Slug:     o.Slug,
			Name:     o.Name,
			Kind:     o.Kind,
			Projects: exported,
		}
	}

	doc := allOrgsDocument{
		ExportedAt: time.Now().UTC(),
		Orgs:       orgs,
	}

	if err := writeExportFile(c.Output, doc); err != nil {
		return err
	}

	logger.InfoContext(ctx, "exported all orgs",
		slog.Int("org_count", len(orgs)),
		slog.Int("project_count", projectCount),
		slog.Int("insight_count", insightCount),
		slog.String("output", c.Output),
	)
	return nil
}

func exportInsightsForProject(ctx context.Context, st *postgres.Store, projectID uuid.UUID) ([]exportInsight, error) {
	insights, err := st.ListInsights(ctx, projectID, "", maxExportInsights)
	if err != nil {
		return nil, fmt.Errorf("failed to list insights: %w", err)
	}

	exported := make([]exportInsight, len(insights))
	for i, in := range insights {
		exported[i] = exportInsight{
			Key:       in.Key,
			Content:   in.Content,
			Tags:      in.Tags,
			Category:  in.Category,
			Source:    in.Source,
			CreatedAt: in.CreatedAt,
			UpdatedAt: in.UpdatedAt,
		}
	}
	return exported, nil
}

func writeExportFile(path string, doc any) error {
	out, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal export: %w", err)
	}

	if err := os.WriteFile(path, out, 0o600); err != nil {
		return fmt.Errorf("failed to write export file: %w", err)
	}
	return nil
}
