import type {
  CountBucket,
  GetProjectDashboardResponse,
} from "../../../api/gen/proto/es/starlogz/v1/ui_pb";
import { ActivityStrip } from "./activity_strip";
import { BucketList } from "./bucket_list";
import "./overview.css";

export function SummaryTiles({
  dashboard,
}: {
  dashboard?: GetProjectDashboardResponse;
}) {
  const total = dashboard?.totalInsights ?? 0;
  return (
    <p className="overview-lead">
      <strong>{total}</strong> {total === 1 ? "insight" : "insights"}
    </p>
  );
}

export function OverviewCharts({
  dashboard,
  topTags,
}: {
  dashboard?: GetProjectDashboardResponse;
  topTags: CountBucket[];
}) {
  return (
    <section className="dashboard-grid">
      <BucketList
        title="Categories"
        buckets={dashboard?.categoryCounts ?? []}
        categoryColors
      />
      <BucketList title="Sources" buckets={dashboard?.sourceCounts ?? []} />
      <BucketList title="Top tags" buckets={topTags.slice(0, 8)} />
      <ActivityStrip buckets={dashboard?.recentActivity ?? []} />
    </section>
  );
}
