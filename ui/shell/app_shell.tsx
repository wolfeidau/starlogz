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

  if (session.error) return <LoginView />;
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
