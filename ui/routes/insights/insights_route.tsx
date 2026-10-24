import { Outlet, useNavigate } from "@tanstack/react-router";
import { LoadMoreButton } from "../../components/load_more_button";
import { useDashboardInsightPages } from "./dashboard_pagination";
import { InsightRow } from "./insight_row";
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
        <div className="search-row">
          <SearchBox
            query={query}
            onQueryChange={(value) => setSearch({ q: value || undefined })}
          />
          <ViewTabs project={project} active="list" q={q} tag={tag} />
        </div>

        <section className="panel insights-panel">
          <div className="panel-heading">
            <h2 id="insights-heading" tabIndex={-1}>
              {query.trim() === "" ? "Insights" : "Search Results"}
            </h2>
            <span>{insights.length} shown</span>
          </div>
          {insightPages.isLoading ? (
            <div className="center-state">Loading insights</div>
          ) : insights.length === 0 ? (
            <div className="empty-panel">No matching insights.</div>
          ) : (
            <>
              {insights.map((insight) => (
                <InsightRow
                  insight={insight}
                  key={insight.id}
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
