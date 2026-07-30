import { afterEach, describe, expect, mock, test } from "bun:test";
import { create } from "@bufbuild/protobuf";
import {
  cleanup,
  fireEvent,
  render,
  screen,
  waitFor,
} from "@testing-library/react";
import {
  GetOperationsOverviewResponseSchema,
  GetOperationsTelemetryResponseSchema,
} from "../../api/gen/proto/es/starlogz/v1/ui_pb";
import { OperationsView } from "./operations";

afterEach(cleanup);

function timestamp(iso: string) {
  return { seconds: BigInt(Date.parse(iso) / 1000), nanos: 0 };
}

describe("operations view", () => {
  test("renders credential-free session and grant summaries", () => {
    const overview = create(GetOperationsOverviewResponseSchema, {
      activeWebSessions: 1,
      activeOauthGrants: 1,
      recentWebSessions: [
        {
          id: "session-id",
          userId: "user-id",
          login: "operator",
          displayName: "Service Operator",
          active: true,
        },
      ],
      recentOauthGrants: [
        {
          id: "grant-id",
          userId: "user-id",
          login: "operator",
          displayName: "Service Operator",
          clientId: "https://client.example/metadata.json",
          clientName: "Example Client",
          scope: "insights:read",
          active: true,
        },
      ],
      recentActions: [
        {
          id: "action-id",
          actorUserId: "operator-id",
          actorLogin: "operator",
          actorDisplayName: "Service Operator",
          action: "oauth_grant.revoke",
          targetId: "old-grant-id",
          targetUserId: "user-id",
          targetLogin: "target",
          targetDisplayName: "Target User",
          targetClientId: "https://client.example/metadata.json",
        },
      ],
    });
    const telemetry = create(GetOperationsTelemetryResponseSchema, {
      available: true,
      totalToolCalls: 12,
      failedToolCalls: 2,
      p95ToolDurationMs: 47n,
      successfulDashboardLogins: 3,
      tools: [{ tool: "insight_search", calls: 12, failures: 2 }],
      oauthFlows: [
        {
          eventName: "oauth.token_exchange.completed",
          success: 5,
          failure: 1,
        },
      ],
      oauthFailures: [
        {
          eventName: "oauth.token_exchange.completed",
          reason: "invalid_request",
          count: 1,
        },
      ],
    });

    render(
      <OperationsView
        overview={overview}
        loading={false}
        error={null}
        telemetry={telemetry}
        telemetryLoading={false}
        telemetryError={null}
      />,
    );

    expect(screen.getByText("Recent dashboard sessions")).toBeTruthy();
    expect(screen.getByText("Recent OAuth grants")).toBeTruthy();
    expect(screen.getAllByText("Service Operator")).toHaveLength(3);
    expect(screen.getByText("Example Client")).toBeTruthy();
    expect(
      screen.getAllByText("https://client.example/metadata.json"),
    ).toHaveLength(2);
    expect(screen.getByText("Tool calls, 24 hours")).toBeTruthy();
    expect(screen.getByText("16.7%")).toBeTruthy();
    expect(screen.getByText("47 ms")).toBeTruthy();
    expect(screen.getByText("Calls by tool")).toBeTruthy();
    expect(screen.getAllByText("token exchange")).toHaveLength(2);
    expect(screen.getByText("invalid_request")).toBeTruthy();
    expect(screen.getByText("Recent operator actions")).toBeTruthy();
    expect(screen.getByText("Revoked OAuth grant")).toBeTruthy();
  });

  test("renders labeled chart axes aligned with hourly buckets", () => {
    const telemetry = create(GetOperationsTelemetryResponseSchema, {
      available: true,
      windowStartedAt: timestamp("2026-07-29T12:30:00Z"),
      windowEndedAt: timestamp("2026-07-30T12:30:00Z"),
      totalToolCalls: 3,
      toolSeries: Array.from({ length: 25 }, (_, hour) => ({
        startedAt: timestamp(
          new Date(
            Date.parse("2026-07-29T12:00:00Z") + hour * 60 * 60 * 1000,
          ).toISOString(),
        ),
        success: hour === 0 ? 1 : hour === 24 ? 2 : 0,
      })),
    });

    render(
      <OperationsView
        loading={false}
        error={null}
        telemetry={telemetry}
        telemetryLoading={false}
        telemetryError={null}
      />,
    );

    const chart = screen.getByRole("img", {
      name: "Successful and failed MCP tool calls by hour",
    });
    const path = chart.querySelector(".chart-line-success")?.getAttribute("d");
    expect(path?.startsWith("M48,")).toBe(true);
    expect(path).toContain("L704,");
    expect(chart.querySelectorAll(".chart-x-tick")).toHaveLength(5);
    expect(chart.querySelectorAll(".chart-y-tick").length).toBeGreaterThan(1);
    expect(chart.querySelector(".chart-axis-title")?.textContent).toBe(
      "Calls per hour",
    );
    expect(chart.querySelector("desc")?.textContent).toContain(
      "3 tool calls in the last 24 hours",
    );
    expect(chart.querySelector(".chart-line-failure")).toBeNull();
    expect(chart.querySelectorAll(".chart-point-success")).toHaveLength(2);
  });

  test("requires confirmation before revoking a session", async () => {
    const overview = create(GetOperationsOverviewResponseSchema, {
      recentWebSessions: [
        {
          id: "019fa8c2-3f3a-7a86-b7d0-091933918c99",
          login: "current-operator",
          displayName: "Current Operator",
          active: true,
        },
        {
          id: "019fa8c2-3f3a-7a86-b7d0-091933918c97",
          login: "operator",
          displayName: "Service Operator",
          active: true,
        },
      ],
      recentOauthGrants: [
        {
          id: "019fa8c2-3f3a-7a86-b7d0-091933918c98",
          login: "operator",
          clientId: "test-client",
          clientName: "Test Client",
          active: true,
        },
      ],
    });
    const revokeWebSession = mock(async () => {});
    const revokeOAuthGrant = mock(async () => {});

    render(
      <OperationsView
        overview={overview}
        loading={false}
        error={null}
        telemetryLoading={false}
        telemetryError={null}
        currentWebSessionId="019fa8c2-3f3a-7a86-b7d0-091933918c99"
        onRevokeWebSession={revokeWebSession}
        onRevokeOAuthGrant={revokeOAuthGrant}
      />,
    );

    expect(screen.getByText("Current")).toBeTruthy();
    expect(
      screen.queryByRole("button", {
        name: "Revoke dashboard session for Current Operator",
      }),
    ).toBeNull();
    fireEvent.click(
      screen.getByRole("button", {
        name: "Revoke dashboard session for Service Operator",
      }),
    );

    expect(revokeWebSession).not.toHaveBeenCalled();
    expect(
      screen.getByRole("region", { name: "Revoke dashboard session?" }),
    ).toBeTruthy();
    expect(
      screen.getByText("This immediately signs out Service Operator."),
    ).toBeTruthy();
    fireEvent.click(screen.getByRole("button", { name: "Confirm revocation" }));

    await waitFor(() =>
      expect(revokeWebSession).toHaveBeenCalledWith(
        "019fa8c2-3f3a-7a86-b7d0-091933918c97",
      ),
    );
    expect(revokeOAuthGrant).not.toHaveBeenCalled();
  });
});
