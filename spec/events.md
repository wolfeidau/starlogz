# Wide event contract

> Status: Current contract
> Last reviewed: 2026-07-19
> Authority: Behavioral contract; current code, tests, and Terraform provide implementation evidence.

Starlogz emits one bounded completion event for each recognized core OAuth, UI session, and MCP tool flow. These events provide operational counts and failure rates without storing user content or authentication material.

## Delivery

When `EVENT_BUS_NAME` is configured, the server sends events synchronously with a 400 ms timeout to the configured EventBridge bus. Publishing is best effort: a failure writes one warning and never changes the user response. When the variable is unset, the publisher is a no-op.

AWS deployments route events with source `starlogz.service` to `/aws/events/starlogz-${env}`. The EventBridge detail type equals `event_name`; the log group retains events for 90 days.

## Envelope

All events use schema version 1:

```json
{
  "schema_version": 1,
  "event_id": "0198...",
  "event_name": "mcp.tool_call.completed",
  "occurred_at": "2026-07-14T04:20:00Z",
  "environment": "dev",
  "service_version": "v0.12.0",
  "request_id": "0198...",
  "trace_id": "4bf92f3577b34da6a3ce929d0e0e4736",
  "outcome": "success",
  "reason": "completed",
  "duration_ms": 18,
  "attributes": {
    "tool": "insight_search",
    "result_count_bucket": "1-10"
  }
}
```

`request_id` and `trace_id` are omitted when unavailable. `attributes` is omitted for flows without approved attributes.

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

Successful `insight_history`, `insight_search`, and `insight_list` calls also
include `result_count_bucket`. The approved buckets are `0`, `1-10`, `11-50`,
`51-100`, and `101-200`. Failed calls omit the bucket because there is no valid
result set. Other tools cannot include it. `insight_restore` emits only its
bounded tool name; target and current revisions, content, keys, tags, warnings,
and actors are not event attributes.

Events never contain insight content, search queries, tags, emails, tokens, OAuth parameters, arbitrary error strings, request or response bodies, headers, query strings, authorization identities, or raw IP addresses.

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

Find failures by flow and bounded reason:

```text
fields @timestamp, detail.event_name, detail.reason, detail.request_id
| filter detail.outcome = "failure"
| stats count() as failures by detail.event_name, detail.reason, bin(1h)
```
