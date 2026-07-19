# Codex compound-knowledge plugin

> Status: Implemented decision
> Last reviewed: 2026-07-19
> Authority: Historical rationale and lasting constraints; the packaged skill, plugin manifests, and README define current implementation details.

## Context

Starlogz provides durable project memory through MCP, but agents need a
repeatable, context-efficient policy for deciding when to recall, verify, and
persist knowledge.
A personal Codex skill validated the workflow during development, but users
needed a supported and repository-owned distribution path.

## Decision

Package the workflow as the `starlogz-codex` plugin in this repository and make
it discoverable through the repository marketplace. The plugin declares the
Starlogz MCP dependency and guides users to connect their own deployment; it
does not bundle or operate a server.

The Codex skill is explicitly invoked through `$compound-knowledge` or the
skill picker. Routine Git, status, and small-edit prompts do not load it. Once
invoked, it follows this lifecycle:

1. Derive the project slug from the current repository unless the user supplies
   one.
2. Run one focused search with five compact results, broadening once only when
   missing context creates material risk.
3. Retrieve full content only for selected results and verify it against user
   instructions and current repository evidence.
4. Persist at most one reusable, specific, verified, concise result when the
   repository does not already preserve it.
5. Continue safely with repository-local context when Starlogz is unavailable.

Repository files remain authoritative. The skill never stores credentials,
access tokens, private keys, secrets, raw logs, speculative findings, or
step-by-step activity. Standard records target one paragraph and at most 600
characters. Stable authoritative values use keyed upserts that replace prior
summaries; decisions, discoveries, and incidents remain append-only history.

## Distribution

The implementation lives in:

```text
.agents/plugins/marketplace.json
plugins/starlogz-codex/.codex-plugin/plugin.json
plugins/starlogz-codex/skills/compound-knowledge/
```

The initial installation path is:

```bash
codex plugin marketplace add wolfeidau/starlogz
codex plugin add starlogz-codex@starlogz
```

Keeping the plugin beside the server allows its workflow to evolve with the MCP
contract. It can move to a dedicated repository if it develops an independent
release cadence without changing the recall, verify, and consolidate model.

## Tradeoffs and outcome

The plugin is optional development tooling, not a Starlogz runtime dependency.
Explicit invocation prevents memory workflow context and tool calls from
affecting unrelated prompts, at the cost of requiring the user to opt in when
prior project knowledge would help. Other agent integrations remain separate
packages even when they share the same concise knowledge policy.

The repository ships the marketplace entry, plugin manifest, MCP dependency
metadata, and explicit compound-knowledge skill. The README documents
installation and on-demand use during Starlogz development.
