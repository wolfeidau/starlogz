# Insight history, optimistic concurrency, and cursor pagination

> Status: Proposed
> Last reviewed: 2026-07-18
> Authority: Design and delivery proposal; not an implementation commitment.

## Summary

Starlogz should add cursor pagination before introducing insight history so every
new collection is bounded from its first release. It should then add a
first-class revision ledger, optimistic concurrency, and restore operations.

The proposal keeps `insights` as the indexed current-state projection and adds a
typed, append-only `insight_revisions` table. Existing insight UUIDv7 identifiers
remain the public identity. A revision is identified by the composite
`(insight_id, revision)` key; it does not receive another UUID.

Existing clients retain their current first-page and last-write-wins behavior
unless they opt into cursors or `expected_revision`. New history and restore
operations use the same organization and project authorization boundaries as
the existing insight tools.

## Context

At the proposal baseline, list and search operations accepted only a limit.
`insight_list` ordered by `updated_at DESC` without a deterministic tie-breaker,
while `insight_search` ordered only by text-search rank. Neither operation could
continue beyond its first bounded result set.

Keyed `insight_write` calls overwrite the current row. `insight_update` and
`insight_delete` likewise mutate the current state without an application-facing
history or concurrency precondition. The generic `audit_log` records old and new
row JSON, but it is an operational mechanism rather than a typed, authorized,
restorable product contract.

Linked insight content already synchronizes outgoing relationships in the same
transaction as content mutations. Revision creation and restoration must
preserve that atomicity.

## Implementation progress

The first four rollout slices are code-complete: MCP and Connect list and search
operations accept opaque, filter-bound cursors; list traversal uses
`updated_at DESC, id DESC`; search traversal uses lossless PostgreSQL `real`
rank followed by the same deterministic tie-breakers; and both fetch
`limit + 1`. Migration 18 adds the matching partial live-row list index using a
retry-safe concurrent build. The dashboard continues both result types through
an explicit **Load more** action. Migration 19 adds the current revision value,
typed revision ledger, and baseline snapshots. Existing mutation paths now
write snapshots atomically, suppress semantic no-ops, expose revisions, and
accept optional concurrency preconditions. Slices 3 and 4 form one releasable
unit, but production release remains gated on the migration measurements below.
MCP and Connect history reads and MCP restore are also implemented.
The dashboard now exposes Connect history through a read-only, explicitly
paginated revision panel. Dashboard restore is outside this proposal because the
current dashboard does not need a restore workflow.
The dev database parity check after migration 19 matched every accepted
revision to exactly one insight audit row with no persisted-state mismatches.
Migration 20 removes the redundant `audit_insights` trigger while preserving
the audit table and all other audit triggers.
The implemented pagination behavior is authoritative in
[Cursor pagination](pagination.md); implemented revision, concurrency, and
history-read and restore behavior is authoritative in
[Insight revisions and optimistic concurrency](insight_revisions.md).

## Goals

- Provide deterministic, stateless continuation for insight list, search, and
  history results.
- Keep page cost bounded as a project grows; do not introduce offset scans.
- Preserve every accepted insight state change as an immutable, restorable
  revision.
- Detect lost updates when a caller supplies an expected revision.
- Preserve compatibility for callers that do not yet send cursors or revision
  preconditions.
- Keep current-state list and search queries independent of history-table size.
- Preserve project and organization isolation, content privacy, and bounded
  telemetry.
- Retain a documented evidence trail for material storage and indexing choices.

## Non-goals

- Searching historical content.
- Delta encoding, event sourcing, or reconstructing current state by replay.
- Valid-time or bitemporal business semantics.
- Cross-project history or restore.
- Pagination for projects, tags, outgoing links, or backlinks in this effort.
- Automatic history expiration or partitioning before measured scale requires
  it.
- Changing public insight UUIDs or adding a UUID to each revision.
- General dashboard authoring, including dashboard restore.

## Evidence and design provenance

### Established patterns and PostgreSQL behavior

System-versioned temporal databases commonly separate a current table from a
history table. [SQL Server temporal tables](https://learn.microsoft.com/en-us/sql/relational-databases/tables/temporal-tables?view=sql-server-ver17)
implement this directly, and the
[PostgreSQL SQL:2011 temporal proposal](https://wiki.postgresql.org/wiki/SQL2011Temporal)
describes the same current/history structure. PostgreSQL 18 lists
[system-versioned tables as unsupported](https://www.postgresql.org/docs/current/unsupported-features-sql-standard.html),
so Starlogz must implement its product semantics in application transactions.

PostgreSQL's [audit-trigger example](https://wiki.postgresql.org/wiki/Audit_trigger)
demonstrates append-only capture of row changes. Starlogz specializes that
pattern into typed snapshots with domain constraints, authorization, bounded
reads, and restore behavior.

PostgreSQL documents that:

- `LIMIT` requires a unique order for predictable subsets, and large `OFFSET`
  values remain inefficient because skipped rows are still computed
  ([LIMIT and OFFSET](https://www.postgresql.org/docs/current/queries-limit.html));
- row constructors support lexicographic B-tree comparisons suitable for
  keyset predicates
  ([row constructor comparison](https://www.postgresql.org/docs/current/functions-comparisons.html#ROW-WISE-COMPARISON));
- B-tree indexes can satisfy `ORDER BY ... LIMIT` directly and can scan in both
  directions
  ([indexes and ordering](https://www.postgresql.org/docs/current/indexes-ordering.html));
- concurrent index builds avoid blocking inserts, updates, and deletes, but
  cannot run inside a transaction block
  ([`CREATE INDEX`](https://www.postgresql.org/docs/current/sql-createindex.html));
- multicolumn B-tree indexes are most effective when predicates constrain their
  leading columns
  ([multicolumn indexes](https://www.postgresql.org/docs/current/indexes-multicolumn.html));
- conditional `UPDATE` statements and `RETURNING` can atomically identify the
  row that was changed
  ([UPDATE](https://www.postgresql.org/docs/current/sql-update.html)).

### Repository evidence and local measurements

- Insights already use native UUIDv7 primary keys.
- Current insight list and search limits are 200 and 100 respectively.
- The current list query lacks an ID tie-breaker; search lacks rank tie-breakers.
- The current audit trigger retains both old and new insight JSON for updates.
- Current content changes and insight-link synchronization share one transaction.
- Local PostgreSQL 18 measurements reported UUID as 16 bytes, bigint as 8 bytes,
  and integer as 4 bytes. In the small local dataset, replacing an insight UUID
  with bigint changed a representative roughly 1.2 KB revision record by 8
  bytes, about 0.7%.

The local measurement is directional, not a production capacity benchmark.
PostgreSQL's documentation for [UUID](https://www.postgresql.org/docs/current/datatype-uuid.html),
[numeric types](https://www.postgresql.org/docs/current/datatype-numeric.html),
[page layout](https://www.postgresql.org/docs/current/storage-page-layout.html),
and [TOAST](https://www.postgresql.org/docs/current/storage-toast.html) provides
the authoritative storage behavior.

### Starlogz design judgments

- Full typed snapshots are preferred over deltas because restore is direct,
  each revision is independently valid, and no replay chain can be damaged.
- A composite `(insight_id, revision)` key matches the only planned history
  access path and avoids a redundant revision identifier and index.
- Historical content should not carry full-text or tag indexes. Search remains a
  current-state projection.
- Cursor tokens should be opaque, stateless, versioned, and bound to normalized
  request filters. They need validation but not encryption because every query
  independently enforces organization and project scope.
- Partitioning and structural content sharing should follow measurements, not be
  introduced speculatively. PostgreSQL's
  [partitioning guidance](https://www.postgresql.org/docs/current/ddl-partitioning.html)
  warns that partitioning has planning and operational costs and benefits very
  large tables with pruneable access patterns.

## Cursor pagination contract

### Scope and compatibility

Cursor pagination applies to:

- MCP `insight_list`;
- MCP `insight_search`;
- Connect `ListInsights`;
- Connect `SearchInsights`; and
- MCP and Connect insight-history operations.

`cursor` is an optional input. `next_cursor` is an additive response field and
is omitted when no later page exists. A request without a cursor returns the
same logical first page as today, subject to the deterministic tie-breakers
defined below. Existing default and maximum limits remain unchanged.

Project listing, tag listing, outgoing links, and backlinks retain their current
bounds and truncation behavior.

### Token behavior

Cursors are URL-safe opaque strings. Clients must store and return them without
interpreting or modifying them.

The decoded internal payload contains:

- a cursor format version;
- the operation kind;
- a hash of normalized request scope and filters; and
- the ordering values of the last returned row.

The filter hash binds a cursor to the project and relevant query, mode, and tag
inputs without embedding raw search text or tags. Reusing a cursor with changed
inputs returns `invalid_cursor` rather than silently changing its meaning.

Cursors do not expire in the initial design. A server may reject an unsupported
format version. Cursor values, decoded fields, search rank, and filter hashes
must not appear in logs or wide events.

### List ordering and continuation

`insight_list` orders live rows by:

```text
updated_at DESC, id DESC
```

The continuation predicate is logically:

```sql
(updated_at, id) < (:cursor_updated_at, :cursor_id)
```

The query fetches `limit + 1` rows, returns at most `limit`, and derives
`next_cursor` only when the additional row exists.

The store adds a partial B-tree index matching the project-scoped live-row
access pattern:

```text
(project_id, updated_at DESC, id DESC) WHERE deleted_at IS NULL
```

The existing tag GIN index remains responsible for tag filtering; query-plan
verification determines whether PostgreSQL combines it with the ordering index
or uses a bounded sort.

### Search ordering and continuation

`insight_search` orders live matching rows by:

```text
rank DESC, updated_at DESC, id DESC
```

Rank is computed once per row for the query. The cursor preserves it losslessly
so the continuation predicate uses the exact PostgreSQL `real` value, followed
by `updated_at` and `id` tie-breakers. The implementation must not round a rank
through a human-formatted decimal representation.

The addition of `updated_at` and `id` changes only the previously unspecified
order among equal-rank rows.

### Consistency under concurrent changes

Cursor pagination does not hold a database snapshot across HTTP or MCP calls.
Each page observes committed state at the time of its query. If an insight is
created, updated, restored, or deleted between pages, it can move across the
cursor boundary and may be skipped or observed again.

The contract guarantees deterministic ordering and correct traversal when the
matching dataset is unchanged. It does not promise exactly-once traversal of a
concurrently mutating dataset.

## Revision model

### Current state

`insights` gains a positive `revision INTEGER NOT NULL` value. New insights
start at revision 1. Every accepted state change increments the value by one.
Revision overflow fails the mutation rather than wrapping.

The revision column is not independently indexed. Insight ID lookup already
selects one row, and an extra mutable index would increase update cost without
serving a planned query.

### Revision snapshots

The conceptual history shape is:

```text
insight_id   UUID         not null, references insights(id)
revision     INTEGER      not null, positive
operation    TEXT         baseline | create | update | delete | restore
key          TEXT         nullable
content      TEXT         not null
tags         TEXT[]       not null
category     TEXT         not null
source       TEXT         not null
deleted_at   TIMESTAMPTZ  nullable
changed_by   UUID         nullable, references users(id) ON DELETE SET NULL
changed_at   TIMESTAMPTZ  not null
primary key (insight_id, revision)
```

Each row is the complete resulting state at that revision, including the
current revision. `changed_by` records the authenticated actor; it uses null on
user removal so history does not block identity erasure. A backfilled baseline
uses null because the existing schema cannot identify the actor responsible for
the latest state without guessing. `changed_at` uses the mutation transaction
timestamp. A baseline uses `deleted_at` for a soft-deleted row and `updated_at`
otherwise, preserving the latest known state-change time.

History rows do not contain generated search vectors and have no content, tag,
operation, timestamp, or actor indexes initially. The composite primary key
supports exact revision lookup and backward history scans for one insight.

Hard deletion of an insight cascades to its revisions. Soft deletion retains
the parent row and all history.

### Revision operations

- `create`: the first state of a newly written insight.
- `baseline`: the state captured for a row that predates the feature.
- `update`: a changed state produced by keyed upsert or `insight_update`.
- `delete`: the state after soft deletion, including `deleted_at`.
- `restore`: a new live state copied from a selected earlier snapshot.

A semantic no-op does not update `updated_at`, increment `revision`, or create a
history row. No-op detection compares the normalized persisted fields affected
by the operation. A content-bearing no-op still repairs the derived link
projection: keyed writes regenerate warnings, while no-op `insight_update`
responses omit them. Tag-only no-ops do not touch links.

Revision insertion, current-row mutation, link synchronization when content is
supplied, warnings, and commit all share one transaction. Failure of any part
rolls back the entire mutation.

### Existing audit log

The revision ledger is the authoritative history for insights. Migration 20
drops only the redundant `audit_insights` trigger after the dev parity check.
Existing `audit_log` rows remain untouched, and auditing for other tables and
insight links is unchanged.

Product revision history is not a substitute for privileged database auditing.
If privileged direct SQL writes become a supported workflow, add a metadata-only
operational audit path rather than duplicating insight content snapshots.
Application roles should not be permitted to bypass the revision-writing
transaction.

## Optimistic concurrency design

The implemented external behavior in this section is authoritative in
[Insight revisions and optimistic concurrency](insight_revisions.md). This
proposal retains the design rationale and its relationship to history and
restore operations.

### Revision exposure

Current insight representations add `revision`. It is returned by:

- `insight_write`;
- `insight_get`;
- `insight_list`;
- `insight_search`;
- `insight_update`; and
- corresponding Connect responses.

`insight_delete` returns the resulting deletion revision after this change.

### Optional preconditions for existing tools

Existing mutation tools gain optional `expected_revision` inputs.

For keyed `insight_write`:

- omitted preserves the current last-write-wins upsert behavior;
- `0` requires that no live insight with the supplied key exists; and
- a positive value requires that the live keyed insight has exactly that
  revision.

For `insight_update` and `insight_delete`:

- omitted preserves current behavior;
- a positive value requires an exact current revision; and
- `0` is invalid.

For keyless `insight_write`, omitted or `0` creates a new insight; a positive
expected revision is invalid because there is no existing target.

The store selects the live target `FOR UPDATE`, compares the optional
precondition with the locked current revision, and retains the row lock through
the current-state mutation, link synchronization, revision insert, and commit.
The mutating statement also predicates the locked revision:

```text
WHERE id = requested_id AND revision = locked_revision
```

When a precondition is supplied, `locked_revision` equals `expected_revision`.
The update increments revision and returns the changed row atomically. A
revision mismatch never mutates content, links, timestamps, or history.

MCP returns a tool execution error with `isError: true` and a bounded JSON body:

```json
{
  "code": "revision_conflict",
  "expected_revision": 2,
  "current_revision": 3
}
```

This follows the MCP distinction between actionable tool execution errors and
protocol errors. Connect write RPCs and their conflict mapping are outside this
proposal. Internally the error retains expected and current revision values
without logging insight content.

## History reads and restore contract

### `insight_history`

The implemented history-read behavior is authoritative in
[Insight revisions and optimistic concurrency](insight_revisions.md). This
section retains its design rationale and relationship to restore.

The read-scoped tool accepts:

```json
{
  "project": "starlogz",
  "id": "019f...",
  "limit": 20,
  "cursor": "opaque"
}
```

History uses insight ID rather than key because a key can be reused after soft
deletion. The operation remains available for a soft-deleted insight when the
caller can access its project.

Revisions are ordered by `revision DESC`. The default limit is 20 and the
maximum is 100. Its cursor contains the operation/version binding and the last
returned revision; no additional history index is required.

The response contains:

- insight ID and key;
- current revision and deletion state;
- bounded full revision snapshots;
- operation, actor, and change timestamp per revision; and
- optional `next_cursor`.

Missing, hard-deleted, and inaccessible insights use the same not-found
behavior. Content, actors, cursor values, and revision snapshots are excluded
from logs and wide events.

### `insight_restore`

The write-scoped tool accepts project, insight ID, target revision, and required
positive `expected_revision`. Restore never rewinds or reuses a number. It
creates `current_revision + 1` with operation `restore` and copies the selected
snapshot's key, content, tags, category, and source into the current row.

Restore clears `deleted_at`, updates `updated_at`, attributes the change to the
current actor, reparses Markdown, synchronizes outgoing links, and returns link
warnings in the same transaction.

If another live insight has claimed the restored key, restore fails with a key
conflict and changes nothing. Restoring the same effective current state is a
no-op only when the current row is already live; restoring a deleted row always
creates a new live revision.

## Connect API and dashboard

Connect list and search requests add `cursor`; their responses add
`next_cursor`. The dashboard uses an infinite-query pattern and an explicit
“Load more” action rather than automatically issuing unbounded requests.

Connect insight messages add `revision`. The shipped read-only history panel
uses the history endpoint and adds no Connect or dashboard mutation RPC.
Dashboard restore is not currently needed and is outside this proposal. Any
future restore control belongs to a separately accepted dashboard write design.

The history panel renders only server-sanitized Markdown for a selected
revision. Diffs are derived from raw text without placing raw content into a
trusted-HTML boundary.

## Authorization, privacy, and telemetry

- All pagination and history queries resolve the authenticated user's
  organization and project before applying cursor values.
- History requires `insights:read`; restore and concurrency-controlled mutations
  require `insights:write`.
- Cursor decoding never substitutes project, organization, or authorization
  scope from the token payload.
- Search queries, tags, cursor payloads, filter hashes, ranks, insight content,
  revisions, and actor IDs remain excluded from access logs and wide events.
- `insight_history` is in the bounded tool-name allowlist and successful calls
  use the existing result-count bucket. `insight_restore` events emit only the
  bounded tool name, without content or revision attributes.
- Conflict and invalid-cursor errors expose bounded codes, not arbitrary
  database or decoding errors.

## Migration and rollout

1. **Implemented:** Add cursor codecs, deterministic ordering, the matching
   live-row index with a retry-safe concurrent build, store page types, and list
   pagination without changing default limits.
2. **Implemented:** Add search pagination, query-bound cursors, lossless rank
   continuation, and dashboard continuation.
3. **Implemented:** Add `insights.revision`, the typed revision table, and one
   `baseline` snapshot for every existing live or soft-deleted insight.
4. **Implemented:** Add revision writes and optional concurrency preconditions
   to existing mutation paths while retaining `audit_insights` for comparison.
5. **Implemented:** Add MCP and Connect history reads with revision cursor
   continuation.
6. **Implemented:** Add MCP restore.
7. **Implemented:** Add the read-only dashboard history panel.
8. **Implemented:** Validate revision/audit parity and drop `audit_insights`
   without deleting historical audit rows.

Steps 3 and 4 are one deployment boundary and are implemented in the same
release unit. Migration 19 must not be separated from the revision-aware
mutation paths because later changes would diverge from the ledger.

The baseline migration can materially increase startup migration time and WAL.
Before production rollout, measure row count, content bytes, expected revision
table size, migration duration, WAL volume, and lock behavior against a
production-shaped copy. If the single migration exceeds the deployment window,
split schema creation, dual writes, bounded backfill, and constraint validation
across releases.

The single-transaction migration deliberately retains its table lock through
the baseline copy so an older application instance cannot mutate an insight
between schema change and snapshot creation. Releasing that lock earlier is not
a safe optimization; the staged fallback must establish dual writes before a
bounded backfill.

An older invocation already waiting on that lock can resume after migration
commit and bypass revision writes. The single-user development deployment
accepts this operational risk only when the operator blocks new requests and
drains the sole writer before migration. A production or multi-writer rollout
must use equivalent traffic quiescence or the staged dual-write fallback.

## Verification

### Cursor correctness

Completed automated verification covers:

- deterministic list traversal where multiple rows share `updated_at`;
- deterministic search traversal across different ranks and equal-rank rows;
- no gaps or duplicates across a static multi-page dataset;
- correct `next_cursor` behavior at zero, exact-limit, and limit-plus-one sizes;
- invalid version, operation, encoding, rank, and filter binding rejection;
- unchanged behavior when cursor is omitted;
- MCP empty and oversized cursors using the documented `invalid_cursor`
  contract;
- transport-backed dashboard list and search continuation; and
- a deterministic test demonstrating the documented concurrent-mutation
  limitation.

Pending pre-production validation:

- `EXPLAIN (ANALYZE, BUFFERS)` verification for first and deep pages on
  production-shaped data; and
- browser verification of dashboard list and search continuation against the
  deployed Connect API.

### Revision correctness

Completed automated verification covers:

- descending, bounded history traversal for live and soft-deleted insights;
- cursor scope binding, terminal empty pages, and invalid cursor rejection;
- project authorization and not-found behavior for inaccessible history;
- nullable baseline and removed actors, including MCP and Connect transport
  responses;
- hard-deleted targets returning not found through MCP and Connect;
- privacy-safe history telemetry;
- create, keyed update, patch update, delete, and restore revision sequences;
- baseline coverage for live and soft-deleted pre-feature rows;
- semantic no-op suppression with derived-link repair for content-bearing calls;
- stale expected-revision conflicts with no partial mutation;
- concurrent updates where exactly one caller with the same expected revision
  succeeds;
- history access after soft deletion and not-found behavior outside scope;
- restore of deleted and live rows, including key-reuse conflicts;
- atomic content, link, warning, revision, and restore behavior.

Store behavior uses real PostgreSQL integration tests. MCP and Connect handlers
use their existing integration patterns. Dashboard pagination, history,
navigation, focus, and sanitized rendering receive Bun tests; deployed-browser
verification remains listed above.

## Risks and mitigations

| Risk | Mitigation |
|---|---|
| Full snapshots grow faster than current-state data | Keep history minimally indexed, remove duplicate insight audit payloads after validation, measure before structural sharing or partitioning. |
| Cursor reuse with changed filters returns incorrect pages | Bind every cursor to normalized scope and filters and reject mismatches. |
| Search rank loses precision in a cursor | Encode the PostgreSQL `real` value losslessly and test round trips. |
| Concurrent mutation moves rows across page boundaries | Document page-at-query-time semantics; do not promise a cross-request snapshot. |
| Optional concurrency leaves legacy callers last-write-wins | Preserve compatibility initially, advertise revisions, and consider requiring preconditions only in a future contract change. |
| Restore collides with a reused key | Detect the live-key conflict in the transaction and return a bounded conflict without mutation. |
| Revision table duplicates generic audit data | Keep the revision ledger authoritative and retain only the generic audit data needed for other tables. |
| Removing the insight trigger loses evidence of out-of-band SQL writes | Restrict direct writes and confirm the operational audit requirement; retain metadata-only auditing if required. |
| Baseline backfill exceeds deployment window | Measure on production-shaped data and split schema, dual write, backfill, and validation if necessary. |

## Review questions

History exposes nullable `changed_by` user IDs. Actor presentation for shared
organizations remains a future dashboard-redesign concern rather than a
transport contract blocker.

1. Is preserving legacy last-write-wins indefinitely acceptable, or should a
   later API version require `expected_revision` for destructive mutations?
2. If privileged direct SQL writes become a supported workflow, should a
   separate metadata-only audit path be added?

## References

### Temporal history and concurrency

- [SQL Server temporal tables](https://learn.microsoft.com/en-us/sql/relational-databases/tables/temporal-tables?view=sql-server-ver17)
- [PostgreSQL SQL:2011 temporal proposal](https://wiki.postgresql.org/wiki/SQL2011Temporal)
- [PostgreSQL unsupported SQL features](https://www.postgresql.org/docs/current/unsupported-features-sql-standard.html)
- [PostgreSQL audit-trigger example](https://wiki.postgresql.org/wiki/Audit_trigger)
- [PostgreSQL `UPDATE`](https://www.postgresql.org/docs/current/sql-update.html)
- [PostgreSQL `SELECT` locking clause](https://www.postgresql.org/docs/current/sql-select.html#SQL-FOR-UPDATE-SHARE)
- [MCP tool error handling](https://modelcontextprotocol.io/specification/2025-11-25/server/tools#error-handling)

### Pagination and indexes

- [PostgreSQL `LIMIT` and `OFFSET`](https://www.postgresql.org/docs/current/queries-limit.html)
- [PostgreSQL row constructor comparison](https://www.postgresql.org/docs/current/functions-comparisons.html#ROW-WISE-COMPARISON)
- [PostgreSQL indexes and ordering](https://www.postgresql.org/docs/current/indexes-ordering.html)
- [PostgreSQL multicolumn indexes](https://www.postgresql.org/docs/current/indexes-multicolumn.html)
- [PostgreSQL B-tree indexes](https://www.postgresql.org/docs/current/btree.html)
- [PostgreSQL `CREATE INDEX`](https://www.postgresql.org/docs/current/sql-createindex.html)

### Storage and future scale

- [PostgreSQL UUID type](https://www.postgresql.org/docs/current/datatype-uuid.html)
- [PostgreSQL UUID functions](https://www.postgresql.org/docs/current/functions-uuid.html)
- [RFC 9562 UUID best practices](https://www.rfc-editor.org/rfc/rfc9562.html#section-6.11)
- [PostgreSQL numeric types](https://www.postgresql.org/docs/current/datatype-numeric.html)
- [PostgreSQL page layout](https://www.postgresql.org/docs/current/storage-page-layout.html)
- [PostgreSQL TOAST](https://www.postgresql.org/docs/current/storage-toast.html)
- [PostgreSQL partitioning](https://www.postgresql.org/docs/current/ddl-partitioning.html)
