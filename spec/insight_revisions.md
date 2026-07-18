# Insight revisions and optimistic concurrency

> Status: Current contract
> Last reviewed: 2026-07-18
> Authority: Behavioral, compatibility, and security contract; current code, migrations, and tests provide implementation evidence.

Starlogz records accepted insight state changes as immutable full snapshots and
exposes a positive revision number for optional optimistic concurrency. History
reads are bounded and authorized; restore operations are not part of this
contract.

## Revision model

Every insight has a positive integer `revision`. New and migrated insights start
at revision 1. Each accepted state change increments the current revision once
and inserts one snapshot with the same `(insight_id, revision)` identity.

Snapshots retain key, content, tags, category, source, deletion state, operation,
actor when known, and change time. Supported operations for current mutation
paths are `create`, `baseline`, `update`, and `delete`. Migration baselines use a
null actor because the legacy schema cannot identify the latest editor. Soft
deletion retains snapshots; hard deletion cascades to them.

Current-state list and search remain backed by `insights`. The revision ledger
does not change current search ranking or pagination.

## Mutation atomicity

Creates, keyed upserts, updates, imports, and soft deletes write their current
row and snapshot in one transaction. Content-bearing writes synchronize derived
insight links in that transaction. A failure in the current-row mutation, link
synchronization, or snapshot insertion rolls back the entire mutation.

The authenticated actor is recorded for interactive mutations. Import records
the importing user. Removing a user sets historical actor references to null so
revision retention does not prevent identity erasure.

## Semantic no-ops

A semantic no-op compares the normalized persisted fields affected by the
request. It does not update `updated_at`, increment `revision`, or insert a
snapshot. An optional revision precondition is checked before no-op detection.

A content-bearing no-op still repairs the derived `insight_links` projection in
the mutation transaction. This preserves lazy extraction for content that
predates link storage. A keyed `insight_write` regenerates link warnings;
`insight_update` continues to omit warnings when persisted content is unchanged.
Tag-only no-ops do not touch relationships.

## Revision exposure

Current insight representations include `revision`. MCP returns it from:

- `insight_write`;
- `insight_get`;
- `insight_list`;
- `insight_search`; and
- `insight_update`.

`insight_delete` returns the resulting deletion revision. The Connect `Insight`
message also includes revision for current read responses. Connect has no insight
mutation RPC in this contract.

## History reads

MCP `insight_history` and Connect `ListInsightHistory` list immutable snapshots
for one insight ID. History uses the insight ID rather than key because a key can
be reused after soft deletion. Both operations require an authenticated caller,
resolve the caller's organization and project first, and return the same
not-found behavior for missing, hard-deleted, and inaccessible insights.

Soft-deleted insights remain readable through history. Responses include the
insight ID and key, current revision, current deletion state, bounded full
snapshots, and an optional `next_cursor`. Each snapshot contains revision,
operation, key, content, tags, category, source, deletion time, nullable actor
UUID, and change time. Connect additionally returns server-sanitized rendered
Markdown for each snapshot.

Revisions are ordered by `revision DESC`. The default limit is 20 and the
maximum is 100. Pagination fetches `limit + 1` and continues with revisions less
than the last returned revision. The `(insight_id, revision)` primary key
supports this scan without another index.

History cursors are opaque, URL-safe, stateless, versioned, and bound to the
operation, project, and insight ID. Empty cursors request the first page.
Malformed, oversized, wrong-version, wrong-operation, or scope-mismatched
cursors return `invalid_cursor`; Connect maps this to `INVALID_ARGUMENT`.

Each page observes committed state at query time. A revision appended between
pages has a higher number than the continuation boundary, so it does not repeat
or displace older revisions on later pages. The response's current revision and
deletion state may reflect a newer mutation than the first page.

History content, actors, cursor values, and snapshot fields are excluded from
logs and wide events. Successful MCP calls emit only the tool name and a bounded
result-count bucket.

## Optional preconditions

Existing MCP mutation tools accept optional `expected_revision`:

- keyed `insight_write`: omission preserves last-write-wins behavior, `0`
  requires no live insight with that key, and a positive value requires an exact
  current revision;
- keyless `insight_write`: omission or `0` creates a new insight; a positive
  value is invalid; and
- `insight_update` and `insight_delete`: omission preserves existing behavior,
  a positive value requires an exact current revision, and `0` is invalid.

The live target is locked before comparing the precondition and remains locked
through link synchronization, snapshot insertion, and commit. A mismatch does
not mutate insight fields, timestamps, links, or snapshots.

MCP reports a mismatch as a tool execution error with `isError: true` and this
bounded JSON body:

```json
{
  "code": "revision_conflict",
  "expected_revision": 2,
  "current_revision": 3
}
```

A missing keyed target has current revision `0`. Conflict responses and
telemetry exclude insight content, keys, tags, and actor identifiers.

The design rationale, migration plan, and proposed restore behavior
remain in [Insight history, optimistic concurrency, and cursor pagination](insight_history_and_pagination.md).
