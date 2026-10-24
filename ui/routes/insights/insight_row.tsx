import { Link } from "@tanstack/react-router";
import type { Insight } from "../../../api/gen/proto/es/starlogz/v1/ui_pb";
import { Pill } from "../../components/pill";
import { Tag } from "../../components/tag";
import { RenderedMarkdown } from "../../lib/markdown";
import { formatTimestamp } from "../../lib/time";
import "./insights.css";

export function InsightRow({
  insight,
  project,
  q,
  tag,
  onOpenInsight,
}: {
  insight: Insight;
  project: string;
  q?: string;
  tag?: string;
  onOpenInsight: (key: string) => void;
}) {
  const label = insight.key || insight.id.slice(0, 8);
  return (
    <article className="insight-row" data-insight-id={insight.id}>
      <div className="insight-row-head">
        <h3>
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
        <span className="insight-row-source">{insight.source}</span>
        <div className="tag-list">
          {insight.tags.map((name) => (
            <Tag key={name}>{name}</Tag>
          ))}
        </div>
        <span className="insight-row-updated">
          {formatTimestamp(insight.updatedAt)}
        </span>
      </div>
      <div className="insight-row-content">
        <RenderedMarkdown
          html={insight.renderedHtml}
          onOpenInsight={onOpenInsight}
        />
      </div>
    </article>
  );
}
