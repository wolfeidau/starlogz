import { describe, expect, test } from "bun:test";
import { openInsightKeyForClick } from "./insight_navigation";

describe("delegated insight actions", () => {
  const event = {
    altKey: false,
    button: 0,
    ctrlKey: false,
    defaultPrevented: false,
    detail: 1,
    metaKey: false,
    shiftKey: false,
  };

  function target(attributes: Record<string, string>) {
    const anchor = {
      getAttribute(name: string) {
        return attributes[name] ?? null;
      },
      hasAttribute(name: string) {
        return name in attributes;
      },
    };
    return { closest: () => anchor };
  }

  test("handles open-insight from a nested click target", () => {
    expect(
      openInsightKeyForClick(
        event,
        target({
          "data-starlogz-action": "open-insight",
          "data-insight-key": "project-workflow",
        }),
      ),
    ).toBe("project-workflow");
  });

  test("retains modified, auxiliary, download, and targeted navigation", () => {
    const anchor = target({
      "data-starlogz-action": "open-insight",
      "data-insight-key": "target",
    });
    for (const changed of [
      { ctrlKey: true },
      { metaKey: true },
      { shiftKey: true },
      { altKey: true },
      { button: 1 },
      { defaultPrevented: true },
      { detail: 0 },
    ]) {
      expect(
        openInsightKeyForClick({ ...event, ...changed }, anchor),
      ).toBeNull();
    }
    expect(
      openInsightKeyForClick(
        event,
        target({
          "data-starlogz-action": "open-insight",
          "data-insight-key": "target",
          download: "",
        }),
      ),
    ).toBeNull();
    expect(
      openInsightKeyForClick(
        event,
        target({
          "data-starlogz-action": "open-insight",
          "data-insight-key": "target",
          target: "_blank",
        }),
      ),
    ).toBeNull();
  });

  test("ignores unknown actions and missing keys", () => {
    expect(
      openInsightKeyForClick(
        event,
        target({
          "data-starlogz-action": "unknown",
          "data-insight-key": "target",
        }),
      ),
    ).toBeNull();
    expect(
      openInsightKeyForClick(
        event,
        target({
          "data-starlogz-action": "open-insight",
        }),
      ),
    ).toBeNull();
  });
});
