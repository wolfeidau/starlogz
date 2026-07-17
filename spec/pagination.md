# Cursor pagination

> Status: Current contract
> Last reviewed: 2026-07-18
> Authority: Behavioral, compatibility, and security contract; current code, migrations, and tests provide implementation evidence.

Starlogz supports cursor pagination for MCP `insight_list` and Connect
`ListInsights`. Search and other collection operations retain their existing
bounds until their pagination contracts are implemented.

## List contract

`cursor` is optional. Requests without it return the first page and preserve
the existing default limit of 50 and maximum limit of 200. Responses include
`next_cursor` only when another row exists.

Live insights are ordered by:

```text
updated_at DESC, id DESC
```

Continuation uses the last returned tuple as an exclusive boundary. The ID
tie-breaker makes traversal deterministic when insights have the same update
timestamp. Tag filters apply to every page.

## Cursor contract

Cursors are opaque, URL-safe, stateless, versioned strings. Clients store and
return them without interpretation. A cursor is bound to the list operation,
project, and tag filter. Changed filters, malformed values, unsupported
versions, and values longer than 1024 characters return `invalid_cursor`.
MCP reports a tool error; Connect maps it to `INVALID_ARGUMENT`.

Cursors do not expire. They contain ordering state and a filter hash, not raw
tags, organization IDs, or insight content. A cursor does not grant access:
each request resolves the authenticated organization and project before the
cursor is applied. Cursor values and decoded fields are excluded from logs and
wide events.

The timestamp field must be present, but its value may be zero or negative.
This preserves traversal for imported insights at the Unix epoch or earlier.

## Concurrent changes

Pages do not share a database snapshot. Each request observes committed state
at query time. Creating, updating, or deleting an insight between pages can
move it across the cursor boundary, so a mutating dataset can produce a skipped
or repeated row. A static matching dataset traverses without gaps or
duplicates.

The broader rationale, PostgreSQL references, and planned search/history work
remain in [Insight history, optimistic concurrency, and cursor pagination](insight_history_and_pagination.md).
