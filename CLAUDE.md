# CLAUDE.md — starlogz

Remote MCP server that lets developers and agents store and retrieve persistent facts about projects. GitHub OAuth2 for humans, scoped API keys for agents. Backing store is PostgreSQL.

Specs live in `spec/` — read them before adding endpoints. `spec/auth.md` covers the OAuth2 flow in detail.

---

## Layout

```
cmd/starlogz-server/         binary entry point, signal handling, logger init
internal/commands/            kong command structs (HTTPCmd, KeyGenCmd)
internal/middleware/          HTTP middleware (access log, CORS)
internal/oidc/                OAuth2/OIDC server — JWKS, discovery, DCR, JWT verify, logout
internal/server/              HTTP mux, MCP tool handlers, health endpoint
internal/store/               PostgreSQL store — users, projects, facts; migration runner
internal/store/migrations/    embedded SQL migration files
internal/telemetry/           OTel init (traces + metrics via OTLP gRPC)
spec/                         design specs (auth.md, facts.md)
```

Public API surface lives under `pkg/` (currently empty — add exported types there when needed).

---

## Key dependencies

| Package | Purpose |
|---------|---------|
| `github.com/alecthomas/kong` | CLI parsing |
| `github.com/modelcontextprotocol/go-sdk` | MCP server + OAuth2 middleware |
| `github.com/lestrrat-go/jwx/v3` | JWT sign/verify, JWKS, JWK key management |
| `github.com/google/uuid` | `jti` and `client_id` generation |
| `github.com/jackc/pgx/v5` | PostgreSQL driver (pgxpool for connection pooling) |
| `github.com/lmittmann/tint` | Coloured slog handler for interactive terminals |
| `github.com/testcontainers/testcontainers-go` | Real Postgres containers for store integration tests |
| `go.opentelemetry.io/otel` | Traces and metrics via OTLP gRPC |

---

## Running locally

```bash
# Generate a signing key (do this once)
go run ./cmd/starlogz-server keygen --output key.jwk

# Start the server
go run ./cmd/starlogz-server http --jwk-path key.jwk
```

Key env vars:

| Var | Default | Description |
|-----|---------|-------------|
| `HTTP_LISTEN_ADDR` | `localhost:8088` | TCP listen address |
| `SERVER_URL` | `http://localhost:8088` | Public base URL (used in discovery docs) |
| `GITHUB_CLIENT_ID` | _(required)_ | GitHub App client ID |
| `GITHUB_CLIENT_SECRET` | _(required)_ | GitHub App client secret |
| `DATABASE_URL` | _(required)_ | PostgreSQL connection string (pgx DSN) |
| `OTEL_EXPORTER_OTLP_ENDPOINT` | _(unset = disabled)_ | OTLP collector endpoint |
| `OTEL_EXPORTER_OTLP_HEADERS` | — | e.g. Honeycomb API key |

Telemetry is opt-in: if `OTEL_EXPORTER_OTLP_ENDPOINT` is not set, no exporters are created and no connection is attempted.

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

---

## Documentation Style

When creating external documentation (README files, design docs, specs) write in the style of an Amazon engineer:
- Start with the customer problem and work backwards
- Use clear, concise, and data-driven language
- Include specific examples and concrete details
- Structure documents with clear headings and bullet points
- Focus on operational excellence, security, and scalability considerations
- Include implementation details and edge cases where they affect the reader's decisions
- Use the passive voice sparingly; prefer active, direct statements

This guidance applies to prose documents only. Code comment style is governed by the **Comments** convention above.