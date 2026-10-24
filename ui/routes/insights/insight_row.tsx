import { Link } from "@tanstack/react-router";
import type { Insight } from "../../../api/gen/proto/es/starlogz/v1/ui_pb";
import { Pill } from "../../components/pill";
import { Tag } from "../../components/tag";
import { RenderedMarkdown } from "../../lib/markdown";
import { formatTime, formatTimestamp } from "../../lib/time";
import "./insights.css";

function StarMark() {
  return (
    <svg
      aria-hidden="true"
      className="insight-row-star"
      focusable="false"
      viewBox="0 0 16 16"
    >
      <path
        d="M8 0.5 L9.6 6.4 L15.5 8 L9.6 9.6 L8 15.5 L6.4 9.6 L0.5 8 L6.4 6.4 Z"
        fill="currentColor"
      />
    </svg>
  );
}

export function InsightRow({
  insight,
  project,
  q,
  tag,
  mode,
  onOpenInsight,
}: {
  insight: Insight;
  project: string;
  q?: string;
  tag?: string;
  mode: "list" | "search";
  onOpenInsight: (key: string) => void;
}) {
  const label = insight.key || insight.id.slice(0, 8);
  return (
    <article
      className="insight-row"
      data-category={insight.category || "general"}
      data-insight-id={insight.id}
    >
      {mode === "list" && <StarMark />}
      <div className="insight-row-head">
        <h3 className="mono">
          {insight.key ? (
            <Link
              className="insight-row-link"
              params={{ project, key: insight.key }}
              search={{ q, tag }}
              to="/dashboard/$project/insights/$key"
            >
              {label}
            </Link>
          ) : (
            <Link
              className="insight-row-link"
              params={{ project, id: insight.id }}
              search={{ q, tag }}
              to="/dashboard/$project/insights/by-id/$id"
            >
              {label}
            </Link>
          )}
        </h3>
        <Pill>{insight.category}</Pill>
        <span className="insight-row-updated">
          {mode === "list"
            ? formatTime(insight.updatedAt)
            : formatTimestamp(insight.updatedAt)}
        </span>
      </div>
      <div className="insight-row-content">
        <RenderedMarkdown
          html={insight.renderedHtml}
          onOpenInsight={onOpenInsight}
        />
      </div>
      <div className="insight-row-meta">
        <span>{insight.source}</span>
        {insight.tags.length > 0 && (
          <div className="tag-list">
            {insight.tags.map((name) => (
              <Tag key={name}>{name}</Tag>
            ))}
          </div>
        )}
      </div>
    </article>
  );
}
