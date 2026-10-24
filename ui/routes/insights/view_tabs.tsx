import { Link } from "@tanstack/react-router";
import "./insights.css";

export function ViewTabs({
  project,
  active,
  q,
  tag,
}: {
  project: string;
  active: "list" | "overview";
  q?: string;
  tag?: string;
}) {
  return (
    <nav aria-label="Project views" className="view-tabs">
      <Link
        activeOptions={{ exact: true }}
        aria-current={active === "list" ? "page" : undefined}
        className="view-tab"
        data-active={active === "list"}
        params={{ project }}
        search={{ q, tag }}
        to="/dashboard/$project"
      >
        List
      </Link>
      <Link
        activeOptions={{ exact: true }}
        aria-current={active === "overview" ? "page" : undefined}
        className="view-tab"
        data-active={active === "overview"}
        params={{ project }}
        to="/dashboard/$project/overview"
      >
        Overview
      </Link>
    </nav>
  );
}
