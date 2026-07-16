# Codex compound-knowledge plugin

> Status: Implemented decision
> Last reviewed: 2026-07-16
> Authority: Historical rationale and lasting constraints; the packaged skill, plugin manifests, and README define current implementation details.

## Context

Starlogz provides durable project memory through MCP, but agents need a
repeatable policy for deciding when to recall, verify, and persist knowledge.
A personal Codex skill validated the workflow during development, but users
needed a supported and repository-owned distribution path.

## Decision

Package the workflow as the `starlogz-codex` plugin in this repository and make
it discoverable through the repository marketplace. The plugin declares the
Starlogz MCP dependency and guides users to connect their own deployment; it
does not bundle or operate a server.

The skill follows this lifecycle:

1. Derive the project slug from the current repository unless the user supplies
   one.
2. Recall task-specific decisions, conventions, architecture, and known
   pitfalls before meaningful work.
3. Verify recalled insights against explicit user instructions and current
   repository evidence.
4. Persist only reusable, specific, verified, minimal knowledge after work.
5. Continue safely with repository-local context when Starlogz is unavailable.

Repository files remain authoritative. The skill never stores credentials,
access tokens, private keys, secrets, raw logs, speculative findings, or
step-by-step activity. Stable authoritative values use keyed upserts; decisions,
discoveries, and incidents remain append-only history.

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
The initial package targets Codex; other agent integrations remain separate
packages even when they share the same knowledge policy.

The repository now ships the marketplace entry, plugin manifest, MCP dependency
metadata, and compound-knowledge skill. The README documents installation and
Starlogz's use as persistent memory during its own development.
