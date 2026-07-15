import { useCallback, useEffect, useMemo, useState } from "react";
import {
  dashboardURL,
  readDashboardLocation,
  type InsightSelector,
} from "./insight_navigation";

type ProjectLocation = {
  slug: string;
};

export function useDashboardNavigation(projects: readonly ProjectLocation[]) {
  const initialLocation = useMemo(
    () => readDashboardLocation(window.location.search),
    [],
  );
  const [selectedProject, setSelectedProject] = useState(
    initialLocation.project,
  );
  const [selectedInsight, setSelectedInsight] =
    useState<InsightSelector | null>(initialLocation.selector);

  const activeProject = projects.some(
    (project) => project.slug === selectedProject,
  )
    ? selectedProject
    : projects[0]?.slug || "";
  const detailSelector =
    selectedProject === activeProject ? selectedInsight : null;

  const navigate = useCallback(
    (project: string, selector: InsightSelector | null, replace = false) => {
      const url = dashboardURL(window.location.href, project, selector);
      if (replace) window.history.replaceState({}, "", url);
      else window.history.pushState({}, "", url);
      setSelectedProject(project);
      setSelectedInsight(selector);
    },
    [],
  );

  useEffect(() => {
    if (!activeProject) return;
    const current = readDashboardLocation(window.location.search);
    if (
      selectedProject !== activeProject ||
      current.project !== activeProject
    ) {
      navigate(activeProject, null, true);
    }
  }, [activeProject, navigate, selectedProject]);

  useEffect(() => {
    const handlePopState = () => {
      const location = readDashboardLocation(window.location.search);
      setSelectedProject(location.project);
      setSelectedInsight(location.selector);
    };
    window.addEventListener("popstate", handlePopState);
    return () => window.removeEventListener("popstate", handlePopState);
  }, []);

  return { activeProject, detailSelector, navigate };
}
