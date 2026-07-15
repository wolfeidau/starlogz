import { Code, ConnectError } from "@connectrpc/connect";
import { createConnectTransport } from "@connectrpc/connect-web";
import { TransportProvider, useQuery } from "@connectrpc/connect-query";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import {
  useCallback,
  useEffect,
  useMemo,
  useState,
  type MouseEvent,
} from "react";
import { createRoot } from "react-dom/client";
import type { Timestamp } from "@bufbuild/protobuf/wkt";
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
  GetInsightResponse,
  Insight,
  InsightReference,
  Project,
} from "../../api/gen/proto/es/starlogz/v1/ui_pb";
import {
  dashboardURL,
  isUnmodifiedPrimaryClick,
  openInsightKeyForClick,
  readDashboardLocation,
  type InsightSelector,
} from "./insight_navigation";
import "./dashboard.css";

function timestampToDate(ts?: Timestamp): Date | null {
  if (!ts) return null;
  return new Date(Number(ts.seconds) * 1000 + Math.floor(ts.nanos / 1_000_000));
}

function formatTimestamp(ts?: Timestamp): string {
  const date = timestampToDate(ts);
  if (!date) return "-";
  return new Intl.DateTimeFormat("en-US", {
    month: "short",
    day: "numeric",
    hour: "2-digit",
    minute: "2-digit",
  }).format(date);
}

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

function RenderedMarkdown({
  html,
  onOpenInsight,
}: {
  html: string;
  onOpenInsight: (key: string) => void;
}) {
  const handleClick = (event: MouseEvent<HTMLDivElement>) => {
    const key = openInsightKeyForClick(event, event.target);
    if (!key) return;
    event.preventDefault();
    onOpenInsight(key);
  };

  return (
    // biome-ignore lint/a11y/noStaticElementInteractions lint/a11y/useKeyWithClickEvents: interaction is delegated to sanitized anchors, whose keyboard behavior remains native.
    <div
      className="markdown-preview"
      onClick={handleClick}
      // biome-ignore lint/security/noDangerouslySetInnerHtml: the server returns allowlist-sanitized HTML.
      dangerouslySetInnerHTML={{ __html: html }}
    />
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
      {reference.category && <span className="pill">{reference.category}</span>}
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

function InsightDetail({
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
    <div className="detail-backdrop">
      <section
        aria-labelledby="insight-detail-title"
        aria-modal="true"
        className="detail-panel"
        role="dialog"
      >
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
              <span className="pill">{insight.category}</span>
              <span>{insight.source}</span>
              <span>{formatTimestamp(insight.updatedAt)}</span>
            </div>
            <RenderedMarkdown
              html={insight.renderedHtml}
              onOpenInsight={(key) => onNavigate({ case: "key", value: key })}
            />
            <div className="detail-tags">
              {insight.tags.map((tag) => (
                <span className="tag" key={tag}>
                  {tag}
                </span>
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
          </>
        ) : (
          <div className="detail-state detail-error">Insight not found.</div>
        )}
      </section>
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
  const initialLocation = useMemo(
    () => readDashboardLocation(window.location.search),
    [],
  );
  const session = useQuery(getSession, {});
  const isAuthenticated = Boolean(session.data) && !session.error;
  const projectsQuery = useQuery(
    listProjects,
    {},
    { enabled: isAuthenticated },
  );
  const projects = projectsQuery.data?.projects ?? [];
  const [selectedProject, setSelectedProject] = useState(
    initialLocation.project,
  );
  const [detailSelector, setDetailSelector] = useState<InsightSelector | null>(
    initialLocation.selector,
  );
  const [query, setQuery] = useState("");
  const [selectedTag, setSelectedTag] = useState("");

  const activeProject = projects.some(
    (project) => project.slug === selectedProject,
  )
    ? selectedProject
    : projects[0]?.slug || "";
  const navigate = useCallback(
    (project: string, selector: InsightSelector | null, replace = false) => {
      const url = dashboardURL(window.location.href, project, selector);
      if (replace) window.history.replaceState({}, "", url);
      else window.history.pushState({}, "", url);
      setSelectedProject(project);
      setDetailSelector(selector);
    },
    [],
  );

  useEffect(() => {
    if (!activeProject) return;
    const current = readDashboardLocation(window.location.search);
    if (
      selectedProject !== activeProject ||
      current.project !== activeProject
    ) {
      navigate(activeProject, null, true);
    }
  }, [activeProject, navigate, selectedProject]);

  useEffect(() => {
    const handlePopState = () => {
      const location = readDashboardLocation(window.location.search);
      setSelectedProject(location.project);
      setDetailSelector(location.selector);
    };
    window.addEventListener("popstate", handlePopState);
    return () => window.removeEventListener("popstate", handlePopState);
  }, []);

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
