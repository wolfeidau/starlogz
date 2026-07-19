# Compound-knowledge context efficiency

> Status: Implemented decision
> Last reviewed: 2026-07-19
> Authority: Historical design rationale and rollout outcome; current contracts,
> code, tests, and packaged skills define implementation behavior.

## Outcome

Starlogz now keeps compound-knowledge recall context bounded through three
coordinated changes:

1. MCP `insight_search` returns compact snippets and metadata instead of full
   insight content;
2. the Codex `compound-knowledge` skill requires explicit invocation; and
3. the Codex and Claude skills preserve concise current-state knowledge rather
   than chronological work logs.

Current behavior is authoritative in:

- [Search](search.md) for the compact MCP search contract;
- [Codex compound-knowledge plugin](codex_plugin.md) for invocation and recall
  policy; and
- `plugins/starlogz-codex/skills/compound-knowledge/` and
  `plugins/starlogz-claude/skills/compound-knowledge/` for the packaged
  workflows.

The server change shipped through PR #82, and the skill and plugin change
shipped through PR #83. Plugin `0.1.2` contains the explicit, focused workflow.

## Problem

MCP search originally returned the complete content of every matching insight.
A broad recall could therefore load several long records even when only one
decision was relevant. The original skill also required broad recall for
meaningful work, allowed implicit Codex invocation, and permitted stable keyed
insights to accumulate rollout chronology.

This coupled routine repository work to avoidable skill and tool context, made
search payload size depend on stored insight length, and weakened the role of a
keyed insight as the current authoritative value.

## Decision

### Compact search discovery

Use a distinct MCP search-hit representation containing a bounded `snippet`
plus metadata. Keep `insight_get` as the authoritative full-content operation.
Connect dashboard search continues returning complete rendered insights.

Generate snippets with PostgreSQL `ts_headline` after ranking and limiting.
Return one fragment of approximately 40 words, cap it at 512 UTF-8 bytes
including any truncation ellipsis, and do not emit HTML highlighting markers.
Tag-only matches receive a bounded leading content fragment.

The pre-production 0.x MCP response intentionally dropped the full `content`
field rather than retaining a context-heavy compatibility mode. Matching,
ranking, ordering, pagination, authorization, and cursor binding did not
change.

### Explicit, focused recall

Set Codex `allow_implicit_invocation` to `false`. Users invoke the workflow
through `$compound-knowledge` or the skill picker. Routine Git, status, and
small-edit requests do not load the skill or call Starlogz automatically.

An invoked workflow runs one focused search with `limit=5`, retrieves full
content only for selected hits, and broadens once only when missing context
creates material risk. Repository instructions, specifications, source, and
tests remain authoritative over recalled records.

### Concise durable writes

Both packaged skills write at most one reusable result. A stable keyed insight
is replaced with its current authoritative summary; revision history retains
prior states. Standard insights target one paragraph and at most 600
characters. Architecture and research insights target at most 1,200 characters
plus essential references.

Writes retain the outcome, rationale, consequence, and one evidence pointer.
They omit filler, hedging, tool narration, activity sequences, raw logs, test
inventories, intermediate commits, and deployment narration. The workflow
skips a write when the repository already preserves the result.

## Rationale and tradeoffs

Compact discovery makes recall cost depend on the requested page size and fixed
snippet bound rather than stored content length. Selective `insight_get` calls
preserve full fidelity without paying that cost for every candidate.

PostgreSQL full-text matching remains the single query language. Raw regular
expressions and embedded `rg` flags were excluded because they would bypass the
existing indexed access path and complicate validation. `ts_headline` output is
JSON text, not trusted HTML; any future rendered use must pass through the
existing sanitization boundary.

Explicit invocation trades automatic recall for predictable context use and
user intent. Concise keyed records trade embedded chronology for a clearer
current state; immutable Starlogz revisions preserve history when needed.

## Verification outcome

PostgreSQL and MCP integration tests cover bounded valid UTF-8 snippets,
approximately 40-word fragments, tag-only matches, omission of full content,
and explicit full retrieval through `insight_get`. Existing search ordering,
cursor, privacy, and authorization coverage remains in place.

The Codex and Claude skill validators and the Codex plugin validator pass. The
merged marketplace package was reinstalled as `0.1.2`; a fresh thread exposes
the skill for explicit invocation with implicit invocation disabled.

## Deferred decisions

The following remain separate, evidence-driven decisions rather than unfinished
scope:

- configurable snippet bounds;
- compact `insight_list` responses;
- raw regular-expression search; and
- server-side enforcement of insight length guidance.

## References

- [OpenAI: Build skills](https://learn.chatgpt.com/docs/build-skills)
- [PostgreSQL: Controlling text search](https://www.postgresql.org/docs/current/textsearch-controls.html#TEXTSEARCH-HEADLINE)
- [Agent Skills specification](https://agentskills.io/specification)
