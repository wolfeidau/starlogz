---
name: compound-knowledge
description: Recall and improve durable project knowledge with Starlogz. Use for prior decisions, conventions, architecture, known pitfalls, preferences, or requests to remember a reusable result.
---

# Compound Knowledge

Maintain a compounding knowledge base for the project. Each session should leave it more accurate, better connected, and no larger than necessary. Recall first so new work builds on prior knowledge, and improve what exists before adding anything new. Revisions preserve history, so rewrite and consolidate freely.

Keep the repository authoritative. Treat Starlogz as context that must be verified.

## Recall

1. Use a supplied project slug; otherwise derive it from the repository root. Call `project_list` only when uncertain.
2. Inspect the available `insight_search` schema. When it supports `detail` and `exclude_ids`, initialize an invocation-local set of surfaced insight IDs and run a focused search with `limit=5` and `detail="brief"`.
3. Build each query from 2–4 distinctive terms, such as identifiers, file names, or error text. The default `query_mode=all` requires every term, so long queries return nothing. For a broader search, use `query_mode=web` with terms joined by `OR`.
4. Add every returned ID to the surfaced set, including candidates not selected for full retrieval. Call `insight_get` only when a candidate's full content materially affects the task.
5. Search again only to answer a distinct unresolved question, or once with fewer or alternative terms after an empty result. Pass the complete surfaced set as `exclude_ids`; a repeated query is allowed when the next-best unseen matches are intentional.
6. Stop when verified context is sufficient, a search returns no new hits, no distinct uncertainty remains, or 100 IDs have surfaced. Never persist or carry the surfaced set into another invocation.
7. If either new search input is unavailable, fall back to one focused search with `limit=5` and at most one broader search.
8. Verify relevant claims against user instructions, specifications, source, and tests. Note surfaced insights that are stale, duplicated, or contradicted.

Use `whoami` only when access is uncertain. Use `insight_list` only when the user asks for recent or enumerated records.

## Improve

Improve existing knowledge before adding new knowledge. In order of preference:

1. Correct a surfaced insight that is stale or contradicted by the repository or this work. Rewrite a keyed insight as one current summary; never append dated update sections.
2. Merge duplicates into one keyed insight and delete the rest. Deletion is soft and revisions remain restorable.
3. Link related keyed insights with `[[key]]` when the relationship materially improves recall.
4. Add a new insight only when no existing insight can hold the result.

Pass `expected_revision` on every update and delete.

## Write

Write only reusable, specific, verified knowledge that will save future work. Skip the write when:

- the repository, its agent instructions, or a file edited in this session already records the result;
- it is task status, progress, verification results, or follow-up work, which belong in commits, pull requests, and issues;
- it applies to the user's work across projects, which belongs in user-level agent instructions.

Size and shape:

- Use a stable `key` for a current preference, procedure, architecture choice, or fact.
- Omit `key` for decisions, discoveries, and incidents that should remain append-only.
- Keep a standard insight to one paragraph and target at most 600 characters.
- Keep architecture or research insights within 1,200 characters plus essential references.

Store the outcome, rationale, consequence, and one evidence pointer. State each fact once. Preserve exact technical terms. Remove filler, hedging, tool narration, activity sequences, raw logs, test inventories, intermediate commits, and deployment narration. Never store secrets, credentials, or speculation.

Call `insight_list_tags` only before a write that selects or introduces tags. Reuse existing lower-case tags, and never tag with the project slug. Set `category` and `source` accurately: `user` only for preferences, facts, or decisions the user explicitly stated; `repo` for repository evidence; `agent` for validated findings, including those the user asked for; and `command` for durable command output.

## Finish

Add at most one new insight per invocation; corrections, merges, and deletions of surfaced insights are not limited. Report each insight added, updated, merged, or deleted. If Starlogz is unavailable or authentication fails, continue safely with repository context and report that no memory was read or written.

If the tools are missing, ask the user to configure their deployment:

```bash
claude mcp add --transport http starlogz https://starlogz.example.com/mcp
```
