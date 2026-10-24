import { useQuery } from "@connectrpc/connect-query";
import { Navigate } from "@tanstack/react-router";
import { listProjects } from "../../../api/gen/proto/es/starlogz/v1/ui-UIService_connectquery";
import { readLastProject } from "./last_project";

export function DashboardRedirect() {
  const projects = useQuery(listProjects, {});
  if (projects.isLoading) return <div className="center-state">Loading</div>;

  const list = projects.data?.projects ?? [];
  if (list.length === 0) {
    return <section className="empty-panel">No projects yet.</section>;
  }

  const last = readLastProject();
  const target = list.find((project) => project.slug === last) ?? list[0];
  return (
    <Navigate
      to="/dashboard/$project"
      params={{ project: target.slug }}
      replace
    />
  );
}
