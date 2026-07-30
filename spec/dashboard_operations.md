# Dashboard operations view

> Status: Current contract
> Last reviewed: 2026-07-29
> Authority: Behavioral and security contract; current code, migrations, and tests provide implementation evidence.

## Authorization boundary

Service-operator access is separate from organization membership and OAuth
scopes. The `org:admin` MCP scope does not grant access to service operations.

`OPERATOR_GITHUB_IDS` configures an allowlist of stable GitHub numeric user IDs.
The server resolves each authenticated dashboard session to its durable user
record and enforces this allowlist on every operations RPC. An empty allowlist
disables operator access.

The dashboard session response includes `is_operator` so the React application
can show operator navigation. This capability hint is not an authorization
boundary; a direct request from a non-operator is denied by the server.

## Read model

The operations overview returns active counts and bounded, most-recent lists for
browser sessions and refresh-capable OAuth grants. A non-positive or greater
than 100 requested limit is replaced with 50.

Browser-session summaries include identity references, bounded display fields,
lifecycle timestamps, revocation state, and derived active status. They omit
the session token hash.

OAuth-grant summaries include identity references, client ID and registered
name when available, scope, lifecycle timestamps, and derived active status.
They omit the Starlogz refresh token, GitHub credentials, token ciphertext, and
JWT ID. The grants table contains only refresh-capable credentials; stateless
access-token JWTs do not create a grant row.

Operator responses do not contain authorization parameters, insight content,
search queries, emails, or raw audit-log JSON.

## UI

`/dashboard` and `/admin/operations` use one React application shell. Operator
navigation is shown only when `is_operator` is true. The operations route
displays active counts and recent browser-session and OAuth-grant tables.

Tool-call and OAuth outcome aggregates are supplied by a separate, optional
CloudWatch read model so query failures do not affect PostgreSQL-backed session
and grant data. Its query, caching, and visualization behavior is described in
[dashboard_operations_telemetry.md](dashboard_operations_telemetry.md).

## Related contracts

- [OAuth2 authentication and authorization](auth.md)
- [OAuth2 refresh-token grant](refresh_tokens.md)
- [Web UI sessions](web_sessions.md)
