import type {
  CountBucket,
  GetProjectDashboardResponse,
} from "../../../api/gen/proto/es/starlogz/v1/ui_pb";
import { Metric } from "../../components/metric";
import { ActivityStrip } from "./activity_strip";
import { BucketList } from "./bucket_list";
import "./overview.css";

export function SummaryTiles({
  dashboard,
}: {
  dashboard?: GetProjectDashboardResponse;
}) {
  return (
    <section className="summary-grid">
      <Metric label="Total insights" value={dashboard?.totalInsights ?? 0} />
    </section>
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
      />
      <BucketList title="Sources" buckets={dashboard?.sourceCounts ?? []} />
      <BucketList title="Top Tags" buckets={topTags.slice(0, 8)} />
      <ActivityStrip buckets={dashboard?.recentActivity ?? []} />
    </section>
  );
}
