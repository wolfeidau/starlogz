# Statelessness and PostgreSQL persistence

> Status: Implemented decision
> Last reviewed: 2026-07-16
> Authority: Historical rationale and lasting constraints; current code, migrations, and tests define implementation details.

## Context

The initial server kept pending OAuth authorizations, authorization codes, and
JWT revocations in process memory. A restart invalidated active login flows and
forgot revocations, and multiple instances could not safely serve the same
flow. Projects were also owned directly by users, leaving no durable tenancy
boundary for future shared organizations.

## Decision

PostgreSQL owns all state that affects correctness across requests or server
instances:

- pending OAuth authorizations;
- single-use authorization codes;
- JWT revocations;
- OAuth client registrations and grants;
- users, organizations, memberships, projects, and insights;
- opaque dashboard sessions.

A server process is stateless when any instance can serve any request, a
restart loses no committed state, and startup requires only configured secrets
and the shared database. Immutable configuration, the loaded signing key,
prebuilt discovery metadata, connection pools, and telemetry clients may remain
in process because they do not create request affinity.

Single-use OAuth records are consumed with atomic delete-and-return operations.
Revocation is checked from PostgreSQL for every accepted JWT. Grant rotation
updates the grant and revokes the previous `jti` in one transaction.

## Tenancy

Organizations are the durable ownership boundary. Users receive a personal
organization and membership; projects belong to organizations rather than
directly to users. Authorization resolves the internal user UUID from the JWT
and scopes project access through organization membership.

Personal-organization slugs are display values and are not globally unique.
Shared-organization slugs remain unique. This avoids embedding a username-like
global namespace into personal tenancy while preserving a stable path for
future shared organizations.

Organization roles are flat and organization-wide. Nested organizations and
per-project roles were intentionally not introduced with this change.

## Migrations and startup

Migrations run in numeric order and record completed versions in PostgreSQL.
The runner holds a PostgreSQL advisory lock on one pinned connection so only one
instance migrates a database at a time. Startup migration failures stop the
server instead of serving against an unknown schema.

Migration files are append-only implementation history. Their SQL, indexes,
constraints, triggers, and current schema are authoritative over this decision
record.

## Shutdown and signing keys

SIGTERM and SIGINT stop new HTTP work, allow in-flight requests to drain within
the configured timeout, and then close shared resources. This supports rolling
deployment without creating a separate application-level coordination system.

Each instance loads the same static ES384 signing key. The JWKS contains one
public key. Replacing that key requires a restart and invalidates access tokens
signed by the previous key. Multi-key, zero-downtime signing-key rotation was
deferred until operational need justifies the added coordination.

## Tradeoffs

- PostgreSQL availability is required for OAuth state, token verification, and
  application data; correctness fails closed during database outages.
- Expired transient and revocation rows are cleaned lazily by relevant write
  paths rather than by a dedicated scheduler.
- GitHub credentials in authorization codes and grants require the configured
  encryption key.
- The design prefers database transactions and constraints over distributed
  locks or instance-local caches.

## Outcome

The server can run multiple interchangeable instances against one PostgreSQL
database. OAuth state survives restarts, authorization codes remain single-use,
revocations are shared, and rolling shutdown is bounded. The refresh-token
behavior built on this model is maintained in
[refresh_tokens.md](refresh_tokens.md).

Implementation evidence is in `internal/store/`,
`internal/store/postgres/`, `internal/store/postgres/migrations/`,
`internal/oidc/`, and the server shutdown path.
