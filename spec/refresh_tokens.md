# Refresh Token Grant — OAuth2 Spec

> Version 0.2 · Draft · April 2026

## Contents

1. [Overview](#overview)
2. [Refresh token format](#refresh-token-format)
3. [Issuance](#issuance)
4. [Refresh grant flow](#refresh-grant-flow)
5. [Token rotation](#token-rotation)
6. [GitHub token rotation](#github-token-rotation)
7. [Expiry and revocation](#expiry-and-revocation)
8. [Error reference](#error-reference)
9. [Discovery changes](#discovery-changes)
10. [Database schema changes](#database-schema-changes)
11. [v0.2 constraints](#v02-constraints)

---

## Overview

v0.1 required users to complete a full GitHub OAuth2 round-trip every time their
7-day JWT expired. v0.2 adds a server-issued opaque refresh token that lets MCP
clients obtain a new JWT silently, without redirecting the user to GitHub again.

**Prerequisite — persistence migration.** This spec assumes
`spec/persistence.md` has been applied first. In particular, it depends on:
- migration 4 (the `our_refresh_token` and `client_id` columns on `grants`),
- the `RevocationStore` interface backed by the `revoked_tokens` table (so JWT
  revocation survives restarts and is visible across instances), and
- the `RotateGrant` / `GetGrantByRefreshToken` / `DeleteGrant` operations on
  the expanded `GrantStore` interface.

**Prerequisite — GitHub App with expiring tokens.** This flow depends on GitHub
issuing a refresh token alongside the initial access token. This only happens
when the server is configured with a **GitHub App** (not an OAuth App) that has
**Expire user authorization tokens** enabled in its settings. Without this,
GitHub returns a non-expiring access token and no refresh token; the `grants`
table will store an empty refresh token and all refresh grant requests will
return `invalid_grant`.

The refresh token is tied to a single authorization grant (`grants` table row,
keyed by `jti`). It is single-use: each successful refresh rotates both the
refresh token and the underlying GitHub App tokens. If the GitHub refresh token
expires (up to 6 months after issuance), the refresh fails and the user must
re-authenticate via the full GitHub flow.

---

## Refresh token format

An opaque, cryptographically random 32-byte value encoded as base64url (no
padding). Example:

```
YWJjZGVmZ2hpamtsbW5vcHFyc3R1dnd4eXoxMjM0NTY
```

Stored in `grants.our_refresh_token`. A UNIQUE constraint prevents reuse of
any token value across rows. Not a JWT — clients must treat it as an opaque
string.

---

## Issuance

The refresh token is issued alongside the JWT at the token endpoint, for both
the authorization code grant (initial login) and the refresh grant (subsequent
renewals).

### Token endpoint response (updated)

```json
{
  "access_token": "<signed-jwt>",
  "token_type": "Bearer",
  "expires_in": 604800,
  "scope": "facts:read facts:write",
  "refresh_token": "<opaque-32-byte-base64url>",
  "refresh_token_expires_in": 15897600
}
```

`refresh_token_expires_in` reflects the remaining lifetime of the underlying
GitHub refresh token (GitHub App user-to-server tokens: up to ~184 days). It is
recalculated on each rotation so the client always knows the exact remaining
window.

---

## Refresh grant flow

```
MCP Client                     Starlogz                      GitHub
    |                              |                            |
    | POST /oauth2/token           |                            |
    | grant_type=refresh_token     |                            |
    | &refresh_token=<opaque>      |                            |
    | &client_id=<id> ------------>|                            |
    |                              |-- look up grant by         |
    |                              |   our_refresh_token        |
    |                              |-- check GitHub refresh     |
    |                              |   token not expired        |
    |                              |-- POST /login/oauth/access_token
    |                              |   grant_type=refresh_token |
    |                              |   &refresh_token=<gh-rt> ->|
    |                              |<-- new gh access_token ----|
    |                              |    + new gh refresh_token  |
    |                              |-- GET /user + /emails ---->|
    |                              |<-- identity ---------------|
    |                              |   (upsert user row)        |
    |                              |-- generate new jti,        |
    |                              |   new our_refresh_token    |
    |                              |-- UPDATE grants row        |
    |                              |   (rotate all tokens)      |
    |                              |-- revoke old JWT jti       |
    |                              |   (revoked_tokens table)   |
    |<-- new JWT + new             |                            |
    |    refresh_token ------------|                            |
    |                              |                            |
    | POST /mcp                    |                            |
    | Authorization: Bearer <jwt> >|                            |
    |<-- MCP response -------------|                            |
```

### Request

```
POST /oauth2/token
Content-Type: application/x-www-form-urlencoded

grant_type=refresh_token
&refresh_token=<opaque-token>
&client_id=<registered-client-id>
```

`client_id` is required and must match the `client_id` stored on the grant row.

### Response (200 OK)

```json
{
  "access_token": "<new-signed-jwt>",
  "token_type": "Bearer",
  "expires_in": 604800,
  "scope": "facts:read facts:write",
  "refresh_token": "<new-opaque-token>",
  "refresh_token_expires_in": 14400000
}
```

The `scope` in the response matches the scope of the original grant unchanged.
Clients cannot request a different scope via the refresh grant.

---

## Token rotation

Each successful refresh atomically:

1. Invalidates the old refresh token (UPDATE clears the old value).
2. Invalidates the old JWT by calling `RevocationStore.RevokeToken(jti, exp)`,
   which inserts into `revoked_tokens` with the original `exp` (so the entry
   self-prunes when the old JWT would have expired anyway). Because the
   table is shared, every instance sees the revocation immediately.
3. Issues a new `jti`, new JWT, and new opaque refresh token.
4. Updates the `grants` row in a single transaction: new `jti`, new
   `our_refresh_token`, new GitHub tokens, new `jwt_expiry`.

A race condition where two requests simultaneously use the same refresh token
is handled by the UNIQUE constraint on `our_refresh_token` — the second UPDATE
will find no matching row and returns `invalid_grant`.

---

## GitHub token rotation

GitHub App user-to-server tokens must themselves be rotated (they expire in 8
hours). The refresh grant handles this transparently:

1. Read the stored (encrypted) GitHub refresh token from the grants row.
2. Call the GitHub token refresh endpoint with `grant_type=refresh_token`.
3. GitHub returns a new access token (8h TTL) and a new refresh token (~184 days).
4. Update the grants row with the newly encrypted GitHub tokens and updated
   expiry times.

The MCP client is unaware of GitHub token rotation — it only sees Starlogz
JWTs and opaque refresh tokens.

---

## Expiry and revocation

### Refresh token lifetime

A refresh token is valid until:

- It is used (rotation invalidates it immediately), or
- The underlying GitHub refresh token expires, or
- The user's grant row is deleted (e.g. user logs out from all sessions via a
  future account management endpoint).

There is no separate expiry clock on the Starlogz refresh token itself — its
validity is entirely determined by the GitHub refresh token's remaining lifetime.
`refresh_token_expires_in` in each response reflects this remaining window.

### Expired GitHub refresh token

When a refresh is attempted and the stored GitHub refresh token is expired:

1. Call `RevocationStore.RevokeToken(jti, exp)` for the grant's current `jti`
   so any in-flight MCP calls with the old JWT also fail immediately.
2. Call `GrantStore.DeleteGrant(jti)` to remove the grant row.
3. Return `invalid_grant`.

The user must complete a full GitHub OAuth2 flow to obtain a new grant.

### Lazy cleanup

Grants whose `jwt_expiry` is in the past are pruned from the database when a
new grant is inserted for the same `github_id` (existing behaviour). This
removes rows for sessions where the user never used their refresh token.

---

## Error reference

All errors use the standard OAuth2 error response format:

```json
{
  "error": "<error_code>",
  "error_description": "<human-readable message>"
}
```

| Error code | HTTP status | Cause |
|------------|-------------|-------|
| `unsupported_grant_type` | 400 | `grant_type` is not `refresh_token` or `authorization_code` |
| `invalid_request` | 400 | `refresh_token` or `client_id` parameter missing |
| `invalid_grant` | 400 | Refresh token not found, already used, or GitHub refresh token expired |
| `invalid_client` | 400 | `client_id` does not match the grant |
| `server_error` | 500 | GitHub token refresh API call failed (transient) |

---

## Discovery changes

The authorization server metadata document (`/.well-known/oauth-authorization-server`)
must be updated to advertise the refresh token grant type:

```json
{
  "grant_types_supported": ["authorization_code", "refresh_token"]
}
```

MCP clients that read the discovery document before initiating a refresh will
see that `refresh_token` is supported and use the standard flow.

---

## Database schema changes

The `grants` table additions (`our_refresh_token`, `client_id`, the unique
index) are owned by `spec/persistence.md` migration 4. See that spec for
the SQL.

`our_refresh_token` and `client_id` are nullable to accommodate grants
created without a GitHub refresh token (test/CLI scenarios with no
GitHub App configured). The refresh handler treats either being null as
"this grant cannot participate in the refresh flow".

---

## v0.2 constraints

- **No refresh token introspection** — there is no endpoint to check whether
  a refresh token is still valid without consuming it.
- **No refresh token revocation endpoint** — users cannot individually revoke
  a session's refresh token; they must wait for the GitHub refresh token to
  expire (up to ~184 days). A session management endpoint
  (`DELETE /sessions/:id`) is planned for v0.3.
- **`client_id` validation best-effort** — `client_id` is validated only when
  present on the grant row. Grants written without a `client_id` (e.g. via
  a flow that did not carry one through) skip the check.
- **Refresh path is synchronous to GitHub** — each refresh blocks on the
  GitHub token endpoint and the `/user`/`/emails` calls. A GitHub outage
  fails refreshes with `server_error`. Caching identity briefly across
  refreshes is a v0.3 candidate.
