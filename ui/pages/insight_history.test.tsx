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
import { InsightHistory } from "./insight_history";

afterEach(cleanup);

function renderHistory(
  handler: Parameters<typeof createRouterTransport>[0],
  onOpenInsight = mock(() => {}),
) {
  const queryClient = new QueryClient({
    defaultOptions: { queries: { retry: false, gcTime: 0 } },
  });
  const transport = createRouterTransport(handler);
  return {
    onOpenInsight,
    ...render(
      <TransportProvider transport={transport}>
        <QueryClientProvider client={queryClient}>
          <InsightHistory
            project="starlogz"
            insightId="insight-id"
            onOpenInsight={onOpenInsight}
          />
        </QueryClientProvider>
      </TransportProvider>,
    ),
  };
}

describe("insight history", () => {
  test("renders selected server HTML and explicitly continues pagination", async () => {
    const requests: string[] = [];
    const onOpenInsight = mock(() => {});
    renderHistory((router) => {
      router.rpc(UIService.method.listInsightHistory, (request) => {
        requests.push(request.cursor);
        if (request.cursor) {
          return {
            insightId: request.id,
            currentRevision: 3,
            revisions: [
              {
                revision: 1,
                operation: "create",
                content: "RAW VERSION ONE",
                category: "fact",
                source: "repo",
                renderedHtml: "<p>Version one</p>",
              },
            ],
          };
        }
        return {
          insightId: request.id,
          currentRevision: 3,
          revisions: [
            {
              revision: 3,
              operation: "restore",
              content: "RAW CURRENT CONTENT",
              tags: ["history"],
              category: "fact",
              source: "repo",
              changedBy: "actor-id",
              renderedHtml:
                '<p>Current <a data-starlogz-action="open-insight" data-insight-key="target" href="?insight_key=target">target</a></p>',
            },
            {
              revision: 2,
              operation: "delete",
              content: "RAW VERSION TWO",
              category: "fact",
              source: "repo",
              deletedAt: { seconds: 1n, nanos: 0 },
              renderedHtml: "<p>Version two</p>",
            },
          ],
          nextCursor: "history-next",
        };
      });
    }, onOpenInsight);

    await waitFor(() =>
      expect(
        screen.getByText(
          (_, element) =>
            element?.tagName === "P" &&
            element.textContent === "Current target",
        ),
      ).toBeTruthy(),
    );
    expect(requests).toEqual([""]);
    expect(screen.queryByText("RAW CURRENT CONTENT")).toBeNull();
    expect(
      screen.queryByRole("button", { name: "Restore revision" }),
    ).toBeNull();
    expect(screen.getByText("Actor actor-id")).toBeTruthy();

    fireEvent.click(screen.getByRole("button", { name: /Revision 2/ }));
    expect(screen.getByText("Version two")).toBeTruthy();
    expect(
      screen.queryByText(
        (_, element) =>
          element?.tagName === "P" && element.textContent === "Current target",
      ),
    ).toBeNull();

    fireEvent.click(screen.getByRole("button", { name: "Load more" }));
    await waitFor(() =>
      expect(screen.getByRole("button", { name: /Revision 1/ })).toBeTruthy(),
    );
    expect(requests).toEqual(["", "history-next"]);

    fireEvent.click(screen.getByRole("button", { name: /Revision 3/ }));
    fireEvent.click(screen.getByRole("link", { name: "target" }), {
      button: 0,
      detail: 1,
    });
    expect(onOpenInsight).toHaveBeenCalledWith("target");
  });

  test("shows a bounded error state", async () => {
    renderHistory((router) => {
      router.rpc(UIService.method.listInsightHistory, () => {
        throw new Error("history failed with private content");
      });
    });

    await waitFor(() =>
      expect(
        screen.getByText("History is unavailable. Try again later."),
      ).toBeTruthy(),
    );
    expect(screen.queryByText(/private content/)).toBeNull();
  });
});
