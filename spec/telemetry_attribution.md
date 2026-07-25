# Telemetry attribution and edge request context

> Status: Implemented decision
>
> Last reviewed: 2026-07-25
>
> Authority: Historical rationale and lasting constraints; current contracts, code, tests, and Terraform define implementation details.

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

This decision narrowly supersedes the API Gateway raw-IP restriction recorded
in [observability_uplift.md](observability_uplift.md) for development only. Its
prohibition on raw IP addresses and raw user-agent strings in application logs
remains unchanged.

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
- Adding identity references to OAuth or UI completion events.
- Measuring agent or model quality; this decision adds attribution, not
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

An existing refresh grant without a stored `client_id` has no authoritative
client binding. The token endpoint MUST reject it with `invalid_grant` and
require reauthorization. It MUST NOT copy the caller-supplied `client_id` into a
new token or rotated grant.

This compatibility rule may be removed in a later change after the maximum
access-token lifetime has elapsed.

### 2. Add identity references to authenticated completion events

Wide-event schema version 2 adds these optional top-level fields:

| Field | Source | Semantics |
|---|---|---|
| `user_id` | Verified `auth.TokenInfo.UserID` | Canonical user UUID |
| `client_id` | Verified `auth.TokenInfo.Extra["client_id"]` | Canonical OAuth client identifier |

An authenticated completion event MUST include each field when the handler has
an authoritative value. It MUST omit a field when the value is unavailable; it
MUST NOT infer, hash, or substitute a display value. In particular, legacy
tokens without the new claim produce events with `user_id` but without
`client_id`.

The required event is `mcp.tool_call.completed`, because every registered MCP
tool enters with verified token information. OAuth and UI completion events are
outside this decision. A later expansion MUST define the session-to-identity
mapping in [web_sessions.md](web_sessions.md) and update the event contract.

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

Terraform MUST add these fields only when `var.env == "dev"`. Every other
environment retains the existing bounded access-log format without raw source IP
or raw user-agent. Enabling either field outside development requires a separate
accepted decision covering retention, access, and incident-response policy.

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

Resource-level `data_classification` tags augment the provider's existing
`application`, `environment`, `branch`, and `component` default tags; they do
not replace them.

The affected CloudWatch log groups MUST be tagged as follows:

| Log group | Environment | Classification | Rationale |
|---|---|---|---|
| API Gateway access logs | Development | `confidential` | Contains source IP and raw user-agent |
| API Gateway access logs | Other | `internal` | Retains the existing bounded operational fields |
| Wide events | All | `confidential` | Contains canonical user and client identifiers |
| Lambda application logs | All | `confidential` | Existing authenticated logs contain canonical user identifiers |

`data_category` and `data_purpose` are deferred until a concrete operational or
governance requirement justifies them. This decision does not require
classifying unrelated AWS resources.

## Tradeoffs

- Access tokens issued before this decision remain valid until expiry, but an
  unbound legacy refresh grant forces reauthorization rather than promoting an
  untrusted caller-supplied client ID.
- Canonical identity references make Lambda and wide-event log groups
  confidential and require an authorized database lookup to resolve user or
  client details.
- Raw IP and user-agent evidence is restricted to the development API Gateway
  log group and its existing 30-day retention.
- Keeping the application UUID and API Gateway request ID separate adds one
  field but preserves existing request semantics while enabling correlation.

## Outcome

New access tokens bind the canonical client identifier and verified MCP
completion events expose canonical user and client references through wide-event
schema version 2. API Gateway overwrites a correlation header that becomes
`edge_request_id` in application logs and wide events.

Development API Gateway access logs retain raw source IP and user-agent; other
environments retain the bounded format. API Gateway, Lambda, and wide-event log
groups carry the classification tags defined above. Current authentication,
refresh, and event behavior is maintained in [auth.md](auth.md),
[refresh_tokens.md](refresh_tokens.md), and [events.md](events.md).

## References

- [AWS API Gateway variables for access logging](https://docs.aws.amazon.com/apigateway/latest/developerguide/http-api-logging-variables.html)
- [AWS API Gateway HTTP API parameter mapping](https://docs.aws.amazon.com/apigateway/latest/developerguide/http-api-parameter-mapping.html)
- [AWS tagging best practices](https://docs.aws.amazon.com/whitepapers/latest/tagging-best-practices/)
- [Terraform AWS provider resource tagging](https://registry.terraform.io/providers/hashicorp/aws/latest/docs/guides/resource-tagging.html)
