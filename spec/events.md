# Wide event contract

> Status: Current contract
> Last reviewed: 2026-08-19
> Authority: Behavioral contract; current code, tests, and Terraform provide implementation evidence.

Starlogz emits one bounded completion event for each recognized core OAuth, UI session, and MCP tool flow. These events provide operational counts and failure rates without storing server-derived user content or authentication material.

## Delivery

When `EVENT_BUS_NAME` is configured, the server sends events synchronously with a 400 ms timeout to the configured EventBridge bus. Publishing is best effort: a failure writes one warning and never changes the user response. When the variable is unset, the publisher is a no-op.

AWS deployments route events with source `starlogz.service` to `/aws/events/starlogz-${env}`. The EventBridge detail type equals `event_name`; the log group retains events for 90 days.

## Envelope

All events use schema version 3:

```json
{
  "schema_version": 3,
  "event_id": "0198...",
  "event_name": "mcp.tool_call.completed",
  "occurred_at": "2026-07-14T04:20:00Z",
  "environment": "dev",
  "service_version": "v0.12.0",
  "request_id": "0198...",
  "edge_request_id": "abc123",
  "trace_id": "4bf92f3577b34da6a3ce929d0e0e4736",
  "user_id": "0198...",
  "client_id": "https://client.example.com/oauth/client-metadata.json",
  "outcome": "success",
  "reason": "completed",
  "duration_ms": 18,
  "attributes": {
    "tool": "insight_search",
    "result_count_bucket": "1-10"
  },
  "telemetry": {
    "context": "Searching the project knowledge base to identify verified decisions that guide the requested implementation and prevent repeated investigation."
  }
}
```

`request_id`, `edge_request_id`, and `trace_id` are omitted when unavailable.
`attributes` is omitted for flows without approved attributes.

`mcp.tool_call.completed` always contains the canonical `user_id` from the
verified access token. It contains the canonical `client_id` when the signed
claim is available; access tokens issued before client attribution omit it.
OAuth and UI events contain neither identity field.

`edge_request_id` is the opaque API Gateway request identifier propagated by the
configured integration. It is correlation evidence only and is never used for a
security decision.

## Events

| Event name | Completion boundary |
|---|---|
| `oauth.authorization.completed` | The `/oauth2/authorize` handler completed. |
| `oauth.authorization_confirmation.completed` | The post-GitHub approval or denial handler completed. Both decisions are successful completions; malformed, rejected, expired, or replayed submissions are failures classified from HTTP status. |
| `oauth.github_callback.completed` | The GitHub callback handler completed. |
| `oauth.token_exchange.completed` | A recognized `authorization_code` token request completed. |
| `oauth.refresh.completed` | A recognized `refresh_token` request completed. |
| `ui.login.completed` | The `/login` initiation handler completed and produced an OAuth redirect. This is not an end-to-end user-login count. |
| `ui.session.created` | The UI callback completed and created a dashboard session. Use this event to count successful dashboard logins. |
| `ui.session.revoked` | The UI logout handler completed. |
| `mcp.tool_call.completed` | A registered MCP tool handler completed. |
| `operator.web_session_revoke.completed` | An operator dashboard session-revocation RPC completed. |
| `operator.oauth_grant_revoke.completed` | An operator OAuth-grant-revocation RPC completed. |

Malformed token endpoint requests are intentionally access-log-only. Wrong methods, unparseable or oversized forms, and unsupported `grant_type` values cannot be assigned truthfully to either the authorization-code or refresh flow, so the server does not guess an event name. Their HTTP status remains visible in the `http_request` access event.

## Outcomes and reasons

`outcome` is `success` or `failure`. Successful events use `completed`. Failure reasons are selected from this bounded set:

- `invalid_request`
- `unauthorized`
- `not_found`
- `method_not_allowed`
- `throttled`
- `upstream_error`
- `server_error`
- `failed`

HTTP events derive the reason from the response status. MCP tool errors use `failed`.

## Attributes

`mcp.tool_call.completed` always includes `tool`, selected from the registered tool names. No other event, including authorization confirmation, currently includes attributes.

Operator revocation events contain no attributes or identity fields. Durable,
credential-free actor and target references are stored in the same database
transaction as the revocation.

Successful `insight_history`, `insight_search`, and `insight_list` calls also
include `result_count_bucket`. The approved buckets are `0`, `1-10`, `11-50`,
`51-100`, and `101-200`. Failed calls omit the bucket because there is no valid
result set. Other tools cannot include it. `insight_restore` emits only its
bounded tool name; target and current revisions, content, keys, tags, warnings,
and actors are not event attributes.

Every MCP tool input requires a `telemetry` object with a `context` field.
`context` explains why the call supports the user's overall goal. It contains
15-25 meaningful words in third-person perspective. Callers must avoid
credentials, passwords, and personal data. `telemetry.context` is untrusted
caller-supplied prose: the server validates its structure, but does not
semantically classify sensitive content. MCP completion events include an
accepted context as `telemetry.context`; no other event includes telemetry.
The confidential wide-event log group retains this field for 90 days.

The server rejects a context outside the word range or containing first- or
second-person terms. If wrapper-level validation rejects a tool call, it emits
an MCP failure event without `telemetry`; schema-invalid requests that do not
reach a registered tool handler remain access-log-only.

Events never contain server-derived insight content, search queries, tags,
emails, tokens,
OAuth parameters, arbitrary error strings, request or response bodies, headers,
query strings, denormalized authorization identity data, or raw IP addresses.
The only identity values are the canonical `user_id` and `client_id` references
on MCP completion events.

## CloudWatch Logs Insights examples

Count successful dashboard logins:

```text
fields @timestamp, detail.event_name, detail.outcome
| filter detail.event_name = "ui.session.created" and detail.outcome = "success"
| stats count() as sessions by bin(1h)
```

Track empty searches:

```text
fields @timestamp, detail.attributes.tool, detail.attributes.result_count_bucket
| filter detail.event_name = "mcp.tool_call.completed"
  and detail.attributes.tool = "insight_search"
  and detail.outcome = "success"
| stats count() as calls by detail.attributes.result_count_bucket, bin(1h)
```

Compare MCP tool outcomes by client:

```text
fields @timestamp, detail.client_id, detail.attributes.tool, detail.outcome
| filter detail.event_name = "mcp.tool_call.completed"
| stats count() as calls by detail.client_id, detail.attributes.tool, detail.outcome
```

Find failures by flow and bounded reason:

```text
fields @timestamp, detail.event_name, detail.reason, detail.request_id
| filter detail.outcome = "failure"
| stats count() as failures by detail.event_name, detail.reason, bin(1h)
```
