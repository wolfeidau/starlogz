# AGENTS.md — starlogz

Remote MCP server that lets developers and agents store and retrieve persistent insights about projects. GitHub OAuth2 for authentication; backing store is PostgreSQL.

---

## Layout

```
cmd/starlogz-server/              binary entry point, signal handling, logger init
internal/commands/                kong command structs (HTTPCmd, KeyGenCmd)
internal/middleware/              HTTP middleware (access log, CORS)
internal/oidc/                    OAuth2/OIDC server — JWKS, discovery, DCR, JWT verify, logout
internal/server/                  HTTP mux, MCP tool handlers, health endpoint
internal/store/                   store interface + types (Insight, WriteInsightParams, …)
internal/store/postgres/          PostgreSQL implementation + migration runner
internal/store/postgres/migrations/  embedded SQL migration files (1–12)
internal/telemetry/               OTel init (traces + metrics via OTLP gRPC)
spec/                             design specs (auth.md, persistence.md, refresh_tokens.md)
```

---

## Data model

The core entity is `Insight` (`internal/store/store.go`):

| Field      | Type     | Notes                                                           |
|------------|----------|-----------------------------------------------------------------|
| `id`       | UUID     | Primary key (uuidv7)                                            |
| `project_id` | UUID   | FK → projects                                                   |
| `key`      | text     | Optional stable identifier; unique per project among live rows  |
| `content`  | text     | The insight body                                                |
| `tags`     | text[]   | GIN-indexed for filtering                                       |
| `category` | text     | Required on write: `fact`, `decision`, `insight`, `preference`, `context`, `general` |
| `source`   | text     | Required on write: `user`, `repo`, `agent`, `command`                    |
| `created_by` | UUID   | FK → users                                                      |
| `created_at` | timestamptz | |
| `updated_at` | timestamptz | |
| `deleted_at` | timestamptz | NULL = live; set on soft-delete                             |

---

## MCP tools

All tools require `insights:read`. Write tools also require `insights:write`.

| Tool               | Scope         | Description |
|--------------------|---------------|-------------|
| `whoami`           | any           | Returns user ID and token scopes |
| `project_ensure`   | `insights:read`  | Creates a project if missing; returns it either way |
| `project_list`     | `insights:read`  | Lists projects in the caller's personal org |
| `insight_write`    | `insights:write` | Writes an insight; auto-creates project. With `key`, upserts. Requires `category` and `source`. |
| `insight_search`   | `insights:read`  | Full-text search over live insights |
| `insight_list`     | `insights:read`  | Lists live insights, newest first. Optional tag filter. |
| `insight_update`   | `insights:write` | Updates content and/or tags of an existing insight |
| `insight_delete`   | `insights:write` | Soft-deletes an insight |
| `insight_list_tags`| `insights:read`  | Returns tags ordered by usage frequency |

---

## Build and test

```bash
# Build
go build ./...

# Run all tests (store tests spin up a real PostgreSQL container via testcontainers-go)
go test ./...

# Run server locally
go run ./cmd/starlogz-server keygen --output key.jwk   # once
go run ./cmd/starlogz-server http --jwk-path key.jwk
```

Docker must be running for store integration tests.

---

## Key dependencies

| Package | Purpose |
|---------|---------|
| `github.com/alecthomas/kong` | CLI parsing |
| `github.com/modelcontextprotocol/go-sdk` | MCP server + OAuth2 middleware |
| `github.com/lestrrat-go/jwx/v3` | JWT sign/verify, JWKS, JWK key management |
| `github.com/jackc/pgx/v5` | PostgreSQL driver (pgxpool) |
| `github.com/testcontainers/testcontainers-go` | Real Postgres containers for integration tests |
| `go.opentelemetry.io/otel` | Traces and metrics via OTLP gRPC |

---

## Code conventions

- Errors: wrap with `%w`, log only at the top level. Never log and return the same error.
- Logging: use `slog` with `InfoContext`/`ErrorContext` in handlers for trace propagation.
- Contexts: first argument to every function that does I/O.
- Comments: only the *why*, never the *what*. One line maximum.
- Tests: use `testify/require`. Store tests use real Postgres via `testcontainers-go` — no mocking.

See `AGENT.md` for the full conventions reference.
