import { Code, ConnectError } from "@connectrpc/connect";
import type { MouseEvent } from "react";
import type {
  GetInsightResponse,
  InsightReference,
} from "../../../api/gen/proto/es/starlogz/v1/ui_pb";
import { Dialog } from "../../components/dialog";
import { Pill } from "../../components/pill";
import { Tag } from "../../components/tag";
import {
  dashboardURL,
  type InsightSelector,
  isUnmodifiedPrimaryClick,
} from "../../lib/insight_navigation";
import { RenderedMarkdown } from "../../lib/markdown";
import { formatTimestamp } from "../../lib/time";
import { InsightHistory } from "./insight_history";
import "./insight_detail.css";

function DetailRelation({
  label,
  reference,
  project,
  selector,
  onNavigate,
}: {
  label: string;
  reference: InsightReference;
  project: string;
  selector: InsightSelector;
  onNavigate: (selector: InsightSelector) => void;
}) {
  const handleClick = (event: MouseEvent<HTMLAnchorElement>) => {
    if (!isUnmodifiedPrimaryClick(event)) return;
    event.preventDefault();
    onNavigate(selector);
  };

  return (
    <li className="relationship-item">
      <a
        href={dashboardURL(window.location.href, project, selector)}
        onClick={handleClick}
      >
        {label}
      </a>
      {reference.category && <Pill>{reference.category}</Pill>}
      {reference.updatedAt && (
        <span className="muted">{formatTimestamp(reference.updatedAt)}</span>
      )}
    </li>
  );
}

function RelationshipSection({
  title,
  references,
  total,
  truncated,
  kind,
  project,
  onNavigate,
}: {
  title: string;
  references: InsightReference[];
  total: number;
  truncated: boolean;
  kind: "links" | "backlinks";
  project: string;
  onNavigate: (selector: InsightSelector) => void;
}) {
  return (
    <section className="relationship-section">
      <div className="relationship-heading">
        <h3>{title}</h3>
        <span>{total}</span>
      </div>
      {references.length === 0 ? (
        <p className="muted">No {title.toLowerCase()}.</p>
      ) : (
        <ul className="relationship-list">
          {references.map((reference) => {
            if (kind === "links" && !reference.resolved) {
              return (
                <li
                  className="relationship-item relationship-unresolved"
                  key={reference.targetKey}
                >
                  <span>{reference.targetKey}</span>
                  <span className="status-label">Unresolved</span>
                </li>
              );
            }

            const selector: InsightSelector =
              kind === "links" || reference.key
                ? {
                    case: "key",
                    value:
                      kind === "links" ? reference.targetKey : reference.key,
                  }
                : { case: "id", value: reference.id };
            const label =
              kind === "links"
                ? reference.targetKey
                : reference.key || reference.id;
            return (
              <DetailRelation
                key={`${selector.case}:${selector.value}`}
                label={label}
                reference={reference}
                project={project}
                selector={selector}
                onNavigate={onNavigate}
              />
            );
          })}
        </ul>
      )}
      {truncated && (
        <p className="relationship-truncated">
          Showing the first {references.length} of {total}.
        </p>
      )}
    </section>
  );
}

export function InsightDetail({
  project,
  selector,
  detail,
  error,
  loading,
  onClose,
  onNavigate,
}: {
  project: string;
  selector: InsightSelector;
  detail?: GetInsightResponse;
  error: Error | null;
  loading: boolean;
  onClose: () => void;
  onNavigate: (selector: InsightSelector) => void;
}) {
  const insight = detail?.insight;
  const notFound =
    error instanceof ConnectError && error.code === Code.NotFound;

  return (
    <Dialog
      className="detail-panel"
      labelledBy="insight-detail-title"
      onClose={onClose}
    >
      <div className="detail-content">
        <header className="detail-header">
          <div>
            <p className="eyebrow">Insight detail</p>
            <h2 id="insight-detail-title">{insight?.key || selector.value}</h2>
          </div>
          <button
            aria-label="Close insight detail"
            className="detail-close"
            onClick={onClose}
            type="button"
          >
            Close
          </button>
        </header>

        {loading ? (
          <div className="detail-state">Loading insight</div>
        ) : error ? (
          <div className="detail-state detail-error">
            <h3>{notFound ? "Insight not found" : "Unable to load insight"}</h3>
            <p>
              {notFound
                ? "The insight is missing, deleted, or unavailable in this project."
                : "The request failed. Close the panel and try again."}
            </p>
          </div>
        ) : insight ? (
          <>
            <div className="detail-metadata">
              <Pill>{insight.category}</Pill>
              <span>{insight.source}</span>
              <span>{formatTimestamp(insight.updatedAt)}</span>
            </div>
            <RenderedMarkdown
              html={insight.renderedHtml}
              onOpenInsight={(key) => onNavigate({ case: "key", value: key })}
            />
            <div className="detail-tags">
              {insight.tags.map((tag) => (
                <Tag key={tag}>{tag}</Tag>
              ))}
            </div>
            <div className="relationships-grid">
              <RelationshipSection
                title="Outgoing links"
                references={detail?.links ?? []}
                total={detail?.linkCount ?? 0}
                truncated={detail?.linksTruncated ?? false}
                kind="links"
                project={project}
                onNavigate={onNavigate}
              />
              <RelationshipSection
                title="Backlinks"
                references={detail?.backlinks ?? []}
                total={detail?.backlinkCount ?? 0}
                truncated={detail?.backlinksTruncated ?? false}
                kind="backlinks"
                project={project}
                onNavigate={onNavigate}
              />
            </div>
            <InsightHistory
              key={insight.id}
              project={project}
              insightId={insight.id}
              onOpenInsight={(key) => onNavigate({ case: "key", value: key })}
            />
          </>
        ) : (
          <div className="detail-state detail-error">Insight not found.</div>
        )}
      </div>
    </Dialog>
  );
}
