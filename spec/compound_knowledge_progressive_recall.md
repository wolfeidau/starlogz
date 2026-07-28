# Duplicate-free progressive compound-knowledge recall

> Status: Implemented decision
> Last reviewed: 2026-07-28
> Authority: Historical rationale and lasting constraints; current contracts,
> code, tests, and packaged skills define implementation behavior.

## Problem

Compact search followed by selective `insight_get` bounded each discovery
response, but separate focused searches could return the same insights. Search
cursors could not solve this across changing queries because they continue one
exact result set, and Starlogz otherwise receives no conversation exposure
history.

## Decision

Keep recall stateless. During one compound-knowledge invocation, the skill
tracks every surfaced insight ID and passes the accumulated set through
`exclude_ids` on each later search. Starlogz validates at most 100 non-nil
UUIDs, canonicalizes them, and excludes matching rows before ranking and
limiting. Unknown or cross-project IDs have no observable effect.

Add a fixed `brief` discovery projection of one approximately 20-word,
256-byte UTF-8 snippet. The existing approximately 40-word, 512-byte
`standard` projection remains the default. Both return the same metadata and
omit full content; `insight_get` remains the deliberate disclosure step.

Bind canonical exclusions to search cursors because they change result
membership. Preserve the prior hash encoding for omitted or empty exclusions.
Do not bind `detail`, which changes only projection. The skill normally begins
a new search as its exclusion set grows and uses cursor continuation only with
an unchanged set.

Version `0.2.0` of both packaged skills uses brief focused searches, accumulates
all returned IDs, and continues only for a distinct unresolved question. It
stops at sufficient verified context, exhaustion, no remaining uncertainty, or
100 surfaced IDs. Servers without the additive inputs retain the earlier
one-focused-search plus one-broadening workflow.

## Alternatives and tradeoffs

Server-backed recall sessions and durable exposure history could deduplicate
without caller cooperation, but would add lifecycle, privacy, and storage
state. Encoding recall state in cursors would misuse unchanged-result-set
pagination and conflict with an expanding exclusion set. MCP resource links
could defer retrieval, but client handling is not uniform and does not define
cross-call deduplication.

Caller-carried exclusions make the guarantee conditional on the skill passing
all surfaced IDs and cap one invocation at 100 results. In return, authorization
and organization isolation stay unchanged, no cross-user or cross-conversation
state is stored, and old clients preserve their response and pagination
behavior.

## Outcome

Repeated focused searches can return the next unseen matches without reducing
the requested page size when more matches exist. Discovery context grows in
small fixed projections, and full insight content enters context only after an
explicit selection. Queries, exclusions, snippets, content, and cursor filter
inputs remain absent from logs, telemetry, and cursor payloads.

## Research provenance

- [MCP tools specification](https://modelcontextprotocol.io/specification/2025-11-25/server/tools)
  defines tool responses but no cross-call deduplication state.
- [MCP pagination](https://modelcontextprotocol.io/specification/2025-11-25/server/utilities/pagination)
  supports opaque continuation tokens for a result set.
- [PostgreSQL array comparisons](https://www.postgresql.org/docs/current/functions-comparisons.html)
  support bounded UUID-array exclusion.
- [OpenAI skill guidance](https://learn.chatgpt.com/docs/build-skills)
  establishes progressive disclosure as a skill design pattern.
