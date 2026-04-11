# Project Facts MCP — Service Specification

> Version 0.1 · Draft · April 2026

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
sessions, are scoped to projects or organisations, and carry enough
metadata to distinguish human decisions from agent-inferred context.

The service exposes a small set of MCP tools over authenticated HTTP.
Developers connect via GitHub OAuth2 and manage API keys for agent use.
The backing store is PostgreSQL with full-text search via `tsvector`.

### Goals

- Agents record decisions, conventions, and context without manual
  developer intervention
- Developers can retrieve what agents have learned about a project
  across sessions
- Org-level facts act as a read-only knowledge base of conventions
  and architecture decisions
- Auth is developer-friendly: GitHub OAuth2 for humans, scoped API
  keys for agents
- Self-hostable from day one; structured for a future paid hosted service

### Non-goals (v0.1)

- Vector / semantic search (planned for v0.3)
- Google OAuth2 (second provider, post-v0.1)
- Real-time fact subscriptions or webhooks
- Fine-grained per-fact access control

---

## Data model

### Schema

```sql
-- Identity
CREATE TABLE users (
  id            UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  email         TEXT UNIQUE NOT NULL,
  display_name  TEXT,
  created_at    TIMESTAMPTZ DEFAULT now()
);

CREATE TABLE oauth_accounts (
  id           UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  user_id      UUID NOT NULL REFERENCES users(id),
  provider     TEXT NOT NULL,         -- 'github', 'google'
  provider_uid TEXT NOT NULL,         -- provider's stable user ID
  email        TEXT,
  UNIQUE (provider, provider_uid)
);

-- Orgs and projects
CREATE TABLE orgs (
  id         UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  slug       TEXT UNIQUE NOT NULL,
  name       TEXT NOT NULL,
  created_at TIMESTAMPTZ DEFAULT now()
);

CREATE TABLE org_members (
  org_id  UUID NOT NULL REFERENCES orgs(id),
  user_id UUID NOT NULL REFERENCES users(id),
  role    TEXT NOT NULL CHECK (role IN ('owner', 'admin', 'member')),
  PRIMARY KEY (org_id, user_id)
);

CREATE TABLE projects (
  id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  org_id      UUID NOT NULL REFERENCES orgs(id),
  slug        TEXT NOT NULL,
  name        TEXT NOT NULL,
  description TEXT,
  created_at  TIMESTAMPTZ DEFAULT now(),
  UNIQUE (org_id, slug)
);

-- Facts
CREATE TABLE facts (
  id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  project_id      UUID REFERENCES projects(id),  -- NULL = org-level
  scope           TEXT NOT NULL CHECK (scope IN ('project', 'org')),
  content         TEXT NOT NULL,
  tags            TEXT[],
  fact_type       TEXT CHECK (fact_type IN (
                    'decision', 'convention', 'context', 'warning', 'question'
                  )),
  confidence      FLOAT CHECK (confidence BETWEEN 0 AND 1) DEFAULT 1.0,
  source_type     TEXT NOT NULL CHECK (source_type IN ('human', 'agent')),
  author          TEXT NOT NULL,      -- username or agent ID
  related_ids     UUID[],             -- links to other facts
  source_url      TEXT,
  archived_at     TIMESTAMPTZ,        -- soft delete
  created_at      TIMESTAMPTZ DEFAULT now(),
  updated_at      TIMESTAMPTZ DEFAULT now(),
  search_vec      TSVECTOR GENERATED ALWAYS AS
                    (to_tsvector('english', content)) STORED
);

CREATE INDEX facts_search_idx      ON facts USING GIN (search_vec);
CREATE INDEX facts_project_scope_idx ON facts (project_id, scope, created_at DESC);
CREATE INDEX facts_tags_idx        ON facts USING GIN (tags);

-- Fact history (audit)
CREATE TABLE fact_history (
  id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  fact_id     UUID NOT NULL REFERENCES facts(id),
  content     TEXT NOT NULL,
  tags        TEXT[],
  confidence  FLOAT,
  changed_by  TEXT NOT NULL,
  note        TEXT,
  changed_at  TIMESTAMPTZ DEFAULT now()
);

-- API keys
CREATE TABLE api_keys (
  id           UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  user_id      UUID NOT NULL REFERENCES users(id),
  project_id   UUID REFERENCES projects(id),  -- NULL = org-scoped
  name         TEXT NOT NULL,
  key_hash     TEXT NOT NULL UNIQUE,
  scope        TEXT[] NOT NULL,   -- e.g. ['facts:read', 'facts:write']
  last_used_at TIMESTAMPTZ,
  expires_at   TIMESTAMPTZ,
  created_at   TIMESTAMPTZ DEFAULT now()
);
```

### Field reference — facts table

| Field        | Type       | Notes                                                                 |
|--------------|------------|-----------------------------------------------------------------------|
| `scope`      | enum       | `project` \| `org`. Org-scope facts require admin role to write.      |
| `fact_type`  | enum       | `decision` · `convention` · `context` · `warning` · `question`.      |
| `confidence` | float 0–1  | Defaults: 1.0 for humans, 0.8 for agents. Set by server.             |
| `source_type`| enum       | Set by server from credential. `human` = JWT, `agent` = API key.     |
| `related_ids`| uuid[]     | Soft links to related or contradicting facts.                        |
| `archived_at`| timestamptz| Soft delete. Null = active. Hard delete is admin-only.               |
| `search_vec` | tsvector   | Generated column. Full-text index over content. GIN indexed.         |

---

## Authentication

### GitHub OAuth2 flow (v0.1)

1. User hits `GET /auth/github` — server generates `state` token, stores
   in short-lived cookie, redirects to GitHub with `client_id`,
   `redirect_uri`, `state`.
2. User approves. GitHub redirects to
   `GET /auth/github/callback?code=xxx&state=yyy`.
3. Server validates `state` against cookie. Exchanges `code` for GitHub
   access token via `POST github.com/login/oauth/access_token`.
4. Server fetches `GET api.github.com/user` and
   `GET api.github.com/user/emails` (primary email may be private).
5. Upsert `users` + `oauth_accounts` in a transaction. Existing account
   → fetch user. New account → insert both rows.
6. Issue signed JWT (`sub`, `email`, `name`, `exp`). Set as
   `httpOnly; Secure; SameSite=Lax` cookie. Redirect to dashboard.

### Auth endpoints

| Endpoint                   | Auth required | Description                              |
|----------------------------|---------------|------------------------------------------|
| `GET /auth/github`         | None          | Initiates OAuth2 redirect                |
| `GET /auth/github/callback`| None          | Handles code exchange, issues JWT        |
| `POST /auth/logout`        | Session       | Clears session cookie                    |
| `GET /auth/me`             | Session       | Returns current user and org memberships |
| `POST /tokens`             | Session       | Creates API key, shows plaintext once    |
| `GET /tokens`              | Session       | Lists API keys (name, scope, last used)  |
| `DELETE /tokens/:id`       | Session       | Revokes an API key                       |

### Credential types

**Session JWT** — for developers using web UI or CLI. Set as `httpOnly`
cookie. 7-day expiry. Payload: `sub`, `email`, `name`. Server sets
`source_type = human` on all writes.

**API key** — for agents. Format: `pfk_live_`. Sent as
`Authorization: Bearer `. Hash stored, plaintext shown once. Server
sets `source_type = agent` on all writes.

### Environment variables

```bash
GITHUB_CLIENT_ID=        # from github.com/settings/developers
GITHUB_CLIENT_SECRET=    # never commit
GITHUB_REDIRECT_URI=     # e.g. https://yourservice.com/auth/github/callback
JWT_SECRET=              # long random string, rotate periodically
DATABASE_URL=            # postgres connection string
```

---

## MCP tools

All tools are exposed over HTTP with `Authorization: Bearer`. Session JWT
and API keys are both accepted. The server infers `source_type` from the
credential — callers cannot override this.

---

### `whoami`

Returns identity, org memberships, and token scopes. Agents should call
this first to verify they have the right access before writing.

**Input:** none

**Output:**
```json
{
  "user": { "id": "", "email": "", "display_name": "" },
  "orgs": [{ "id": "", "slug": "", "role": "owner|admin|member" }],
  "token_scope": ["facts:read", "facts:write"],
  "auth_type": "session | api_key"
}
```

---

### `list_projects`

Lists projects the caller has access to, optionally filtered by org.

**Scope required:** `facts:read`

**Input:**
```json
{ "org_slug": "optional" }
```

**Output:**
```json
{
  "projects": [{
    "id": "",
    "slug": "",
    "name": "",
    "org_slug": "",
    "fact_count": 0,
    "last_activity_at": ""
  }]
}
```

---

### `create_project`

Creates a new project under an org. Slug must be URL-safe and unique
per org.

**Scope required:** `org:admin`

**Input:**
```json
{
  "org_slug": "required",
  "slug": "e.g. api-rewrite",
  "name": "Human label",
  "description": "optional"
}
```

**Output:**
```json
{ "project": { "id": "", "slug": "", "name": "", "created_at": "" } }
```

---

### `record_fact`

Writes a fact, decision, or piece of context to a project. `source_type`
is set automatically by the server based on credential type. Agents should
link to contradicting facts via `related_fact_ids` rather than silently
overwriting.

**Scope required:** `facts:write`

**Input:**
```json
{
  "project_slug": "required",
  "content": "The fact, plain text or markdown",
  "tags": ["optional", "array"],
  "fact_type": "decision | convention | context | warning | question",
  "confidence": 0.9,
  "related_fact_ids": ["uuid"],
  "source_url": "https://github.com/..."
}
```

> `confidence` defaults: 0.8 for agents, 1.0 for humans. Callers may
> lower but not exceed their default.

**Output:**
```json
{
  "fact": {
    "id": "",
    "content": "",
    "tags": [],
    "fact_type": "",
    "author": "",
    "source_type": "human | agent",
    "confidence": 0.8,
    "created_at": ""
  }
}
```

---

### `search_facts`

Full-text search across accessible facts using PostgreSQL `tsvector`.
Scopes to the caller's accessible projects unless `project_slug` is
specified. Results ordered by relevance rank then recency.

**Scope required:** `facts:read`

**Input:**
```json
{
  "query": "required search string",
  "project_slug": "optional",
  "include_org_facts": true,
  "tags": ["optional AND filter"],
  "fact_type": "optional",
  "source_type": "human | agent",
  "limit": 10
}
```

**Output:**
```json
{
  "results": [{
    "id": "",
    "content": "",
    "tags": [],
    "fact_type": "",
    "project_slug": "",
    "scope": "project | org",
    "author": "",
    "source_type": "",
    "confidence": 1.0,
    "created_at": "",
    "rank": 0.85
  }],
  "total": 0
}
```

---

### `get_facts`

Structured retrieval without a search query. Useful at agent session
start to load recent project context. Supports pagination.

**Scope required:** `facts:read`

**Input:**
```json
{
  "project_slug": "required",
  "scope": "project | org | all",
  "tags": ["optional"],
  "fact_type": "optional",
  "source_type": "human | agent",
  "since": "2026-04-01T00:00:00Z",
  "limit": 20,
  "offset": 0
}
```

**Output:** same shape as `search_facts`, no `rank` field.

---

### `update_fact`

Corrects or annotates an existing fact. Always creates a history record
in `fact_history` before applying the change. Agents may only update
facts they authored unless the API key has `facts:admin` scope.

**Scope required:** `facts:write`

**Input:**
```json
{
  "fact_id": "required",
  "content": "optional replacement",
  "tags": ["optional replacement set"],
  "confidence": 0.9,
  "note": "reason for the update"
}
```

**Output:**
```json
{
  "fact": {},
  "previous_version": { "content": "", "updated_at": "" }
}
```

---

### `delete_fact`

Soft-deletes a fact by setting `archived_at`. Archived facts are excluded
from search and retrieval by default. Hard delete is an admin dashboard
operation only.

**Scope required:** `facts:write`

**Input:**
```json
{ "fact_id": "required", "reason": "optional" }
```

**Output:**
```json
{ "deleted": true, "fact_id": "", "archived_at": "" }
```

---

### `get_org_facts`

Read-only retrieval of org-level conventions and decisions. All org
members can read. Writing org facts requires `org:admin` scope via
`record_org_fact`.

**Scope required:** `facts:read`

**Input:**
```json
{
  "org_slug": "required",
  "tags": ["optional"],
  "fact_type": "optional",
  "limit": 20
}
```

**Output:** same shape as `get_facts`.

---

### `record_org_fact`

Writes an org-level convention or decision. Same input shape as
`record_fact` minus `project_slug`. Requires admin or owner role in
the org.

**Scope required:** `org:admin`

---

### `list_tags`

Returns existing tags for a project ordered by usage frequency. Agents
should call this before writing tags to avoid fragmentation (`auth` vs
`authentication` vs `oauth`).

**Scope required:** `facts:read`

**Input:**
```json
{ "project_slug": "required", "limit": 50 }
```

**Output:**
```json
{ "tags": [{ "name": "auth", "count": 12 }] }
```

---

## Agent usage patterns

### Session start

```
1. whoami           → verify identity and scopes
2. get_org_facts    → load org conventions before doing anything
3. list_tags        → load existing tags for the project
4. get_facts        → load recent project context (e.g. since=7d ago)
```

### Recording a decision mid-task

```
1. search_facts "connection pooling"   → check if already decided
2. record_fact  type=decision          → write new decision
               related_fact_ids=[...]  → link if it supersedes something
```

### Disagreeing with an existing fact

```
Option A — update_fact + note            → correct it with a reason
Option B — record_fact                   → write a new fact with
           related_fact_ids=[old_id]       a link to the original
           confidence=0.7                  and lower confidence

Never: silently write a contradicting fact with no link.
```

### Human review workflow

```
1. get_facts  source_type=agent, since=yesterday  → review agent writes
2. update_fact on anything that looks wrong       → correct with note
3. record_fact type=decision, confidence=1.0      → confirm or override
```

---

## Design rules

### Server-enforced invariants

- `source_type` is always set by the server from the credential type —
  callers cannot self-report it
- Agents default to `confidence=0.8`; humans default to `1.0`. Callers
  may lower but not exceed their default
- `update_fact` always writes to `fact_history` before applying the change
- Org facts are read-only for non-admins — `record_org_fact` returns 403
  for members
- Soft delete only from MCP tools — `archived_at` is set, row is never
  removed
- API keys store only the hash — plaintext is shown once on creation
  and never retrievable

### Tag hygiene

- Agents must call `list_tags` before writing new tags to avoid
  fragmentation
- Tags are lowercase, hyphen-separated. Server normalises on write:
  `Auth` → `auth`
- Future: a `suggest_tags` tool will propose canonical tags based on
  content

### Concurrency

- Multiple agents writing to the same project is supported — facts are
  append-only by default
- No optimistic locking on `update_fact` in v0.1 — last write wins,
  previous version captured in history

---

## Roadmap

| Version | Scope |
|---------|-------|
| v0.1 | GitHub OAuth2, JWT sessions, API keys, core fact tools, Postgres full-text search, self-hosted Docker Compose |
| v0.2 | Google OAuth2, API key scopes, Row Level Security policies, audit log endpoint, basic dashboard UI |
| v0.3 | Vector/semantic search via pg_vector, `suggest_tags` tool, tag canonicalisation, conflict detection |
| v0.4 | Usage metering, rate limiting per API key, hosted service tier, billing integration |
