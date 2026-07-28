import { afterEach, describe, expect, test } from "bun:test";
import { create } from "@bufbuild/protobuf";
import { cleanup, render, screen } from "@testing-library/react";
import { GetOperationsOverviewResponseSchema } from "../../api/gen/proto/es/starlogz/v1/ui_pb";
import { OperationsView } from "./operations";

afterEach(cleanup);

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

    render(<OperationsView overview={overview} loading={false} error={null} />);

    expect(screen.getByText("Recent dashboard sessions")).toBeTruthy();
    expect(screen.getByText("Recent OAuth grants")).toBeTruthy();
    expect(screen.getAllByText("Service Operator")).toHaveLength(2);
    expect(screen.getByText("Example Client")).toBeTruthy();
    expect(
      screen.getByText("https://client.example/metadata.json"),
    ).toBeTruthy();
    expect(screen.getByText("Tool-call aggregates")).toBeTruthy();
  });
});
