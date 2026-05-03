# Project Facts MCP — Service Specification

> Version 0.2 · Draft · May 2026 · North-star

## Contents

1. [Overview](#overview)
2. [Data model](#data-model)
3. [Authentication](#authentication)
4. [MCP tools](#mcp-tools)
5. [Agent usage patterns](#agent-usage-patterns)
6. [Design rules](#design-rules)
7. [Roadmap](#roadmap)

---

## Overview

A remote MCP service that lets developers and agents record, retrieve,
and search persistent facts about their projects. Facts survive across
sessions, are scoped to projects within an org, and carry enough metadata
to distinguish human decisions from agent-inferred context.

The service exposes a small set of MCP tools over authenticated HTTP. The
authentication model is the standard MCP OAuth2 dance: clients discover
the authorization server, register dynamically (RFC 7591), and obtain a
bearer JWT after a GitHub-backed authorization code grant with PKCE. The
backing store is PostgreSQL with full-text search via `tsvector`.

This document is the **north-star spec**: it describes the steady-state
service. For the current state of any given subsystem, see the
sub-specs in `spec/`:

- `spec/auth.md` — OAuth2 flow as currently implemented
- `spec/facts.md` — facts data model and MCP tools as currently implemented
- `spec/persistence.md` — v0.2 statelessness, orgs migration, persistence
- `spec/refresh_tokens.md` — v0.2 refresh token grant

### Goals

- **Persistent agent memory.** Agents record decisions, conventions, and
  context once and retrieve them across sessions and across agents.
- **Org-shared knowledge.** Facts written within a shared org are visible
  to every member; org-level facts act as a read-only knowledge base of
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
- Real-time fact subscriptions or webhooks
- Fine-grained per-fact access control beyond the org boundary
- API keys distinct from OAuth2 JWTs (see [Future](#future))
- A web dashboard (planned for v0.3+)

---

## Data model

The schema below is the steady-state model. The current SQL lives under
`internal/store/postgres/migrations/`. `spec/persistence.md` describes
the v0.2 transition.

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
- **Facts** belong to one project (and transitively to one org).

### Schema (steady state)

```sql
CREATE TABLE users (
    id         UUID        PRIMARY KEY DEFAULT uuidv7(),
    github_id  BIGINT      NOT NULL UNIQUE,
    email      TEXT        NOT NULL,
    login      TEXT        NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE orgs (
    id         UUID        PRIMARY KEY DEFAULT uuidv7(),
    slug       TEXT        NOT NULL UNIQUE,
    name       TEXT        NOT NULL,
    kind       TEXT        NOT NULL CHECK (kind IN ('personal', 'shared')),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE org_members (
    org_id    UUID        NOT NULL REFERENCES orgs(id) ON DELETE CASCADE,
    user_id   UUID        NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    role      TEXT        NOT NULL CHECK (role IN ('owner', 'admin', 'member')),
    joined_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (org_id, user_id)
);

CREATE TABLE projects (
    id         UUID        PRIMARY KEY DEFAULT uuidv7(),
    org_id     UUID        NOT NULL REFERENCES orgs(id) ON DELETE CASCADE,
    slug       TEXT        NOT NULL,
    name       TEXT        NOT NULL,
    created_by UUID        NOT NULL REFERENCES users(id),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (org_id, slug)
);

CREATE TABLE facts (
    id            UUID        PRIMARY KEY DEFAULT uuidv7(),
    project_id    UUID        NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    key           TEXT,                    -- optional stable identifier for upsert
    content       TEXT        NOT NULL,
    tags          TEXT[]      NOT NULL DEFAULT '{}',
    source_type   TEXT        NOT NULL CHECK (source_type IN ('human', 'agent')),
    created_by    UUID        NOT NULL REFERENCES users(id),
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    deleted_at    TIMESTAMPTZ,             -- soft delete
    search_vector TSVECTOR    GENERATED ALWAYS AS
                              (to_tsvector('english', content)) STORED
);

CREATE UNIQUE INDEX facts_project_key_live
    ON facts (project_id, key)
    WHERE key IS NOT NULL AND deleted_at IS NULL;
CREATE INDEX facts_project_active ON facts (project_id) WHERE deleted_at IS NULL;
CREATE INDEX facts_search         ON facts USING GIN (search_vector);
CREATE INDEX facts_tags           ON facts USING GIN (tags);
```

### Field reference — facts table

| Field         | Type        | Notes                                                      |
|---------------|-------------|------------------------------------------------------------|
| `key`         | text        | Optional stable identifier; unique per project among live facts. Provide on `fact_write` to upsert in place. |
| `source_type` | enum        | `human` or `agent`. Set by the server from the credential type — callers cannot self-report. |
| `tags`        | text[]      | Lowercase, hyphen-separated by convention. Server normalises on write. |
| `deleted_at`  | timestamptz | Soft delete. NULL = active. Hard delete is not exposed via MCP. |
| `search_vector` | tsvector  | Generated column, GIN-indexed. English-only in v0.2. |

### OAuth2 / persistence tables

`oauth_clients`, `grants`, `pending_auths`, `auth_codes`, `revoked_tokens`
are all part of the auth subsystem. See `spec/auth.md` and
`spec/persistence.md` for their definitions.

### Fact richness — deferred

The original v0.1 spec proposed `fact_type`, `confidence`, `related_ids`,
`source_url`, and a `fact_history` audit table. These were intentionally
dropped from v0.1 and v0.2 to focus on getting the auth UX and
multi-instance deployment story right first. They are good candidates
for v0.3 once usage data shows which dimensions agents actually rely on.
The current `key` column gives much of what `related_ids` was meant to
provide — an agent that wants to "update the decision about connection
pooling" writes with `key="connection-pooling"` and gets in-place upsert.

---

## Authentication

Starlogz is itself the OAuth2 Authorization Server. MCP clients discover
it, register via DCR, and obtain bearer JWTs through an authorization
code + PKCE flow that uses GitHub as the upstream identity provider.

The full handshake, endpoint reference, JWKS format, and DCR rules are
specified in `spec/auth.md`. This section describes the model in summary.

### Credential type

A signed bearer JWT (ES384, P-384) issued by Starlogz. Sent as
`Authorization: Bearer <jwt>` on every authenticated request.

Claims:

| Claim   | Description                                                       |
|---------|-------------------------------------------------------------------|
| `iss`   | Server base URL                                                   |
| `sub`   | Internal `users.id` (UUID). v0.1 used the GitHub numeric ID; v0.2 switches to the internal UUID now that personal orgs need a stable identifier. |
| `aud`   | `["<base-url>/mcp"]` — RFC 8707 audience-bound                    |
| `email` | User's primary GitHub email                                       |
| `scope` | Space-delimited scope list                                        |
| `jti`   | Unique JWT ID, checked against `revoked_tokens` on every request  |
| `exp`   | 7-day expiry                                                      |
| `iat`   | Issued at                                                         |

### Scopes

| Scope         | Gates                                                          |
|---------------|----------------------------------------------------------------|
| `facts:read`  | Read facts, search, list projects and tags, whoami             |
| `facts:write` | Create, update, soft-delete facts                              |
| `org:admin`   | Create/delete projects, manage org membership, write org-level facts (when implemented) |

All MCP tool calls require at least `facts:read`. The `/mcp` endpoint
enforces this at the transport layer.

### Refresh tokens (v0.2)

Each token response includes a single-use opaque refresh token. The
client exchanges it at the token endpoint (`grant_type=refresh_token`)
to obtain a new JWT without redirecting the user back to GitHub. Full
flow and rotation rules in `spec/refresh_tokens.md`.

### Revocation

`POST /auth/logout` revokes the current JWT by inserting its `jti` into
`revoked_tokens`. The table is shared across instances, so logout takes
effect immediately everywhere.

### Environment variables

```bash
GITHUB_CLIENT_ID=        # GitHub App client ID
GITHUB_CLIENT_SECRET=    # GitHub App client secret
SERVER_URL=              # public base URL, used in discovery docs
DATABASE_URL=            # postgres DSN
TOKEN_ENCRYPTION_KEY=    # NaCl secretbox key for at-rest encryption
                         # of GitHub tokens in grants and auth_codes
HTTP_LISTEN_ADDR=        # default localhost:8088
OTEL_EXPORTER_OTLP_ENDPOINT=  # optional; enables tracing + metrics
```

The signing key is loaded from `--jwk-path` and must be the same file on
every instance. Generate once with `starlogz-server keygen --output key.jwk`.

---

## MCP tools

All tools require at minimum `facts:read`. Write tools require
`facts:write`. The exact JSON shapes for tools currently implemented
live in `spec/facts.md`. This section describes the steady-state surface.

### Implemented (v0.1 / v0.2)

| Tool             | Scope         | Purpose |
|------------------|---------------|---------|
| `whoami`         | (any)         | Returns user ID and token scopes. |
| `project_ensure` | `facts:read`  | Creates a project if missing; returns it either way. |
| `project_list`   | `facts:read`  | Lists projects accessible to the caller. |
| `fact_write`     | `facts:write` | Writes a fact. Auto-creates the project. With `key`, upserts in place. |
| `fact_search`    | `facts:read`  | Full-text search across live facts in a project. |
| `fact_list`      | `facts:read`  | Lists live facts in a project, newest first. |
| `fact_update`    | `facts:write` | Updates content and/or tags of an existing fact. |
| `fact_delete`    | `facts:write` | Soft-deletes a fact. |
| `fact_list_tags` | `facts:read`  | Returns tags for a project ordered by usage frequency. |

Each project-scoped tool takes a `project` slug. With orgs in v0.2, tools
also accept an optional `org` slug; if omitted, the user's personal org
is assumed. Single-user UX is unchanged.

### Planned with org rollout (v0.2 / v0.3)

| Tool            | Scope        | Purpose |
|-----------------|--------------|---------|
| `org_create`    | (auth)       | Creates a shared org with the caller as owner. |
| `org_list`      | `facts:read` | Lists orgs the caller belongs to. |
| `org_invite`    | `org:admin`  | Adds a user to an org by GitHub login. |
| `org_remove`    | `org:admin`  | Removes a member. |
| `org_facts_get` | `facts:read` | Reads org-scope conventions/decisions (when org-level facts ship). |
| `org_facts_write` | `org:admin` | Writes an org-scope fact. |

### Planned with fact richness (v0.3+)

If `fact_type` / `confidence` / `related_ids` ship, `fact_write` and
`fact_search` gain matching optional inputs. No new tools.

---

## Agent usage patterns

### Session start

```
1. whoami           → verify identity and scopes
2. project_list     → discover what projects the caller can see
3. fact_list_tags   → load existing tags for the target project
4. fact_list        → load recent project context
```

### Recording a decision mid-task

```
1. fact_search "connection pooling"  → check if already decided
2. fact_write key="connection-pooling"
                                     → write or upsert in place
```

### Disagreeing with an existing fact

```
Option A — fact_update + new content    → correct it in place
Option B — fact_write key=...           → upsert with the same key,
                                          replacing the old content
```

The simpler current model (no `confidence`, no `related_ids`) means
"contradicting" is "overwriting". When fact richness ships in v0.3, this
section will gain the link-don't-overwrite pattern from the v0.1 design.

### Human review workflow

When a future tool exposes `source_type=agent` filtering and audit history,
this section will describe the human review loop. For now, deleted facts
are gone from search and `fact_search` returns whoever wrote each fact.

---

## Design rules

### Server-enforced invariants

- `source_type` is set by the server from the credential type — callers
  cannot self-report. (v0.1 always writes `human`; once a separate
  agent-credential path exists, it will write `agent`.)
- The signing key is the same on every instance. Rotating it requires
  restarting all instances and accepting that outstanding JWTs become
  invalid. Multi-key JWKS rotation is planned for v0.3.
- Soft delete only via MCP — `deleted_at` is set, the row is never
  removed by tool calls. Hard delete is reserved for an admin path.
- `revoked_tokens` is consulted on every authenticated request. Logout
  takes effect across all instances immediately.
- GitHub access and refresh tokens are encrypted at rest with NaCl
  secretbox before being stored in `grants` or `auth_codes`.

### Statelessness

- Any process can serve any request. There is no in-memory state that
  changes correctness.
- Schema migrations take a Postgres advisory lock so concurrent boots do
  not race.
- HTTP servers shut down gracefully on SIGTERM, draining in-flight
  requests up to a configurable deadline (default 30s).

See `spec/persistence.md` for full details.

### Tag hygiene

- Agents should call `fact_list_tags` before writing new tags to avoid
  fragmentation.
- Tags are lowercase, hyphen-separated. Server normalises on write:
  `Auth` → `auth`.
- A `suggest_tags` tool that proposes canonical tags from content is a
  v0.3+ candidate.

### Concurrency

- Multiple agents writing to the same project is supported.
- Keyed `fact_write` is a per-key upsert: last write wins, no version
  history (until v0.3+ adds it).

---

## Roadmap

| Version | Scope |
|---------|-------|
| v0.1 (shipped) | OAuth2 AS with DCR + PKCE, JWT bearer tokens, GitHub upstream IdP, in-memory auth state, single-server, core fact tools, English full-text search, NaCl-encrypted GitHub token persistence in `grants`. |
| v0.2 (in progress) | Stateless server processes (auth state in Postgres, advisory-lock migrations, graceful shutdown), refresh token grant, org tenancy (personal + shared orgs, membership, project ownership by org). |
| v0.3 | Multi-key JWKS for hot key rotation, scheduled prune workers, session management endpoints (`DELETE /sessions/:id`), web dashboard, `org_*` MCP tools, second OAuth2 provider (likely Google), fact richness (`fact_type`, `confidence`, `related_ids`, `fact_history`). |
| v0.4 | Vector / semantic search via pgvector, `suggest_tags`, conflict detection, hosted service tier, usage metering, rate limiting. |

### Future (no committed version)

- **Standalone API keys** (`pfk_live_…`). The MCP OAuth2 flow with DCR
  covers the agent-bearer-token use case for now. A separate API key
  format may be added later for non-MCP automation, but it is not on the
  near-term plan.
- **Multi-provider OAuth at scale** — beyond GitHub + Google, e.g.
  Microsoft, Bitbucket, GitLab. Driven by demand.
- **Per-fact access control** finer than the org boundary.
