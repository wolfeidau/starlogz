import type { Project } from "../../../api/gen/proto/es/starlogz/v1/ui_pb";

export function ProjectSelector({
  projects,
  selected,
  onSelect,
}: {
  projects: Project[];
  selected: string;
  onSelect: (slug: string) => void;
}) {
  return (
    <select
      aria-label="Project"
      id="project-selector"
      name="project"
      value={selected}
      onChange={(event) => onSelect(event.target.value)}
    >
      {projects.map((project) => (
        <option key={project.id} value={project.slug}>
          {project.name || project.slug}
        </option>
      ))}
    </select>
  );
}
