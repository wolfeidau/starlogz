# Telemetry attribution and edge request context

> Status: Proposed
>
> Last reviewed: 2026-07-25
>
> Authority: Design proposal; not an implementation commitment until accepted and implemented.

## Context

The current telemetry shows aggregate service outcomes but cannot reliably
attribute authenticated activity to a client or correlate an application event
with its API Gateway access record.

The database remains the source of truth for user and client details. Telemetry
must use canonical identifiers and must not duplicate denormalized values such
as email addresses or client names.

API Gateway currently discards the raw source IP address and user-agent from its
access log. These values are imperfect identifiers, but they are useful evidence
for abuse monitoring and retrospective incident analysis in the development
environment.

## Goals

1. Bind the canonical OAuth client identifier into access tokens.
2. Attribute authenticated completion events to canonical user and client
   identifiers.
3. Retain source IP and raw user-agent in API Gateway access logs.
4. Correlate API Gateway access records with application wide events.

## Non-goals

- Duplicating email addresses, client names, or other identity profile data in
  logs or events.
- Recording tokens, authorization headers, cookies, OAuth parameters, request
  or response bodies, search terms, insight content, or arbitrary query strings.
- Treating source IP or user-agent as authenticated identity.
- Adding encryption keys, log masking, break-glass roles, or other security-log
  controls in this development-only iteration.
- Defining production retention, access, or incident-response policy.
- Measuring agent or model quality; this proposal adds attribution, not
  evaluation semantics.

## Decisions

### 1. Bind `client_id` into access tokens

New access tokens MUST contain a private `client_id` claim. Its value MUST be the
exact canonical client identifier bound to the authorization grant or refresh
grant. This applies to both database-backed DCR clients and CIMD clients; it does
not require a client database row.

The signed claim is the request-time source of client attribution. JWT
verification MUST expose it as `auth.TokenInfo.Extra["client_id"]`, avoiding a
database lookup on each authenticated MCP request.

During rollout, an otherwise valid access token without `client_id` MUST remain
valid. Such a request has an unknown client for telemetry purposes, so the
`client_id` event field is omitted. All newly issued and refreshed access tokens
MUST include the claim. If the claim is present but is empty, has the wrong type,
or exceeds 2048 bytes, verification MUST fail.

This compatibility rule may be removed in a later change after the maximum
access-token lifetime has elapsed.

### 2. Add identity references to authenticated completion events

Wide-event schema version 2 adds these optional top-level fields:

| Field | Source | Semantics |
|---|---|---|
| `user_id` | Verified JWT subject or an equivalent authenticated session | Canonical user UUID |
| `client_id` | Verified signed JWT claim or an equivalent authenticated session | Canonical OAuth client identifier |

An authenticated completion event MUST include each field when the handler has
an authoritative value. It MUST omit a field when the value is unavailable; it
MUST NOT infer, hash, or substitute a display value. In particular, legacy
tokens without the new claim produce events with `user_id` but without
`client_id`.

The initial required event is `mcp.tool_call.completed`, because every registered
MCP tool enters with verified token information. OAuth and UI completion events
MAY include the same fields when both the event boundary and implementation have
an authoritative value, but expanding those events is not required by this
proposal.

Identity references belong in the event envelope rather than the bounded
event-specific attributes map. Existing tool attributes such as `tool` and
`result_count_bucket` remain unchanged.

Events MUST NOT include user email, client name, GitHub login, authorization
identity payloads, or other denormalized identity details. Investigators may
resolve the canonical identifiers against the database when authorized.

### 3. Retain edge source context in API Gateway access logs

The API Gateway JSON access-log format MUST add:

| Log field | API Gateway variable |
|---|---|
| `source_ip` | `$context.identity.sourceIp` |
| `user_agent` | `$context.identity.userAgent` |

`source_ip` records the immediate peer observed by API Gateway. `user_agent` is
untrusted caller input. Neither value may be used for authentication or
authorization.

The existing application access log continues to record bounded, parsed
user-agent dimensions for operational aggregation. The raw user-agent is
retained only in the API Gateway access log in this iteration.

The existing 30-day development retention remains unchanged.

### 4. Propagate the edge request identifier

The API Gateway integration MUST overwrite the
`x-starlogz-edge-request-id` request header with
`$context.requestId`.

Application middleware MUST:

- read the propagated value;
- treat it as an opaque value and accept it only when it is non-empty, at most
  128 bytes, valid UTF-8, and contains no control characters;
- store it in request context independently of the application-generated
  `request_id`; and
- emit it as optional top-level `edge_request_id` in access logs and wide
  events.

The application-generated UUID remains the service request identifier and MUST
NOT be replaced. The edge identifier is correlation evidence only and MUST NOT
influence authentication, authorization, rate limiting, or other security
decisions. API Gateway's overwrite mapping prevents a caller-provided value
from reaching Lambda in the AWS deployment; middleware cannot independently
establish that provenance.

API Gateway access logs continue to record `$context.requestId` as
`request_id`. An investigator can therefore search both log groups for the same
value using:

```text
API Gateway request_id = application edge_request_id
```

Requests without the propagated header, including normal local tests, omit
`edge_request_id` and otherwise behave normally.

## Data classification tags

This iteration uses one lower-case AWS resource tag:

```text
data_classification = public | internal | confidential | restricted
```

The tag is inventory metadata, not an access control.

The affected CloudWatch log groups MUST be tagged as follows:

| Log group | Classification | Rationale |
|---|---|---|
| API Gateway access logs | `confidential` | Contains source IP and raw user-agent |
| Wide events | `confidential` | Contains canonical user and client identifiers |
| Lambda application logs | `confidential` | Existing authenticated logs contain canonical user identifiers |

`data_category` and `data_purpose` are deferred until a concrete operational or
governance requirement justifies them. This proposal does not require
classifying unrelated AWS resources.

## Implementation sequence

1. Add optional claim verification and `TokenInfo.Extra` propagation first, then
   add `client_id` to every access-token issuance and refresh path.
2. Add wide-event schema version 2 identity and edge fields, request-context
   propagation, and validation.
3. Update the API Gateway access-log format and integration parameter mapping.
4. Apply `data_classification = confidential` to the three log groups.
5. Deploy to development and verify correlation and legacy-token compatibility
   before considering stricter claim enforcement or production rollout.

Implementation of this proposal MUST update the current contracts in
`spec/auth.md` and `spec/events.md`.

## Repository evidence

- `internal/oidc/server.go` signs and verifies access tokens but does not
  currently issue or expose `client_id`.
- `internal/store/store.go` persists the canonical client identifier on OAuth
  grants; CIMD uses the resolved URL directly as that identifier.
- `internal/server/tools.go` receives verified `auth.TokenInfo` for every
  registered MCP tool and emits `mcp.tool_call.completed`.
- `internal/wideevent/event.go` enforces schema version 1 and has no identity or
  edge request fields.
- `internal/middleware/access_log.go` generates an application UUID and records
  parsed user-agent dimensions but not the raw edge values.
- `infra/terraform/apigw.tf` records the API Gateway request ID but not source
  IP, raw user-agent, or an integration request-header mapping.

## Acceptance criteria

- Access tokens issued by authorization-code and refresh flows contain the
  canonical `client_id`.
- JWT verification exposes a valid claim through
  `auth.TokenInfo.Extra["client_id"]`.
- A valid legacy token without the claim remains accepted and produces no
  `client_id` event field.
- Authenticated MCP completion events contain the canonical `user_id` and, for
  new tokens, `client_id`; they contain no email address or client display data.
- API Gateway access records contain `source_ip` and raw `user_agent`.
- The API Gateway `request_id` equals the corresponding application
  `edge_request_id`.
- Direct or local requests without an edge identifier continue to work.
- The three affected CloudWatch log groups have
  `data_classification = confidential`.
- Automated tests cover token issuance, verification, legacy compatibility,
  malformed claims, event serialization, header validation, and missing-header
  behavior.
- Terraform validation and planning complete without an unexpected resource
  replacement.

## References

- [AWS API Gateway variables for access logging](https://docs.aws.amazon.com/apigateway/latest/developerguide/http-api-logging-variables.html)
- [AWS API Gateway HTTP API parameter mapping](https://docs.aws.amazon.com/apigateway/latest/developerguide/http-api-parameter-mapping.html)
- [AWS tagging best practices](https://docs.aws.amazon.com/whitepapers/latest/tagging-best-practices/)
