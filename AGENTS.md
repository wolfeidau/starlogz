# AGENTS.md — starlogz

Remote MCP server that lets developers and agents store and retrieve persistent insights about projects. GitHub OAuth2 for humans, scoped API keys for agents. Backing store is PostgreSQL.

Specs live in `spec/` — read them before adding endpoints. `spec/auth.md` covers the OAuth2 flow in detail.

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
internal/store/postgres/migrations/  embedded SQL migration files (1–13)
internal/telemetry/               OTel init (traces + metrics via OTLP gRPC)
spec/                             design specs (auth.md, persistence.md, refresh_tokens.md)
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
| `deleted_at` | timestamptz | NULL = live; set on soft-delete                             |

---

## MCP tools

All tools require `insights:read`. Write tools also require `insights:write`.

| Tool               | Scope         | Description |
|--------------------|---------------|-------------|
| `whoami`           | any           | Returns user ID and token scopes |
| `project_ensure`   | `insights:read`  | Creates a project if missing; returns it either way |
| `project_list`     | `insights:read`  | Lists projects in the caller's personal org |
| `insight_write`    | `insights:write` | Writes an insight; auto-creates project. Requires `category` and `source`. With `key`, upserts in-place — suited for single authoritative values (e.g. `preferred-language`). Without `key`, appends a new row — suited for logs, decisions, and observations. |
| `insight_search`   | `insights:read`  | Full-text search over live insights |
| `insight_list`     | `insights:read`  | Lists live insights, newest first. Optional tag filter. |
| `insight_update`   | `insights:write` | Updates content and/or tags of an existing insight |
| `insight_delete`   | `insights:write` | Soft-deletes an insight |
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
| `go.opentelemetry.io/otel` | Traces and metrics via OTLP gRPC |

---

## Build and test

```bash
# Build
go build ./...

# Run all tests (store tests spin up a real PostgreSQL container via testcontainers-go)
go test ./...

# Generate a signing key (once)
go run ./cmd/starlogz-server keygen --output key.jwk

# Run database migrations only (then exit)
go run ./cmd/starlogz-server migrate

# Start the server
go run ./cmd/starlogz-server http --jwk-path key.jwk

# Start the server with coloured debug logging (development mode)
go run ./cmd/starlogz-server --development http --jwk-path key.jwk

# Query the local Postgres instance (runs psql inside the docker compose container)
bin/psql
```

Docker must be running for store integration tests.

Key env vars:

| Var | Default | Description |
|-----|---------|-------------|
| `HTTP_LISTEN_ADDR` | `localhost:8088` | TCP listen address |
| `SERVER_URL` | `http://localhost:8088` | Public base URL (used in discovery docs) |
| `GITHUB_CLIENT_ID` | _(required)_ | GitHub App client ID |
| `GITHUB_CLIENT_SECRET` | _(required)_ | GitHub App client secret |
| `DATABASE_URL` | _(required)_ | PostgreSQL connection string (pgx DSN) |
| `TOKEN_ENCRYPTION_KEY` | _(required)_ | Base64-encoded 32-byte key for encrypting stored GitHub tokens (`openssl rand -base64 32`) |
| `REFRESH_TOKEN_GRACE_PERIOD` | `30s` | How long a rotated refresh token remains accepted for retry; `0s` to disable |
| `RETIRED_REFRESH_TOKEN_RETENTION` | `24h` | How long hashed retired refresh tokens are retained for refresh diagnostics |
| `SENTRY_DSN` | _(unset = disabled)_ | Sentry DSN; enables error reporting and structured log capture |
| `SENTRY_ENVIRONMENT` | — | Sentry environment tag (e.g. `production`) |
| `OTEL_EXPORTER_OTLP_ENDPOINT` | _(unset = disabled)_ | OTLP collector endpoint |
| `OTEL_EXPORTER_OTLP_HEADERS` | — | e.g. Honeycomb API key |

Telemetry is opt-in: if `OTEL_EXPORTER_OTLP_ENDPOINT` is not set, no exporters are created and no connection is attempted. Sentry is likewise opt-in: omitting `SENTRY_DSN` disables it entirely.

---

## Code conventions

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

All OAuth2 logic lives in `internal/oidc/`. Do not add OAuth2 logic to `internal/commands/http.go`.

Tokens are ES384-signed JWTs. Every issued token must include a `jti` (UUID v4). `VerifyJWT` checks the `jti` against the in-memory revocation blocklist before accepting a token.

The blocklist (`Server.revoked`) is protected by `sync.RWMutex`. Write lock on `RevokeToken` (which also prunes expired entries); read lock on `VerifyJWT`.

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

Do not mock the database. Tests that touch Postgres use a real database via `testcontainers-go` (postgres module). Each test gets its own container via `newTestStore(t)` in `internal/store/store_test.go`.

### End-to-end (MCP Inspector)

Use the MCP Inspector (`npx @modelcontextprotocol/inspector`) to validate the full OAuth2 + MCP flow interactively. This is the primary tool for validating the browser-interactive GitHub OAuth2 leg. Point it at `http://localhost:8088/mcp`.

---

## Git conventions

Branch names: `feat_<short_description>`, `fix_<short_description>`.
Commits: conventional commits style (`feat:`, `fix:`, `chore:`, `docs:`).
