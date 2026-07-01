package commands

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"

	"github.com/google/uuid"
	"github.com/wolfeidau/starlogz/internal/store"
	"github.com/wolfeidau/starlogz/internal/store/postgres"
)

type ImportCmd struct {
	DatabaseURL string `help:"PostgreSQL connection string." env:"DATABASE_URL" required:""`
	Input       string `help:"Path to a JSON file produced by the export command." required:""`
	OrgID       string `help:"ID of the org to import projects into. Must already exist on this instance." required:""`
	CreatedBy   string `help:"ID of the user to attribute imported projects and insights to." required:""`
}

// importDocument accepts either export shape: a single project, or a full
// --all export. Only one of the two fields will be set in a given file.
type importDocument struct {
	Project *exportProject `json:"project"`
	Orgs    []exportOrg    `json:"orgs"`
}

func (c *ImportCmd) Run(ctx context.Context, globals *Globals) error {
	orgID, err := uuid.Parse(c.OrgID)
	if err != nil {
		return fmt.Errorf("failed to parse org id: %w", err)
	}
	createdBy, err := uuid.Parse(c.CreatedBy)
	if err != nil {
		return fmt.Errorf("failed to parse created-by id: %w", err)
	}

	raw, err := os.ReadFile(c.Input)
	if err != nil {
		return fmt.Errorf("failed to read import file: %w", err)
	}
	var doc importDocument
	if err := json.Unmarshal(raw, &doc); err != nil {
		return fmt.Errorf("failed to parse import file: %w", err)
	}

	var projects []exportProject
	if doc.Project != nil {
		projects = append(projects, *doc.Project)
	}
	slugSourceOrgs := map[string][]string{}
	for _, o := range doc.Orgs {
		projects = append(projects, o.Projects...)
		for _, p := range o.Projects {
			slugSourceOrgs[p.Slug] = append(slugSourceOrgs[p.Slug], o.Slug)
		}
	}
	for slug, orgSlugs := range slugSourceOrgs {
		if len(orgSlugs) > 1 {
			return fmt.Errorf("project slug %q appears in multiple source orgs: %v; refusing to merge", slug, orgSlugs)
		}
	}
	if len(projects) == 0 {
		return fmt.Errorf("import file contains no projects")
	}

	importProjects := make([]store.ImportProject, len(projects))
	for i, p := range projects {
		insights := make([]store.ImportInsight, len(p.Insights))
		for j, in := range p.Insights {
			insights[j] = store.ImportInsight{
				Key:       in.Key,
				Content:   in.Content,
				Tags:      in.Tags,
				Category:  in.Category,
				Source:    in.Source,
				CreatedAt: in.CreatedAt,
				UpdatedAt: in.UpdatedAt,
			}
		}
		importProjects[i] = store.ImportProject{Slug: p.Slug, Name: p.Name, Insights: insights}
	}

	st, err := postgres.New(ctx, c.DatabaseURL, store.NewEncryptor([32]byte{}))
	if err != nil {
		return fmt.Errorf("failed to connect to database: %w", err)
	}
	defer st.Close()

	// A single transaction covers every project and insight so a failure partway
	// through (e.g. an invalid category) leaves no partial data behind.
	projectCount, insightCount, err := st.ImportProjects(ctx, orgID, createdBy, importProjects)
	if err != nil {
		return fmt.Errorf("failed to import projects: %w", err)
	}

	globals.Logger.InfoContext(ctx, "imported data",
		slog.Int("project_count", projectCount),
		slog.Int("insight_count", insightCount),
		slog.String("input", c.Input),
	)

	return nil
}
