# Dashboard operations view

> Status: Proposed
> Last reviewed: 2026-07-28
> Authority: Design proposal; no behavioral commitment until accepted and implemented.

## Context

The dashboard currently exposes project-scoped insight data to the owner of a
personal organization. Operators also need a service-wide view of recent
dashboard sessions, OAuth grants, and aggregate MCP tool activity.

These are different authorization domains. Organization membership and the
forward-compatible `org:admin` MCP scope do not grant service-operator access.
Dashboard sessions also do not retain OAuth scopes.

## Proposed authorization boundary

The initial service-operator role is an allowlist of stable GitHub numeric IDs
configured through `OPERATOR_GITHUB_IDS`. The server resolves the authenticated
dashboard session to its durable user record and checks the allowlist for every
operations RPC. The UI receives only an `is_operator` capability hint for
navigation; it is never the enforcement boundary.

An empty allowlist disables service-operator access. Operator responses must not
contain credentials, token hashes, JWT IDs, authorization parameters, insight
content, search queries, emails, or raw audit-log JSON.

## Proposed read model

PostgreSQL supplies recent browser sessions and OAuth grants:

- Browser-session rows omit `token_hash` and expose only identity references,
  bounded display fields, lifecycle timestamps, and derived status.
- OAuth-grant rows omit Starlogz and GitHub credentials and expose only identity
  references, client ID and registered name when available, scope, lifecycle
  timestamps, and derived status.
- The UI labels the latter as grants rather than sessions. Stateless MCP access
  tokens and clients without a refresh grant do not have a durable session row.

The existing CloudWatch wide-event log remains the proposed source for aggregate
OAuth outcomes and MCP tool calls. The server will execute fixed, bounded Logs
Insights queries, cache the aggregate result briefly, and return no raw log
events. This avoids duplicating the existing 90-day operational event store.
A PostgreSQL aggregate read model remains an option if measured query latency or
scan cost becomes material.

## Proposed UI

`/dashboard` and `/admin/operations` share one React application shell. The
operations route is visible only when `is_operator` is true and still relies on
server enforcement.

Simple categorical bars remain CSS. Time-series charts use modular D3 packages
such as `d3-scale`, `d3-shape`, and `d3-array`, with React rendering SVG
declaratively. D3 selection-based DOM mutation is excluded unless a later
interaction requires it.

## References

- [D3 in React and modular imports](https://d3js.org/getting-started)
- [CloudWatch Logs StartQuery](https://docs.aws.amazon.com/AmazonCloudWatchLogs/latest/APIReference/API_StartQuery.html)
- [CloudWatch Logs Insights](https://docs.aws.amazon.com/AmazonCloudWatch/latest/logs/AnalyzingLogData.html)
- [Wide event contract](events.md)
- [Telemetry attribution](telemetry_attribution.md)
- [Web UI sessions](web_sessions.md)
