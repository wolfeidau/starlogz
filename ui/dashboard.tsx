import "./styles/tokens.css";
import "./styles/base.css";
import { TransportProvider } from "@connectrpc/connect-query";
import { createConnectTransport } from "@connectrpc/connect-web";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { useMemo } from "react";
import { createRoot } from "react-dom/client";
import { DashboardView } from "./shell/dashboard_view";

const queryClient = new QueryClient({
  defaultOptions: {
    queries: {
      retry: false,
    },
  },
});

function App() {
  const transport = useMemo(
    () =>
      createConnectTransport({
        baseUrl: window.location.origin,
      }),
    [],
  );

  return (
    <TransportProvider transport={transport}>
      <QueryClientProvider client={queryClient}>
        <DashboardView />
      </QueryClientProvider>
    </TransportProvider>
  );
}

const app = document.getElementById("app");
if (!app) throw new Error("App element not found");
createRoot(app).render(<App />);
