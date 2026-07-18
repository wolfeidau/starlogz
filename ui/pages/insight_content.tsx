import type { Timestamp } from "@bufbuild/protobuf/wkt";
import type { MouseEvent } from "react";
import { openInsightKeyForClick } from "./insight_navigation";

function timestampToDate(ts?: Timestamp): Date | null {
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

export function RenderedMarkdown({
  html,
  onOpenInsight,
}: {
  html: string;
  onOpenInsight: (key: string) => void;
}) {
  const handleClick = (event: MouseEvent<HTMLDivElement>) => {
    const key = openInsightKeyForClick(event, event.target);
    if (!key) return;
    event.preventDefault();
    onOpenInsight(key);
  };

  return (
    // biome-ignore lint/a11y/noStaticElementInteractions lint/a11y/useKeyWithClickEvents: interaction is delegated to sanitized anchors, whose keyboard behavior remains native.
    <div
      className="markdown-preview"
      onClick={handleClick}
      // biome-ignore lint/security/noDangerouslySetInnerHtml: the server returns allowlist-sanitized HTML.
      dangerouslySetInnerHTML={{ __html: html }}
    />
  );
}
