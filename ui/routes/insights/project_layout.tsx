import { useQuery } from "@connectrpc/connect-query";
import { Navigate, Outlet, useParams } from "@tanstack/react-router";
import { useEffect } from "react";
import { listProjects } from "../../../api/gen/proto/es/starlogz/v1/ui-UIService_connectquery";
import { writeLastProject } from "./last_project";

export function ProjectLayout() {
  const { project } = useParams({ from: "/dashboard/$project" });
  const projects = useQuery(listProjects, {});
  const known = projects.data?.projects.some((p) => p.slug === project);

  useEffect(() => {
    if (known) writeLastProject(project);
  }, [known, project]);

  if (projects.isLoading) return <div className="center-state">Loading</div>;
  if (!known) return <Navigate to="/dashboard" replace />;
  return <Outlet />;
}
