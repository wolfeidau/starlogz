# AWS logging and wide-event uplift

## Objective

Improve production logging and basic operational detection without introducing a general analytics platform. The uplift covers secure structured logs, privacy-safe core-flow events, API Gateway access logs, CloudWatch alarms, and SNS notifications.

## Target architecture

```text
Application stdout ----------------> CloudWatch Lambda logs
API Gateway access events ---------> CloudWatch API access logs
Core-flow events -> EventBridge bus -> CloudWatch event log group
CloudWatch alarms -----------------> SNS operations topic
                                          `-> email subscriptions
```

EventBridge is the routing plane, not the analytics store. A future rule can deliver the same events through Firehose to S3/Athena or another consumer without changing producers.

## 1. Secure application logging

Add a configurable `LOG_LEVEL`:

- AWS defaults explicitly to `INFO`.
- Local development defaults to `DEBUG`.

Never log:

- raw query strings;
- OAuth codes, state, PKCE values, or tokens;
- authorization or cookie headers;
- insight content or search queries;
- email addresses or redirect URIs;
- raw client IP addresses.

Replace the existing access record with one bounded structured event per request:

```json
{
  "level": "INFO",
  "msg": "http_request",
  "request_id": "...",
  "trace_id": "...",
  "method": "POST",
  "route": "/oauth2/token",
  "status": 200,
  "duration_ms": 387,
  "response_bytes": 412
}
```

Use the route template where possible, never the raw URL. Express durations in milliseconds. Add tests that capture log output and assert that known OAuth secrets and query values are absent.

## 2. API Gateway access logs

Create `/aws/apigateway/starlogz-${env}` with 30-day retention. Use JSON records containing only:

- API Gateway request ID;
- route key and HTTP method;
- status and response length;
- response and integration latency;
- integration status;
- domain name and protocol.

Do not include query strings, headers, authorization identity, raw IP addresses, or request and response bodies. These logs provide an independent signal when a request fails before reaching Lambda.

## 3. SNS notifications and basic alarms

Create an encrypted `starlogz-${env}-operations` SNS topic. Add an `alarm_email_endpoints` Terraform variable and create an email subscription for each address. AWS requires recipients to confirm subscriptions.

Initial alarms:

| Alarm | Initial threshold |
|---|---|
| Lambda errors | At least 1 in 5 minutes |
| Lambda throttles | At least 1 in 5 minutes |
| Lambda p95 duration | Over 2 seconds for 3 periods |
| API Gateway 5xx | At least 1 for 2 consecutive periods |
| API Gateway p95 integration latency | Over 2 seconds for 3 periods |

Missing data does not breach. Send both `ALARM` and `OK` transitions to SNS. Do not alert on aggregate 4xx initially because authentication failures and internet scanning would create noise.

Add a small CloudWatch dashboard for the same metrics. Database readiness and OAuth failure alarms are deferred until those signals are reliable.

## 4. EventBridge wide events

Create:

- custom bus `starlogz-${env}`;
- log group `/aws/events/starlogz-${env}` with 90-day retention;
- a rule matching `source = "starlogz.service"` and targeting the event log group;
- least-privilege Lambda permission for `events:PutEvents` on this bus only.

Use a versioned, bounded envelope:

```json
{
  "schema_version": 1,
  "event_id": "...",
  "event_name": "mcp.tool_call.completed",
  "occurred_at": "...",
  "environment": "dev",
  "service_version": "...",
  "request_id": "...",
  "trace_id": "...",
  "outcome": "success",
  "reason": "completed",
  "duration_ms": 18,
  "attributes": {
    "tool": "insight_search",
    "result_count_bucket": "1-10"
  }
}
```

Initial event names:

- `oauth.authorization.completed`;
- `oauth.github_callback.completed`;
- `oauth.token_exchange.completed`;
- `oauth.refresh.completed`;
- `ui.login.completed`;
- `ui.session.created`;
- `ui.session.revoked`;
- `mcp.tool_call.completed`.

Each flow emits exactly one completion event with an outcome, bounded reason, duration, safe operational attributes, and correlation identifiers. Events never contain content, queries, tags, emails, tokens, OAuth parameters, arbitrary error strings, or raw IP addresses.

### Delivery behavior

Introduce a small `EventPublisher` interface with EventBridge and no-op implementations. The EventBridge publisher uses a short bounded timeout, initially 250-500 milliseconds. A publish failure produces one warning but never changes the user response.

Use synchronous best-effort delivery initially. Background goroutines are unreliable when Lambda freezes an execution environment, while a durable queue or transactional outbox is disproportionate for this uplift.

## Verification

Before deployment:

- test log redaction and event schemas;
- test successful and failed wide events;
- run `terraform fmt` and `terraform validate`;
- review the Terraform plan for IAM scope, destinations, and secret exposure.

After deployment:

1. Confirm SNS subscriptions.
2. Exercise health, OAuth, UI, and MCP flows.
3. Confirm API Gateway JSON access events.
4. Confirm EventBridge events contain no sensitive fields.
5. Trigger a temporary test alarm and verify `ALARM` and `OK` notifications.
6. Run a CloudWatch Logs Insights scan for forbidden OAuth field patterns.
7. Confirm production application logs contain no `DEBUG` records.

## Out of scope

- Firehose, S3, Athena, or ClickHouse delivery;
- a long-term event archive;
- OpenTelemetry expansion;
- synthetic health checks;
- product analytics dashboards;
- PagerDuty or Slack integration;
- database performance instrumentation.
