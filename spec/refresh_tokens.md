# OAuth2 refresh-token grant

> Status: Current contract
> Last reviewed: 2026-07-16
> Authority: Behavioral and security contract; current code, migrations, and tests provide implementation evidence.

## Overview

Eligible MCP clients receive a Starlogz-issued opaque refresh token with their
access-token JWT. The refresh token allows the client to obtain another
15-minute JWT without repeating the interactive GitHub authorization flow.

The grant depends on an upstream GitHub App refresh token. Starlogz does not
issue its own refresh token when GitHub did not supply one or when the registered
client does not support the `refresh_token` grant.

## Token and response

A Starlogz refresh token is 32 cryptographically random bytes encoded as
unpadded base64url. It is opaque to clients and bound to one persisted grant,
client ID, user, scope, access-token `jti`, and upstream GitHub token chain.

Token responses use `Cache-Control: no-store`:

```json
{
  "access_token": "<signed-jwt>",
  "token_type": "Bearer",
  "expires_in": 900,
  "scope": "insights:read insights:write",
  "refresh_token": "<opaque-token>",
  "refresh_token_expires_in": 15897600
}
```

`refresh_token_expires_in` is derived from the remaining upstream GitHub
refresh-token lifetime. The Starlogz token has no independent expiry clock.

## Refresh request

The client sends a form-encoded request:

```text
POST /oauth2/token

grant_type=refresh_token
&refresh_token=<opaque-token>
&client_id=<registered-client-id>
```

Both values are required. A stored client ID must match the request. The
original scope is preserved; the refresh grant cannot expand or reduce it.

For a current token, Starlogz:

1. Loads and decrypts the associated GitHub refresh token.
2. Rejects and removes the grant if the upstream token is already expired.
3. Calls GitHub to rotate its access and refresh tokens and refreshes the user
   profile.
4. Generates a new `jti`, access-token JWT, and Starlogz refresh token.
5. Atomically replaces the grant, revokes the previous `jti`, and records a
   hash of the consumed refresh token.
6. Returns the new token pair with the unchanged scope.

Once GitHub rotation starts, the upstream call and database update use a
non-cancelable child context. Client disconnection must not leave GitHub's new
token unrecorded after GitHub has invalidated the old one.

If GitHub rejects the stored refresh token, reports that it has expired, or
returns no replacement refresh token, Starlogz revokes the current JWT, deletes
the grant, retains bounded diagnostic metadata, and requires interactive
authentication again. Transient GitHub or storage failures return
`server_error` without intentionally deleting a still-valid grant.

## Rotation and retry grace

Normal refresh tokens are single-use. Concurrent rotation is serialized by an
atomic conditional update: only a request matching the current token can
replace the grant.

The consumed token is stored only as a SHA-256 hash in
`retired_refresh_tokens`. By default, a rotated token remains eligible for a
30-second retry grace period. An accepted retry:

- verifies the same client ID;
- loads the replacement grant;
- reissues the replacement grant's existing JWT and refresh token;
- preserves the same `jti`, scope, and expiries;
- does not call GitHub or rotate again.

This supports clients that did not receive or persist the first successful
response. After the grace period, reuse returns `invalid_grant`.

`REFRESH_TOKEN_GRACE_PERIOD` controls the window. `0s` disables grace retries;
values above 60 seconds are rejected. Retired token hashes are retained for
24 hours by default for bounded diagnostics and lazy cleanup.
`RETIRED_REFRESH_TOKEN_RETENTION` must be positive and at least as long as the
grace period.

Retired records contain a token hash, bounded reason, client and grant
references, grace expiry, and retention expiry. They never contain the raw
Starlogz or GitHub refresh token.

## Expiry and revocation

A refresh token stops being usable when:

- it is successfully rotated and its retry grace expires;
- the upstream GitHub refresh token expires or is rejected;
- GitHub fails to return a replacement refresh token;
- its grant is deleted; or
- the stored grant or replacement grant no longer exists.

Access-token revocation is persistent. Successful rotation records the old
`jti` in the same transaction as the new grant. Broken-grant teardown revokes
the current `jti` and removes the grant. Revocation records remain relevant only
until the corresponding JWT expiry.

## Error contract

Errors use the OAuth2 JSON shape:

```json
{
  "error": "invalid_grant",
  "error_description": "refresh token not found or already used"
}
```

| Error | HTTP status | Meaning |
|---|---:|---|
| `unsupported_grant_type` | 400 | The token endpoint does not recognize the requested grant. |
| `invalid_request` | 400 | `refresh_token` or `client_id` is missing. |
| `invalid_client` | 400 | The client ID does not match the current or retired grant. |
| `invalid_grant` | 400 | The token is unknown, already used outside grace, removed, expired, or invalid upstream. |
| `server_error` | 500 | GitHub or internal persistence failed transiently. |

Public errors intentionally avoid disclosing detailed grant history. Bounded
operator telemetry distinguishes successful rotation and grace retry from
unknown, expired, removed, mismatched, upstream-invalid, and server-error
outcomes. Clients must not depend on those internal reason labels.

## Discovery and constraints

Authorization-server metadata advertises both `authorization_code` and
`refresh_token` grant types.

There is no refresh-token introspection or standalone revocation endpoint.
Refresh requests synchronously depend on GitHub. Client binding is enforced
when the grant contains a client ID; legacy or test grants without one retain
the existing best-effort compatibility behavior.
