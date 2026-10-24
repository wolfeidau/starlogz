export type InsightSelector = {
  case: "id" | "key";
  value: string;
};

type ClickLike = {
  altKey: boolean;
  button: number;
  ctrlKey: boolean;
  defaultPrevented: boolean;
  detail: number;
  metaKey: boolean;
  shiftKey: boolean;
};

type ActionAnchor = {
  getAttribute(name: string): string | null;
  hasAttribute(name: string): boolean;
};

type ClosestTarget = {
  closest(selector: string): ActionAnchor | null;
};

export function isUnmodifiedPrimaryClick(event: ClickLike): boolean {
  return (
    !event.defaultPrevented &&
    event.detail > 0 &&
    event.button === 0 &&
    !event.altKey &&
    !event.ctrlKey &&
    !event.metaKey &&
    !event.shiftKey
  );
}

export function openInsightKeyForClick(
  event: ClickLike,
  target: unknown,
): string | null {
  if (!isUnmodifiedPrimaryClick(event) || !isClosestTarget(target)) return null;

  const anchor = target.closest("a[data-starlogz-action]");
  if (!anchor) return null;
  if (anchor.getAttribute("data-starlogz-action") !== "open-insight") {
    return null;
  }
  if (anchor.hasAttribute("download") || anchor.hasAttribute("target")) {
    return null;
  }

  return anchor.getAttribute("data-insight-key") || null;
}

function isClosestTarget(value: unknown): value is ClosestTarget {
  return (
    typeof value === "object" &&
    value !== null &&
    "closest" in value &&
    typeof value.closest === "function"
  );
}
