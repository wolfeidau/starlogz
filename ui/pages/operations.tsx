import type { Timestamp } from "@bufbuild/protobuf/wkt";
import { max } from "d3-array";
import { scaleLinear, scaleUtc } from "d3-scale";
import { line } from "d3-shape";
import type {
  GetOperationsOverviewResponse,
  GetOperationsTelemetryResponse,
  OperationsTimeBucket,
} from "../../api/gen/proto/es/starlogz/v1/ui_pb";
import { formatTimestamp } from "./insight_content";

function Status({ active }: { active: boolean }) {
  return (
    <span className={`status ${active ? "status-active" : "status-inactive"}`}>
      {active ? "Active" : "Inactive"}
    </span>
  );
}

function timestampToDate(timestamp?: Timestamp): Date | null {
  if (!timestamp) return null;
  return new Date(
    Number(timestamp.seconds) * 1000 + Math.floor(timestamp.nanos / 1_000_000),
  );
}

function failureRate(total: number, failures: number): string {
  if (total === 0) return "0.0%";
  return `${((failures / total) * 100).toFixed(1)}%`;
}

function flowName(eventName: string): string {
  return eventName
    .replace("oauth.", "")
    .replace(".completed", "")
    .replaceAll("_", " ");
}

function ToolCallChart({
  telemetry,
}: {
  telemetry: GetOperationsTelemetryResponse;
}) {
  const width = 720;
  const height = 220;
  const margin = { top: 16, right: 20, bottom: 32, left: 38 };
  const buckets = telemetry.toolSeries.filter((bucket) => bucket.startedAt);
  const start = timestampToDate(buckets[0]?.startedAt);
  const end = timestampToDate(buckets.at(-1)?.startedAt);
  if (
    !start ||
    !end ||
    buckets.length === 0 ||
    telemetry.totalToolCalls === 0
  ) {
    return <p className="muted">No tool calls in this window.</p>;
  }

  const x = scaleUtc()
    .domain([start, end])
    .range([margin.left, width - margin.right]);
  const y = scaleLinear()
    .domain([0, max(buckets, (bucket) => bucket.success + bucket.failure) ?? 1])
    .nice()
    .range([height - margin.bottom, margin.top]);
  const path = (field: "success" | "failure") =>
    line<OperationsTimeBucket>()
      .x((bucket) => x(timestampToDate(bucket.startedAt) ?? start))
      .y((bucket) => y(bucket[field]))(buckets) ?? "";

  return (
    <div>
      <svg
        className="operations-chart"
        viewBox={`0 0 ${width} ${height}`}
        role="img"
        aria-label="Successful and failed MCP tool calls by hour"
      >
        <title>Successful and failed MCP tool calls by hour</title>
        <line
          className="chart-axis"
          x1={margin.left}
          x2={width - margin.right}
          y1={height - margin.bottom}
          y2={height - margin.bottom}
        />
        <path className="chart-line chart-line-success" d={path("success")} />
        <path className="chart-line chart-line-failure" d={path("failure")} />
        <text x={margin.left} y={height - 8}>
          {start.toLocaleTimeString([], { hour: "2-digit", minute: "2-digit" })}
        </text>
        <text textAnchor="end" x={width - margin.right} y={height - 8}>
          {end.toLocaleTimeString([], { hour: "2-digit", minute: "2-digit" })}
        </text>
      </svg>
      <div className="chart-legend">
        <span>
          <i className="legend-success" /> Success
        </span>
        <span>
          <i className="legend-failure" /> Failure
        </span>
      </div>
    </div>
  );
}

function OperationsTelemetry({
  telemetry,
  loading,
  error,
}: {
  telemetry?: GetOperationsTelemetryResponse;
  loading: boolean;
  error: Error | null;
}) {
  if (loading) {
    return (
      <section className="panel operations-panel">Loading telemetry</section>
    );
  }
  if (error) {
    return (
      <section className="panel operations-panel">
        <h2>Operational telemetry unavailable</h2>
        <p className="muted">
          CloudWatch could not be queried. Session and grant data remains
          available.
        </p>
      </section>
    );
  }
  if (!telemetry?.available) {
    return (
      <section className="panel operations-panel">
        <h2>Operational telemetry not configured</h2>
        <p className="muted">
          Configure the CloudWatch wide-event log group to enable 24-hour
          aggregates.
        </p>
      </section>
    );
  }

  const maxToolCalls = Math.max(
    1,
    ...telemetry.tools.map((tool) => tool.calls),
  );
  return (
    <>
      <section className="summary-grid operations-summary">
        <div className="metric">
          <span>Tool calls, 24 hours</span>
          <strong>{telemetry.totalToolCalls}</strong>
        </div>
        <div className="metric">
          <span>Tool failure rate</span>
          <strong>
            {failureRate(telemetry.totalToolCalls, telemetry.failedToolCalls)}
          </strong>
        </div>
        <div className="metric">
          <span>Tool duration p95</span>
          <strong>{Number(telemetry.p95ToolDurationMs)} ms</strong>
        </div>
        <div className="metric">
          <span>Dashboard logins</span>
          <strong>{telemetry.successfulDashboardLogins}</strong>
        </div>
      </section>

      <section className="operations-telemetry-grid">
        <div className="panel">
          <div className="panel-heading">
            <div>
              <p className="eyebrow">MCP activity</p>
              <h2>Tool-call volume</h2>
            </div>
            <span>Hourly, last 24 hours</span>
          </div>
          <ToolCallChart telemetry={telemetry} />
        </div>
        <div className="panel">
          <div className="panel-heading">
            <div>
              <p className="eyebrow">MCP activity</p>
              <h2>Calls by tool</h2>
            </div>
          </div>
          <div className="operations-tool-list">
            {telemetry.tools.map((tool) => (
              <div className="operations-tool-row" key={tool.tool}>
                <code>{tool.tool}</code>
                <div className="bar-track">
                  <div
                    className="bar-fill"
                    style={{ width: `${(tool.calls / maxToolCalls) * 100}%` }}
                  />
                </div>
                <strong>{tool.calls}</strong>
                <span>{tool.failures} failed</span>
              </div>
            ))}
            {telemetry.tools.length === 0 && (
              <p className="muted">No tool calls in this window.</p>
            )}
          </div>
        </div>
      </section>

      <section className="operations-telemetry-grid">
        <div className="panel">
          <div className="panel-heading">
            <div>
              <p className="eyebrow">OAuth</p>
              <h2>Flow outcomes</h2>
            </div>
          </div>
          <div className="table-wrap">
            <table className="operations-aggregate-table">
              <thead>
                <tr>
                  <th>Flow</th>
                  <th>Success</th>
                  <th>Failure</th>
                </tr>
              </thead>
              <tbody>
                {telemetry.oauthFlows.map((flow) => (
                  <tr key={flow.eventName}>
                    <td>{flowName(flow.eventName)}</td>
                    <td>{flow.success}</td>
                    <td>{flow.failure}</td>
                  </tr>
                ))}
                {telemetry.oauthFlows.length === 0 && (
                  <tr>
                    <td className="muted" colSpan={3}>
                      No OAuth flows in this window.
                    </td>
                  </tr>
                )}
              </tbody>
            </table>
          </div>
        </div>
        <div className="panel">
          <div className="panel-heading">
            <div>
              <p className="eyebrow">OAuth</p>
              <h2>Failure reasons</h2>
            </div>
          </div>
          <div className="table-wrap">
            <table className="operations-aggregate-table">
              <thead>
                <tr>
                  <th>Flow</th>
                  <th>Reason</th>
                  <th>Count</th>
                </tr>
              </thead>
              <tbody>
                {telemetry.oauthFailures.map((failure) => (
                  <tr key={`${failure.eventName}:${failure.reason}`}>
                    <td>{flowName(failure.eventName)}</td>
                    <td>
                      <code>{failure.reason}</code>
                    </td>
                    <td>{failure.count}</td>
                  </tr>
                ))}
                {telemetry.oauthFailures.length === 0 && (
                  <tr>
                    <td className="muted" colSpan={3}>
                      No OAuth failures in this window.
                    </td>
                  </tr>
                )}
              </tbody>
            </table>
          </div>
        </div>
      </section>
    </>
  );
}

export function OperationsView({
  overview,
  loading,
  error,
  telemetry,
  telemetryLoading,
  telemetryError,
}: {
  overview?: GetOperationsOverviewResponse;
  loading: boolean;
  error: Error | null;
  telemetry?: GetOperationsTelemetryResponse;
  telemetryLoading: boolean;
  telemetryError: Error | null;
}) {
  if (loading) {
    return <div className="center-state">Loading operations</div>;
  }
  if (error) {
    return (
      <section className="empty-panel">Operations data unavailable.</section>
    );
  }

  return (
    <>
      <section className="summary-grid operations-summary">
        <div className="metric">
          <span>Active dashboard sessions</span>
          <strong>{overview?.activeWebSessions ?? 0}</strong>
        </div>
        <div className="metric">
          <span>Active OAuth grants</span>
          <strong>{overview?.activeOauthGrants ?? 0}</strong>
        </div>
        <div className="metric">
          <span>Recent sessions shown</span>
          <strong>{overview?.recentWebSessions.length ?? 0}</strong>
        </div>
        <div className="metric">
          <span>Recent grants shown</span>
          <strong>{overview?.recentOauthGrants.length ?? 0}</strong>
        </div>
      </section>

      <OperationsTelemetry
        telemetry={telemetry}
        loading={telemetryLoading}
        error={telemetryError}
      />

      <section className="panel operations-panel">
        <div className="panel-heading">
          <div>
            <p className="eyebrow">Browser access</p>
            <h2>Recent dashboard sessions</h2>
          </div>
          <span>Credential-free lifecycle data</span>
        </div>
        <div className="table-wrap">
          <table>
            <thead>
              <tr>
                <th>User</th>
                <th>Status</th>
                <th>Created</th>
                <th>Last seen</th>
                <th>Idle expiry</th>
                <th>Absolute expiry</th>
              </tr>
            </thead>
            <tbody>
              {(overview?.recentWebSessions ?? []).map((session) => (
                <tr key={session.id}>
                  <td>
                    <strong>{session.displayName || session.login}</strong>
                    <span className="table-secondary">{session.login}</span>
                  </td>
                  <td>
                    <Status active={session.active} />
                  </td>
                  <td className="nowrap">
                    {formatTimestamp(session.createdAt)}
                  </td>
                  <td className="nowrap">
                    {formatTimestamp(session.lastSeenAt)}
                  </td>
                  <td className="nowrap">
                    {formatTimestamp(session.idleExpiresAt)}
                  </td>
                  <td className="nowrap">
                    {formatTimestamp(session.expiresAt)}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
        {(overview?.recentWebSessions.length ?? 0) === 0 && (
          <p className="muted">No dashboard sessions recorded.</p>
        )}
      </section>

      <section className="panel operations-panel">
        <div className="panel-heading">
          <div>
            <p className="eyebrow">MCP authorization</p>
            <h2>Recent OAuth grants</h2>
          </div>
          <span>Refresh-capable grants only</span>
        </div>
        <div className="table-wrap">
          <table>
            <thead>
              <tr>
                <th>User</th>
                <th>Client</th>
                <th>Status</th>
                <th>Scope</th>
                <th>Updated</th>
                <th>Refresh expiry</th>
              </tr>
            </thead>
            <tbody>
              {(overview?.recentOauthGrants ?? []).map((grant) => (
                <tr
                  key={`${grant.userId}:${grant.clientId}:${grant.updatedAt?.seconds}`}
                >
                  <td>
                    <strong>{grant.displayName || grant.login}</strong>
                    <span className="table-secondary">{grant.login}</span>
                  </td>
                  <td>
                    <strong>{grant.clientName || "Unregistered client"}</strong>
                    <span className="table-secondary client-id">
                      {grant.clientId || "Unknown client"}
                    </span>
                  </td>
                  <td>
                    <Status active={grant.active} />
                  </td>
                  <td>
                    <code>{grant.scope}</code>
                  </td>
                  <td className="nowrap">{formatTimestamp(grant.updatedAt)}</td>
                  <td className="nowrap">
                    {formatTimestamp(grant.refreshTokenExpiresAt)}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
        {(overview?.recentOauthGrants.length ?? 0) === 0 && (
          <p className="muted">No OAuth grants recorded.</p>
        )}
      </section>
    </>
  );
}
