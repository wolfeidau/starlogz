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

The first two rollout slices are implemented: MCP and Connect list and search
operations accept opaque, filter-bound cursors; list traversal uses
`updated_at DESC, id DESC`; search traversal uses lossless PostgreSQL `real`
rank followed by the same deterministic tie-breakers; and both fetch
`limit + 1`. Migration 18 adds the matching partial live-row list index using a
retry-safe concurrent build. The dashboard continues both result types through
an explicit **Load more** action. All revision work remains proposed. The
implemented behavior is authoritative in [Cursor pagination](pagination.md).

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
- General dashboard authoring. Dashboard restore is a separately gated slice
  within this proposal.

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
- the proposed MCP and Connect insight-history operations.

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
user removal so history does not block identity erasure. `changed_at` uses the
mutation transaction timestamp.

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

A semantic no-op does not update `updated_at`, increment `revision`, create a
history row, or resynchronize unchanged links. No-op detection compares the
normalized persisted fields affected by the operation.

Revision insertion, current-row mutation, link synchronization when content
changes, warnings, and commit all share one transaction. Failure of any part
rolls back the entire mutation.

### Existing audit log

The revision ledger becomes the authoritative history for insights. Keeping the
generic `audit_insights` trigger permanently would duplicate large old and new
payloads.

Rollout keeps the trigger for one validation release while revision writes are
compared against audit records. A later migration drops only the insight audit
trigger after verification. Existing `audit_log` rows remain untouched, and
auditing for other tables and insight links is unchanged.

Product revision history is not a substitute for privileged database auditing.
Before removing `audit_insights`, confirm whether direct SQL writes must remain
independently auditable. If they must, retain a metadata-only operational audit
path rather than duplicating insight content snapshots. Application roles
should not be permitted to bypass the revision-writing transaction.

## Optimistic concurrency contract

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

The store performs the comparison in the mutation statement:

```text
WHERE id = requested_id AND revision = expected_revision
```

The update increments revision and returns the changed row atomically. A
revision mismatch never mutates content, links, timestamps, or history.

The public MCP and Connect representation of `revision_conflict` remains a
review question below. Internally it must retain expected and current revision
values without logging insight content.

## History and restore contract

### `insight_history`

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

Connect insight messages add `revision`. A read-only history panel can ship with
the history endpoint. Dashboard restore is a separate delivery slice because it
introduces a session-authenticated write operation and must revalidate the web
session's CSRF and same-origin boundary before implementation.

The history panel renders only server-sanitized Markdown for a selected
revision. Diffs are derived from raw text without placing raw content into a
trusted-HTML boundary.

## Import and export

History is durable user data. General availability should include a versioned
export shape that preserves revision number, operation, snapshot fields, and
timestamps while continuing to omit instance-bound user and organization UUIDs.
On import, revision actors are attributed to the importing user because source
instance actor UUIDs are intentionally absent.

The importer continues to accept the existing current-state export shape. A
history-bearing import remains atomic across its projects, current insights,
revisions, and reconstructed links.

Exact export-version negotiation and collision behavior for importing history
into a project with existing revisions remain review questions.

## Authorization, privacy, and telemetry

- All pagination and history queries resolve the authenticated user's
  organization and project before applying cursor values.
- History requires `insights:read`; restore and concurrency-controlled mutations
  require `insights:write`.
- Cursor decoding never substitutes project, organization, or authorization
  scope from the token payload.
- Search queries, tags, cursor payloads, filter hashes, ranks, insight content,
  revisions, and actor IDs remain excluded from access logs and wide events.
- `insight_history` and `insight_restore` are added to the bounded tool-name
  allowlist. Successful history calls may use the existing result-count bucket;
  restore does not emit content or revision attributes.
- Conflict and invalid-cursor errors expose bounded codes, not arbitrary
  database or decoding errors.

## Migration and rollout

1. **Implemented:** Add cursor codecs, deterministic ordering, the matching
   live-row index with a retry-safe concurrent build, store page types, and list
   pagination without changing default limits.
2. **Implemented:** Add search pagination, query-bound cursors, lossless rank
   continuation, and dashboard continuation.
3. Add `insights.revision`, the typed revision table, and one `baseline`
   snapshot for every existing live or soft-deleted insight.
4. Add revision writes and optional concurrency preconditions to existing
   mutation paths while retaining `audit_insights` for comparison.
5. Add MCP and Connect history reads, then MCP restore.
6. Add dashboard history review; add dashboard restore only after the web write
   boundary is accepted.
7. Add revision-aware export/import.
8. Validate revision/audit parity, then drop `audit_insights` in a later
   migration without deleting historical audit rows.

The baseline migration can materially increase startup migration time and WAL.
Before production rollout, measure row count, content bytes, expected revision
table size, migration duration, WAL volume, and lock behavior against a
production-shaped copy. If the single migration exceeds the deployment window,
split schema creation, dual writes, bounded backfill, and constraint validation
across releases.

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

- create, keyed update, patch update, delete, and restore revision sequences;
- baseline coverage for live and soft-deleted pre-feature rows;
- semantic no-op suppression;
- stale expected-revision conflicts with no partial mutation;
- concurrent updates where exactly one caller with the same expected revision
  succeeds;
- history access after soft deletion and not-found behavior outside scope;
- restore of deleted and live rows, including key-reuse conflicts;
- atomic content, link, warning, revision, and restore behavior;
- revision-aware import/export round trips;
- audit/revision parity during the validation release.

Store behavior uses real PostgreSQL integration tests. MCP and Connect handlers
use their existing integration patterns. Dashboard pagination, history, browser
navigation, focus, sanitized rendering, conflict, and restore states receive Bun
tests and browser verification.

## Risks and mitigations

| Risk | Mitigation |
|---|---|
| Full snapshots grow faster than current-state data | Keep history minimally indexed, remove duplicate insight audit payloads after validation, measure before structural sharing or partitioning. |
| Cursor reuse with changed filters returns incorrect pages | Bind every cursor to normalized scope and filters and reject mismatches. |
| Search rank loses precision in a cursor | Encode the PostgreSQL `real` value losslessly and test round trips. |
| Concurrent mutation moves rows across page boundaries | Document page-at-query-time semantics; do not promise a cross-request snapshot. |
| Optional concurrency leaves legacy callers last-write-wins | Preserve compatibility initially, advertise revisions, and consider requiring preconditions only in a future contract change. |
| Restore collides with a reused key | Detect the live-key conflict in the transaction and return a bounded conflict without mutation. |
| Revision table duplicates generic audit data | Validate both for one release, then drop only `audit_insights`. |
| Removing the insight trigger loses evidence of out-of-band SQL writes | Restrict direct writes and confirm the operational audit requirement; retain metadata-only auditing if required. |
| Baseline backfill exceeds deployment window | Measure on production-shaped data and split schema, dual write, backfill, and validation if necessary. |
| Dashboard restore introduces CSRF risk | Ship read-only history separately and accept the web write boundary before restore UI. |

## Review questions

1. Should MCP conflicts use structured tool-error results with
   `revision_conflict`, expected revision, and current revision, or retain
   protocol errors with a stable message?
2. Should Connect map revision conflicts to `ABORTED`, `FAILED_PRECONDITION`, or
   a typed error detail?
3. Is preserving legacy last-write-wins indefinitely acceptable, or should a
   later API version require `expected_revision` for destructive mutations?
4. Should revision history expose `changed_by` user IDs now, or defer actor
   identity until shared organizations define collaborator presentation?
5. Is revision-aware export/import a general-availability gate, and how should
   history merge with an existing destination insight?
6. Should dashboard restore be part of this program or remain deferred until a
   broader dashboard write contract is designed?
7. Is one release of audit/revision dual recording sufficient before removing
   `audit_insights`, and must a metadata-only audit continue for privileged
   direct SQL writes?

## References

### Temporal history and concurrency

- [SQL Server temporal tables](https://learn.microsoft.com/en-us/sql/relational-databases/tables/temporal-tables?view=sql-server-ver17)
- [PostgreSQL SQL:2011 temporal proposal](https://wiki.postgresql.org/wiki/SQL2011Temporal)
- [PostgreSQL unsupported SQL features](https://www.postgresql.org/docs/current/unsupported-features-sql-standard.html)
- [PostgreSQL audit-trigger example](https://wiki.postgresql.org/wiki/Audit_trigger)
- [PostgreSQL `UPDATE`](https://www.postgresql.org/docs/current/sql-update.html)

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
