import type { ActivityBucket } from "../../../api/gen/proto/es/starlogz/v1/ui_pb";
import "./overview.css";

export function ActivityStrip({ buckets }: { buckets: ActivityBucket[] }) {
  const max = Math.max(1, ...buckets.map((b) => b.count));
  return (
    <section className="panel activity-panel">
      <h2>Recent Activity</h2>
      <div className="activity-strip">
        {buckets.map((bucket) => (
          <div className="activity-day" key={bucket.date}>
            <div
              className="activity-bar"
              style={{ height: `${Math.max(4, (bucket.count / max) * 72)}px` }}
              title={`${bucket.date}: ${bucket.count}`}
            />
            <span>{bucket.date.slice(5)}</span>
          </div>
        ))}
      </div>
    </section>
  );
}
