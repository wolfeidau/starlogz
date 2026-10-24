import type { CountBucket } from "../../../api/gen/proto/es/starlogz/v1/ui_pb";
import "./overview.css";

const categories = new Set([
  "fact",
  "decision",
  "insight",
  "preference",
  "context",
  "general",
]);

export function BucketList({
  title,
  buckets,
  categoryColors = false,
}: {
  title: string;
  buckets: CountBucket[];
  categoryColors?: boolean;
}) {
  const max = Math.max(1, ...buckets.map((b) => b.count));
  return (
    <section className="panel">
      <h2>{title}</h2>
      <div className="bucket-list">
        {buckets.length === 0 ? (
          <p className="muted">No data</p>
        ) : (
          buckets.map((bucket) => (
            <div className="bucket-row" key={bucket.name}>
              <span>{bucket.name || "unknown"}</span>
              <div className="bar-track">
                <div
                  className="bar-fill"
                  style={{
                    width: `${Math.max(8, (bucket.count / max) * 100)}%`,
                    ...(categoryColors && {
                      background: `var(--cat-${categories.has(bucket.name) ? bucket.name : "general"})`,
                    }),
                  }}
                />
              </div>
              <strong>{bucket.count}</strong>
            </div>
          ))
        )}
      </div>
    </section>
  );
}
