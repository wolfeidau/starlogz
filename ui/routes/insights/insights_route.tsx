import { Outlet, useNavigate } from "@tanstack/react-router";
import { Fragment, useEffect, useState } from "react";
import { LoadMoreButton } from "../../components/load_more_button";
import { useDashboardInsightPages } from "./dashboard_pagination";
import { formatSinceLabel } from "../../lib/time";
import { InsightRow } from "./insight_row";
import { toGroups } from "./log_groups";
import { newSinceIndex, readLastSeen, writeLastSeen } from "./last_seen";
import { ProjectRail } from "./project_rail";
import { SearchBox } from "./search_box";
import { ViewTabs } from "./view_tabs";
import "./insights.css";

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

  // Held for the whole visit so the divider stays put while we record "now"; the router remounts per project.
  const [lastSeen] = useState(() => readLastSeen(project));
  useEffect(() => writeLastSeen(project, new Date()), [project]);
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
        <h1 className="project-title" id="project-title" tabIndex={-1}>
          {project}
        </h1>
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
              {toGroups(insights, searching, new Date()).map((group) => (
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
                            <span>{formatSinceLabel(lastSeen)}</span>
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
