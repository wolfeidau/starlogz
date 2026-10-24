import type { ActivityBucket } from "../../../api/gen/proto/es/starlogz/v1/ui_pb";
import "./overview.css";

function localToday() {
  const now = new Date();
  const month = String(now.getMonth() + 1).padStart(2, "0");
  const day = String(now.getDate()).padStart(2, "0");
  return `${now.getFullYear()}-${month}-${day}`;
}

export function ActivityStrip({ buckets }: { buckets: ActivityBucket[] }) {
  const max = Math.max(1, ...buckets.map((b) => b.count));
  const today = localToday();
  const todayDate = buckets.findLast((b) => b.date === today)?.date;
  return (
    <section className="panel activity-panel">
      <h2>Recent activity</h2>
      <div className="activity-strip">
        {buckets.map((bucket) => (
          <div className="activity-day" key={bucket.date}>
            <div
              className={
                bucket.date === todayDate
                  ? "activity-bar activity-bar-today"
                  : "activity-bar"
              }
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
