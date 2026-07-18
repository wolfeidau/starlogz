import { useInfiniteQuery } from "@connectrpc/connect-query";
import { useState } from "react";
import type { InsightRevision } from "../../api/gen/proto/es/starlogz/v1/ui_pb";
import { listInsightHistory } from "../../api/gen/proto/es/starlogz/v1/ui-UIService_connectquery";
import { LoadMoreButton, nextPageCursor } from "./dashboard_pagination";
import { formatTimestamp, RenderedMarkdown } from "./insight_content";

function operationLabel(operation: string): string {
  const labels: Record<string, string> = {
    baseline: "Baseline",
    create: "Created",
    update: "Updated",
    delete: "Deleted",
    restore: "Restored",
  };
  return labels[operation] ?? "Changed";
}

function RevisionPreview({
  revision,
  onOpenInsight,
}: {
  revision: InsightRevision;
  onOpenInsight: (key: string) => void;
}) {
  return (
    <article
      aria-labelledby={`revision-${revision.revision}-title`}
      className="history-preview"
    >
      <header className="history-preview-header">
        <div>
          <p className="eyebrow">Read-only snapshot</p>
          <h4 id={`revision-${revision.revision}-title`}>
            Revision {revision.revision}
          </h4>
        </div>
        <span className="status-label">
          {operationLabel(revision.operation)}
        </span>
      </header>
      <div className="history-preview-metadata">
        <span className="pill">{revision.category}</span>
        <span>{revision.source}</span>
        <span>{formatTimestamp(revision.changedAt)}</span>
        <span>
          {revision.changedBy ? `Actor ${revision.changedBy}` : "Actor unknown"}
        </span>
        {revision.deletedAt && (
          <span className="history-deleted">Deleted state</span>
        )}
      </div>
      <RenderedMarkdown
        html={revision.renderedHtml}
        onOpenInsight={onOpenInsight}
      />
      {revision.tags.length > 0 && (
        <div className="detail-tags">
          {revision.tags.map((tag) => (
            <span className="tag" key={tag}>
              {tag}
            </span>
          ))}
        </div>
      )}
    </article>
  );
}

export function InsightHistory({
  project,
  insightId,
  onOpenInsight,
}: {
  project: string;
  insightId: string;
  onOpenInsight: (key: string) => void;
}) {
  const [selectedRevision, setSelectedRevision] = useState<number>();
  const history = useInfiniteQuery(
    listInsightHistory,
    { project, id: insightId, limit: 20, cursor: "" },
    {
      enabled: project !== "" && insightId !== "",
      pageParamKey: "cursor",
      getNextPageParam: nextPageCursor,
    },
  );
  const revisions = history.data?.pages.flatMap((page) => page.revisions) ?? [];
  const selected =
    revisions.find((revision) => revision.revision === selectedRevision) ??
    revisions[0];
  const currentState = history.data?.pages.at(-1);

  return (
    <section
      aria-labelledby="insight-history-title"
      className="history-section"
    >
      <div className="history-heading">
        <div>
          <h3 id="insight-history-title">History</h3>
          <p className="muted">Immutable snapshots, newest first.</p>
        </div>
        {currentState && (
          <span>
            Current revision {currentState.currentRevision}
            {currentState.deleted ? " · deleted" : ""}
          </span>
        )}
      </div>

      {history.isLoading ? (
        <div className="history-state">Loading history</div>
      ) : history.error && revisions.length === 0 ? (
        <div className="history-state history-error">
          History is unavailable. Try again later.
        </div>
      ) : revisions.length === 0 ? (
        <div className="history-state">No history available.</div>
      ) : (
        <div className="history-layout">
          <div className="history-navigation">
            <ol className="history-list">
              {revisions.map((revision) => (
                <li key={revision.revision}>
                  <button
                    aria-pressed={selected?.revision === revision.revision}
                    className="history-revision-button"
                    onClick={() => setSelectedRevision(revision.revision)}
                    type="button"
                  >
                    <span>
                      <strong>Revision {revision.revision}</strong>
                      {revision.revision === currentState?.currentRevision && (
                        <span className="status-label">Current</span>
                      )}
                      {revision.deletedAt && (
                        <span className="history-deleted">Deleted</span>
                      )}
                    </span>
                    <span>
                      {operationLabel(revision.operation)} ·{" "}
                      {formatTimestamp(revision.changedAt)}
                    </span>
                  </button>
                </li>
              ))}
            </ol>
            {history.isFetchNextPageError && (
              <p className="history-pagination-error" role="alert">
                Unable to load more history. Loaded revisions remain available.
              </p>
            )}
            {(history.hasNextPage || history.isFetchNextPageError) && (
              <LoadMoreButton
                loading={history.isFetchingNextPage}
                label={history.isFetchNextPageError ? "Retry" : "Load more"}
                onLoadMore={() => void history.fetchNextPage()}
              />
            )}
          </div>
          {selected && (
            <RevisionPreview
              revision={selected}
              onOpenInsight={onOpenInsight}
            />
          )}
        </div>
      )}
    </section>
  );
}
