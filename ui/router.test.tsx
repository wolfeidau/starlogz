import { afterEach, beforeEach, describe, expect, test } from "bun:test";
import { Code, ConnectError, createRouterTransport } from "@connectrpc/connect";
import { TransportProvider } from "@connectrpc/connect-query";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { createMemoryHistory, RouterProvider } from "@tanstack/react-router";
import {
  act,
  cleanup,
  fireEvent,
  render,
  screen,
  waitFor,
  within,
} from "@testing-library/react";
import { UIService } from "../api/gen/proto/es/starlogz/v1/ui_pb";
import { createAppRouter } from "./router";

beforeEach(() => window.localStorage.clear());
afterEach(cleanup);

type Call = Record<string, unknown>;

function setup({
  entries,
  projects = ["alpha", "beta"],
  isOperator = false,
  sessionError = false,
}: {
  entries: string[];
  projects?: string[];
  isOperator?: boolean;
  sessionError?: boolean;
}) {
  const calls = {
    getInsight: [] as Call[],
    listInsights: [] as Call[],
    searchInsights: [] as Call[],
  };
  const insight = {
    id: "id-1",
    key: "source",
    category: "fact",
    source: "agent",
    renderedHtml:
      '<p><a class="insight-link" href="/dashboard/alpha/insights/target" data-starlogz-action="open-insight" data-insight-key="target">target</a></p>',
  };
  const transport = createRouterTransport((router) => {
    router.rpc(UIService.method.getSession, () => {
      if (sessionError) throw new ConnectError("no", Code.Unauthenticated);
      return { login: "u", displayName: "User", isOperator, webSessionId: "w" };
    });
    router.rpc(UIService.method.listProjects, () => ({
      projects: projects.map((slug) => ({ id: slug, slug, name: slug })),
    }));
    router.rpc(UIService.method.listTags, () => ({
      tags: [{ name: "auth", count: 1 }],
    }));
    router.rpc(UIService.method.listInsights, (req) => {
      calls.listInsights.push({ ...req });
      return { insights: [insight] };
    });
    router.rpc(UIService.method.searchInsights, (req) => {
      calls.searchInsights.push({ ...req });
      return { insights: [insight] };
    });
    router.rpc(UIService.method.getInsight, (req) => {
      calls.getInsight.push({ ...req });
      return { insight: { ...insight, key: "source" } };
    });
    router.rpc(UIService.method.listInsightHistory, () => ({ revisions: [] }));
    router.rpc(UIService.method.getProjectDashboard, () => ({
      totalInsights: 7,
    }));
    router.rpc(UIService.method.getOperationsOverview, () => ({}));
    router.rpc(UIService.method.getOperationsTelemetry, () => ({}));
  });
  const queryClient = new QueryClient({
    defaultOptions: { queries: { retry: false, gcTime: 0 } },
  });
  const router = createAppRouter({
    queryClient,
    history: createMemoryHistory({ initialEntries: entries }),
  });
  render(
    <TransportProvider transport={transport}>
      <QueryClientProvider client={queryClient}>
        <RouterProvider router={router} />
      </QueryClientProvider>
    </TransportProvider>,
  );
  return { router, calls };
}

const pathOf = (router: { state: { location: { href: string } } }) =>
  router.state.location.href;

describe("deep links", () => {
  test("key route renders the list and opens the dialog for the key", async () => {
    const { calls } = setup({ entries: ["/dashboard/alpha/insights/source"] });
    expect(await screen.findByRole("dialog", { name: "source" })).toBeTruthy();
    expect(screen.getByRole("heading", { name: "Insights" })).toBeTruthy();
    expect(calls.getInsight[0]).toMatchObject({
      project: "alpha",
      selector: { case: "key", value: "source" },
    });
  });

  test("by-id route uses the id selector", async () => {
    const { calls } = setup({
      entries: ["/dashboard/alpha/insights/by-id/019f-abc"],
    });
    await screen.findByRole("dialog");
    expect(calls.getInsight[0]).toMatchObject({
      selector: { case: "id", value: "019f-abc" },
    });
  });

  test("keys with slash and space round-trip through the URL", async () => {
    const { router, calls } = setup({
      entries: ["/dashboard/alpha/insights/a%2Fb%20c"],
    });
    await screen.findByRole("dialog");
    expect(calls.getInsight[0]).toMatchObject({
      selector: { case: "key", value: "a/b c" },
    });

    await act(() =>
      router.navigate({
        to: "/dashboard/$project/insights/$key",
        params: { project: "alpha", key: "x/y?z#w" },
      }),
    );
    await waitFor(() =>
      expect(calls.getInsight.at(-1)).toMatchObject({
        selector: { case: "key", value: "x/y?z#w" },
      }),
    );
    expect(router.state.location.pathname).toBe(
      "/dashboard/alpha/insights/x%2Fy%3Fz%23w",
    );
  });

  test("overview route renders summary tiles", async () => {
    setup({ entries: ["/dashboard/alpha/overview"] });
    expect(await screen.findByText("Total insights")).toBeTruthy();
    expect(screen.queryByText("No matching insights.")).toBeNull();
  });

  test("unknown paths render the not-found view", async () => {
    setup({ entries: ["/dashboard/alpha/nope/nope"] });
    expect(await screen.findByText("Page not found.")).toBeTruthy();
  });
});

describe("dashboard redirects", () => {
  test("legacy key URLs redirect to the path route", async () => {
    const { router, calls } = setup({
      entries: ["/dashboard?project=alpha&insight_key=source"],
    });
    await screen.findByRole("dialog");
    expect(pathOf(router)).toBe("/dashboard/alpha/insights/source");
    expect(calls.getInsight[0]).toMatchObject({
      selector: { case: "key", value: "source" },
    });
  });

  test("legacy id and project-only URLs redirect", async () => {
    const first = setup({
      entries: ["/dashboard?project=alpha&insight_id=019f-abc"],
    });
    await screen.findByRole("dialog");
    expect(pathOf(first.router)).toBe(
      "/dashboard/alpha/insights/by-id/019f-abc",
    );
    cleanup();

    const second = setup({ entries: ["/dashboard?project=beta"] });
    await screen.findByRole("heading", { name: "Insights" });
    expect(pathOf(second.router)).toBe("/dashboard/beta");
  });

  test("uses the stored project when it exists", async () => {
    window.localStorage.setItem("starlogz.lastProject", "beta");
    const { router } = setup({ entries: ["/dashboard"] });
    await waitFor(() => expect(pathOf(router)).toBe("/dashboard/beta"));
  });

  test("falls back to the first project when the stored one is gone", async () => {
    window.localStorage.setItem("starlogz.lastProject", "gone");
    const { router } = setup({ entries: ["/dashboard"] });
    await waitFor(() => expect(pathOf(router)).toBe("/dashboard/alpha"));
  });

  test("shows the empty state without projects", async () => {
    setup({ entries: ["/dashboard"], projects: [] });
    expect(await screen.findByText("No projects yet.")).toBeTruthy();
  });

  test("unknown projects redirect to the default project", async () => {
    const { router } = setup({ entries: ["/dashboard/missing"] });
    await waitFor(() => expect(pathOf(router)).toBe("/dashboard/alpha"));
  });

  test("records the active project", async () => {
    setup({ entries: ["/dashboard/beta"] });
    await screen.findByRole("heading", { name: "Insights" });
    await waitFor(() =>
      expect(window.localStorage.getItem("starlogz.lastProject")).toBe("beta"),
    );
  });

  test("unauthenticated sessions render the login view", async () => {
    setup({ entries: ["/dashboard/alpha"], sessionError: true });
    expect(await screen.findByText("Sign in with GitHub")).toBeTruthy();
  });
});

describe("search state", () => {
  test("q and tag drive list and search requests", async () => {
    const { calls } = setup({
      entries: ["/dashboard/alpha?tag=auth"],
    });
    await screen.findByRole("heading", { name: "Insights" });
    await waitFor(() =>
      expect(calls.listInsights.at(-1)).toMatchObject({ tag: "auth" }),
    );

    cleanup();
    const searched = setup({ entries: ["/dashboard/alpha?q=123&tag=auth"] });
    await screen.findByText("Search Results");
    expect(searched.calls.searchInsights.at(-1)).toMatchObject({
      query: "123",
      tags: ["auth"],
    });
  });

  test("typing updates q without adding history entries", async () => {
    const { router, calls } = setup({ entries: ["/dashboard/alpha"] });
    const input = await screen.findByLabelText("Search insights");
    const before = router.history.length;
    fireEvent.change(input, { target: { value: "hello" } });
    await waitFor(() =>
      expect(calls.searchInsights.at(-1)).toMatchObject({ query: "hello" }),
    );
    expect(pathOf(router)).toBe("/dashboard/alpha?q=hello");
    expect(router.history.length).toBe(before);

    fireEvent.change(input, { target: { value: "" } });
    await waitFor(() => expect(pathOf(router)).toBe("/dashboard/alpha"));
  });

  test("opening, closing, and going back preserve q and tag", async () => {
    const { router } = setup({ entries: ["/dashboard/alpha?q=hi&tag=auth"] });
    await screen.findByText("Search Results");

    await act(() =>
      router.navigate({
        to: "/dashboard/$project/insights/$key",
        params: { project: "alpha", key: "source" },
        search: { q: "hi", tag: "auth" },
      }),
    );
    expect(pathOf(router)).toBe(
      "/dashboard/alpha/insights/source?q=hi&tag=auth",
    );
    await screen.findByRole("dialog");

    await act(async () => router.history.back());
    await waitFor(() =>
      expect(pathOf(router)).toBe("/dashboard/alpha?q=hi&tag=auth"),
    );
    await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());

    const dialogTarget = await screen.findByRole("link", { name: "target" });
    fireEvent.click(dialogTarget, { button: 0, detail: 1 });
    await screen.findByRole("dialog");
    fireEvent.click(
      screen.getByRole("button", { name: "Close insight detail" }),
    );
    await waitFor(() =>
      expect(pathOf(router)).toBe("/dashboard/alpha?q=hi&tag=auth"),
    );
  });

  test("selecting a project drops search state", async () => {
    const { router } = setup({ entries: ["/dashboard/alpha?q=hi"] });
    const select = await screen.findByLabelText("Project");
    fireEvent.change(select, { target: { value: "beta" } });
    await waitFor(() => expect(pathOf(router)).toBe("/dashboard/beta"));
  });
});

describe("insight links", () => {
  test("rendered links open the key route and keep search", async () => {
    const { router } = setup({ entries: ["/dashboard/alpha?tag=auth"] });
    const link = await screen.findByRole("link", { name: "target" });
    fireEvent.click(link, { button: 0, detail: 1, ctrlKey: true });
    expect(pathOf(router)).toBe("/dashboard/alpha?tag=auth");
    fireEvent.click(link, { button: 0, detail: 1, metaKey: true });
    expect(pathOf(router)).toBe("/dashboard/alpha?tag=auth");

    fireEvent.click(link, { button: 0, detail: 1 });
    await waitFor(() =>
      expect(pathOf(router)).toBe("/dashboard/alpha/insights/target?tag=auth"),
    );
  });

  test("detail relationship links are router links", async () => {
    const { router } = setup({ entries: ["/dashboard/alpha/insights/source"] });
    const dialog = await screen.findByRole("dialog");
    const link = within(dialog).getByRole("link", { name: "target" });
    expect(link.getAttribute("href")).toBe("/dashboard/alpha/insights/target");
    expect(pathOf(router)).toBe("/dashboard/alpha/insights/source");
  });
});

describe("operations", () => {
  test("non-operators see the access-required panel", async () => {
    setup({ entries: ["/admin/operations"] });
    expect(
      await screen.findByText("Service-operator access is required."),
    ).toBeTruthy();
  });

  test("operators see the operations view", async () => {
    setup({ entries: ["/admin/operations"], isOperator: true });
    expect(
      await screen.findByRole("link", { name: "Operations" }),
    ).toBeTruthy();
    expect(
      screen.queryByText("Service-operator access is required."),
    ).toBeNull();
  });
});
