import { afterEach, describe, expect, mock, test } from "bun:test";
import { cleanup, render, screen } from "@testing-library/react";
import { Dialog } from "./dialog";

afterEach(cleanup);

function TestDialog({
  onClose,
  label,
  returnFocusFallback,
}: {
  onClose: () => void;
  label: string;
  returnFocusFallback?: () => HTMLElement | null;
}) {
  return (
    <Dialog
      labelledBy="test-title"
      onClose={onClose}
      returnFocusFallback={returnFocusFallback}
    >
      <h2 id="test-title">Test</h2>
      <button className="detail-close" type="button">
        Close
      </button>
      <a href="/next">{label}</a>
    </Dialog>
  );
}

describe("dialog", () => {
  test("cancel still closes after the focused element is replaced", () => {
    const onClose = mock(() => {});
    const view = render(<TestDialog onClose={onClose} label="first" />);
    const dialog = screen.getByRole("dialog", { name: "Test" });

    (document.activeElement as HTMLElement | null)?.blur();
    view.rerender(<TestDialog onClose={onClose} label="second" />);
    expect(document.activeElement).toBe(document.body);

    dialog.dispatchEvent(new Event("cancel", { cancelable: true }));
    expect(onClose).toHaveBeenCalledTimes(1);
  });

  test("returns focus to a connected opener on unmount", () => {
    const opener = document.createElement("button");
    document.body.append(opener);
    opener.focus();
    const view = render(<TestDialog onClose={() => {}} label="first" />);

    view.unmount();
    expect(document.activeElement).toBe(opener);
    opener.remove();
  });

  test("falls back when the opener was removed", () => {
    const opener = document.createElement("button");
    const fallback = document.createElement("button");
    document.body.append(opener, fallback);
    opener.focus();
    const view = render(
      <TestDialog
        onClose={() => {}}
        label="first"
        returnFocusFallback={() => fallback}
      />,
    );

    opener.remove();
    view.unmount();
    expect(document.activeElement).toBe(fallback);
    fallback.remove();
  });
});
