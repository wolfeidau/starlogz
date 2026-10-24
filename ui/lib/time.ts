import type { Timestamp } from "@bufbuild/protobuf/wkt";

export function timestampToDate(ts?: Timestamp): Date | null {
  if (!ts) return null;
  return new Date(Number(ts.seconds) * 1000 + Math.floor(ts.nanos / 1_000_000));
}

export function formatTimestamp(ts?: Timestamp): string {
  const date = timestampToDate(ts);
  if (!date) return "-";
  return formatDateTime(date);
}

// Joined by hand: ICU versions differ on the date/time separator ("," vs "at").
function formatDateTime(date: Date): string {
  const day = new Intl.DateTimeFormat("en-US", {
    month: "short",
    day: "numeric",
  }).format(date);
  return `${day}, ${formatClock(date)}`;
}

function formatClock(date: Date): string {
  return new Intl.DateTimeFormat("en-US", {
    hour: "2-digit",
    minute: "2-digit",
    hourCycle: "h23",
  }).format(date);
}

export function formatSinceLabel(date: Date): string {
  return `New since ${formatDateTime(date)}`;
}

export function formatTime(ts?: Timestamp): string {
  const date = timestampToDate(ts);
  if (!date) return "-";
  return formatClock(date);
}

export function dayKey(date: Date): string {
  const month = String(date.getMonth() + 1).padStart(2, "0");
  const day = String(date.getDate()).padStart(2, "0");
  return `${date.getFullYear()}-${month}-${day}`;
}

export function formatDayLabel(date: Date, now: Date): string {
  if (dayKey(date) === dayKey(now)) return "Today";
  const yesterday = new Date(
    now.getFullYear(),
    now.getMonth(),
    now.getDate() - 1,
  );
  if (dayKey(date) === dayKey(yesterday)) return "Yesterday";
  return new Intl.DateTimeFormat("en-US", {
    month: "short",
    day: "numeric",
    year: date.getFullYear() === now.getFullYear() ? undefined : "numeric",
  }).format(date);
}
