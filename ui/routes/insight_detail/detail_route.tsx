import { useQuery } from "@connectrpc/connect-query";
import { useNavigate, useSearch } from "@tanstack/react-router";
import { getInsight } from "../../../api/gen/proto/es/starlogz/v1/ui-UIService_connectquery";
import type { InsightSelector } from "../../lib/insight_navigation";
import { InsightDetail } from "./insight_detail";

export function InsightDetailRoute({
  project,
  selector,
}: {
  project: string;
  selector: InsightSelector;
}) {
  const navigate = useNavigate();
  const { q, tag } = useSearch({ strict: false });
  const detail = useQuery(getInsight, {
    project,
    selector,
    relationLimit: 50,
  });

  return (
    <InsightDetail
      project={project}
      selector={selector}
      detail={detail.data}
      error={detail.error}
      loading={detail.isLoading}
      onClose={() =>
        void navigate({
          to: "/dashboard/$project",
          params: { project },
          search: { q, tag },
        })
      }
      onNavigate={(next) =>
        void (next.case === "key"
          ? navigate({
              to: "/dashboard/$project/insights/$key",
              params: { project, key: next.value },
              search: { q, tag },
            })
          : navigate({
              to: "/dashboard/$project/insights/by-id/$id",
              params: { project, id: next.value },
              search: { q, tag },
            }))
      }
    />
  );
}
