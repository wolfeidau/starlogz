import { useQuery } from "@connectrpc/connect-query";
import { Link } from "@tanstack/react-router";
import {
  getProjectDashboard,
  listTags,
} from "../../../api/gen/proto/es/starlogz/v1/ui-UIService_connectquery";
import { OverviewCharts, SummaryTiles } from "./summary";

export function OverviewRoute({ project }: { project: string }) {
  const dashboard = useQuery(getProjectDashboard, { project });
  const tags = useQuery(listTags, { project, limit: 60 });
  const topTags = tags.data?.tags ?? dashboard.data?.topTags ?? [];

  return (
    <>
      <Link to="/dashboard/$project" params={{ project }}>
        Insights
      </Link>
      <SummaryTiles dashboard={dashboard.data} tagCount={topTags.length} />
      <OverviewCharts dashboard={dashboard.data} topTags={topTags} />
    </>
  );
}
