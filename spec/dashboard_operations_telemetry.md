# Dashboard operations telemetry

> Status: Current contract
> Last reviewed: 2026-07-29
> Authority: Behavioral and security contract; current code, Terraform, and tests provide implementation evidence.

## Read model

The operator dashboard exposes aggregate OAuth, dashboard-login, and MCP
tool-call activity from the existing CloudWatch wide-event log. Telemetry uses
a separate `GetOperationsTelemetry` RPC so CloudWatch failure does not prevent
the PostgreSQL-backed operations overview from loading.

The server executes fixed Logs Insights queries over the preceding 24 hours.
Callers cannot supply query text, log groups, timestamps, bucket sizes, or
limits. Results are cached in each server process for 60 seconds. Each refresh
has a five-second deadline and attempts to stop queries that remain incomplete.

The response contains:

- total and failed MCP tool calls;
- MCP tool-call p95 duration;
- successful dashboard-session creation count;
- hourly successful and failed MCP tool-call buckets;
- per-tool call and failure counts; and
- OAuth success, failure, and bounded failure-reason counts.

The response never contains raw log events, `@ptr`, user or client identity,
request IDs, queries, tokens, credentials, insight content, or arbitrary error
strings. The existing operator allowlist is enforced on the RPC.

When `OPERATIONS_LOG_GROUP_NAME` is empty, the RPC returns telemetry as
unavailable without loading AWS configuration. Terraform configures the
wide-event log group and grants the Lambda role only the query actions required
to start, retrieve, and stop Logs Insights queries.

## Visualization

The dashboard shows summary cards, an hourly tool-call time series, per-tool
bars, OAuth flow outcomes, and bounded OAuth failure reasons.

Time-series calculations use direct modular imports from `d3-array`,
`d3-scale`, and `d3-shape`. React renders the SVG declaratively. D3 does not
mutate the DOM.

## References

- [D3 in React and modular imports](https://d3js.org/getting-started)
- [CloudWatch Logs StartQuery](https://docs.aws.amazon.com/AmazonCloudWatchLogs/latest/APIReference/API_StartQuery.html)
- [CloudWatch Logs Insights](https://docs.aws.amazon.com/AmazonCloudWatch/latest/logs/AnalyzingLogData.html)
- [Wide event contract](events.md)
- [Telemetry attribution](telemetry_attribution.md)
