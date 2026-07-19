# AGENTS.md — starlogz

Remote MCP server that lets developers and agents store and retrieve persistent
insights about projects. GitHub-backed OAuth2 with DCR authenticates MCP clients;
the dashboard uses opaque database-backed sessions. PostgreSQL is the backing
store.

Specifications and implemented decision records live in `spec/`. Read
`spec/README.md` and check each document's lifecycle status before using it.
Current contracts define behavior that code and tests must preserve;
implemented decisions retain historical rationale, not implementation detail.
Read `spec/auth.md` before changing the OAuth2 flow or adding related endpoints.

---

## Layout

```
cmd/starlogz-server/              binary entry point, signal handling, logger init
internal/commands/                kong command structs (HTTPCmd, KeyGenCmd)
internal/middleware/              HTTP middleware (access log, CORS)
internal/oidc/                    OAuth2/OIDC server — JWKS, discovery, DCR, JWT verify, logout
internal/server/                  HTTP mux, MCP tool handlers, health endpoint
internal/server/public/           generated dashboard assets embedded in the server binary
internal/store/                   store interface + types (Insight, WriteInsightParams, …)
internal/store/postgres/          PostgreSQL implementation + migration runner
internal/store/postgres/migrations/  embedded SQL migration files (1–19)
internal/telemetry/               OTel init (traces + metrics via OTLP gRPC)
spec/                             current contracts and implemented decision records
ui/                               React dashboard source and generated Connect clients
```


## Data model

The core entity is `Insight` (`internal/store/store.go`):

| Field      | Type     | Notes                                                           |
|------------|----------|-----------------------------------------------------------------|
| `id`       | UUID     | Primary key (uuidv7)                                            |
| `project_id` | UUID   | FK → projects                                                   |
| `key`      | text     | Optional stable identifier; unique per project among live rows. Omit for append-only log entries; supply a key to upsert (each write overwrites the previous value in-place). |
| `content`  | text     | The insight body                                                |
| `tags`     | text[]   | GIN-indexed for filtering                                       |
| `category` | text     | Required on write: `fact`, `decision`, `insight`, `preference`, `context`, `general` |
| `source`   | text     | Required on write: `user`, `repo`, `agent`, `command`                    |
| `created_by` | UUID   | FK → users                                                      |
| `created_at` | timestamptz | |
| `updated_at` | timestamptz | |
| `revision` | integer | Positive optimistic-concurrency value; starts at 1 and increments on accepted state changes. |
| `deleted_at` | timestamptz | NULL = live; set on soft-delete                             |

`insight_revisions` stores immutable full snapshots keyed by
`(insight_id, revision)`. Current-state list and search remain backed by
`insights`.

---

## MCP tools

All tools require `insights:read`. Write tools also require `insights:write`.

| Tool               | Scope         | Description |
|--------------------|---------------|-------------|
| `whoami`           | any           | Returns user ID and token scopes |
| `project_ensure`   | `insights:read`  | Creates a project if missing; returns it either way |
| `project_list`     | `insights:read`  | Lists projects in the caller's personal org |
| `insight_write`    | `insights:write` | Writes an insight; auto-creates project. Requires `category` and `source`. With `key`, upserts in-place — suited for single authoritative values (e.g. `preferred-language`). Without `key`, appends a new row — suited for logs, decisions, and observations. Accepts optional `expected_revision`; returns revision and link warnings. |
| `insight_get`      | `insights:read`  | Gets one insight by ID or key with bounded outgoing links and backlinks. |
| `insight_history`  | `insights:read`  | Lists immutable revisions for an insight by ID, including soft-deleted insights, with opaque cursor continuation. |
| `insight_restore`  | `insights:write` | Restores an earlier snapshot as a new live revision. Requires `expected_revision`; returns revision and link warnings. |
| `insight_search`   | `insights:read`  | Full-text search over live insights by `rank DESC, updated_at DESC, id DESC`. Returns bounded snippets and metadata; use `insight_get` for full content. `query_mode=all` requires all terms; `query_mode=web` supports `OR`, quoted phrases, and exclusions. `tag_mode=all\|any` controls tag matching. Modes default to `all`; opaque cursor continuation is optional. |
| `insight_list`     | `insights:read`  | Lists live insights by `updated_at DESC, id DESC`. Optional tag filter and opaque cursor continuation. |
| `insight_update`   | `insights:write` | Updates content and/or tags of an existing insight. Accepts optional `expected_revision`; content changes return link warnings and tag-only updates omit them. |
| `insight_delete`   | `insights:write` | Soft-deletes an insight. Accepts optional `expected_revision` and returns the deletion revision. |
| `insight_list_tags`| `insights:read`  | Returns tags ordered by usage frequency |

---

## Key dependencies

| Package | Purpose |
|---------|---------|
| `github.com/alecthomas/kong` | CLI parsing |
| `github.com/modelcontextprotocol/go-sdk` | MCP server + OAuth2 middleware |
| `github.com/lestrrat-go/jwx/v3` | JWT sign/verify, JWKS, JWK key management |
| `github.com/google/uuid` | `jti` and `client_id` generation |
| `github.com/jackc/pgx/v5` | PostgreSQL driver (pgxpool) |
| `github.com/lmittmann/tint` | Coloured slog handler for interactive terminals |
| `github.com/testcontainers/testcontainers-go` | Real Postgres containers for integration tests |
| `github.com/yuin/goldmark` | CommonMark parsing and insight-link AST extension |
| `github.com/microcosm-cc/bluemonday` | Allowlist sanitization for server-rendered insight HTML |
| `go.opentelemetry.io/otel` | Traces and metrics via OTLP gRPC |

---

## Build and test

Run project commands through `mise exec --` so the repository's configured tool versions are used. Build the dashboard assets before Go package loading; `internal/server/web.go` embeds `internal/server/public/*`.

```bash
# Install UI dependencies and generate embedded dashboard assets
mise exec -- bun install --frozen-lockfile
mise exec -- bun run build

# Build
mise exec -- go build ./...

# Run all tests (store tests spin up a real PostgreSQL container via testcontainers-go)
mise exec -- go test ./...

# Generate a signing key (once)
mise exec -- go run ./cmd/starlogz-server keygen --output key.jwk

# Run database migrations only (then exit)
mise exec -- go run ./cmd/starlogz-server migrate

# Start the server
mise exec -- go run ./cmd/starlogz-server http --jwk-path key.jwk

# Start the server with coloured debug logging (development mode)
mise exec -- go run ./cmd/starlogz-server --development http --jwk-path key.jwk

# Query the local Postgres instance (runs psql inside the docker compose container)
bin/psql
```

Docker must be running for store integration tests.

Deploy Lambda changes with `mise exec -- bin/deploy`; Terraform consumes the artifact uploaded by that script and does not build or upload application code itself.

Key env vars:

| Var | Default | Description |
|-----|---------|-------------|
| `HTTP_LISTEN_ADDR` | `localhost:8088` | TCP listen address |
| `LOG_LEVEL` | `INFO` in production; `DEBUG` with `--development` | Application log level. Accepts `slog.Level` text values (`DEBUG`, `INFO`, `WARN`, `ERROR`) with optional `+N` or `-N` offsets. |
| `SERVER_URL` | `http://localhost:8088` | Public base URL (used in discovery docs) |
| `GITHUB_CLIENT_ID` | _(required)_ | GitHub App client ID |
| `GITHUB_CLIENT_SECRET` | _(required)_ | GitHub App client secret |
| `DATABASE_URL` | _(required)_ | PostgreSQL connection string (pgx DSN) |
| `TOKEN_ENCRYPTION_KEY` | _(required)_ | Base64-encoded 32-byte key for encrypting stored GitHub tokens (`openssl rand -base64 32`) |
| `REFRESH_TOKEN_GRACE_PERIOD` | `30s` | How long a rotated refresh token remains accepted for retry; `0s` to disable |
| `RETIRED_REFRESH_TOKEN_RETENTION` | `24h` | How long hashed retired refresh tokens are retained for refresh diagnostics |
| `UI_SESSION_IDLE_TTL` | `168h` | How long an inactive dashboard session remains valid |
| `UI_SESSION_TTL` | `720h` | Maximum lifetime of a dashboard session |
| `EVENT_BUS_NAME` | _(unset = disabled)_ | EventBridge bus for privacy-safe core-flow events |
| `ENVIRONMENT` | `local` | Deployment environment included in wide events |
| `SENTRY_DSN` | _(unset = disabled)_ | Sentry DSN; enables error reporting and structured log capture |
| `SENTRY_ENVIRONMENT` | — | Sentry environment tag (e.g. `production`) |
| `OTEL_EXPORTER_OTLP_ENDPOINT` | _(unset = disabled)_ | OTLP collector endpoint |
| `OTEL_EXPORTER_OTLP_HEADERS` | — | e.g. Honeycomb API key |

Telemetry is opt-in: if `OTEL_EXPORTER_OTLP_ENDPOINT` is not set, no exporters are created and no connection is attempted. Sentry is likewise opt-in: omitting `SENTRY_DSN` disables it entirely.

---

## Code conventions

### PostgreSQL migrations

Migrations run transactionally by default. An index added to an existing
populated table uses `CREATE INDEX CONCURRENTLY` and starts its migration file
with `-- starlogz:concurrent-index <index_name>`. The runner executes that
migration outside a transaction, repairs an invalid prior build, and records
the schema version after PostgreSQL reports the index valid. The file contains
one concurrent index statement and does not insert into `schema_migrations`.

### Errors

Return errors up the stack; log only at the top level (the command `Run` function or the HTTP handler boundary). Never log an error and then return it — it will be logged twice.

Always wrap with context using `%w`:

```go
return fmt.Errorf("failed to parse base URL: %w", err)
```

Do not add error handling for impossible cases. Trust the compiler and internal invariants.

### Logging

Use `slog` throughout. Pass the logger via `Globals.Logger` rather than calling `slog.Default()` in business logic. Reserve `slog.Default()` for package-level handlers (middleware, telemetry) where dependency injection is impractical.

Use `InfoContext` / `ErrorContext` inside HTTP handlers so trace IDs propagate automatically.

```go
logger.InfoContext(r.Context(), "access", slog.String("method", r.Method), ...)
```

### Contexts

Pass `context.Context` as the first argument to every function that does I/O, makes external calls, or should respect cancellation. Do not store contexts in structs. Use the context for both cancellation and OTel trace propagation.

### Package names

Lowercase, single word, descriptive of the domain — not the layer. Examples: `oidc`, `middleware`, `telemetry`, `tokens`, `commands`. Avoid generic names like `util`, `helpers`, `common`.

### Comments

Only comment the **why**, never the **what**. No docstrings for obvious functions. A one-line comment is the maximum.

### HTTP handlers

Every handler checks its method explicitly and returns 405 for wrong-method requests before doing any work. Stub endpoints return 501 with a JSON body, not 404.

Middleware chain order: `otelhttp → AccessLog → mux`

### OAuth2 / OIDC

OAuth2/OIDC protocol logic lives in `internal/oidc/`. Dashboard client and opaque web-session handling lives in `internal/server/ui_auth.go`. Do not add OAuth2 logic to `internal/commands/http.go`.

Tokens are ES384-signed JWTs. Every issued token must include a `jti` (UUID v4). `VerifyJWT` checks the `jti` through the persistent `RevocationStore` before accepting a token.

The first-party dashboard OAuth client registers the `authorization_code` grant only. It exchanges the code for an access token, then creates an opaque database-backed web session; it does not retain an OAuth refresh grant.

---

## Testing

### Automated (httptest)

Use `net/http/httptest` for all deterministic endpoints. Tests go in `internal/oidc/` and `internal/middleware/` alongside the code they test. No mocking of the OIDC server — construct a real `oidc.Server` with a generated test key.

```go
privkey, _ := ecdsa.GenerateKey(elliptic.P384(), rand.Reader)
// wrap with jwk, pass to oidc.NewServer(...)
```

Use `github.com/stretchr/testify/require` for assertions. Prefer `require` over `assert` so test functions fail fast on the first unmet condition.

```go
require.NoError(t, err)
require.Equal(t, http.StatusCreated, resp.StatusCode)
```

Do not mock the database. Tests that touch Postgres use a real database via
`testcontainers-go` (postgres module). Each test gets its own cloned database
from a migrated template via `newTestStore(t)` in
`internal/store/postgres/store_test.go`.

### End-to-end (MCP Inspector)

Use the MCP Inspector (`npx @modelcontextprotocol/inspector`) to validate the full OAuth2 + MCP flow interactively. This is the primary tool for validating the browser-interactive GitHub OAuth2 leg. Point it at `http://localhost:8088/mcp`.

---

## Git conventions

Branch names: `feat_<short_description>`, `fix_<short_description>`.
Commits: conventional commits style (`feat:`, `fix:`, `chore:`, `docs:`).
