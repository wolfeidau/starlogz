import { useMutation, useQuery } from "@connectrpc/connect-query";
import { useState } from "react";
import {
  getInsight,
  getOperationsOverview,
  getOperationsTelemetry,
  getProjectDashboard,
  getSession,
  listProjects,
  listTags,
  revokeOperationsOAuthGrant,
  revokeOperationsWebSession,
} from "../../api/gen/proto/es/starlogz/v1/ui-UIService_connectquery";
import { LoadMoreButton } from "../components/load_more_button";
import { InsightDetail } from "../routes/insight_detail/insight_detail";
import { useDashboardInsightPages } from "../routes/insights/dashboard_pagination";
import { useDashboardNavigation } from "../routes/insights/dashboard_navigation";
import { InsightTable } from "../routes/insights/insight_table";
import { InsightsToolbar } from "../routes/insights/toolbar";
import { OperationsView } from "../routes/operations/operations";
import { OverviewCharts, SummaryTiles } from "../routes/overview/summary";
import { LoginView } from "./login_view";
import { TopBar } from "./top_bar";
import "./dashboard_view.css";

export function DashboardView() {
  const session = useQuery(getSession, {});
  const isAuthenticated = Boolean(session.data) && !session.error;
  const operationsRoute = window.location.pathname === "/admin/operations";
  const projectsQuery = useQuery(
    listProjects,
    {},
    { enabled: isAuthenticated && !operationsRoute },
  );
  const projects = projectsQuery.data?.projects ?? [];
  const { activeProject, detailSelector, navigate } =
    useDashboardNavigation(projects);
  const [query, setQuery] = useState("");
  const [selectedTag, setSelectedTag] = useState("");

  const dashboard = useQuery(
    getProjectDashboard,
    { project: activeProject },
    { enabled: isAuthenticated && activeProject !== "" },
  );
  const tags = useQuery(
    listTags,
    { project: activeProject, limit: 60 },
    { enabled: isAuthenticated && activeProject !== "" },
  );
  const insightPages = useDashboardInsightPages({
    isAuthenticated,
    activeProject,
    query,
    selectedTag,
  });
  const detail = useQuery(
    getInsight,
    {
      project: activeProject,
      selector: detailSelector ?? { case: undefined },
      relationLimit: 50,
    },
    {
      enabled:
        isAuthenticated && activeProject !== "" && detailSelector !== null,
    },
  );
  const operations = useQuery(
    getOperationsOverview,
    { limit: 50 },
    {
      enabled:
        isAuthenticated && operationsRoute && Boolean(session.data?.isOperator),
    },
  );
  const operationsTelemetry = useQuery(
    getOperationsTelemetry,
    {},
    {
      enabled:
        isAuthenticated && operationsRoute && Boolean(session.data?.isOperator),
    },
  );
  const revokeWebSession = useMutation(revokeOperationsWebSession);
  const revokeOAuthGrant = useMutation(revokeOperationsOAuthGrant);

  const onRevokeWebSession = async (id: string) => {
    await revokeWebSession.mutateAsync({ id });
    await operations.refetch();
  };
  const onRevokeOAuthGrant = async (id: string) => {
    await revokeOAuthGrant.mutateAsync({ id });
    await operations.refetch();
  };

  if (session.error) {
    return <LoginView />;
  }
  if (session.isLoading || (!operationsRoute && projectsQuery.isLoading)) {
    return <div className="center-state">Loading</div>;
  }
  if (operationsRoute) {
    if (!session.data?.isOperator) {
      return (
        <main className="app-shell">
          <TopBar
            login={session.data?.login ?? ""}
            displayName={session.data?.displayName ?? ""}
            avatarUrl={session.data?.avatarUrl ?? ""}
            isOperator={false}
            activeView="operations"
          />
          <section className="empty-panel">
            Service-operator access is required.
          </section>
        </main>
      );
    }
    return (
      <main className="app-shell">
        <TopBar
          login={session.data.login}
          displayName={session.data.displayName}
          avatarUrl={session.data.avatarUrl}
          isOperator={session.data.isOperator}
          activeView="operations"
        />
        <OperationsView
          overview={operations.data}
          loading={operations.isLoading}
          error={operations.error}
          telemetry={operationsTelemetry.data}
          telemetryLoading={operationsTelemetry.isLoading}
          telemetryError={operationsTelemetry.error}
          currentWebSessionId={session.data.webSessionId}
          onRevokeWebSession={onRevokeWebSession}
          onRevokeOAuthGrant={onRevokeOAuthGrant}
        />
      </main>
    );
  }
  if (projects.length === 0) {
    return (
      <main className="app-shell">
        <TopBar
          login={session.data?.login ?? ""}
          displayName={session.data?.displayName ?? ""}
          avatarUrl={session.data?.avatarUrl ?? ""}
          isOperator={session.data?.isOperator ?? false}
          activeView="insights"
        />
        <section className="empty-panel">No projects yet.</section>
      </main>
    );
  }

  const insights = insightPages.insights;
  const topTags = tags.data?.tags ?? dashboard.data?.topTags ?? [];

  return (
    <main className="app-shell">
      <TopBar
        login={session.data?.login ?? ""}
        displayName={session.data?.displayName ?? ""}
        avatarUrl={session.data?.avatarUrl ?? ""}
        isOperator={session.data?.isOperator ?? false}
        activeView="insights"
      />

      <InsightsToolbar
        projects={projects}
        activeProject={activeProject}
        onSelectProject={(slug) => {
          navigate(slug, null);
          setSelectedTag("");
          setQuery("");
        }}
        query={query}
        onQueryChange={setQuery}
        selectedTag={selectedTag}
        onTagChange={setSelectedTag}
        topTags={topTags}
      />

      <SummaryTiles dashboard={dashboard.data} tagCount={topTags.length} />

      <OverviewCharts dashboard={dashboard.data} topTags={topTags} />

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
                navigate(activeProject, { case: "key", value: key })
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

      {detailSelector && (
        <InsightDetail
          project={activeProject}
          selector={detailSelector}
          detail={detail.data}
          error={detail.error}
          loading={detail.isLoading}
          onClose={() => navigate(activeProject, null)}
          onNavigate={(selector) => navigate(activeProject, selector)}
        />
      )}
    </main>
  );
}
