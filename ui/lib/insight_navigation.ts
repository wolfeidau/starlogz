export type InsightSelector = {
  case: "id" | "key";
  value: string;
};

export type DashboardLocation = {
  project: string;
  selector: InsightSelector | null;
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

export function readDashboardLocation(search: string): DashboardLocation {
  const params = new URLSearchParams(search);
  const project = params.get("project") ?? "";
  if (!project) return { project: "", selector: null };

  const key = params.get("insight_key");
  if (key) return { project, selector: { case: "key", value: key } };

  const id = params.get("insight_id");
  if (id) return { project, selector: { case: "id", value: id } };

  return { project, selector: null };
}

export function dashboardURL(
  href: string,
  project: string,
  selector: InsightSelector | null,
): string {
  const url = new URL(href);
  url.searchParams.delete("insight_key");
  url.searchParams.delete("insight_id");

  if (project) url.searchParams.set("project", project);
  else url.searchParams.delete("project");

  if (selector?.case === "key") {
    url.searchParams.set("insight_key", selector.value);
  } else if (selector?.case === "id") {
    url.searchParams.set("insight_id", selector.value);
  }

  return `${url.pathname}${url.search}${url.hash}`;
}

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
