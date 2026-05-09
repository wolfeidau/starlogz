# starlogz

An MCP server that gives developers and AI agents a persistent, searchable facts store scoped to their projects. Agents can write what they discover ("the prod database lives on postgres-01.internal"), search before making decisions, and delete stale information, all over a standard MCP connection secured by OAuth2.

## The problem

AI agents working on a codebase have no memory between sessions. Every run re-discovers the same facts from scratch: which hosts run which services, which flags are deprecated, which team owns which module. starlogz gives those facts a home outside the agent context window, addressable by project slug and searchable by keyword or tag.

Human developers benefit too: notes written once stay available to every agent and collaborator working on the same project.

## How it works

1. An MCP client (Claude Code, Cursor, any compliant host) connects to `https://your-server/mcp`.
2. The client authenticates via OAuth2 — the full browser-based flow is handled automatically using Dynamic Client Registration (RFC 7591).
3. The issued JWT carries scopes (`facts:read`, `facts:write`) that gate every tool call.
4. Facts are stored in PostgreSQL with full-text search, soft-delete, and optional stable keys for upsert semantics.

## MCP tools

All tools require `facts:read`. Write tools require `facts:write`.

| Tool | Scope | Description |
|------|-------|-------------|
| `whoami` | `facts:read` | Returns your user ID and token scopes. Useful for verifying authentication. |
| `project_ensure` | `facts:read` | Creates a project if it does not exist; returns it either way. Use when you want a custom display name. |
| `fact_write` | `facts:write` | Writes a fact to a project. Auto-creates the project if it does not exist. Provide a `key` for upsert semantics. |
| `fact_search` | `facts:read` | Full-text search over live facts using PostgreSQL `tsvector`. Returns results ordered by relevance. |
| `fact_list` | `facts:read` | Lists all live facts in a project, newest first. Optional tag filter. |
| `fact_delete` | `facts:write` | Soft-deletes a fact by ID. Does not appear in search or list after deletion. |

See [`spec/facts.md`](spec/facts.md) for full input/output schemas.

## Quickstart

### Prerequisites

- Go 1.26+
- Docker (for local Postgres)
- A [GitHub App](https://github.com/settings/apps) (not an OAuth App) with the callback URL set to `http://localhost:8088/auth/github/callback` and **Expire user authorization tokens** enabled

### Install a release binary

```bash
curl -fsSL https://raw.githubusercontent.com/wolfeidau/starlogz/main/install.sh | bash
```

This downloads the latest release for your OS and architecture, verifies the SHA256 checksum, and installs `starlogz-server` to `/usr/local/bin`. Use `sudo` automatically if the directory is not writable.

To install to a different directory:

```bash
curl -fsSL https://raw.githubusercontent.com/wolfeidau/starlogz/main/install.sh | INSTALL_DIR=~/bin bash
```

Or download and run directly:

```bash
./install.sh --dir ~/bin
```

### Run locally

```bash
# Start Postgres
docker compose up -d postgres

# Generate a signing key (once)
go run ./cmd/starlogz-server keygen --output key.jwk

# Start the server
go run ./cmd/starlogz-server http --jwk-path key.jwk
```

The server listens on `http://localhost:8088`. Point an MCP client at `http://localhost:8088/mcp`.

The `bin/start-dev` script wraps this: it generates `key.jwk` if missing, then runs the server and tails logs to `logs/server.log`.

```bash
./bin/start-dev
```

### Connect with MCP Inspector

```bash
npx @modelcontextprotocol/inspector
```

Point it at `http://localhost:8088/mcp`. The inspector walks through the full OAuth2 browser flow.

## Configuration

All configuration is via environment variables.

| Variable | Default | Required | Description |
|----------|---------|----------|-------------|
| `HTTP_LISTEN_ADDR` | `localhost:8088` | No | TCP listen address |
| `SERVER_URL` | `http://localhost:8088` | No | Public base URL, used in OAuth2 discovery documents |
| `GITHUB_CLIENT_ID` | (none) | Yes | GitHub app client ID |
| `GITHUB_CLIENT_SECRET` | (none) | Yes | GitHub app client secret |
| `DATABASE_URL` | (none) | Yes | PostgreSQL connection string (pgx DSN) |
| `OTEL_EXPORTER_OTLP_ENDPOINT` | (none) | No | OTLP collector endpoint. Omit to disable telemetry entirely. |
| `OTEL_EXPORTER_OTLP_HEADERS` | (none) | No | e.g. `x-honeycomb-team=<key>` |

## Commands

```
starlogz-server http     --jwk-path <path>   # Run the MCP HTTP server
starlogz-server keygen   --output <path>      # Generate an ES384 signing key (JWK)
```

`keygen` prints the public key fingerprint (SHA256) on success. Store `key.jwk` securely; it signs every token the server issues.

## Authentication

starlogz implements a standards-compliant OAuth2 authorization server:

- **Discovery** `/.well-known/oauth-authorization-server` (RFC 8414)
- **Dynamic Client Registration** `/oauth2/register` (RFC 7591, unauthenticated)
- **Authorization** `/oauth2/authorize` — PKCE required (S256 only)
- **Token** `/oauth2/token` — `authorization_code` grant only
- **JWKS** `/.well-known/jwks` — public key set, cached 24 h
- **Logout** `/auth/logout` — revokes the bearer token; JTI added to in-memory blocklist

Tokens are ES384-signed JWTs with a 7-day expiry. Every token includes a `jti` (UUID v4) checked against the revocation blocklist on each request.

**GitHub App required.** starlogz uses a GitHub App (not an OAuth App) as its identity provider. Create one at [github.com/settings/apps](https://github.com/settings/apps) and enable **Expire user authorization tokens** in its settings — this causes GitHub to issue short-lived access tokens (8 h) alongside a long-lived refresh token (~184 days), which starlogz stores encrypted in the `grants` table for future silent renewal.

User permissions requested: `read:user`, `user:email`. If the primary email is private, the server falls back to the `/user/emails` endpoint to obtain a verified address.

GitHub is the only supported identity provider right now. Additional providers (GitLab, Google, and others) are planned using [`golang.org/x/oauth2`](https://pkg.go.dev/golang.org/x/oauth2).

See [`spec/auth.md`](spec/auth.md) for the full flow.

## Database

PostgreSQL is the only backing store. The schema is applied automatically at startup via an embedded migration runner.

### Schema overview

`users`: created or upserted on each successful GitHub login.

`projects`: owned by one user, addressed by `(owner_id, slug)`. Auto-created on first `fact_write`.

`facts`: the core table. Key fields:

| Column | Type | Notes |
|--------|------|-------|
| `key` | `text \| NULL` | Optional stable identifier; unique per project among live facts |
| `content` | `text` | The fact body |
| `tags` | `text[]` | GIN-indexed for tag filtering |
| `search_vector` | `tsvector` | Generated column; GIN-indexed for full-text search |
| `deleted_at` | `timestamptz \| NULL` | `NULL` = live; set on soft-delete |

Facts are never physically deleted. `deleted_at` gates all list and search queries.

### psql wrapper

```bash
./bin/psql                 # connects to the Docker Compose Postgres instance
./bin/psql -c "SELECT count(*) FROM facts WHERE deleted_at IS NULL;"
```

## Tests

```bash
go test ./...
```

Store tests spin up a real PostgreSQL container via `testcontainers-go`. Docker must be running.

- `internal/oidc/` — full OAuth2 flow: DCR, PKCE authorization, token exchange, revocation
- `internal/server/` — health endpoint, JWT middleware, MCP tool dispatch
- `internal/store/` — user/project/fact CRUD, full-text search, soft-delete

## Observability

Traces and metrics are exported via OTLP gRPC when `OTEL_EXPORTER_OTLP_ENDPOINT` is set. All HTTP handlers are wrapped with `otelhttp`. Omitting the endpoint disables telemetry with no overhead.

## Current limitations

- Personal projects only, no org or team ownership
- No fact versioning, keyed-fact updates overwrite content and tags in place
- No cross-user access control, any authenticated user can read or write any project by slug
- English full-text only, `to_tsvector('english', ...)` is hardcoded
- No pagination, cursor `limit` is the only bound on list and search results
- `source_type` is always `human` API keys (which would set `agent`) are not yet implemented

## Built with AI

This project is built with AI-assisted development workflows, using tools such as [Claude Code](https://claude.ai/code) and [Cursor](https://cursor.com).

## License

This application is released under Apache 2.0 license and is copyright [Mark Wolfe](https://www.wolfe.id.au).
