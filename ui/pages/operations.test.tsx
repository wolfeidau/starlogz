import { afterEach, describe, expect, test } from "bun:test";
import { create } from "@bufbuild/protobuf";
import { cleanup, render, screen } from "@testing-library/react";
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
          userId: "user-id",
          login: "operator",
          displayName: "Service Operator",
          clientId: "https://client.example/metadata.json",
          clientName: "Example Client",
          scope: "insights:read",
          active: true,
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
    expect(screen.getAllByText("Service Operator")).toHaveLength(2);
    expect(screen.getByText("Example Client")).toBeTruthy();
    expect(
      screen.getByText("https://client.example/metadata.json"),
    ).toBeTruthy();
    expect(screen.getByText("Tool calls, 24 hours")).toBeTruthy();
    expect(screen.getByText("16.7%")).toBeTruthy();
    expect(screen.getByText("47 ms")).toBeTruthy();
    expect(screen.getByText("Calls by tool")).toBeTruthy();
    expect(screen.getAllByText("token exchange")).toHaveLength(2);
    expect(screen.getByText("invalid_request")).toBeTruthy();
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
});
