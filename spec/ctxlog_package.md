# Context-aware logging package

> Status: Implemented decision
> Last reviewed: 2026-07-16
> Authority: Historical rationale and package-boundary decision; current code and tests define implementation details.

## Context

The PostgreSQL store originally used a fixed logger created from
`slog.Default()`. Store records therefore missed request and authorization
context, and the context-logger helpers lived in HTTP middleware where lower
layers could not use them without depending on transport code.

## Decision

Move context logger and request-ID helpers to `internal/ctxlog`, a neutral
package with no internal dependencies. Middleware seeds the request context;
OIDC and server handlers enrich it as bounded identifiers become known; store
operations read that logger and add `component=store`.

Routine store call traces use Debug rather than Info. Connection creation is
not logged per request; connection visibility belongs in metrics when needed.

The lasting dependency direction is:

```text
middleware ─┐
oidc ───────┼──> ctxlog
server ─────┤
postgres ───┘
```

`ctxlog` must not depend on middleware, HTTP handlers, OIDC, the store, or
telemetry packages.

## Security follow-up

The original change intentionally left sensitive-field filtering for later.
That gap is now closed by the shared `internal/logattr` privacy handler and
call-site cleanup described in
[observability_uplift.md](observability_uplift.md). Context propagation must not
be used to attach raw tokens, OAuth parameters, insight content, emails, or
other prohibited values.

## Outcome

Request, client, user, and JWT correlation can flow into lower-level logs
without reversing package dependencies. Current behavior is implemented in
`internal/ctxlog/` and its callers.
