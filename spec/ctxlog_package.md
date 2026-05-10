# Completed: Extract ctxlog package and fix store logging

## Context

The store previously used a fixed `*slog.Logger` baked in at construction via `slog.Default().With("component", "store")`. Store logs therefore did not inherit request context such as `request_id`, `client_id`, or `jti`, making them hard to attribute while debugging a multi-client OAuth/MCP flow.

The context logger helpers also lived in `internal/middleware`, which made it awkward for lower-level packages like the postgres store to use them without depending on HTTP infrastructure.

## Outcome

The context logger helpers now live in `internal/ctxlog`, a neutral package with no internal dependencies. Middleware seeds the request logger there, and other layers can read or extend that logger without importing `internal/middleware`.

Store operation logs now use the logger from context and add local source attribution with `component=store`. The routine store call-trace logs were demoted from Info to Debug, so they remain available in development and debugging sessions without adding production Info-level noise.

OIDC handlers now re-seed enriched loggers into context as auth/session fields become known. This lets downstream store logs inherit fields such as `request_id`, `client_id`, `github_id`, and `jti`, making it easier to follow a single request or auth session through the logs.

The postgres connection `AfterConnect` debug log was removed. It was not useful for request tracing and can be replaced by a metric later if connection visibility is needed.

## Current Dependency Shape

- `internal/ctxlog` has no internal dependencies.
- `internal/middleware` imports `internal/ctxlog` to seed request loggers.
- `internal/oidc` imports `internal/ctxlog` to read and enrich request/session loggers.
- `internal/store/postgres` imports `internal/ctxlog` to emit context-aware store logs.
- `internal/server` wires the higher-level pieces together.

## Verified Behavior

- `go build ./...` passes.
- `go test ./internal/...` passes.
- Running `bin/start-dev` and reauthing through Starlogz shows store logs at Debug level with `component=store`.
- Live OAuth/MCP logs show request correlation via `request_id`.
- Auth/session logs include `client_id`, `github_id`, and `jti` once those values are known.
- The Starlogz MCP `project_list` call succeeds and its request is visible in the logs.
- `bin/psql` confirms the reauth flow creates a fresh OAuth client and grant, while transient pending auth and auth code rows are consumed.

## Out Of Scope

Sensitive field redaction is intentionally not handled by this change. Existing Debug logs can still include values such as refresh tokens, auth codes, state values, fact content, and OAuth client metadata. A follow-up should obscure sensitive values before leaning heavily on these logs outside local debugging.
