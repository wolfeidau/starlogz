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
} from "@testing-library/react";
import { UIService } from "../../api/gen/proto/es/starlogz/v1/ui_pb";
import { createAppRouter } from "../router";

afterEach(cleanup);

function setup(sessionError: () => ConnectError | null) {
  const transport = createRouterTransport((router) => {
    router.rpc(UIService.method.getSession, () => {
      const err = sessionError();
      if (err) throw err;
      return { login: "u", displayName: "User", webSessionId: "w" };
    });
    router.rpc(UIService.method.listProjects, () => ({ projects: [] }));
    router.rpc(UIService.method.listTags, () => ({ tags: [] }));
  });
  const queryClient = new QueryClient({
    defaultOptions: { queries: { retry: false, gcTime: 0 } },
  });
  const router = createAppRouter({
    queryClient,
    history: createMemoryHistory({ initialEntries: ["/dashboard"] }),
  });
  render(
    <TransportProvider transport={transport}>
      <QueryClientProvider client={queryClient}>
        <RouterProvider router={router} />
      </QueryClientProvider>
    </TransportProvider>,
  );
}

describe("app shell session errors", () => {
  test("unauthenticated shows the login view", async () => {
    setup(() => new ConnectError("no", Code.Unauthenticated));
    expect(await screen.findByText("Sign in with GitHub")).toBeTruthy();
    expect(
      screen.getByRole("heading", { level: 1, name: "starlogz" }),
    ).toBeTruthy();
  });

  test("other errors show a retry state that recovers", async () => {
    let failing = true;
    setup(() => (failing ? new ConnectError("down", Code.Unavailable) : null));

    const alert = await screen.findByRole("alert");
    expect(alert.textContent).toContain("Unable to reach starlogz.");
    expect(screen.queryByText("Sign in with GitHub")).toBeNull();

    failing = false;
    fireEvent.click(screen.getByRole("button", { name: "Retry" }));
    await waitFor(() => expect(screen.queryByRole("alert")).toBeNull());
    expect(await screen.findByText("User")).toBeTruthy();
  });
});
