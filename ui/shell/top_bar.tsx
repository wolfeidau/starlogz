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
      <div>
        <p className="eyebrow">starlogz</p>
        <h1>
          {activeView === "operations" ? "Operations" : "Insights Dashboard"}
        </h1>
      </div>
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
        {avatarUrl && <img className="user-avatar" src={avatarUrl} alt="" />}
        <span>{displayName || login}</span>
        <form action="/logout" method="post">
          <button className="logout-button" type="submit">
            Logout
          </button>
        </form>
      </div>
    </header>
  );
}
