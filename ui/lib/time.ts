import type { Timestamp } from "@bufbuild/protobuf/wkt";

export function timestampToDate(ts?: Timestamp): Date | null {
  if (!ts) return null;
  return new Date(Number(ts.seconds) * 1000 + Math.floor(ts.nanos / 1_000_000));
}

export function formatTimestamp(ts?: Timestamp): string {
  const date = timestampToDate(ts);
  if (!date) return "-";
  return new Intl.DateTimeFormat("en-US", {
    month: "short",
    day: "numeric",
    hour: "2-digit",
    minute: "2-digit",
  }).format(date);
}
