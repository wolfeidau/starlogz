# AWS logging and wide-event uplift

> Status: Implemented decision
> Last reviewed: 2026-07-16
> Authority: Historical rationale and lasting constraints; current code, Terraform, tests, and the event contract define implementation details.

## Context

The service needed production diagnostics and basic operational alerting without
creating a general analytics platform or increasing exposure of OAuth and
insight data. Existing access and debug logs could contain sensitive request
values, while AWS infrastructure lacked bounded API access logs and core service
alarms.

## Decision

The implemented observability model has four bounded channels:

```text
Application stdout ----------------> CloudWatch Lambda logs
API Gateway access events ---------> CloudWatch API access logs
Core-flow events -> EventBridge ----> CloudWatch event log group
CloudWatch alarms -----------------> SNS email notifications
```

### Application logging

Production defaults to `INFO`; development defaults to `DEBUG`. HTTP access
records use route templates and bounded fields rather than raw URLs or request
data. A shared `slog.Handler` privacy boundary removes prohibited attribute keys
before JSON, OpenTelemetry, or Sentry sinks receive them.

Logs must not contain raw query strings, OAuth codes or state, PKCE values,
tokens, authorization or cookie headers, insight content, search queries,
emails, redirect URIs, raw client IPs, or raw user-agent strings. Call sites
remain responsible for safe messages and values; the privacy handler is a
defense-in-depth key filter.

### AWS access logs and alarms

API Gateway writes bounded JSON access records without headers, query strings,
bodies, authorization identity, or raw IP addresses. The log group retains
records for 30 days.

An environment-scoped SNS topic receives both `ALARM` and `OK` transitions for:

- Lambda errors and throttles;
- Lambda p95 duration;
- API Gateway 5xx responses;
- API Gateway p95 integration latency.

Missing data does not breach. Aggregate 4xx alarms were excluded because normal
authentication failures and internet scanning would create noise.

The SNS topic is intentionally unencrypted while it carries only bounded
operational metadata and delivers through email. Reconsider encryption if
notification content becomes sensitive or delivery expands.

### Wide events

Core OAuth, dashboard-session, and MCP flows publish versioned completion events
to a custom EventBridge bus. Delivery is synchronous with a short timeout and
best effort: publication failure logs one warning and never changes the user
response. A no-op publisher is used when events are disabled.

The current event schema, privacy boundary, delivery behavior, and operator
queries are maintained in [events.md](events.md). EventBridge is a routing
plane; the initial consumer is a 90-day CloudWatch log group.

## Tradeoffs

- Synchronous best-effort publishing can lose events but avoids unreliable
  background goroutines in a frozen Lambda environment.
- A transactional outbox was disproportionate for operational counts.
- Native AWS metrics and alarms were preferred over a new dashboard or
  analytics store.
- Firehose, long-term archives, product analytics, synthetic checks, database
  performance telemetry, PagerDuty, Slack, and a first-party service-status UI
  were deferred.

## Outcome

Production has privacy-filtered structured logs, bounded API Gateway access
logs, environment-scoped alarms and notifications, and content-free core-flow
events. Current implementation evidence is in `internal/logattr/`,
`internal/middleware/`, `internal/wideevent/`, and `infra/terraform/`.
