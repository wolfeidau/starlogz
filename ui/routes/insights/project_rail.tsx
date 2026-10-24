import { useQuery } from "@connectrpc/connect-query";
import { useNavigate } from "@tanstack/react-router";
import {
  listProjects,
  listTags,
} from "../../../api/gen/proto/es/starlogz/v1/ui-UIService_connectquery";
import { ProjectSelector } from "./project_selector";
import "./insights.css";

export function ProjectRail({
  project,
  selectedTag,
  onTagChange,
}: {
  project: string;
  selectedTag: string;
  onTagChange: (tag: string | undefined) => void;
}) {
  const navigate = useNavigate();
  const projects = useQuery(listProjects, {});
  const tags = useQuery(listTags, { project, limit: 60 });

  return (
    <aside className="project-rail">
      <ProjectSelector
        projects={projects.data?.projects ?? []}
        selected={project}
        onSelect={(slug) =>
          void navigate({
            to: "/dashboard/$project",
            params: { project: slug },
          })
        }
      />
      <section aria-labelledby="rail-tags-title">
        <h2 className="rail-title" id="rail-tags-title">
          Tags
        </h2>
        <div className="tag-chips">
          {(tags.data?.tags ?? []).map((bucket) => {
            const pressed = bucket.name === selectedTag;
            return (
              <button
                aria-pressed={pressed}
                className="tag-chip"
                key={bucket.name}
                onClick={() => onTagChange(pressed ? undefined : bucket.name)}
                type="button"
              >
                <span>{bucket.name}</span>
                <span className="tag-chip-count">{bucket.count}</span>
              </button>
            );
          })}
        </div>
      </section>
    </aside>
  );
}
