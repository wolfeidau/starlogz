import type { MouseEvent } from "react";
import { openInsightKeyForClick } from "./insight_navigation";
import "./markdown.css";

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
