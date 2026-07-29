# Dashboard operations telemetry

> Status: Proposed
> Last reviewed: 2026-07-29
> Authority: Design proposal; no behavioral commitment until accepted and implemented.

## Context

The current [dashboard operations view](dashboard_operations.md) exposes recent
dashboard sessions and refresh-capable OAuth grants from PostgreSQL. Operators
also need aggregate OAuth outcomes and MCP tool activity without exposing raw
event payloads or duplicating the operational event store.

## Proposed read model

Use the existing CloudWatch wide-event log as the source for aggregate OAuth
outcomes and MCP tool calls. The server executes fixed, bounded Logs Insights
queries, briefly caches aggregate results, and returns no raw log events.

A PostgreSQL aggregate read model remains an option if measured query latency
or scan cost becomes material.

## Proposed visualization

Keep simple categorical bars in CSS. Use direct modular D3 packages such as
`d3-scale`, `d3-shape`, and `d3-array` for time-series calculations, while
React renders the SVG declaratively. Avoid D3 selection-based DOM mutation
unless a later interaction demonstrates a need for it.

This keeps the dependency and runtime surface small without introducing a
charting framework. Bundle size and accessibility must be verified with the
first implemented chart.

## References

- [D3 in React and modular imports](https://d3js.org/getting-started)
- [CloudWatch Logs StartQuery](https://docs.aws.amazon.com/AmazonCloudWatchLogs/latest/APIReference/API_StartQuery.html)
- [CloudWatch Logs Insights](https://docs.aws.amazon.com/AmazonCloudWatch/latest/logs/AnalyzingLogData.html)
- [Wide event contract](events.md)
- [Telemetry attribution](telemetry_attribution.md)
