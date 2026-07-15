import { createConnectTransport } from "@connectrpc/connect-web";
import { TransportProvider, useQuery } from "@connectrpc/connect-query";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { useMemo, useState } from "react";
import { createRoot } from "react-dom/client";
import {
  getInsight,
  getProjectDashboard,
  getSession,
  listInsights,
  listProjects,
  listTags,
  searchInsights,
} from "../../api/gen/proto/es/starlogz/v1/ui-UIService_connectquery";
import type {
  ActivityBucket,
  CountBucket,
  Insight,
  Project,
} from "../../api/gen/proto/es/starlogz/v1/ui_pb";
import { useDashboardNavigation } from "./dashboard_navigation";
import {
  formatTimestamp,
  InsightDetail,
  RenderedMarkdown,
} from "./insight_detail";
import "./dashboard.css";

function LoginView() {
  return (
    <main className="login-shell">
      <section className="login-panel">
        <div>
          <p className="eyebrow">starlogz</p>
          <h1>Project insights</h1>
          <p className="login-copy">
            Browse facts, decisions, preferences, and context captured by your
            agents.
          </p>
        </div>
        <a className="primary-link" href="/login">
          Sign in with GitHub
        </a>
      </section>
    </main>
  );
}

function BucketList({
  title,
  buckets,
}: {
  title: string;
  buckets: CountBucket[];
}) {
  const max = Math.max(1, ...buckets.map((b) => b.count));
  return (
    <section className="panel">
      <h2>{title}</h2>
      <div className="bucket-list">
        {buckets.length === 0 ? (
          <p className="muted">No data</p>
        ) : (
          buckets.map((bucket) => (
            <div className="bucket-row" key={bucket.name}>
              <span>{bucket.name || "unknown"}</span>
              <div className="bar-track">
                <div
                  className="bar-fill"
                  style={{
                    width: `${Math.max(8, (bucket.count / max) * 100)}%`,
                  }}
                />
              </div>
              <strong>{bucket.count}</strong>
            </div>
          ))
        )}
      </div>
    </section>
  );
}

function ActivityStrip({ buckets }: { buckets: ActivityBucket[] }) {
  const max = Math.max(1, ...buckets.map((b) => b.count));
  return (
    <section className="panel activity-panel">
      <h2>Recent Activity</h2>
      <div className="activity-strip">
        {buckets.map((bucket) => (
          <div className="activity-day" key={bucket.date}>
            <div
              className="activity-bar"
              style={{ height: `${Math.max(4, (bucket.count / max) * 72)}px` }}
              title={`${bucket.date}: ${bucket.count}`}
            />
            <span>{bucket.date.slice(5)}</span>
          </div>
        ))}
      </div>
    </section>
  );
}

function InsightTable({
  insights,
  onOpenInsight,
}: {
  insights: Insight[];
  onOpenInsight: (key: string) => void;
}) {
  return (
    <div className="table-wrap">
      <table>
        <thead>
          <tr>
            <th>Insight</th>
            <th>Category</th>
            <th>Source</th>
            <th>Tags</th>
            <th>Updated</th>
          </tr>
        </thead>
        <tbody>
          {insights.map((insight) => (
            <tr key={insight.id}>
              <td>
                <div className="insight-content">
                  <RenderedMarkdown
                    html={insight.renderedHtml}
                    onOpenInsight={onOpenInsight}
                  />
                </div>
                {insight.key && <code>{insight.key}</code>}
              </td>
              <td>
                <span className="pill">{insight.category}</span>
              </td>
              <td>{insight.source}</td>
              <td>
                <div className="tag-list">
                  {insight.tags.map((tag) => (
                    <span className="tag" key={tag}>
                      {tag}
                    </span>
                  ))}
                </div>
              </td>
              <td className="nowrap">{formatTimestamp(insight.updatedAt)}</td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  );
}

function ProjectSelector({
  projects,
  selected,
  onSelect,
}: {
  projects: Project[];
  selected: string;
  onSelect: (slug: string) => void;
}) {
  return (
    <select
      aria-label="Project"
      id="project-selector"
      name="project"
      value={selected}
      onChange={(event) => onSelect(event.target.value)}
    >
      {projects.map((project) => (
        <option key={project.id} value={project.slug}>
          {project.name || project.slug}
        </option>
      ))}
    </select>
  );
}

function DashboardView() {
  const session = useQuery(getSession, {});
  const isAuthenticated = Boolean(session.data) && !session.error;
  const projectsQuery = useQuery(
    listProjects,
    {},
    { enabled: isAuthenticated },
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
  const search = useQuery(
    searchInsights,
    {
      project: activeProject,
      query,
      tags: selectedTag ? [selectedTag] : [],
      limit: 100,
    },
    { enabled: isAuthenticated && activeProject !== "" && query.trim() !== "" },
  );
  const listed = useQuery(
    listInsights,
    { project: activeProject, tag: selectedTag, limit: 100 },
    { enabled: isAuthenticated && activeProject !== "" && query.trim() === "" },
  );
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

  if (session.error) {
    return <LoginView />;
  }
  if (session.isLoading || projectsQuery.isLoading) {
    return <div className="center-state">Loading</div>;
  }
  if (projects.length === 0) {
    return (
      <main className="app-shell">
        <TopBar
          login={session.data?.login ?? ""}
          displayName={session.data?.displayName ?? ""}
          avatarUrl={session.data?.avatarUrl ?? ""}
        />
        <section className="empty-panel">No projects yet.</section>
      </main>
    );
  }

  const insights =
    query.trim() === ""
      ? (listed.data?.insights ?? [])
      : (search.data?.insights ?? []);
  const topTags = tags.data?.tags ?? dashboard.data?.topTags ?? [];

  return (
    <main className="app-shell">
      <TopBar
        login={session.data?.login ?? ""}
        displayName={session.data?.displayName ?? ""}
        avatarUrl={session.data?.avatarUrl ?? ""}
      />

      <section className="toolbar">
        <ProjectSelector
          projects={projects}
          selected={activeProject}
          onSelect={(slug) => {
            navigate(slug, null);
            setSelectedTag("");
            setQuery("");
          }}
        />
        <input
          aria-label="Search insights"
          id="insight-search"
          name="query"
          value={query}
          onChange={(event) => setQuery(event.target.value)}
          placeholder="Search insights"
        />
        <select
          aria-label="Filter by tag"
          id="tag-filter"
          name="tag"
          value={selectedTag}
          onChange={(event) => setSelectedTag(event.target.value)}
        >
          <option value="">All tags</option>
          {topTags.map((tag) => (
            <option key={tag.name} value={tag.name}>
              {tag.name}
            </option>
          ))}
        </select>
      </section>

      <section className="summary-grid">
        <div className="metric">
          <span>Total insights</span>
          <strong>{dashboard.data?.totalInsights ?? 0}</strong>
        </div>
        <div className="metric">
          <span>Categories</span>
          <strong>{dashboard.data?.categoryCounts.length ?? 0}</strong>
        </div>
        <div className="metric">
          <span>Sources</span>
          <strong>{dashboard.data?.sourceCounts.length ?? 0}</strong>
        </div>
        <div className="metric">
          <span>Tags</span>
          <strong>{topTags.length}</strong>
        </div>
      </section>

      <section className="dashboard-grid">
        <BucketList
          title="Categories"
          buckets={dashboard.data?.categoryCounts ?? []}
        />
        <BucketList
          title="Sources"
          buckets={dashboard.data?.sourceCounts ?? []}
        />
        <BucketList title="Top Tags" buckets={topTags.slice(0, 8)} />
        <ActivityStrip buckets={dashboard.data?.recentActivity ?? []} />
      </section>

      <section className="panel insights-panel">
        <div className="panel-heading">
          <h2>{query.trim() === "" ? "Insights" : "Search Results"}</h2>
          <span>{insights.length} shown</span>
        </div>
        {listed.isLoading || search.isLoading ? (
          <div className="center-state">Loading insights</div>
        ) : insights.length === 0 ? (
          <div className="empty-panel">No matching insights.</div>
        ) : (
          <InsightTable
            insights={insights}
            onOpenInsight={(key) =>
              navigate(activeProject, { case: "key", value: key })
            }
          />
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

function TopBar({
  login,
  displayName,
  avatarUrl,
}: {
  login: string;
  displayName: string;
  avatarUrl: string;
}) {
  return (
    <header className="topbar">
      <div>
        <p className="eyebrow">starlogz</p>
        <h1>Insights Dashboard</h1>
      </div>
      <div className="topbar-actions">
        {avatarUrl && <img className="user-avatar" src={avatarUrl} alt="" />}
        <span>{displayName || login}</span>
        <form action="/logout" method="post">
          <button className="logout-button" type="submit">
            Logout
          </button>
        </form>
      </div>
    </header>
  );
}

const queryClient = new QueryClient({
  defaultOptions: {
    queries: {
      retry: false,
    },
  },
});

function App() {
  const transport = useMemo(
    () =>
      createConnectTransport({
        baseUrl: window.location.origin,
        credentials: "include",
      }),
    [],
  );

  return (
    <TransportProvider transport={transport}>
      <QueryClientProvider client={queryClient}>
        <DashboardView />
      </QueryClientProvider>
    </TransportProvider>
  );
}

const app = document.getElementById("app");
if (!app) throw new Error("App element not found");
createRoot(app).render(<App />);
