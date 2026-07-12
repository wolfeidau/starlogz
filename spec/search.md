# Insight search

`insight_search` provides project-scoped PostgreSQL full-text search over live
insight content and tags. Results are ordered by text-search rank.

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
