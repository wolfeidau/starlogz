import type {
  CountBucket,
  Project,
} from "../../../api/gen/proto/es/starlogz/v1/ui_pb";
import { ProjectSelector } from "./project_selector";
import "./insights.css";

export function InsightsToolbar({
  projects,
  activeProject,
  onSelectProject,
  query,
  onQueryChange,
  selectedTag,
  onTagChange,
  topTags,
}: {
  projects: Project[];
  activeProject: string;
  onSelectProject: (slug: string) => void;
  query: string;
  onQueryChange: (query: string) => void;
  selectedTag: string;
  onTagChange: (tag: string) => void;
  topTags: CountBucket[];
}) {
  return (
    <section className="toolbar">
      <ProjectSelector
        projects={projects}
        selected={activeProject}
        onSelect={onSelectProject}
      />
      <input
        aria-label="Search insights"
        id="insight-search"
        name="query"
        value={query}
        onChange={(event) => onQueryChange(event.target.value)}
        placeholder="Search insights"
      />
      <select
        aria-label="Filter by tag"
        id="tag-filter"
        name="tag"
        value={selectedTag}
        onChange={(event) => onTagChange(event.target.value)}
      >
        <option value="">All tags</option>
        {topTags.map((tag) => (
          <option key={tag.name} value={tag.name}>
            {tag.name}
          </option>
        ))}
      </select>
    </section>
  );
}
