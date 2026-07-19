---
name: compound-knowledge
description: Recall and preserve durable project knowledge with Starlogz. Invoke explicitly with $compound-knowledge when prior decisions, conventions, architecture, known pitfalls, preferences, or a reusable result are needed.
---

# Compound Knowledge

Keep the repository authoritative. Treat Starlogz as context that must be verified.

## Recall

1. Use a supplied project slug; otherwise derive it from the repository root. Call `project_list` only when uncertain.
2. Run one focused `insight_search` with `limit=5`.
3. Inspect snippets and metadata. Call `insight_get` only for selected hits that require full content.
4. Broaden once only when focused recall is insufficient and missing context creates material risk.
5. Verify relevant claims against user instructions, specifications, source, and tests.

Use `whoami` only when access is uncertain. Use `insight_list` only when the user asks for recent or enumerated records.

## Write

Write only reusable, specific, verified knowledge that will save future work:

- Use a stable `key` for a current preference, procedure, architecture choice, or fact. Replace the prior summary; revisions retain history.
- Omit `key` for decisions, discoveries, and incidents that should remain append-only.
- Keep a standard insight to one paragraph and target at most 600 characters.
- Keep architecture or research insights within 1,200 characters plus essential references.

Store the outcome, rationale, consequence, and one evidence pointer. State each fact once. Preserve exact technical terms. Remove filler, hedging, tool narration, activity sequences, raw logs, test inventories, intermediate commits, and deployment narration. Never store secrets, credentials, or speculation. Skip the write when the repository already preserves the result.

Call `insight_list_tags` only before a write that selects or introduces tags. Reuse existing lower-case tags. Set `category` and `source` accurately: `user` for explicit user input, `repo` for repository evidence, `agent` for validated findings, and `command` for durable command output. Link only durable keyed insights when the relationship materially improves recall.

## Finish

Write at most one concise result. Report the insight written or updated. If Starlogz is unavailable or authentication fails, continue safely with repository context and report that no memory was read or written.

If the tools are missing, ask the user to configure their deployment:

```bash
codex mcp add starlogz --url https://starlogz.example.com/mcp
```
