# Authentication & Authorization — OAuth2 Spec

> Version 0.1 · Draft · April 2026

## Contents

1. [Architecture overview](#architecture-overview)
2. [Credential types](#credential-types)
3. [Scopes](#scopes)
4. [Authorization code flow (PKCE)](#authorization-code-flow-pkce)
5. [Dynamic Client Registration](#dynamic-client-registration)
6. [JWKS and token verification](#jwks-and-token-verification)
7. [Token format](#token-format)
8. [Endpoint reference](#endpoint-reference)
9. [Discovery documents](#discovery-documents)
10. [v0.1 constraints](#v01-constraints)

---

## Architecture overview

Starlogz acts as both OAuth2 Authorization Server and MCP Resource Server.
GitHub is the upstream identity provider — users authenticate with GitHub,
and Starlogz issues its own signed JWTs for subsequent API calls.

```
MCP Client                Starlogz                    GitHub
    |                        |                           |
    |  1. Initial probe (no token)                       |
    |-- POST /mcp ---------->|                           |
    |<-- 401 Unauthorized ---|                           |
    |    WWW-Authenticate:   |                           |
    |    Bearer realm="mcp", |                           |
    |    resource_metadata=  |                           |
    |    ".../.well-known/   |                           |
    |    oauth-protected-    |                           |
    |    resource"           |                           |
    |                        |                           |
    |  2. Protected resource metadata discovery          |
    |-- GET /.well-known/    |                           |
    |   oauth-protected-     |                           |
    |   resource ----------->|                           |
    |<-- { resource,         |                           |
    |      authorization_    |                           |
    |      servers: [issuer] |                           |
    |      scopes_supported }|                           |
    |                        |                           |
    |  3. Authorization server discovery                 |
    |-- GET /.well-known/    |                           |
    |   oauth-authorization- |                           |
    |   server ------------->|                           |
    |<-- { issuer,           |                           |
    |      authorization_    |                           |
    |      endpoint,         |                           |
    |      token_endpoint,   |                           |
    |      registration_     |                           |
    |      endpoint,         |                           |
    |      code_challenge_   |                           |
    |      methods: ["S256"] }                           |
    |                        |                           |
    |  4. Dynamic Client Registration                    |
    |-- POST /oauth2/        |                           |
    |   register ----------->|                           |
    |<-- { client_id } ------|                           |
    |                        |                           |
    |  5. Authorization (PKCE)                           |
    |-- GET /oauth2/         |                           |
    |   authorize?client_id= |                           |
    |   &code_challenge=...  |                           |
    |   &state=... --------->|                           |
    |                        |-- GET /login/oauth/ ----->|
    |                        |   authorize               |
    |<-- redirect to GitHub --|                           |
    |                        |                           |
    |<------- GitHub login + consent -------------------|
    |                        |                           |
    |                        |<-- GET /auth/github/ -----|
    |                        |    callback?code=&state=  |
    |                        |-- exchange code --------->|
    |                        |<-- access_token ----------|
    |                        |-- GET /user + /emails --->|
    |                        |<-- identity --------------|
    |                        |   (upsert user row)       |
    |<-- redirect to client  |                           |
    |    redirect_uri?code=  |                           |
    |    &state= ------------|                           |
    |                        |                           |
    |  6. Token exchange                                 |
    |-- POST /oauth2/token   |                           |
    |   code=&verifier=& --->|                           |
    |   client_id=           |                           |
    |<-- signed JWT ----------|                           |
    |                        |                           |
    |  7. Authenticated MCP call                         |
    |-- POST /mcp            |                           |
    |   Authorization:       |                           |
    |   Bearer <jwt> ------->|                           |
    |<-- MCP response --------|                           |
```

---

## Credential types

### Session JWT (humans)

Issued after a successful GitHub OAuth2 flow. Sent as a bearer token.
Server sets `source_type = human` on all fact writes.

- Algorithm: ES384
- Expiry: 7 days
- Transport: `Authorization: Bearer <token>`

### API key (agents)

Format: `pfk_live_<random>`. Created via `POST /tokens` by an authenticated
human. Hash stored in `api_keys` table; plaintext shown once on creation.
Server sets `source_type = agent` on all fact writes.

- Transport: `Authorization: Bearer pfk_live_<random>`
- Scoped per key: `facts:read`, `facts:write`, `org:admin`
- Optionally scoped to a specific project

---

## Scopes

| Scope | Gates |
|-------|-------|
| `facts:read` | Read facts, search, list projects and tags |
| `facts:write` | Create, update, soft-delete facts |
| `org:admin` | Create projects, write org-level facts, manage members |

All MCP tool calls require at minimum `facts:read`. The `/mcp` endpoint
enforces this at the transport layer before any tool handler runs.

Scopes are stored in JWT as a space-delimited `scope` claim (RFC 9068).

---

## Authorization code flow (PKCE)

PKCE is mandatory for all authorization code grants (RFC 7636 `S256` method).
MCP clients that cannot verify `code_challenge_methods_supported` in the
discovery document MUST refuse to proceed.

### Steps

1. **Discover** — client fetches `/.well-known/oauth-authorization-server`
2. **Register** — client registers via DCR at `registration_endpoint`
3. **Generate verifier** — client generates `code_verifier` (43–128 random chars),
   computes `code_challenge = BASE64URL(SHA256(code_verifier))`
4. **Authorize** — client redirects user to:
   ```
   GET /oauth2/authorize
     ?client_id=<id>
     &redirect_uri=<uri>
     &response_type=code
     &scope=facts:read facts:write
     &state=<random>
     &code_challenge=<challenge>
     &code_challenge_method=S256
   ```
5. **GitHub redirect** — server stores PKCE state in short-lived session,
   redirects to GitHub OAuth2
6. **Callback** — GitHub redirects to `GET /auth/github/callback?code=...&state=...`
   Server validates state, exchanges GitHub code, upserts user, issues
   short-lived auth code, redirects to client's `redirect_uri`
7. **Token exchange** — client posts:
   ```
   POST /oauth2/token
   Content-Type: application/x-www-form-urlencoded

   grant_type=authorization_code
   &code=<auth_code>
   &redirect_uri=<uri>
   &client_id=<id>
   &code_verifier=<verifier>
   ```
8. **JWT issued** — server verifies PKCE, returns signed JWT
9. **Call MCP** — client uses JWT as bearer token on `POST /mcp`

---

## Dynamic Client Registration

Endpoint: `POST /oauth2/register` (RFC 7591)

### Request

```json
{
  "redirect_uris": ["https://client.example.com/callback"],
  "client_name": "My MCP Client",
  "grant_types": ["authorization_code"],
  "response_types": ["code"],
  "token_endpoint_auth_method": "none",
  "scope": "facts:read facts:write"
}
```

Required: `redirect_uris` (non-empty array).

Only `token_endpoint_auth_method=none` is accepted (public clients only).
`grant_types` is normalised to `["authorization_code"]` — unsupported types
are silently dropped per RFC 7591 §3.2.1 rather than rejected.

### Response (201 Created)

```json
{
  "client_id": "550e8400-e29b-41d4-a716-446655440000",
  "client_id_issued_at": 1745000000,
  "redirect_uris": ["https://client.example.com/callback"],
  "client_name": "My MCP Client",
  "grant_types": ["authorization_code"],
  "response_types": ["code"],
  "token_endpoint_auth_method": "none"
}
```

No `client_secret` is issued. Client registrations are not persisted in v0.1.

### Error response (400 Bad Request)

```json
{
  "error": "invalid_client_metadata",
  "error_description": "redirect_uris is required"
}
```

---

## JWKS and token verification

Endpoint: `GET /.well-known/jwks`

Returns the public key set used to verify JWTs:

```json
{
  "keys": [{
    "kty": "EC",
    "crv": "P-384",
    "kid": "<sha256-thumbprint>",
    "x": "...",
    "y": "...",
    "use": "sig",
    "alg": "ES384"
  }]
}
```

The `kid` in each JWT header matches a key in this document. Clients SHOULD
cache this document for up to 24 hours (`Cache-Control: public, max-age=86400`).

Key rotation: restart the server with a new key generated by `starlogz-server keygen`.
Clients that cached the old JWKS will fail verification and need to refetch.

---

## Token format

Algorithm: ES384 (ECDSA over P-384 with SHA-384)

Claims:

| Claim | Type | Description |
|-------|------|-------------|
| `iss` | string | Issuer — the server's base URL |
| `sub` | string | GitHub user ID as decimal string (v0.1); will be internal UUID once the `users` table is added |
| `email` | string | User's primary email |
| `scope` | string | Space-delimited list of granted scopes |
| `jti` | string | Unique token ID (UUID v4) — required for revocation |
| `exp` | int | Unix timestamp — expiry |
| `iat` | int | Unix timestamp — issued at |
| `kid` | string (header) | Key ID matching JWKS entry |

Example payload:
```json
{
  "iss": "https://starlogz.example.com",
  "sub": "12345678",
  "email": "user@example.com",
  "scope": "facts:read facts:write",
  "jti": "a1b2c3d4-e5f6-7890-abcd-ef1234567890",
  "exp": 1745604800,
  "iat": 1745000000
}
```

### Token revocation

`VerifyJWT` requires a `jti` claim and rejects tokens found in the revocation
blocklist. The logout handler (`POST /auth/logout`) calls `RevokeToken(jti, exp)`
to add the token to the blocklist, making logout effective immediately even
though the JWT has not yet expired.

**v0.1 constraint — in-memory blocklist:** The blocklist lives in the server
process. A server restart clears it, meaning previously revoked tokens become
valid again until their `exp`. Acceptable for v0.1; v0.2 will persist the
blocklist to a `revoked_tokens` table in Postgres and clean it up via a
background job keyed on `exp`.

---

## Endpoint reference

| Endpoint | Method | Auth | Description |
|----------|--------|------|-------------|
| `/.well-known/oauth-authorization-server` | GET | None | RFC 8414 discovery (primary) |
| `/.well-known/openid-configuration` | GET | None | OIDC discovery (fallback) |
| `/.well-known/jwks` | GET | None | Public key set |
| `/.well-known/oauth-protected-resource` | GET | None | RFC 9728 resource metadata |
| `/oauth2/register` | POST | None | Dynamic Client Registration (RFC 7591) |
| `/oauth2/authorize` | GET | None | Authorization endpoint — redirects to GitHub |
| `/oauth2/token` | POST | None | Token endpoint — issues JWT |
| `/auth/github/callback` | GET | None | GitHub OAuth2 callback |
| `/auth/logout` | POST | Bearer JWT | Revokes token via jti blocklist |
| `/auth/me` | GET | Bearer JWT | Returns current user and orgs _(not yet implemented)_ |
| `/tokens` | POST | Bearer JWT | Creates API key _(not yet implemented)_ |
| `/tokens` | GET | Bearer JWT | Lists API keys _(not yet implemented)_ |
| `/tokens/:id` | DELETE | Bearer JWT | Revokes API key _(not yet implemented)_ |
| `/mcp` | POST | Bearer JWT | MCP StreamableHTTP endpoint |
| `/health` | GET | None | Health check |

---

## Discovery documents

### Authorization server (`/.well-known/oauth-authorization-server`)

```json
{
  "issuer": "https://starlogz.example.com",
  "authorization_endpoint": "https://starlogz.example.com/oauth2/authorize",
  "token_endpoint": "https://starlogz.example.com/oauth2/token",
  "jwks_uri": "https://starlogz.example.com/.well-known/jwks",
  "registration_endpoint": "https://starlogz.example.com/oauth2/register",
  "response_types_supported": ["code"],
  "grant_types_supported": ["authorization_code"],
  "scopes_supported": ["facts:read", "facts:write", "org:admin"],
  "code_challenge_methods_supported": ["S256"],
  "token_endpoint_auth_methods_supported": ["none"]
}
```

### Protected resource (`/.well-known/oauth-protected-resource`)

```json
{
  "resource": "https://starlogz.example.com/mcp",
  "resource_name": "Starlogz MCP Server",
  "authorization_servers": ["https://starlogz.example.com"],
  "scopes_supported": ["facts:read", "facts:write", "org:admin"],
  "bearer_methods_supported": ["header"]
}
```

Note: `authorization_servers` contains the issuer URL, not the discovery URL.
MCP clients construct the discovery URL from the issuer.

---

## v0.1 constraints

- **No refresh tokens** — clients must re-authorize when the JWT expires (7 days)
- **No token introspection** — not advertised in discovery
- **No token revocation** — not advertised in discovery
- **Public clients only** — `token_endpoint_auth_method=none`; no `client_secret`
- **Client registrations not persisted** — DCR returns a `client_id` but does not
  store it; the server cannot validate the `client_id` at the token endpoint yet
- **GitHub only** — Google OAuth2 is planned for v0.2
- **No API key validation yet** — API key bearer tokens are not implemented;
  only JWTs from the GitHub OAuth2 flow are verified
- **`sub` is GitHub user ID** — the `sub` JWT claim is the GitHub numeric user
  ID as a decimal string; it will become an internal UUID once the `users` table
  is added in v0.2
- **In-memory OAuth2 state** — pending authorizations (10 min TTL) and issued
  auth codes (5 min TTL) are stored in-process; a server restart during an
  active login flow will invalidate the state and require the user to start over
