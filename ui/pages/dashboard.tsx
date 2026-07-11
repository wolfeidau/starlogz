import { createConnectTransport } from "@connectrpc/connect-web";
import { TransportProvider, useQuery } from "@connectrpc/connect-query";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { Fragment, useEffect, useMemo, useState, type ReactNode } from "react";
import { createRoot } from "react-dom/client";
import type { Timestamp } from "@bufbuild/protobuf/wkt";
import {
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

function InlineMarkdown({ text }: { text: string }) {
  const parts = text.split(/(`[^`]+`|\*\*[^*]+\*\*|\[[^\]]+\]\([^)]+\))/g);
  let offset = 0;
  const keyedParts = parts.map((part) => {
    const start = text.indexOf(part, offset);
    offset = start + part.length;
    return { key: `${start}:${part}`, part };
  });
  return (
    <>
      {keyedParts.map(({ key, part }) => {
        if (part.startsWith("`") && part.endsWith("`")) {
          return <code key={key}>{part.slice(1, -1)}</code>;
        }
        if (part.startsWith("**") && part.endsWith("**")) {
          return <strong key={key}>{part.slice(2, -2)}</strong>;
        }
        const link = part.match(/^\[([^\]]+)\]\(([^)]+)\)$/);
        if (link) {
          return (
            <a key={key} href={link[2]} rel="noreferrer" target="_blank">
              {link[1]}
            </a>
          );
        }
        return <Fragment key={key}>{part}</Fragment>;
      })}
    </>
  );
}

function MarkdownPreview({ content }: { content: string }) {
  const blocks: ReactNode[] = [];
  const lines = content.replace(/\r\n/g, "\n").split("\n");
  let i = 0;

  while (i < lines.length) {
    const line = lines[i];
    if (line.trim() === "") {
      i += 1;
      continue;
    }

    if (line.startsWith("```")) {
      const codeLines: string[] = [];
      i += 1;
      while (i < lines.length && !lines[i].startsWith("```")) {
        codeLines.push(lines[i]);
        i += 1;
      }
      i += lines[i]?.startsWith("```") ? 1 : 0;
      blocks.push(
        <pre key={blocks.length}>
          <code>{codeLines.join("\n")}</code>
        </pre>,
      );
      continue;
    }

    const heading = line.match(/^(#{1,3})\s+(.+)$/);
    if (heading) {
      const level = heading[1].length;
      const Tag = `h${level + 2}` as "h3" | "h4" | "h5";
      blocks.push(
        <Tag key={blocks.length}>
          <InlineMarkdown text={heading[2]} />
        </Tag>,
      );
      i += 1;
      continue;
    }

    if (line.startsWith("> ")) {
      const quoteLines: string[] = [];
      while (i < lines.length && lines[i].startsWith("> ")) {
        quoteLines.push(lines[i].slice(2));
        i += 1;
      }
      blocks.push(
        <blockquote key={blocks.length}>
          <InlineMarkdown text={quoteLines.join(" ")} />
        </blockquote>,
      );
      continue;
    }

    if (/^\s*[-*]\s+/.test(line)) {
      const items: Array<{ key: string; text: string }> = [];
      while (i < lines.length && /^\s*[-*]\s+/.test(lines[i])) {
        items.push({
          key: `line-${i}`,
          text: lines[i].replace(/^\s*[-*]\s+/, ""),
        });
        i += 1;
      }
      blocks.push(
        <ul key={blocks.length}>
          {items.map((item) => (
            <li key={item.key}>
              <InlineMarkdown text={item.text} />
            </li>
          ))}
        </ul>,
      );
      continue;
    }

    if (/^\s*\d+\.\s+/.test(line)) {
      const items: Array<{ key: string; text: string }> = [];
      while (i < lines.length && /^\s*\d+\.\s+/.test(lines[i])) {
        items.push({
          key: `line-${i}`,
          text: lines[i].replace(/^\s*\d+\.\s+/, ""),
        });
        i += 1;
      }
      blocks.push(
        <ol key={blocks.length}>
          {items.map((item) => (
            <li key={item.key}>
              <InlineMarkdown text={item.text} />
            </li>
          ))}
        </ol>,
      );
      continue;
    }

    const paragraph: string[] = [];
    while (
      i < lines.length &&
      lines[i].trim() !== "" &&
      !lines[i].startsWith("```") &&
      !/^(#{1,3})\s+/.test(lines[i]) &&
      !lines[i].startsWith("> ") &&
      !/^\s*[-*]\s+/.test(lines[i]) &&
      !/^\s*\d+\.\s+/.test(lines[i])
    ) {
      paragraph.push(lines[i]);
      i += 1;
    }
    blocks.push(
      <p key={blocks.length}>
        <InlineMarkdown text={paragraph.join(" ")} />
      </p>,
    );
  }

  return <div className="markdown-preview">{blocks}</div>;
}

function InsightTable({ insights }: { insights: Insight[] }) {
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
                  <MarkdownPreview content={insight.content} />
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
    <select value={selected} onChange={(event) => onSelect(event.target.value)}>
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
  const [selectedProject, setSelectedProject] = useState("");
  const [query, setQuery] = useState("");
  const [selectedTag, setSelectedTag] = useState("");

  const activeProject = selectedProject || projects[0]?.slug || "";
  useEffect(() => {
    if (!selectedProject && activeProject) {
      setSelectedProject(activeProject);
    }
  }, [activeProject, selectedProject]);

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
            setSelectedProject(slug);
            setSelectedTag("");
            setQuery("");
          }}
        />
        <input
          value={query}
          onChange={(event) => setQuery(event.target.value)}
          placeholder="Search insights"
        />
        <select
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
          <InsightTable insights={insights} />
        )}
      </section>
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
