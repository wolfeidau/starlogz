import type { QueryClient } from "@tanstack/react-query";
import {
  type RouterHistory,
  createRootRouteWithContext,
  createRoute,
  createRouter,
  lazyRouteComponent,
  redirect,
} from "@tanstack/react-router";
import { InsightDetailRoute } from "./routes/insight_detail/detail_route";
import { DashboardRedirect } from "./routes/insights/dashboard_redirect";
import { InsightsRoute } from "./routes/insights/insights_route";
import { ProjectLayout } from "./routes/insights/project_layout";
import { OverviewRoute } from "./routes/overview/overview_route";
import { AppShell } from "./shell/app_shell";
import { NotFound } from "./shell/not_found";

const rootRoute = createRootRouteWithContext<{ queryClient: QueryClient }>()({
  component: AppShell,
  notFoundComponent: NotFound,
});

function optionalString(value: unknown): string | undefined {
  return typeof value === "string" && value !== "" ? value : undefined;
}

const dashboardIndexRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: "/dashboard",
  validateSearch: (
    search: Record<string, unknown>,
  ): { project?: string; insight_key?: string; insight_id?: string } => ({
    project: optionalString(search.project),
    insight_key: optionalString(search.insight_key),
    insight_id: optionalString(search.insight_id),
  }),
  beforeLoad: ({ search }) => {
    const { project, insight_key: key, insight_id: id } = search;
    if (!project) return;
    if (key) {
      throw redirect({
        to: "/dashboard/$project/insights/$key",
        params: { project, key },
        replace: true,
      });
    }
    if (id) {
      throw redirect({
        to: "/dashboard/$project/insights/by-id/$id",
        params: { project, id },
        replace: true,
      });
    }
    throw redirect({
      to: "/dashboard/$project",
      params: { project },
      replace: true,
    });
  },
  component: DashboardRedirect,
});

const projectRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: "/dashboard/$project",
  component: ProjectLayout,
});

// Pathless layout: keeps the list mounted beneath detail children (a "/" index route cannot have children).
const listRoute = createRoute({
  getParentRoute: () => projectRoute,
  id: "list",
  validateSearch: (
    search: Record<string, unknown>,
  ): { q?: string; tag?: string } => ({
    q: optionalString(search.q),
    tag: optionalString(search.tag),
  }),
  component: function ListPage() {
    const { project } = listRoute.useParams();
    const { q, tag } = listRoute.useSearch();
    return <InsightsRoute project={project} q={q} tag={tag} />;
  },
});

const listIndexRoute = createRoute({
  getParentRoute: () => listRoute,
  path: "/",
});

const insightKeyRoute = createRoute({
  getParentRoute: () => listRoute,
  path: "insights/$key",
  component: function InsightKeyPage() {
    const { project, key } = insightKeyRoute.useParams();
    return (
      <InsightDetailRoute
        project={project}
        selector={{ case: "key", value: key }}
      />
    );
  },
});

const insightIdRoute = createRoute({
  getParentRoute: () => listRoute,
  path: "insights/by-id/$id",
  component: function InsightIdPage() {
    const { project, id } = insightIdRoute.useParams();
    return (
      <InsightDetailRoute
        project={project}
        selector={{ case: "id", value: id }}
      />
    );
  },
});

const overviewRoute = createRoute({
  getParentRoute: () => projectRoute,
  path: "overview",
  component: function OverviewPage() {
    const { project } = overviewRoute.useParams();
    return <OverviewRoute project={project} />;
  },
});

const operationsRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: "/admin/operations",
  component: lazyRouteComponent(
    () => import("./routes/operations/operations_route"),
    "OperationsRoute",
  ),
});

const routeTree = rootRoute.addChildren([
  dashboardIndexRoute,
  projectRoute.addChildren([
    listRoute.addChildren([listIndexRoute, insightKeyRoute, insightIdRoute]),
    overviewRoute,
  ]),
  operationsRoute,
]);

// Search text such as "123" must stay a string, so skip the default JSON/number coercion.
function parseSearch(search: string): Record<string, string> {
  return Object.fromEntries(new URLSearchParams(search));
}

function stringifySearch(search: Record<string, unknown>): string {
  const params = new URLSearchParams();
  for (const [key, value] of Object.entries(search)) {
    if (value !== undefined && value !== null) params.set(key, String(value));
  }
  const text = params.toString();
  return text ? `?${text}` : "";
}

export function createAppRouter({
  queryClient,
  history,
}: {
  queryClient: QueryClient;
  history?: RouterHistory;
}) {
  return createRouter({
    routeTree,
    history,
    context: { queryClient },
    parseSearch,
    stringifySearch,
  });
}

declare module "@tanstack/react-router" {
  interface Register {
    router: ReturnType<typeof createAppRouter>;
  }
}
