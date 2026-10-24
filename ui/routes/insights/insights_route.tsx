import { useQuery } from "@connectrpc/connect-query";
import { Link, Outlet, useNavigate } from "@tanstack/react-router";
import {
  listProjects,
  listTags,
} from "../../../api/gen/proto/es/starlogz/v1/ui-UIService_connectquery";
import { LoadMoreButton } from "../../components/load_more_button";
import { useDashboardInsightPages } from "./dashboard_pagination";
import { InsightTable } from "./insight_table";
import { InsightsToolbar } from "./toolbar";

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
  const projects = useQuery(listProjects, {});
  const tags = useQuery(listTags, { project, limit: 60 });
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
    <>
      <InsightsToolbar
        projects={projects.data?.projects ?? []}
        activeProject={project}
        onSelectProject={(slug) =>
          void navigate({
            to: "/dashboard/$project",
            params: { project: slug },
          })
        }
        query={query}
        onQueryChange={(value) => setSearch({ q: value || undefined })}
        selectedTag={selectedTag}
        onTagChange={(value) => setSearch({ tag: value || undefined })}
        topTags={tags.data?.tags ?? []}
      />

      <Link to="/dashboard/$project/overview" params={{ project }}>
        Overview
      </Link>

      <section className="panel insights-panel">
        <div className="panel-heading">
          <h2>{query.trim() === "" ? "Insights" : "Search Results"}</h2>
          <span>{insights.length} shown</span>
        </div>
        {insightPages.isLoading ? (
          <div className="center-state">Loading insights</div>
        ) : insights.length === 0 ? (
          <div className="empty-panel">No matching insights.</div>
        ) : (
          <>
            <InsightTable
              insights={insights}
              onOpenInsight={(key) =>
                void navigate({
                  to: "/dashboard/$project/insights/$key",
                  params: { project, key },
                  search: { q, tag },
                })
              }
            />
            {insightPages.hasNextPage && (
              <LoadMoreButton
                loading={insightPages.isFetchingNextPage}
                onLoadMore={() => void insightPages.fetchNextPage()}
              />
            )}
          </>
        )}
      </section>

      <Outlet />
    </>
  );
}
