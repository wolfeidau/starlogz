import { Code, ConnectError } from "@connectrpc/connect";
import { Link, useSearch } from "@tanstack/react-router";
import type {
  GetInsightResponse,
  InsightReference,
} from "../../../api/gen/proto/es/starlogz/v1/ui_pb";
import { Dialog } from "../../components/dialog";
import { Pill } from "../../components/pill";
import { Tag } from "../../components/tag";
import type { InsightSelector } from "../../lib/insight_navigation";
import { RenderedMarkdown } from "../../lib/markdown";
import { formatTimestamp } from "../../lib/time";
import { InsightHistory } from "./insight_history";
import "./insight_detail.css";

function DetailRelation({
  label,
  reference,
  project,
  selector,
}: {
  label: string;
  reference: InsightReference;
  project: string;
  selector: InsightSelector;
}) {
  const { q, tag } = useSearch({ strict: false });
  return (
    <li className="relationship-item">
      {selector.case === "key" ? (
        <Link
          to="/dashboard/$project/insights/$key"
          params={{ project, key: selector.value }}
          search={{ q, tag }}
        >
          {label}
        </Link>
      ) : (
        <Link
          to="/dashboard/$project/insights/by-id/$id"
          params={{ project, id: selector.value }}
          search={{ q, tag }}
        >
          {label}
        </Link>
      )}
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
}: {
  title: string;
  references: InsightReference[];
  total: number;
  truncated: boolean;
  kind: "links" | "backlinks";
  project: string;
}) {
  return (
    <section className="relationship-section">
      <div className="relationship-heading">
        <h3>{title}</h3>
        <span className="relationship-count">{total}</span>
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

// Row of the shown insight if listed, else the visible project title, so focus never falls to <body>.
function insightFocusTarget(id: string): HTMLElement | null {
  for (const row of document.querySelectorAll<HTMLElement>(
    "[data-insight-id]",
  )) {
    if (id && row.dataset.insightId === id) {
      const link = row.querySelector<HTMLElement>("a.insight-row-link");
      if (link) return link;
    }
  }
  return document.getElementById("project-title");
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
  const shownId = insight?.id ?? (selector.case === "id" ? selector.value : "");
  const notFound =
    error instanceof ConnectError && error.code === Code.NotFound;

  return (
    <Dialog
      className="detail-panel"
      labelledBy="insight-detail-title"
      onClose={onClose}
      preferFallback
      returnFocusFallback={() => insightFocusTarget(shownId)}
      variant="drawer"
    >
      <div className="detail-content">
        <header className="detail-header">
          <h2 className="mono" id="insight-detail-title">
            {insight?.key || selector.value}
          </h2>
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
              />
              <RelationshipSection
                title="Backlinks"
                references={detail?.backlinks ?? []}
                total={detail?.backlinkCount ?? 0}
                truncated={detail?.backlinksTruncated ?? false}
                kind="backlinks"
                project={project}
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
