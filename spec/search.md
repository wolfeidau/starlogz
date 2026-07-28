# Insight search

> Status: Current contract
> Last reviewed: 2026-07-28
> Authority: Behavioral contract; current code and tests provide implementation evidence.

`insight_search` provides project-scoped PostgreSQL full-text search over live
insight content and tags. Results are ordered by text-search rank, update time,
and ID as defined in [Cursor pagination](pagination.md).

## MCP result projection

MCP search results contain a bounded `snippet` plus ID, optional key, tags,
category, source, update time, and revision. They do not contain an unbounded
`content` field; callers use `insight_get` to retrieve a selected result in
full. A short insight may fit entirely inside the bounded snippet.

The optional `detail` input selects the discovery projection:

| Detail | Fragments | Approximate words | UTF-8 byte maximum |
|---|---:|---:|---:|
| `standard` | 1 | 40 | 512 |
| `brief` | 1 | 20 | 256 |

`standard` is the default and preserves the existing response. Both projections
return the same metadata fields and omit full content.

Snippets use PostgreSQL `ts_headline` with the same text-search configuration
and query used for matching. Generation occurs after the ranked page is
selected. A byte-truncated snippet ends with an ellipsis within its bound.
Highlight markers are disabled. A result matching through tags alone receives
a bounded leading content fragment.

Snippet text is returned only as JSON text and is not trusted HTML. Any future
rendered use must pass through the existing server sanitization boundary. The
Connect dashboard search contract is unchanged and continues to return complete
insights with server-rendered sanitized HTML.

This intentionally replaces the pre-production 0.x MCP response that embedded
complete content in every search hit.

## Query modes

`query_mode` is optional and defaults to `all` for compatibility.

- `all` uses `plainto_tsquery`; every meaningful query term must match.
- `web` uses `websearch_to_tsquery`; callers can use uppercase `OR`, quoted
  phrases, and `-excluded` terms. Unqualified terms are combined with AND.

The web mode intentionally does not expose PostgreSQL's lower-level tsquery
operators. Web-search syntax is easier for humans and agents to produce and
does not reject otherwise malformed user input with syntax errors.

## Tag modes

`tag_mode` is optional and defaults to `all`.

- `all` requires the insight to contain every supplied tag.
- `any` requires the insight to contain at least one supplied tag.

An empty tag list does not filter results. Tags are normalized to lowercase at
the MCP boundary.

## Limits and scope

Search is limited to the caller's personal organization and the requested
project. Soft-deleted insights are excluded. The default result limit is 20
and the maximum is 100.

The optional `exclude_ids` array suppresses already surfaced insights before
ranking and limiting. It accepts at most 100 valid, non-nil UUIDs. Values are
canonicalized, deduplicated, and sorted; malformed values fail before the store
is queried. Unknown IDs and IDs from another project match nothing and disclose
no existence information. Omitted or empty exclusions preserve existing
behavior.

Callers own exclusion state. Starlogz does not retain exposure history or create
server-side recall sessions. A compound-knowledge invocation can accumulate
returned IDs across focused searches and use `insight_get` only for candidates
whose full content is required.
