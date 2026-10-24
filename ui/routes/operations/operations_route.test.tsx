import { afterEach, describe, expect, test } from "bun:test";
import { Code, ConnectError, createRouterTransport } from "@connectrpc/connect";
import { TransportProvider } from "@connectrpc/connect-query";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { createMemoryHistory, RouterProvider } from "@tanstack/react-router";
import {
  cleanup,
  fireEvent,
  render,
  screen,
  waitFor,
  within,
} from "@testing-library/react";
import { UIService } from "../../../api/gen/proto/es/starlogz/v1/ui_pb";
import { createAppRouter } from "../../router";

afterEach(cleanup);

const grantId = "019fa8c2-3f3a-7a86-b7d0-091933918c98";
const sessionId = "019fa8c2-3f3a-7a86-b7d0-091933918c97";

function setup({ failRevoke = false }: { failRevoke?: boolean } = {}) {
  const calls = {
    overview: 0,
    revokeSession: [] as string[],
    revokeGrant: [] as string[],
  };
  const transport = createRouterTransport((router) => {
    router.rpc(UIService.method.getSession, () => ({
      login: "op",
      displayName: "Operator",
      isOperator: true,
      webSessionId: "current",
    }));
    router.rpc(UIService.method.listProjects, () => ({ projects: [] }));
    router.rpc(UIService.method.listTags, () => ({ tags: [] }));
    router.rpc(UIService.method.getOperationsTelemetry, () => ({}));
    router.rpc(UIService.method.getOperationsOverview, () => {
      calls.overview++;
      return {
        recentWebSessions: [
          { id: sessionId, login: "svc", displayName: "Service", active: true },
        ],
        recentOauthGrants: [
          {
            id: grantId,
            login: "svc",
            clientId: "client",
            clientName: "Test Client",
            active: true,
          },
        ],
      };
    });
    router.rpc(UIService.method.revokeOperationsOAuthGrant, (req) => {
      if (failRevoke) throw new ConnectError("boom", Code.Internal);
      calls.revokeGrant.push(req.id);
      return {};
    });
    router.rpc(UIService.method.revokeOperationsWebSession, (req) => {
      calls.revokeSession.push(req.id);
      return {};
    });
  });
  const queryClient = new QueryClient({
    defaultOptions: { queries: { retry: false, gcTime: 0 } },
  });
  const router = createAppRouter({
    queryClient,
    history: createMemoryHistory({ initialEntries: ["/admin/operations"] }),
  });
  render(
    <TransportProvider transport={transport}>
      <QueryClientProvider client={queryClient}>
        <RouterProvider router={router} />
      </QueryClientProvider>
    </TransportProvider>,
  );
  return calls;
}

const openGrantDialog = async () => {
  fireEvent.click(
    await screen.findByRole("button", {
      name: "Revoke OAuth grant for Test Client",
    }),
  );
  return screen.findByRole("dialog", { name: "Revoke OAuth grant?" });
};

describe("operations revoke dialog", () => {
  test("cancel closes without calling the RPC", async () => {
    const calls = setup();
    const dialog = await openGrantDialog();
    fireEvent.click(within(dialog).getByRole("button", { name: "Cancel" }));
    await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
    expect(calls.revokeGrant).toEqual([]);
  });

  test("confirm revokes and refetches the overview", async () => {
    const calls = setup();
    const dialog = await openGrantDialog();
    await waitFor(() => expect(calls.overview).toBe(1));
    fireEvent.click(
      within(dialog).getByRole("button", { name: "Confirm revocation" }),
    );
    await waitFor(() => expect(calls.revokeGrant).toEqual([grantId]));
    await waitFor(() => expect(calls.overview).toBe(2));
    await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
    expect(calls.revokeSession).toEqual([]);
  });

  test("failure shows an error and keeps the dialog open", async () => {
    setup({ failRevoke: true });
    const dialog = await openGrantDialog();
    fireEvent.click(
      within(dialog).getByRole("button", { name: "Confirm revocation" }),
    );
    expect(await within(dialog).findByText(/Revocation failed/)).toBeTruthy();
    expect(screen.getByRole("dialog")).toBeTruthy();
  });
});
