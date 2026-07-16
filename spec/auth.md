# OAuth2 authentication and authorization

> Status: Current contract
> Last reviewed: 2026-07-16
> Authority: Behavioral and security contract; current code, migrations, and tests provide implementation evidence.

## Architecture

Starlogz is both an OAuth2 authorization server and an MCP resource server.
GitHub is the upstream identity provider. After GitHub authentication, Starlogz
issues its own credentials for the MCP resource at `<server-url>/mcp`.

MCP clients use a short-lived ES384 access-token JWT and, when available, an
opaque refresh token. The first-party dashboard exchanges an authorization code
for a JWT only as a login bootstrap, then replaces it with the opaque browser
session described in [web_sessions.md](web_sessions.md).

Production requires a GitHub App with expiring user authorization tokens
enabled. GitHub access and refresh tokens are encrypted before persistence.
Without a GitHub refresh token, Starlogz can issue an access-token JWT but
cannot issue its own refresh token.

## Discovery and authorization flow

An MCP client:

1. Probes `/mcp` and receives a bearer challenge with the protected-resource
   metadata URL.
2. Reads protected-resource and authorization-server metadata.
3. Registers as a public client through Dynamic Client Registration (DCR).
4. Starts an authorization-code flow using PKCE `S256`.
5. Redirects the user through GitHub authentication.
6. Receives a single-use authorization code at its registered redirect URI.
7. Exchanges the code, matching `client_id`, `redirect_uri`, and PKCE verifier,
   for a Starlogz access token and, when eligible, a refresh token.
8. Sends the access token as `Authorization: Bearer <token>` to `/mcp`.

Pending authorization state and authorization codes are single-use PostgreSQL
records. Pending state expires after 10 minutes; authorization codes expire
after 5 minutes. Atomic consume operations prevent replay across server
instances.

## Scopes

| Scope | Current use |
|---|---|
| `insights:read` | Required by the MCP transport and all MCP tools. |
| `insights:write` | Additionally required by write, update, and delete tools. |
| `org:admin` | Advertised and accepted for forward-compatible organization administration; no current MCP tool requires it. |

Scopes are stored in the JWT as a space-delimited `scope` claim. An authorization
request cannot exceed the client's registered scope set. When the request omits
scope, Starlogz uses the registered client scope when available and otherwise
defaults to `insights:read`.

## Dynamic Client Registration

`POST /oauth2/register` implements RFC 7591 for public clients.

Example request:

```json
{
  "redirect_uris": ["https://client.example.com/callback"],
  "client_name": "My MCP Client",
  "grant_types": ["authorization_code", "refresh_token"],
  "response_types": ["code"],
  "token_endpoint_auth_method": "none",
  "scope": "insights:read insights:write"
}
```

`redirect_uris` is required and each value must satisfy one of these policies:

- absolute HTTPS URI with a host;
- HTTP loopback URI using `localhost`, `127.0.0.1`, or `[::1]`;
- absolute custom-scheme URI for a native application.

Relative URIs and URIs containing fragments, wildcards, or userinfo are
rejected. `javascript:` and `data:` schemes are rejected. DCR accepts at most
10 redirect URIs; the body is limited to 64 KiB; redirect URIs are limited to
2048 bytes; `client_name` to 256 bytes; and `scope` to 1024 bytes.

Only `token_endpoint_auth_method=none`, authorization codes, refresh tokens,
and response type `code` are supported. Omitted scope defaults to
`insights:read insights:write`. Supplied scopes must be supported. Successful
DCR normalizes grant types to `authorization_code` and `refresh_token` and does
not issue a client secret.

Registrations are persisted without expiry and can be reused. Successful
authorization and token operations update `last_used_at` at most once per
24 hours. This is an activity signal for future conservative cleanup, not a
current expiry policy.

## Authorization-code grant

PKCE `S256` is mandatory. The authorization request includes:

```text
GET /oauth2/authorize
  ?client_id=<registered-client-id>
  &redirect_uri=<registered-uri>
  &response_type=code
  &scope=insights:read insights:write
  &state=<client-state>
  &code_challenge=<base64url-sha256-verifier>
  &code_challenge_method=S256
```

The token request is form encoded:

```text
POST /oauth2/token

grant_type=authorization_code
&code=<authorization-code>
&redirect_uri=<same-redirect-uri>
&client_id=<same-client-id>
&code_verifier=<pkce-verifier>
```

The server consumes the authorization code before validating the exchange, so
a failed or replayed exchange requires restarting the authorization flow. The
code is bound to the client ID, redirect URI, granted scope, and PKCE challenge.

The response always has `Cache-Control: no-store`. An access token expires in
15 minutes. A refresh token is included only when the registered client supports
the refresh grant and GitHub supplied a refresh token:

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

Refresh behavior is specified in [refresh_tokens.md](refresh_tokens.md).

## Access-token JWT

Tokens are signed with ES384 using a P-384 key. Every issued token contains:

| Claim | Contract |
|---|---|
| `iss` | Exact configured server base URL. |
| `sub` | Internal Starlogz user UUID. |
| `aud` | Array containing the exact `<server-url>/mcp` resource URL. |
| `scope` | Non-empty, space-delimited granted scopes. |
| `jti` | Unique UUID v4 used for revocation. |
| `iat` | Issued-at timestamp. |
| `exp` | Expiry timestamp, normally 15 minutes after issuance. |

Verification requires a valid ES384 signature, exact issuer, matching MCP
audience, non-empty subject and scope, expiry, and `jti`. It then checks the
persistent revocation store. A revocation-store failure rejects the token rather
than accepting it without a revocation check.

`POST /auth/logout` verifies the bearer token and records its `jti` through its
expiry. Refresh rotation also revokes the previous access token atomically with
the grant update. Revocations are shared across server instances.

## JWKS and signing-key rotation

`GET /.well-known/jwks` returns the current public P-384 key with `use=sig`,
`alg=ES384`, and a thumbprint-derived `kid`. The response is cacheable for
24 hours.

The server publishes one signing key. Rotation requires restarting with a new
key. Tokens signed by the previous key stop verifying after rotation, so users
must authenticate again. Zero-downtime multi-key rotation is not supported.

## Persisted credentials

The `grants` table associates a JWT `jti`, internal user UUID, client ID, scope,
Starlogz refresh token, encrypted GitHub access and refresh tokens, and their
expiries. `TOKEN_ENCRYPTION_KEY` supplies the 32-byte encryption key. Current
table shape and encryption mechanics are owned by the migrations and store
implementation.

Standalone API-key bearer authentication is not implemented. OAuth access-token
JWTs are the only credentials accepted by the MCP bearer-token verifier.

## Endpoint reference

| Endpoint | Method | Authentication | Purpose |
|---|---|---|---|
| `/.well-known/oauth-authorization-server` | GET | None | RFC 8414 authorization-server metadata. |
| `/.well-known/openid-configuration` | GET | None | Compatibility discovery document. |
| `/.well-known/oauth-protected-resource` | GET | None | RFC 9728 MCP resource metadata. |
| `/.well-known/jwks` | GET | None | Current JWT verification key. |
| `/oauth2/register` | POST | None | Dynamic Client Registration. |
| `/oauth2/authorize` | GET | None | Starts the GitHub-backed authorization flow. |
| `/oauth2/token` | POST | None | Exchanges authorization codes or refresh tokens. |
| `/auth/github/callback` | GET | GitHub state | Completes upstream authentication. |
| `/auth/logout` | POST | Bearer JWT | Revokes the access token. |
| `/mcp` | GET, POST | Bearer JWT with `insights:read` | Stateless MCP transport. |
| `/health` | GET | None | Health response. |

Dashboard login, callback, session, and logout behavior is covered by
[web_sessions.md](web_sessions.md).

## Discovery contract

Authorization-server metadata advertises:

```json
{
  "authorization_endpoint": "<server-url>/oauth2/authorize",
  "token_endpoint": "<server-url>/oauth2/token",
  "jwks_uri": "<server-url>/.well-known/jwks",
  "registration_endpoint": "<server-url>/oauth2/register",
  "response_types_supported": ["code"],
  "grant_types_supported": ["authorization_code", "refresh_token"],
  "scopes_supported": ["insights:read", "insights:write", "org:admin"],
  "code_challenge_methods_supported": ["S256"],
  "token_endpoint_auth_methods_supported": ["none"]
}
```

Protected-resource metadata identifies `<server-url>/mcp`, names the server,
points `authorization_servers` at the issuer URL, advertises the same scopes,
and allows bearer credentials only in the `Authorization` header.

## Current constraints

- GitHub is the only upstream identity provider.
- Only public OAuth clients are supported; no client secret is issued.
- Token introspection and RFC token-revocation endpoints are not advertised.
- DCR registrations are permanent; automated stale-registration cleanup is not
  implemented.
- OAuth state, authorization codes, revocations, clients, and grants require
  PostgreSQL. Database failures fail closed.
