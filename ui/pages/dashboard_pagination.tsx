import { useInfiniteQuery } from "@connectrpc/connect-query";
import {
  listInsights,
  searchInsights,
} from "../../api/gen/proto/es/starlogz/v1/ui-UIService_connectquery";

export function nextPageCursor(page: { nextCursor: string }) {
  return page.nextCursor || undefined;
}

export function useDashboardInsightPages({
  isAuthenticated,
  activeProject,
  query,
  selectedTag,
}: {
  isAuthenticated: boolean;
  activeProject: string;
  query: string;
  selectedTag: string;
}) {
  const searching = query.trim() !== "";
  const search = useInfiniteQuery(
    searchInsights,
    {
      project: activeProject,
      query,
      tags: selectedTag ? [selectedTag] : [],
      limit: 100,
      cursor: "",
    },
    {
      enabled: isAuthenticated && activeProject !== "" && searching,
      pageParamKey: "cursor",
      getNextPageParam: nextPageCursor,
    },
  );
  const listed = useInfiniteQuery(
    listInsights,
    { project: activeProject, tag: selectedTag, limit: 100, cursor: "" },
    {
      enabled: isAuthenticated && activeProject !== "" && !searching,
      pageParamKey: "cursor",
      getNextPageParam: nextPageCursor,
    },
  );
  const active = searching ? search : listed;

  return {
    insights: active.data?.pages.flatMap((page) => page.insights) ?? [],
    isLoading: listed.isLoading || search.isLoading,
    hasNextPage: active.hasNextPage,
    isFetchingNextPage: active.isFetchingNextPage,
    fetchNextPage: active.fetchNextPage,
  };
}

export function LoadMoreButton({
  loading,
  label = "Load more",
  onLoadMore,
}: {
  loading: boolean;
  label?: string;
  onLoadMore: () => void;
}) {
  return (
    <div className="pagination-actions">
      <button
        className="load-more-button"
        type="button"
        disabled={loading}
        onClick={onLoadMore}
      >
        {loading ? "Loading" : label}
      </button>
    </div>
  );
}
