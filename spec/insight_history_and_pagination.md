# Insight history, optimistic concurrency, and cursor pagination

> Status: Implemented decision
> Last reviewed: 2026-07-19
> Authority: Historical design rationale and rollout outcome; current contracts,
> code, migrations, and tests define implementation behavior.

## Outcome

Starlogz implemented bounded cursor pagination before adding first-class insight
history, optimistic concurrency, restore, and a read-only dashboard history
panel. Current behavior is authoritative in:

- [Cursor pagination](pagination.md); and
- [Insight revisions and optimistic concurrency](insight_revisions.md).

The implementation keeps `insights` as the indexed current-state projection and
stores immutable full snapshots in `insight_revisions`. Existing UUIDv7 insight
identifiers remain the public identity; each snapshot is identified by the
composite `(insight_id, revision)` key.

Existing clients retain first-page and last-write-wins behavior unless they opt
into cursors or `expected_revision`. History and restore use the same
organization and project authorization boundaries as existing insight tools.

## Problem

List and search initially returned only one bounded result set and lacked fully
deterministic ordering. Current insight mutations overwrote or soft-deleted rows
without an application-facing history, restore operation, or lost-update
precondition. The generic insight audit trigger retained row JSON but was not a
typed, authorized, restorable product contract.

Linked insight content already synchronized derived relationships in the same
transaction as content mutations. Pagination, revision creation, and restore
therefore had to preserve deterministic traversal, mutation atomicity, project
isolation, and bounded telemetry without moving current search onto a history
model.

## Decision

### Cursor pagination

Use stateless keyset pagination rather than offsets:

- insight lists order by `updated_at DESC, id DESC`;
- search orders by PostgreSQL `real` rank, then `updated_at DESC, id DESC`;
- history orders by `revision DESC`; and
- every query fetches `limit + 1` and returns an opaque `next_cursor` only when
  another page exists.

Cursors are versioned and bound to their operation, project, and normalized
filters. Search rank is encoded losslessly. Cursor contents do not grant access,
and raw queries, tags, cursor values, filter hashes, and ranks remain excluded
from logs and wide events.

Pages intentionally do not share a database snapshot. A static dataset
traverses without gaps or duplicates, while concurrent mutations can move rows
across a continuation boundary.

### Revision storage

Keep the live `insights` row as the searchable projection and add a positive
revision number plus an append-only typed snapshot ledger. Store complete
snapshots rather than deltas so every revision is independently valid and
restore does not depend on replaying a chain.

The composite `(insight_id, revision)` primary key matches exact revision lookup
and descending per-insight history without another identifier or index.
Historical content does not receive full-text or tag indexes. Soft deletion
retains snapshots; hard deletion cascades to them.

Migration baselines use a null actor because the legacy schema cannot identify
the latest editor. Removing a user also clears historical actor references so
history retention does not block identity erasure.

### Atomic mutations and concurrency

Create, keyed upsert, update, soft delete, and restore write the current row,
derived links when applicable, and one resulting snapshot in the same
transaction. Semantic no-ops preserve timestamps and revision numbers while
content-bearing calls still repair the derived link projection.

Existing mutation tools accept optional optimistic-concurrency preconditions to
preserve compatibility. A supplied revision is checked while the target row is
locked, and a mismatch changes no current fields, links, timestamps, or history.
Restore always requires a positive `expected_revision`, creates a new revision
rather than rewinding, and relies on the live-key uniqueness constraint to
reject key reuse atomically.

### Product boundary

Expose bounded history through MCP and Connect, and expose restore through MCP.
The dashboard provides an explicitly paginated, read-only history panel and
renders only server-sanitized revision HTML.

Dashboard restore and general dashboard authoring were excluded. They require a
separately accepted session-authenticated write and CSRF design rather than
expanding this read-only dashboard change.

## Rationale and provenance

### Established patterns

The current-row plus history-table shape follows established temporal-table and
audit-table patterns. SQL Server implements a current/history temporal model,
and the PostgreSQL SQL:2011 temporal proposal describes the same separation.
PostgreSQL 18 does not implement SQL system-versioned tables, so Starlogz owns
revision, actor, restore, and concurrency semantics in application
transactions.

PostgreSQL documentation also establishes the underlying pagination and
concurrency behavior:

- `LIMIT` needs a unique order for predictable subsets, while large offsets
  still compute skipped rows;
- row comparisons and ordered B-tree scans support keyset continuation;
- concurrent index builds avoid blocking normal writes but cannot run inside a
  transaction; and
- conditional updates with `RETURNING` support compare-and-swap behavior.

### Project-specific judgments

- Full typed snapshots were preferred over deltas because restore is direct,
  integrity checks are local to one row, and no replay chain can be damaged.
- The composite revision key matches the only required history access path and
  avoids a redundant public or internal revision UUID.
- Current-state search remains independent of history-table growth.
- Opaque filter-bound cursors prevent accidental cross-query reuse without
  embedding private filter values.
- Partitioning, structural content sharing, and mandatory preconditions remain
  measurement- or compatibility-driven future decisions.

## Migration and operational tradeoffs

Migration 18 added the partial live-row list index using the repository's
retry-safe concurrent-index path. Migration 19 added the revision column,
snapshot ledger, and baselines for live and soft-deleted insights. Revision-aware
mutations shipped in the same release boundary so later writes could not escape
the ledger. Migration 20 removed the redundant `audit_insights` trigger after
the development parity check while retaining historical audit rows and audit
coverage for other tables.

Migration 19 deliberately holds its table lock through the baseline copy. The
single-user development rollout accepted the legacy in-flight-writer risk only
with new requests blocked and the sole writer drained. Any future rollout of
the migration to a larger or multi-writer database must first measure row count,
content bytes, expected ledger size, duration, WAL, and lock behavior on a
production-shaped copy. If the window is excessive, use a staged schema,
dual-write, bounded-backfill, and validation rollout. This is an operational
deployment gate, not unfinished feature scope.

## Verification outcome

Real PostgreSQL integration tests cover deterministic list, search, and history
continuation; cursor validation and scope binding; snapshot sequences; semantic
no-ops; stale and concurrent conflicts; restore; authorization; atomic rollback;
and soft- and hard-deletion boundaries. MCP and Connect integration tests cover
transport contracts, and Bun tests cover dashboard pagination, navigation,
focus, error retention, and sanitized history rendering.

Final local Chrome validation used a disposable PostgreSQL dataset that crossed
each dashboard page boundary:

- list pagination expanded from 100 to 105 insights;
- search pagination expanded from 100 to 105 results; and
- history pagination expanded from 20 to 25 revisions, with revision 1
  selectable and rendered correctly.

All continuation calls returned HTTP 200, and the run produced no browser
console or server errors. The disposable project and revision rows were removed
after validation.

## Deferred decisions

The following are outside the completed scope and require separate evidence or
product decisions:

- dashboard restore or other session-authenticated insight writes;
- requiring revision preconditions for all destructive mutations;
- privileged direct SQL writes and any metadata-only operational audit path;
- history expiration, partitioning, or structural content sharing; and
- production-shaped query-plan and migration measurements for a materially
  larger deployment.

## References

### Temporal history and concurrency

- [SQL Server temporal tables](https://learn.microsoft.com/en-us/sql/relational-databases/tables/temporal-tables?view=sql-server-ver17)
- [PostgreSQL SQL:2011 temporal proposal](https://wiki.postgresql.org/wiki/SQL2011Temporal)
- [PostgreSQL unsupported SQL features](https://www.postgresql.org/docs/current/unsupported-features-sql-standard.html)
- [PostgreSQL audit-trigger example](https://wiki.postgresql.org/wiki/Audit_trigger)
- [PostgreSQL `UPDATE`](https://www.postgresql.org/docs/current/sql-update.html)
- [PostgreSQL `SELECT` locking clause](https://www.postgresql.org/docs/current/sql-select.html#SQL-FOR-UPDATE-SHARE)
- [MCP tool error handling](https://modelcontextprotocol.io/specification/2025-11-25/server/tools#error-handling)

### Pagination and indexing

- [PostgreSQL `LIMIT` and `OFFSET`](https://www.postgresql.org/docs/current/queries-limit.html)
- [PostgreSQL row comparison](https://www.postgresql.org/docs/current/functions-comparisons.html#ROW-WISE-COMPARISON)
- [PostgreSQL indexes and ordering](https://www.postgresql.org/docs/current/indexes-ordering.html)
- [PostgreSQL multicolumn indexes](https://www.postgresql.org/docs/current/indexes-multicolumn.html)
- [PostgreSQL `CREATE INDEX`](https://www.postgresql.org/docs/current/sql-createindex.html)

### Storage and future scale

- [PostgreSQL UUID type](https://www.postgresql.org/docs/current/datatype-uuid.html)
- [PostgreSQL numeric types](https://www.postgresql.org/docs/current/datatype-numeric.html)
- [PostgreSQL page layout](https://www.postgresql.org/docs/current/storage-page-layout.html)
- [PostgreSQL TOAST](https://www.postgresql.org/docs/current/storage-toast.html)
- [PostgreSQL partitioning](https://www.postgresql.org/docs/current/ddl-partitioning.html)
