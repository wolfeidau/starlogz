# Insight search

> Status: Current contract
> Last reviewed: 2026-07-19
> Authority: Behavioral contract; current code and tests provide implementation evidence.

`insight_search` provides project-scoped PostgreSQL full-text search over live
insight content and tags. Results are ordered by text-search rank, update time,
and ID as defined in [Cursor pagination](pagination.md).

## MCP result projection

MCP search results contain a bounded `snippet` plus ID, optional key, tags,
category, source, update time, and revision. They do not contain an unbounded
`content` field; callers use `insight_get` to retrieve a selected result in
full. A short insight may fit entirely inside the bounded snippet.

Snippets use PostgreSQL `ts_headline` with the same text-search configuration
and query used for matching. Generation occurs after the ranked page is
selected and is limited to one fragment of approximately 40 words. Highlight
markers are disabled. A result matching through tags alone receives a bounded
leading content fragment.

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
