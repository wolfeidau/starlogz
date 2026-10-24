import { useQuery } from "@connectrpc/connect-query";
import { useNavigate } from "@tanstack/react-router";
import {
  getProjectDashboard,
  listTags,
} from "../../../api/gen/proto/es/starlogz/v1/ui-UIService_connectquery";
import { ProjectRail } from "../insights/project_rail";
import { ViewTabs } from "../insights/view_tabs";
import { OverviewCharts, SummaryTiles } from "./summary";

export function OverviewRoute({ project }: { project: string }) {
  const navigate = useNavigate();
  const timeZone = Intl.DateTimeFormat().resolvedOptions().timeZone || "UTC";
  const dashboard = useQuery(getProjectDashboard, { project, timeZone });
  const tags = useQuery(listTags, { project, limit: 60 });
  const topTags = tags.data?.tags ?? dashboard.data?.topTags ?? [];

  return (
    <div className="project-layout">
      <ProjectRail
        project={project}
        selectedTag=""
        onTagChange={(tag) =>
          void navigate({
            to: "/dashboard/$project",
            params: { project },
            search: { tag },
          })
        }
      />
      <div className="project-main">
        <h1 className="project-title">{project}</h1>
        <ViewTabs project={project} active="overview" />
        <SummaryTiles dashboard={dashboard.data} />
        <OverviewCharts
          dashboard={dashboard.data}
          topTags={topTags}
          timeZone={timeZone}
        />
      </div>
    </div>
  );
}
