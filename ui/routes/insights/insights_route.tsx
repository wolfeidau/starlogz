import { Outlet, useNavigate } from "@tanstack/react-router";
import { Fragment, useEffect, useRef, useState } from "react";
import type { Insight } from "../../../api/gen/proto/es/starlogz/v1/ui_pb";
import { LoadMoreButton } from "../../components/load_more_button";
import { useDashboardInsightPages } from "./dashboard_pagination";
import { dayKey, formatDayLabel, timestampToDate } from "../../lib/time";
import { InsightRow } from "./insight_row";
import { newSinceIndex, readLastSeen, writeLastSeen } from "./last_seen";
import { ProjectRail } from "./project_rail";
import { SearchBox } from "./search_box";
import { ViewTabs } from "./view_tabs";
import "./insights.css";

type DayGroup = { key: string; label: string; items: [Insight, number][] };

function groupByDay(insights: Insight[], now: Date): DayGroup[] {
  const groups: DayGroup[] = [];
  insights.forEach((insight, index) => {
    const date = timestampToDate(insight.updatedAt);
    const key = date ? dayKey(date) : "unknown";
    const last = groups.at(-1);
    if (last && last.key === key) {
      last.items.push([insight, index]);
    } else {
      groups.push({
        key,
        label: date ? formatDayLabel(date, now) : "Unknown date",
        items: [[insight, index]],
      });
    }
  });
  return groups;
}

function formatSince(date: Date): string {
  const text = new Intl.DateTimeFormat("en-US", {
    month: "short",
    day: "numeric",
    hour: "2-digit",
    minute: "2-digit",
    hour12: false,
  }).format(date);
  return `New since ${text}`;
}

export function InsightsRoute({
  project,
  q,
  tag,
}: {
  project: string;
  q?: string;
  tag?: string;
}) {
  const navigate = useNavigate();
  const query = q ?? "";
  const selectedTag = tag ?? "";
  const insightPages = useDashboardInsightPages({
    isAuthenticated: true,
    activeProject: project,
    query,
    selectedTag,
  });
  const insights = insightPages.insights;
  const searching = query.trim() !== "";

  // Held for the whole visit so the divider stays put while we record "now".
  const [lastSeen, setLastSeen] = useState(() => readLastSeen(project));
  const seenProject = useRef(project);
  useEffect(() => {
    if (seenProject.current !== project) {
      seenProject.current = project;
      setLastSeen(readLastSeen(project));
    }
    writeLastSeen(project, new Date());
  }, [project]);
  const sinceIndex = searching ? null : newSinceIndex(insights, lastSeen);

  const setSearch = (next: { q?: string; tag?: string }) =>
    void navigate({
      to: "/dashboard/$project",
      params: { project },
      search: { q, tag, ...next },
      replace: true,
    });

  return (
    <div className="project-layout">
      <ProjectRail
        project={project}
        selectedTag={selectedTag}
        onTagChange={(next) => setSearch({ tag: next })}
      />

      <div className="project-main">
        <h1 className="project-title">{project}</h1>
        <div className="search-row">
          <SearchBox
            query={query}
            onQueryChange={(value) => setSearch({ q: value || undefined })}
          />
          <ViewTabs project={project} active="list" q={q} tag={tag} />
        </div>

        <section
          className="log-ledger"
          aria-labelledby="insights-heading"
          data-searching={searching}
        >
          <div className="log-heading">
            <h2 id="insights-heading" tabIndex={-1}>
              {searching ? "Search results" : "Insights"}
            </h2>
            <span>{insights.length} shown</span>
          </div>
          {insightPages.isLoading ? (
            <div className="log-state">Loading insights</div>
          ) : insights.length === 0 ? (
            <div className="log-state">No matching insights.</div>
          ) : (
            <>
              {(searching
                ? [
                    {
                      key: "results",
                      label: "",
                      items: insights.map(
                        (i, n) => [i, n] as [Insight, number],
                      ),
                    },
                  ]
                : groupByDay(insights, new Date())
              ).map((group) => (
                <div
                  className="log-day"
                  data-mode={searching ? "search" : "list"}
                  key={group.key}
                >
                  {!searching && (
                    <h3 className="log-day-heading">{group.label}</h3>
                  )}
                  <div className="log-entries">
                    {group.items.map(([insight, index]) => (
                      <Fragment key={insight.id}>
                        {index === sinceIndex && lastSeen && (
                          <div className="log-since">
                            <span>{formatSince(lastSeen)}</span>
                          </div>
                        )}
                        <InsightRow
                          insight={insight}
                          mode={searching ? "search" : "list"}
                          project={project}
                          q={q}
                          tag={tag}
                          onOpenInsight={(key) =>
                            void navigate({
                              to: "/dashboard/$project/insights/$key",
                              params: { project, key },
                              search: { q, tag },
                            })
                          }
                        />
                      </Fragment>
                    ))}
                  </div>
                </div>
              ))}
              {insightPages.hasNextPage && (
                <LoadMoreButton
                  loading={insightPages.isFetchingNextPage}
                  onLoadMore={() => void insightPages.fetchNextPage()}
                />
              )}
            </>
          )}
        </section>
      </div>

      <Outlet />
    </div>
  );
}
