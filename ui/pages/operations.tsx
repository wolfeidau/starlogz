import type { GetOperationsOverviewResponse } from "../../api/gen/proto/es/starlogz/v1/ui_pb";
import { formatTimestamp } from "./insight_content";

function Status({ active }: { active: boolean }) {
  return (
    <span className={`status ${active ? "status-active" : "status-inactive"}`}>
      {active ? "Active" : "Inactive"}
    </span>
  );
}

export function OperationsView({
  overview,
  loading,
  error,
}: {
  overview?: GetOperationsOverviewResponse;
  loading: boolean;
  error: Error | null;
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

      <section className="panel operations-panel operations-next">
        <p className="eyebrow">Next</p>
        <h2>Tool-call aggregates</h2>
        <p className="muted">
          The next slice will add fixed-window CloudWatch aggregates and modular
          D3 time-series charts.
        </p>
      </section>
    </>
  );
}
