import { timestampToDate } from "../../lib/time";

const storageKey = (project: string) => `starlogz.lastSeen.${project}`;

function parseDate(raw: string | null): Date | null {
  if (!raw) return null;
  const date = new Date(/^\d+$/.test(raw) ? Number(raw) : raw);
  return Number.isNaN(date.getTime()) ? null : date;
}

// Storage can be unavailable or blocked; the marker is only a convenience.
// The baseline is pinned per tab session so remounts (e.g. via Overview) keep the divider.
export function readLastSeen(project: string): Date | null {
  try {
    const key = storageKey(project);
    const pinned = window.sessionStorage.getItem(key);
    if (pinned !== null) return parseDate(pinned);
    const previous = window.localStorage.getItem(key) ?? "";
    window.sessionStorage.setItem(key, previous);
    return parseDate(previous);
  } catch {
    return null;
  }
}

export function writeLastSeen(project: string, now: Date) {
  try {
    window.localStorage.setItem(storageKey(project), now.toISOString());
  } catch {}
}

// Index of the first insight at or before lastSeen, only when the list straddles it.
export function newSinceIndex(
  insights: { updatedAt?: Parameters<typeof timestampToDate>[0] }[],
  lastSeen: Date | null,
): number | null {
  if (!lastSeen) return null;
  const cutoff = lastSeen.getTime();
  const index = insights.findIndex((insight) => {
    const date = timestampToDate(insight.updatedAt);
    return date !== null && date.getTime() <= cutoff;
  });
  return index > 0 ? index : null;
}
