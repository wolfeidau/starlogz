import "./load_more_button.css";

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
