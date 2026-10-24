import "./top_bar.css";

export function TopBar({
  login,
  displayName,
  avatarUrl,
  isOperator,
  activeView,
}: {
  login: string;
  displayName: string;
  avatarUrl: string;
  isOperator: boolean;
  activeView: "insights" | "operations";
}) {
  return (
    <header className="topbar">
      <div className="topbar-inner">
        <span className="wordmark">
          <span className="wordmark-star" aria-hidden="true">
            ✦
          </span>
          starlogz
        </span>
        <div className="topbar-actions">
          <nav className="app-nav" aria-label="Primary">
            <a
              className={activeView === "insights" ? "active" : undefined}
              href="/dashboard"
            >
              Insights
            </a>
            {isOperator && (
              <a
                className={activeView === "operations" ? "active" : undefined}
                href="/admin/operations"
              >
                Operations
              </a>
            )}
          </nav>
          <div className="topbar-user">
            {avatarUrl && (
              <img className="user-avatar" src={avatarUrl} alt="" />
            )}
            <span>{displayName || login}</span>
            <form action="/logout" method="post">
              <button className="logout-button" type="submit">
                Sign out
              </button>
            </form>
          </div>
        </div>
      </div>
    </header>
  );
}
