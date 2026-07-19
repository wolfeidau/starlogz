# Compound-knowledge context efficiency

> Status: Proposed
> Last reviewed: 2026-07-19
> Authority: Design and delivery proposal; not an implementation commitment.

## Summary

Reduce Starlogz context use through three coordinated changes:

1. add bounded, match-aware content projections to MCP `insight_search`;
2. make the Codex `compound-knowledge` skill explicitly invoked; and
3. require concise, current-state insight records rather than chronological
   work logs.

MCP search becomes a compact discovery operation: every result contains a
bounded snippet and metadata, never an unbounded `content` field. A short
insight may fit entirely inside the snippet. Callers use `insight_get` for
authoritative full retrieval. This intentionally breaks the pre-production 0.x
response contract rather than retaining a context-heavy compatibility mode.

## Context

At the proposal baseline, MCP `insight_search` returned the complete content of
every matching insight. A broad recall could therefore load several long
records even when only one decision was relevant. The baseline skill also
required a broad second search for meaningful work and permitted stable keyed
insights to grow into rollout chronologies.

The baseline Codex skill metadata permitted implicit invocation. That made the
full skill body and Starlogz workflow eligible for routine status, Git, and
small-edit requests that do not need persistent project memory.

Proposal-baseline repository evidence:

- `internal/server/tools.go` converts every search hit through the full insight
  response shape;
- `internal/store/postgres/store.go` selects complete content for every search
  result;
- `plugins/starlogz-codex/skills/compound-knowledge/agents/openai.yaml` sets
  `allow_implicit_invocation: true`; and
- the Codex and Claude packages contain separate copies of the same knowledge
  policy.

## Implementation progress

Slice 1 was merged and deployed: MCP search returns bounded snippets,
`insight_get` retains full retrieval, and Connect dashboard search remains
unchanged. Slice 2 is code-complete on
`fix_compound_knowledge_context_churn`: Codex invocation is explicit, both
packaged skills use focused recall and concise writes, and plugin `0.1.2` carries
the new policy. Both skill validators and the plugin validator pass. Local
reinstall and fresh-thread invocation checks remain pending until the branch is
merged because the configured marketplace tracks remote `main`.

## Goals

- Bound search-result context independently of stored insight length.
- Preserve full-fidelity retrieval through `insight_get`.
- Stop automatic Starlogz use during routine repository work.
- Keep current keyed insights concise and authoritative.
- Preserve existing search ordering, pagination, privacy, and authorization.
- Measure the resulting payload reduction with representative long insights.

## Non-goals

- Raw regular-expression search over insight content.
- Changing PostgreSQL full-text matching or ranking.
- Model-generated summaries at read time.
- Rejecting legitimate long-form insight writes at the server boundary.
- Changing Connect dashboard search responses in the first rollout.
- Preserving the MCP search response's full `content` field.
- Defining Claude Code invocation policy in this proposal.

## Compact search contract

### Response

MCP `insight_search` keeps its existing request filters:

```json
{
  "project": "starlogz",
  "query": "database migration",
  "limit": 5
}
```

Every result uses a distinct search-hit representation containing `snippet`
instead of `content`, plus ID, key, tags, category, source, update time, and
revision. `insight_get` remains unchanged and is the only way to retrieve a
selected record in full.

### Snippet generation

Use PostgreSQL `ts_headline` with the same text-search configuration and
`tsquery` used for matching. Apply fixed server-side bounds initially:

- one fragment;
- approximately 40 words;
- 512 UTF-8 bytes, including a truncation ellipsis; and
- no HTML highlighting markers.

Generate snippets after ranking and limiting so excerpt work is bounded by the
requested page size. Results matching only through tags may return a bounded
leading content fragment; tags remain present in result metadata.

`ts_headline` output is not trusted HTML. MCP returns it only as JSON text. Any
future rendered use must pass through the existing sanitization boundary.

### Pagination

The response-shape change does not alter matching, ranking, ordering, page
membership, or cursor filter binding. Existing cursors remain valid across the
deployment because their ordering and filter inputs are unchanged.

Existing query modes retain their current behavior. `query_mode=web` already
provides quoted phrases, `OR`, and exclusions. Embedding `rg` flags or raw regex
inside the query is deferred because it would create a second query language,
bypass the current full-text access path, and complicate validation.

## Explicit Codex invocation

Set the Codex skill policy to:

```yaml
policy:
  allow_implicit_invocation: false
```

The skill remains available through `$compound-knowledge` and the Codex skill
picker. Update its description and default prompt to make explicit invocation
clear. After plugin reinstall, validate behavior in a new thread because skill
metadata is loaded at thread startup.

Routine prompts such as `git status`, `commit this`, or `are we done?` must not
load the skill or call Starlogz unless the user explicitly invokes it.

## Concise insight policy

Adopt these writing rules in both packaged skill copies:

- Remove filler, pleasantries, hedging, and tool narration.
- State each fact once.
- Preserve exact technical names, commands, errors, and identifiers.
- Do not invent abbreviations or sacrifice clarity for fragments.
- Store outcomes and durable rationale, not activity sequences.
- Reference an authoritative repository path or commit instead of duplicating
  its detail.
- Never store raw logs, test inventories, intermediate commits, or deployment
  narration.
- Skip the write when the repository already preserves the result adequately.

Length guidance:

- standard insight: one paragraph, target at most 600 characters;
- architecture or research insight: target at most 1,200 characters plus
  essential references; and
- exceed a target only when compression would remove material correctness or
  provenance.

A stable keyed insight represents the current authoritative value. Updating it
replaces the previous summary rather than appending chronology. Starlogz
revisions preserve earlier states when historical inspection is required.

Example:

```text
Decision: dashboard history stays read-only. Restore remains MCP-only because
dashboard writes require a separately accepted session-authenticated write and
CSRF design. Evidence: spec/insight_revisions.md.
```

## Recall workflow

When explicitly invoked:

1. derive or confirm the project slug;
2. run one focused `insight_search` with `limit=5`;
3. inspect metadata and snippets;
4. call `insight_get` only for selected hits whose full content is required;
5. broaden once only when focused recall is insufficient and missing context
   presents material risk; and
6. write one concise durable result only when it will save future work.

Use `insight_list` only when the user asks for recent or enumerated records. Call
`whoami` only when access is uncertain and `insight_list_tags` only before a
write that introduces or selects tags.

## Delivery plan

### Slice 1: compact MCP search

- Update `spec/search.md` with the current contract.
- Replace the MCP full-insight result with a bounded search-hit response.
- Add an internal search-result type carrying a snippet and optional full
  content for Connect callers.
- Compute bounded snippets after the ranked page is selected.
- Cover snippet bounds, tag-only matches, cursor continuation, privacy, and the
  intentional response break in PostgreSQL and MCP integration tests.
- Deploy the server before distributing the focused skill workflow.

Suggested branch and commit:

```text
feat_compact_insight_search
feat: add compact insight search results
```

### Slice 2: explicit and concise skill

- Set Codex implicit invocation to false.
- Replace mandatory broad recall with the focused workflow above.
- Apply the concise insight policy to Codex and Claude skill bodies.
- Keep product-specific installation and failure guidance separate.
- Update `spec/codex_plugin.md` and README usage examples.
- Bump the Codex plugin patch version.
- Validate the skill and plugin, use the cachebuster reinstall flow, and test in
  a new thread.

Suggested branch and commit:

```text
fix_compound_knowledge_context_churn
fix: make compound knowledge explicit and concise
```

## Verification

### Server

- MCP search never returns a `content` field or a snippet exceeding 512 bytes.
- Connect search continues returning complete rendered insights.
- Search IDs, ordering, and cursors remain unchanged.
- Snippet generation does not introduce trusted HTML.
- Existing cursor and authorization tests remain unchanged and pass.

### Skill

- A routine Git request produces no Starlogz tool calls.
- Explicit `$compound-knowledge` produces one focused compact search.
- Full retrieval occurs only for selected hits.
- A keyed update replaces prior prose instead of appending a timeline.
- Trivial work produces no insight write.
- Skill and plugin validators pass after packaging changes.

### Context budget

Use fixtures containing multiple long insights and adversarial multibyte tokens,
then compare serialized MCP responses. Every snippet must remain valid UTF-8 and
at most 512 bytes. The compact five-result response should be at least 75%
smaller than the equivalent full response and remain below 5 KB unless result
metadata alone exceeds that bound.

## Rollout

1. Merge and deploy compact MCP search.
2. Verify compact search and explicit full retrieval against the deployed MCP
   endpoint.
3. Merge the skill and plugin change.
4. Reinstall the plugin through the repository marketplace.
5. Start a new thread and run the explicit/implicit invocation checks.
6. Monitor real recall payloads before adding configurable snippet bounds or
   compact list responses.

## References

- [OpenAI: Build skills](https://learn.chatgpt.com/docs/build-skills)
- [PostgreSQL: Controlling text search](https://www.postgresql.org/docs/current/textsearch-controls.html#TEXTSEARCH-HEADLINE)
- [Caveman `SKILL.md`](https://github.com/JuliusBrussee/caveman/blob/main/skills/caveman/SKILL.md)
- [Agent Skills specification](https://agentskills.io/specification)
