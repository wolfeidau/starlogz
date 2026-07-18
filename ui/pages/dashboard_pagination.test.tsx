import { afterEach, describe, expect, mock, test } from "bun:test";
import { createRouterTransport } from "@connectrpc/connect";
import { TransportProvider } from "@connectrpc/connect-query";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import {
  cleanup,
  fireEvent,
  render,
  screen,
  waitFor,
} from "@testing-library/react";
import { UIService } from "../../api/gen/proto/es/starlogz/v1/ui_pb";
import {
  LoadMoreButton,
  nextPageCursor,
  useDashboardInsightPages,
} from "./dashboard_pagination";

afterEach(cleanup);

describe("dashboard pagination", () => {
  test("continues only when the user requests another page", () => {
    const loadMore = mock(() => {});
    render(<LoadMoreButton loading={false} onLoadMore={loadMore} />);

    expect(loadMore).not.toHaveBeenCalled();
    fireEvent.click(screen.getByRole("button", { name: "Load more" }));
    expect(loadMore).toHaveBeenCalledTimes(1);
  });

  test("uses only non-empty continuation cursors", () => {
    expect(nextPageCursor({ nextCursor: "opaque" })).toBe("opaque");
    expect(nextPageCursor({ nextCursor: "" })).toBeUndefined();
  });

  test("disables continuation while the next page is loading", () => {
    const loadMore = mock(() => {});
    render(<LoadMoreButton loading={true} onLoadMore={loadMore} />);

    fireEvent.click(screen.getByRole("button", { name: "Loading" }));
    expect(loadMore).not.toHaveBeenCalled();
  });
});

function PaginationHarness({
  query,
  selectedTag,
}: {
  query: string;
  selectedTag: string;
}) {
  const pages = useDashboardInsightPages({
    isAuthenticated: true,
    activeProject: "demo",
    query,
    selectedTag,
  });
  return (
    <div>
      {pages.insights.map((insight) => (
        <span key={insight.id}>{insight.content}</span>
      ))}
      {pages.hasNextPage && (
        <LoadMoreButton
          loading={pages.isFetchingNextPage}
          onLoadMore={() => void pages.fetchNextPage()}
        />
      )}
    </div>
  );
}

describe("dashboard infinite-query integration", () => {
  test("passes the list cursor, appends pages, and resets when the tag changes", async () => {
    const requests: Array<{ cursor: string; tag: string }> = [];
    const transport = createRouterTransport((router) => {
      router.rpc(UIService.method.listInsights, (request) => {
        requests.push({ cursor: request.cursor, tag: request.tag });
        const page = request.cursor === "" ? "first" : "second";
        return {
          insights: [
            {
              id: `${request.tag}-${page}`,
              content: `${request.tag} ${page}`,
            },
          ],
          nextCursor: request.cursor === "" ? "list-next" : "",
        };
      });
    });
    const queryClient = new QueryClient({
      defaultOptions: { queries: { retry: false, gcTime: 0 } },
    });
    const view = render(
      <TransportProvider transport={transport}>
        <QueryClientProvider client={queryClient}>
          <PaginationHarness query="" selectedTag="go" />
        </QueryClientProvider>
      </TransportProvider>,
    );

    await waitFor(() => expect(screen.getByText("go first")).toBeTruthy());
    expect(requests).toEqual([{ cursor: "", tag: "go" }]);

    fireEvent.click(screen.getByRole("button", { name: "Load more" }));
    await waitFor(() => expect(screen.getByText("go second")).toBeTruthy());
    expect(screen.getByText("go first")).toBeTruthy();
    expect(requests).toEqual([
      { cursor: "", tag: "go" },
      { cursor: "list-next", tag: "go" },
    ]);

    view.rerender(
      <TransportProvider transport={transport}>
        <QueryClientProvider client={queryClient}>
          <PaginationHarness query="" selectedTag="db" />
        </QueryClientProvider>
      </TransportProvider>,
    );
    await waitFor(() => expect(screen.getByText("db first")).toBeTruthy());
    expect(requests.at(-1)).toEqual({ cursor: "", tag: "db" });
  });

  test("passes the search cursor, appends pages, and resets when the query changes", async () => {
    const requests: Array<{
      cursor: string;
      query: string;
      tags: string[];
    }> = [];
    const transport = createRouterTransport((router) => {
      router.rpc(UIService.method.searchInsights, (request) => {
        requests.push({
          cursor: request.cursor,
          query: request.query,
          tags: [...request.tags],
        });
        const page = request.cursor === "" ? "first" : "second";
        return {
          insights: [
            {
              id: `${request.query}-${page}`,
              content: `${request.query} ${page}`,
            },
          ],
          nextCursor: request.cursor === "" ? "search-next" : "",
        };
      });
    });
    const queryClient = new QueryClient({
      defaultOptions: { queries: { retry: false, gcTime: 0 } },
    });
    const view = render(
      <TransportProvider transport={transport}>
        <QueryClientProvider client={queryClient}>
          <PaginationHarness query="alpha" selectedTag="go" />
        </QueryClientProvider>
      </TransportProvider>,
    );

    await waitFor(() => expect(screen.getByText("alpha first")).toBeTruthy());
    expect(requests).toEqual([
      { cursor: "", query: "alpha", tags: ["go"] },
    ]);

    fireEvent.click(screen.getByRole("button", { name: "Load more" }));
    await waitFor(() => expect(screen.getByText("alpha second")).toBeTruthy());
    expect(screen.getByText("alpha first")).toBeTruthy();
    expect(requests.at(-1)).toEqual({
      cursor: "search-next",
      query: "alpha",
      tags: ["go"],
    });

    view.rerender(
      <TransportProvider transport={transport}>
        <QueryClientProvider client={queryClient}>
          <PaginationHarness query="beta" selectedTag="go" />
        </QueryClientProvider>
      </TransportProvider>,
    );
    await waitFor(() => expect(screen.getByText("beta first")).toBeTruthy());
    expect(requests.at(-1)).toEqual({
      cursor: "",
      query: "beta",
      tags: ["go"],
    });
  });
});
