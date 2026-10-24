import { afterEach, describe, expect, mock, test } from "bun:test";
import { create } from "@bufbuild/protobuf";
import { Code, ConnectError, createRouterTransport } from "@connectrpc/connect";
import { TransportProvider } from "@connectrpc/connect-query";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import {
  createMemoryHistory,
  createRootRoute,
  createRouter,
  RouterProvider,
} from "@tanstack/react-router";
import {
  cleanup,
  fireEvent,
  render,
  screen,
  within,
} from "@testing-library/react";
import type { ReactNode } from "react";
import {
  GetInsightResponseSchema,
  UIService,
} from "../../../api/gen/proto/es/starlogz/v1/ui_pb";
import { InsightDetail } from "./insight_detail";

afterEach(cleanup);

async function renderDetail(ui: ReactNode) {
  const queryClient = new QueryClient({
    defaultOptions: { queries: { retry: false, gcTime: 0 } },
  });
  const transport = createRouterTransport((router) => {
    router.rpc(UIService.method.listInsightHistory, () => ({ revisions: [] }));
  });
  // Links only need a router context; the real route tree is covered in router.test.tsx.
  const router = createRouter({
    routeTree: createRootRoute({ component: () => ui }),
    history: createMemoryHistory({ initialEntries: ["/"] }),
  });
  await router.load();
  return render(<RouterProvider router={router as never} />, {
    wrapper: ({ children }) => (
      <TransportProvider transport={transport}>
        <QueryClientProvider client={queryClient}>
          {children}
        </QueryClientProvider>
      </TransportProvider>
    ),
  });
}

function insightDetail() {
  return create(GetInsightResponseSchema, {
    insight: {
      id: "source-id",
      key: "source",
      content: "raw content",
      tags: ["testing"],
      category: "context",
      source: "agent",
      renderedHtml:
        '<p>Rendered <a class="insight-link" href="?project=starlogz&amp;insight_key=rendered-target" data-starlogz-action="open-insight" data-insight-key="rendered-target">rendered target</a>.</p>',
    },
    links: [
      {
        targetKey: "target",
        resolved: true,
        id: "target-id",
        category: "fact",
      },
      { targetKey: "missing", resolved: false },
    ],
    backlinks: [
      {
        resolved: true,
        id: "backlink-id",
        category: "context",
      },
    ],
    linkCount: 3,
    backlinkCount: 1,
    linksTruncated: true,
  });
}

function detailProps(overrides: Record<string, unknown> = {}) {
  return {
    project: "starlogz",
    selector: { case: "key" as const, value: "source" },
    detail: insightDetail(),
    error: null,
    loading: false,
    onClose: mock(() => {}),
    onNavigate: mock(() => {}),
    ...overrides,
  };
}

describe("insight detail content", () => {
  test("distinguishes not-found and generic request errors", async () => {
    const props = detailProps({
      detail: undefined,
      error: new ConnectError("missing", Code.NotFound),
    });
    const view = await renderDetail(<InsightDetail {...props} />);
    expect(
      await screen.findByRole("heading", { name: "Insight not found" }),
    ).toBeTruthy();
    expect(screen.getByText(/missing, deleted, or unavailable/)).toBeTruthy();
    view.unmount();

    await renderDetail(
      <InsightDetail {...props} error={new Error("network failure")} />,
    );
    expect(
      await screen.findByRole("heading", { name: "Unable to load insight" }),
    ).toBeTruthy();
    expect(screen.getByText(/request failed/)).toBeTruthy();
  });

  test("renders relationship links as router links and routes rendered links", async () => {
    const onNavigate = mock(() => {});
    await renderDetail(<InsightDetail {...detailProps({ onNavigate })} />);
    const dialog = await screen.findByRole("dialog", { name: "source" });

    expect(within(dialog).getByText("Unresolved")).toBeTruthy();
    expect(within(dialog).getByText("Showing the first 2 of 3.")).toBeTruthy();
    expect(within(dialog).queryByRole("link", { name: "missing" })).toBeNull();

    expect(
      within(dialog).getByRole("link", { name: "target" }).getAttribute("href"),
    ).toBe("/dashboard/starlogz/insights/target");
    expect(
      within(dialog)
        .getByRole("link", { name: "backlink-id" })
        .getAttribute("href"),
    ).toBe("/dashboard/starlogz/insights/by-id/backlink-id");

    fireEvent.click(
      within(dialog).getByRole("link", { name: "rendered target" }),
      { button: 0, detail: 1 },
    );
    expect(onNavigate).toHaveBeenLastCalledWith({
      case: "key",
      value: "rendered-target",
    });
  });
});

describe("insight detail modal behavior", () => {
  test("opens modally, closes on cancel, and restores focus", async () => {
    const opener = document.createElement("button");
    opener.textContent = "Open insight";
    document.body.append(opener);
    opener.focus();
    const onClose = mock(() => {});
    const view = await renderDetail(
      <InsightDetail {...detailProps({ onClose })} />,
    );
    const dialog = await screen.findByRole("dialog", { name: "source" });
    const close = screen.getByRole("button", { name: "Close insight detail" });

    expect((dialog as HTMLDialogElement).open).toBe(true);
    expect(document.activeElement).toBe(close);
    expect(document.body.style.overflow).toBe("hidden");

    const cancel = new Event("cancel", { cancelable: true });
    dialog.dispatchEvent(cancel);
    expect(cancel.defaultPrevented).toBe(true);
    expect(onClose).toHaveBeenCalledTimes(1);

    view.unmount();
    expect(document.body.style.overflow).toBe("");
    expect(document.activeElement).toBe(opener);
    opener.remove();
  });

  test("closes only when the dialog backdrop itself is pressed", async () => {
    const onClose = mock(() => {});
    await renderDetail(<InsightDetail {...detailProps({ onClose })} />);
    const dialog = await screen.findByRole("dialog", { name: "source" });
    const content = dialog.querySelector<HTMLElement>(".detail-content");

    if (!content) throw new Error("detail content missing");
    fireEvent.mouseDown(content);
    expect(onClose).not.toHaveBeenCalled();
    fireEvent.mouseDown(dialog, { button: 2 });
    expect(onClose).not.toHaveBeenCalled();
    fireEvent.mouseDown(dialog);
    expect(onClose).toHaveBeenCalledTimes(1);
  });
});
