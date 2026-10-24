import { Code, ConnectError } from "@connectrpc/connect";
import { useQuery } from "@connectrpc/connect-query";
import { Outlet, useRouterState } from "@tanstack/react-router";
import { getSession } from "../../api/gen/proto/es/starlogz/v1/ui-UIService_connectquery";
import { LoginView } from "./login_view";
import { TopBar } from "./top_bar";
import "./app_shell.css";

export function AppShell() {
  const session = useQuery(getSession, {});
  const operationsView = useRouterState({
    select: (state) => state.location.pathname.startsWith("/admin/"),
  });

  if (session.error) {
    if (ConnectError.from(session.error).code === Code.Unauthenticated) {
      return <LoginView />;
    }
    return (
      <main className="app-shell">
        <section className="empty-panel" role="alert">
          <p>Unable to reach starlogz.</p>
          <button
            className="retry-button"
            type="button"
            disabled={session.isFetching}
            onClick={() => session.refetch()}
          >
            {session.isFetching ? "Retrying" : "Retry"}
          </button>
        </section>
      </main>
    );
  }
  if (session.isLoading) return <div className="center-state">Loading</div>;

  return (
    <main className="app-shell">
      <TopBar
        login={session.data?.login ?? ""}
        displayName={session.data?.displayName ?? ""}
        avatarUrl={session.data?.avatarUrl ?? ""}
        isOperator={session.data?.isOperator ?? false}
        activeView={operationsView ? "operations" : "insights"}
      />
      <Outlet />
    </main>
  );
}
