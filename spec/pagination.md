# Cursor pagination

> Status: Current contract
> Last reviewed: 2026-07-18
> Authority: Behavioral, compatibility, and security contract; current code, migrations, and tests provide implementation evidence.

Starlogz supports cursor pagination for MCP `insight_list` and
`insight_search`, and Connect `ListInsights` and `SearchInsights`. Other
current-state collection operations retain their existing bounds. Revision
cursor behavior for MCP and Connect history reads is defined in
[Insight revisions and optimistic concurrency](insight_revisions.md).

## List contract

`cursor` is optional. Requests that omit it or send an empty value return the
first page and preserve the existing default limit of 50 and maximum limit of
200. Responses include `next_cursor` only when another row exists.

Live insights are ordered by:

```text
updated_at DESC, id DESC
```

Continuation uses the last returned tuple as an exclusive boundary. The ID
tie-breaker makes traversal deterministic when insights have the same update
timestamp. Tag filters apply to every page.

## Cursor contract

Cursors are opaque, URL-safe, stateless, versioned strings. Clients store and
return them without interpretation. Each cursor is bound to its operation,
project, and relevant filters: the optional tag for lists and the search inputs
defined below. Changed filters, malformed values, unsupported versions, and
values longer than 1024 characters return `invalid_cursor`. MCP reports a tool
error; Connect maps it to `INVALID_ARGUMENT`.

Cursors do not expire. They contain ordering state and a filter hash, not raw
queries, tags, organization IDs, or insight content. A cursor does not grant
access: each request resolves the authenticated organization and project before
the cursor is applied. Cursor values, decoded fields, filter hashes, and ranks
are excluded from logs and wide events.

The timestamp field must be present, but its value may be zero or negative.
This preserves traversal for imported insights at the Unix epoch or earlier.

## Search contract

Search cursors are bound to the project, exact query, effective query mode,
effective tag mode, and a canonical tag set. Tag order and duplicates are
ignored because they do not change PostgreSQL array containment or overlap
semantics. Existing MCP lower-casing is applied before canonicalization;
Connect retains its existing case-sensitive input behavior.

Search results are ordered by:

```text
rank DESC, updated_at DESC, id DESC
```

Rank is PostgreSQL `real`. The cursor stores its binary representation rather
than a formatted decimal, preserving the exact continuation boundary. Invalid,
non-finite, and negative rank values return `invalid_cursor`.

Search requests that omit the cursor or send an empty value preserve the
existing default and maximum limits. The addition of `updated_at` and `id`
defines only the previously unspecified order among equal-rank results.
Responses include `next_cursor` only when another row exists.

The dashboard uses cursor-backed infinite queries for list and search but only
requests another page after an explicit **Load more** action.

## Concurrent changes

Pages do not share a database snapshot. Each request observes committed state
at query time. Creating, updating, or deleting an insight between pages can
move it across the cursor boundary, so a mutating dataset can produce a skipped
or repeated row. A static matching dataset traverses without gaps or
duplicates.

The broader rationale, PostgreSQL references, and planned history work
remain in [Insight history, optimistic concurrency, and cursor pagination](insight_history_and_pagination.md).
