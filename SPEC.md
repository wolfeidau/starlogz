# Project Insights MCP — Service Specification

> Version 0.2 · May 2026 · North-star

## Contents

1. [Overview](#overview)
2. [Data model](#data-model)
3. [Authentication](#authentication)
4. [MCP tools](#mcp-tools)
5. [Agent usage patterns](#agent-usage-patterns)
6. [Roadmap](#roadmap)

---

## Overview

A remote MCP service that lets developers and agents record, retrieve,
and search persistent insights about their projects. Insights survive across
sessions, are scoped to projects within an org, and carry enough metadata
to distinguish human decisions from agent-inferred context.

The service exposes a small set of MCP tools over authenticated HTTP. The
authentication model is the standard MCP OAuth2 dance: clients discover
the authorization server, register dynamically (RFC 7591), and obtain a
bearer JWT after a GitHub-backed authorization code grant with PKCE. The
backing store is PostgreSQL with full-text search via `tsvector`.

For implementation detail see:

- `AGENTS.md` — commands, env vars, tool reference, code conventions
- `spec/auth.md` — OAuth2 flow as currently implemented
- `spec/persistence.md` — statelessness, orgs migration, persistence
- `spec/refresh_tokens.md` — refresh token grant

### Goals

- **Persistent agent memory.** Agents record decisions, conventions, and
  context once and retrieve them across sessions and across agents.
- **Org-shared knowledge.** Insights written within a shared org are visible
  to every member; org-level insights act as a read-only knowledge base of
  conventions and architecture decisions.
- **Stateless from day one.** Any server process can serve any request;
  rolling restarts and multi-instance deployments are first-class. State
  lives in Postgres, not in process memory.
- **MCP-native auth.** Clients use the standard MCP OAuth2 flow with DCR
  and PKCE; no proprietary credential format. GitHub is the upstream
  identity provider for v0.2; additional providers come later.
- **Self-hostable.** A single binary plus Postgres is enough to run the
  service. The same code path runs as one instance or many.

### Non-goals (v0.2)

- Vector / semantic search (planned for v0.3+)
- Google or other OAuth2 providers (planned for v0.3+)
- Real-time insight subscriptions or webhooks
- Fine-grained per-insight access control beyond the org boundary
- API keys distinct from OAuth2 JWTs (see [Future](#future))
- A web dashboard (planned for v0.3+)

---

## Data model

### Identity and tenancy

- **Users** are GitHub-authenticated identities. Each user has one row
  keyed by `github_id`.
- **Orgs** are the tenant boundary. Two kinds:
  - *Personal* — created automatically on first login, sole-member, owned
    by the user. Slug defaults to the GitHub login.
  - *Shared* — created explicitly, joined by invitation, with roles
    `owner`, `admin`, `member`.
- **Org members** is a many-to-many between users and orgs with a role.
- **Projects** belong to one org. Slugs are unique within an org.
- **Insights** belong to one project (and transitively to one org).

### Insights

The core entity. Key semantic fields:

| Field        | Notes                                                                          |
|--------------|--------------------------------------------------------------------------------|
| `key`        | Optional stable identifier. Supply to upsert in place; omit to append.        |
| `category`   | Required. One of: `fact`, `decision`, `insight`, `preference`, `context`, `general`. |
| `source`     | Required. One of: `user`, `repo`, `agent`, `command`.                         |
| `tags`       | Lowercase, hyphen-separated. Server normalises on write.                       |
| `deleted_at` | Soft-delete. NULL = active. Hard delete is not exposed via MCP.                |

Insights support full-text search via a generated `tsvector` column (English, v0.2).

### Deferred richness

The v0.1 spec proposed `confidence`, `related_ids`, `source_url`, and an
`insight_history` audit table. These were dropped from v0.1 and v0.2 to
focus on auth UX and multi-instance deployment. They are v0.3+ candidates
once usage shows which dimensions agents actually rely on. The `key` field
covers much of the `related_ids` use case — writing with a stable key gives
in-place upsert without needing explicit links.

---

## Authentication

Starlogz is itself the OAuth2 Authorization Server. MCP clients discover
it, register via DCR, and obtain bearer JWTs through an authorization
code + PKCE flow that uses GitHub as the upstream identity provider.

Tokens are short-lived ES384-signed JWTs. Each token response also
includes a single-use opaque refresh token so clients can rotate silently
without redirecting the user to GitHub again.

### Scopes

| Scope            | Gates                                                                  |
|------------------|------------------------------------------------------------------------|
| `insights:read`  | Read insights, search, list projects and tags, whoami                  |
| `insights:write` | Create, update, soft-delete insights                                   |
| `org:admin`      | Create/delete projects, manage org membership, write org-level insights (when implemented) |

All MCP tool calls require at least `insights:read`.

---

## MCP tools

### Implemented (v0.1 / v0.2)

| Tool               | Scope            | Purpose |
|--------------------|------------------|---------|
| `whoami`           | (any)            | Returns user ID and token scopes. |
| `project_ensure`   | `insights:read`  | Creates a project if missing; returns it either way. |
| `project_list`     | `insights:read`  | Lists projects accessible to the caller. |
| `insight_write`    | `insights:write` | Writes an insight. Auto-creates the project. With `key`, upserts in place. Requires `category` and `source`. |
| `insight_search`   | `insights:read`  | Full-text search across live insights in a project. |
| `insight_list`     | `insights:read`  | Lists live insights in a project, newest first. |
| `insight_update`   | `insights:write` | Updates content and/or tags of an existing insight. |
| `insight_delete`   | `insights:write` | Soft-deletes an insight. |
| `insight_list_tags`| `insights:read`  | Returns tags for a project ordered by usage frequency. |

### Planned with org rollout (v0.3)

| Tool                 | Scope           | Purpose |
|----------------------|-----------------|---------|
| `org_create`         | (auth)          | Creates a shared org with the caller as owner. |
| `org_list`           | `insights:read` | Lists orgs the caller belongs to. |
| `org_invite`         | `org:admin`     | Adds a user to an org by GitHub login. |
| `org_remove`         | `org:admin`     | Removes a member. |
| `org_insights_get`   | `insights:read` | Reads org-scope conventions/decisions. |
| `org_insights_write` | `org:admin`     | Writes an org-scope insight. |

---

## Agent usage patterns

### Session start

```
1. whoami               → verify identity and scopes
2. project_list         → discover what projects the caller can see
3. insight_list_tags    → load existing tags for the target project
4. insight_list         → load recent project context
```

### Recording a decision mid-task

```
1. insight_search "connection pooling"  → check if already decided
2. insight_write key="connection-pooling" category="decision"
                                        → write or upsert in place
```

### Disagreeing with an existing insight

```
Option A — insight_update + new content  → correct it in place
Option B — insight_write key=...         → upsert with the same key,
                                           replacing the old content
```

The current model (no `confidence`, no `related_ids`) means contradicting
is overwriting. Insight richness in v0.3 will add the link-don't-overwrite
pattern.

### Tag hygiene

Call `insight_list_tags` before writing new tags to avoid fragmentation.
Tags are lowercase, hyphen-separated; the server normalises on write.

---

## Roadmap

### Shipped

- OAuth2 AS with DCR + PKCE, JWT bearer tokens, GitHub upstream IdP
- Core insight tools with full-text search (English)
- NaCl-encrypted GitHub token persistence
- Stateless server processes — auth state in Postgres, advisory-lock migrations, graceful shutdown
- Refresh token grant with rotation and grace period
- Personal org tenancy, audit log

### Planned

- Shared orgs — creation, invitations, membership roles, `org_*` MCP tools
- Multi-key JWKS for zero-downtime key rotation
- Session management (`DELETE /sessions/:id`)
- Second OAuth2 provider (likely Google)
- Insight richness — `confidence`, `related_ids`, `insight_history`
- Vector / semantic search via pgvector
- `suggest_tags` tool
- Web dashboard
- Hosted service tier — usage metering, rate limiting

### Future (no committed timeline)

- **Standalone API keys** (`pfk_live_…`). The MCP OAuth2 flow with DCR
  covers the agent-bearer-token use case for now. A separate API key
  format may be added later for non-MCP automation.
- **Multi-provider OAuth at scale** — beyond GitHub + Google, e.g.
  Microsoft, Bitbucket, GitLab. Driven by demand.
- **Per-insight access control** finer than the org boundary.
