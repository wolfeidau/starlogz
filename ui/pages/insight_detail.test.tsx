import { create } from "@bufbuild/protobuf";
import { Code, ConnectError } from "@connectrpc/connect";
import { afterEach, describe, expect, mock, test } from "bun:test";
import {
  cleanup,
  fireEvent,
  render,
  screen,
  within,
} from "@testing-library/react";
import { GetInsightResponseSchema } from "../../api/gen/proto/es/starlogz/v1/ui_pb";
import { InsightDetail } from "./insight_detail";

afterEach(cleanup);

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
  test("distinguishes not-found and generic request errors", () => {
    const props = detailProps({
      detail: undefined,
      error: new ConnectError("missing", Code.NotFound),
    });
    const view = render(<InsightDetail {...props} />);
    expect(
      screen.getByRole("heading", { name: "Insight not found" }),
    ).toBeTruthy();
    expect(screen.getByText(/missing, deleted, or unavailable/)).toBeTruthy();

    view.rerender(
      <InsightDetail {...props} error={new Error("network failure")} />,
    );
    expect(
      screen.getByRole("heading", { name: "Unable to load insight" }),
    ).toBeTruthy();
    expect(screen.getByText(/request failed/)).toBeTruthy();
  });

  test("renders relationships and routes resolved traversal", () => {
    const onNavigate = mock(() => {});
    render(<InsightDetail {...detailProps({ onNavigate })} />);
    const dialog = screen.getByRole("dialog", { name: "source" });

    expect(within(dialog).getByText("Unresolved")).toBeTruthy();
    expect(within(dialog).getByText("Showing the first 2 of 3.")).toBeTruthy();
    expect(within(dialog).queryByRole("link", { name: "missing" })).toBeNull();

    fireEvent.click(within(dialog).getByRole("link", { name: "target" }), {
      button: 0,
      detail: 1,
    });
    expect(onNavigate).toHaveBeenLastCalledWith({
      case: "key",
      value: "target",
    });

    fireEvent.click(within(dialog).getByRole("link", { name: "backlink-id" }), {
      button: 0,
      detail: 1,
    });
    expect(onNavigate).toHaveBeenLastCalledWith({
      case: "id",
      value: "backlink-id",
    });

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
  test("moves and traps focus, closes on Escape, and restores focus", () => {
    const opener = document.createElement("button");
    opener.textContent = "Open insight";
    document.body.append(opener);
    opener.focus();
    const onClose = mock(() => {});
    const view = render(<InsightDetail {...detailProps({ onClose })} />);
    const close = screen.getByRole("button", { name: "Close insight detail" });
    const lastLink = screen.getByRole("link", { name: "backlink-id" });

    expect(document.activeElement).toBe(close);
    fireEvent.keyDown(close, { key: "Tab", shiftKey: true });
    expect(document.activeElement).toBe(lastLink);
    fireEvent.keyDown(lastLink, { key: "Tab" });
    expect(document.activeElement).toBe(close);

    fireEvent.keyDown(close, { key: "Escape" });
    expect(onClose).toHaveBeenCalledTimes(1);

    view.unmount();
    expect(document.activeElement).toBe(opener);
    opener.remove();
  });

  test("closes only when the backdrop itself is pressed", () => {
    const onClose = mock(() => {});
    const { container } = render(
      <InsightDetail {...detailProps({ onClose })} />,
    );
    const backdrop = container.querySelector<HTMLElement>(".detail-backdrop");
    const panel = screen.getByRole("dialog", { name: "source" });

    if (!backdrop) throw new Error("detail backdrop missing");
    fireEvent.mouseDown(panel);
    expect(onClose).not.toHaveBeenCalled();
    fireEvent.mouseDown(backdrop, { button: 2 });
    expect(onClose).not.toHaveBeenCalled();
    fireEvent.mouseDown(backdrop);
    expect(onClose).toHaveBeenCalledTimes(1);
  });
});
