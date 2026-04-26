# Facts — Data Model & MCP API

> Version 0.1 · Draft · April 2026

## Contents

1. [Overview](#overview)
2. [Data model](#data-model)
3. [MCP tools](#mcp-tools)
4. [PostgreSQL schema](#postgresql-schema)
5. [v0.1 constraints](#v01-constraints)

---

## Overview

Facts are freeform text assertions stored against a project. A developer or agent working on a
codebase can write facts to record what it knows ("the prod DB is on postgres-01.internal"),
search for relevant context before making decisions, and delete stale information.

Each fact belongs to a project. Projects are owned by a user and addressed by a short slug.
On first successful GitHub login the user row is created; subsequent logins upsert it.

---

## Data model

### User

Created (or updated) on each successful GitHub OAuth login.

| Field | Type | Description |
|-------|------|-------------|
| `id` | UUID | Internal primary key |
| `github_id` | int8 | GitHub numeric user ID — matches JWT `sub` in v0.1 |
| `email` | text | Primary verified email |
| `login` | text | GitHub username |
| `created_at` | timestamptz | |
| `updated_at` | timestamptz | |

### Project

Owned by one user, addressed by `slug` (unique per owner).

| Field | Type | Description |
|-------|------|-------------|
| `id` | UUID | |
| `owner_id` | UUID | FK → `users.id` |
| `slug` | text | URL-safe short name, e.g. `myapp` |
| `name` | text | Display name |
| `created_at` | timestamptz | |

### Fact

| Field | Type | Description |
|-------|------|-------------|
| `id` | UUID | |
| `project_id` | UUID | FK → `projects.id` |
| `key` | text \| NULL | Optional stable identifier; unique per project among live facts |
| `content` | text | The fact body |
| `tags` | text[] | Freeform labels for filtering |
| `source_type` | text | `human` or `agent` |
| `created_by` | UUID | FK → `users.id` |
| `created_at` | timestamptz | |
| `updated_at` | timestamptz | |
| `deleted_at` | timestamptz \| NULL | NULL = live; set on soft-delete |

---

## MCP tools

All tools require `facts:read`. Write tools require `facts:write`.

### `project_ensure`

Creates the project if it does not exist; returns it either way. Use this when you want a custom
display name. `fact_write` auto-creates the project with `name = slug`, so explicit setup is not
required for simple cases.

**Requires:** `facts:read`

| Input | Required | Description |
|-------|----------|-------------|
| `slug` | yes | Short project identifier |
| `name` | no | Display name — defaults to `slug` |

**Returns:** `{ "id": "uuid", "slug": "...", "name": "..." }`

---

### `fact_write`

Writes a fact to a project. Auto-creates the project (name = slug) if it does not exist.

If `key` is provided and a live fact with that key already exists in the project, the content and
tags are updated in place. Otherwise a new fact is inserted.

**Requires:** `facts:write`

| Input | Required | Description |
|-------|----------|-------------|
| `project` | yes | Project slug |
| `content` | yes | The fact body |
| `key` | no | Stable identifier for upsert semantics |
| `tags` | no | String array of labels |

**Returns:** `{ "id": "uuid", "updated": false }` — `updated: true` when an existing keyed fact
was overwritten.

---

### `fact_search`

Full-text search over live facts in a project using PostgreSQL `tsvector`. Results are ordered by
relevance.

**Requires:** `facts:read`

| Input | Required | Description |
|-------|----------|-------------|
| `project` | yes | Project slug |
| `query` | yes | Search terms (passed to `plainto_tsquery`) |
| `tags` | no | Restrict to facts that have **all** of these tags |
| `limit` | no | Max results, default 20, max 100 |

**Returns:** `{ "facts": [ { "id", "key", "content", "tags", "updated_at" }, ... ] }`

---

### `fact_list`

Lists all live facts in a project, newest first.

**Requires:** `facts:read`

| Input | Required | Description |
|-------|----------|-------------|
| `project` | yes | Project slug |
| `tag` | no | Filter to facts that include this tag |
| `limit` | no | Max results, default 50, max 200 |

**Returns:** same shape as `fact_search`

---

### `fact_delete`

Soft-deletes a fact by ID. The row is retained with `deleted_at` set; it no longer appears in
list or search results.

**Requires:** `facts:write`

| Input | Required | Description |
|-------|----------|-------------|
| `id` | yes | Fact UUID |

**Returns:** `{}` on success; error if the fact is not found or already deleted.

---

## PostgreSQL schema

See `internal/store/migrations/1_initial_schema.sql`.

---

## v0.1 constraints

- **Personal projects only** — no org/team ownership; `org:admin` scope is reserved but not
  enforced
- **No fact versioning** — keyed-fact updates overwrite content and tags in place
- **No access control on facts** — any authenticated user can read or write any project by slug
- **`source_type` is always `human`** — API keys (which would set `agent`) are not implemented
  yet
- **English full-text only** — `to_tsvector('english', ...)` is hardcoded
- **No pagination cursor** — `limit` is the only bound; no keyset pagination yet
