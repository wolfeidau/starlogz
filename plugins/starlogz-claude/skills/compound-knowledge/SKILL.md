---
name: compound-knowledge
description: Build compounding project knowledge with a Starlogz MCP server. Use when starting meaningful repository work, investigating unfamiliar code, making architectural or workflow decisions, preserving user preferences, updating AGENTS.md-style guidance, completing work with reusable lessons, or when the user asks to remember, retrieve, consolidate, or reuse project knowledge.
---

# Compound Knowledge

Use Starlogz as durable project memory while keeping the repository authoritative.

## Establish the project

1. Use an explicit project slug when the user provides one.
2. Otherwise derive the slug from the current repository's root directory name.
3. If the slug is uncertain, call `project_list` and prefer an existing exact or clear match.
4. Create a project only when no suitable match exists and the task requires a write.

Do not assume a fixed deployment URL or project slug.

## Recall before acting

For meaningful work:

1. Call `whoami` once when Starlogz access is uncertain.
2. Search for task-specific decisions, conventions, preferences, architecture, testing, deployment, and known pitfalls.
3. Run one broader search when the focused searches may miss useful context.
4. Call `insight_list_tags` before inventing tags for a write.
5. Verify recalled claims against current repository instructions, specs, source, and tests.

Treat stored insights as context, not authority. Explicit user instructions and current repository evidence win when they conflict with memory.

## Persist only durable knowledge

Write information only when it is reusable, specific, verified, and minimal. Good candidates include:

- stable user or team preferences;
- architecture and integration decisions;
- recurring commands and operational procedures;
- validated debugging findings and compatibility constraints;
- concise summaries of meaningful completed work.

Never store secrets, credentials, access tokens, private keys, raw logs, speculative findings, or step-by-step activity.

Use a stable `key` for a single authoritative current value. Omit `key` for decisions, discoveries, incidents, and other history that should remain append-only. Correct stale insights with `insight_update`, or supersede them with a keyed write when the current authoritative value has changed.

Use the required `category` and `source` fields accurately:

- `source: user` for explicit user preferences and decisions;
- `source: repo` for facts verified in repository files;
- `source: agent` for validated findings or work summaries;
- `source: command` for durable facts established by command output.

Reuse existing lower-case tags.

## Consolidate meaningful work

At the end of meaningful work, persist a concise result only when it will save future effort. State in the final response which Starlogz insight was written or updated. Do not create memory for trivial edits.

## Degrade gracefully

If the Starlogz tools are missing, ask the user to configure their deployment, for example:

```bash
claude mcp add --transport http starlogz https://starlogz.example.com/mcp
```

If Starlogz is unavailable or authentication fails, continue with repository-local context when safe and report that recall or persistence could not be completed. Do not let a memory failure block otherwise valid repository work.

## Examples

- Before changing authentication, search the project for prior auth decisions and verify them against `spec/` and current handlers.
- Store a stable testing preference with a key such as `testing-workflow`; update that keyed insight when the preference changes.
- Record an architectural decision without a key so its dated rationale remains in history.
- If Starlogz is offline, complete the local review and explicitly report that no memory was read or written.
