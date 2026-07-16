# Web UI sessions

> Status: Current contract
> Last reviewed: 2026-07-16
> Authority: Behavioral and security contract; current code, migrations, and tests provide implementation evidence.

The dashboard uses GitHub OAuth2 to establish identity, then replaces the OAuth
credential with a server-side browser session. MCP clients continue to use the
OAuth2 bearer-token and refresh-token flow described in `auth.md` and
`refresh_tokens.md`.

## Session credential

The server generates 32 random bytes and sends their base64url representation in
the `starlogz_session` cookie. The database stores only the SHA-256 hash. The raw
credential must never be stored or logged.

The cookie is `HttpOnly`, `SameSite=Lax`, scoped to `/`, and `Secure` on HTTPS.
Its maximum age matches the session's absolute expiry.

## Lifetime

Sessions have both an idle expiry and an absolute expiry. Requests update
`last_seen_at` and extend the idle expiry at most once per hour. An expired or
revoked session is rejected and its cookie is cleared.

Database failures return 500 without clearing the cookie so transient
infrastructure faults do not force users to authenticate again.

Default policy:

- idle lifetime: 7 days
- absolute lifetime: 30 days
- activity update interval: 1 hour

The lifetimes are configured with `UI_SESSION_IDLE_TTL` and `UI_SESSION_TTL`.

## User profile

GitHub profile data is durable user data and is stored on `users`, not on the
session. Each successful GitHub login refreshes the login, verified primary
email, display name, avatar URL, profile URL, and bio. Optional GitHub values
are stored as empty strings.

## Login and logout

The existing authorization-code and PKCE flow remains the login bootstrap. On
successful code exchange, the server verifies the returned JWT, resolves its
user ID, creates a browser session, and sets the opaque cookie.

The UI client registers only the `authorization_code` grant. The shared token
handler issues the bootstrap JWT but does not persist an MCP grant or return a
refresh token for clients that do not register the `refresh_token` grant.

Logout accepts POST only, revokes the current web session, clears the cookie,
and redirects to `/`. SameSite cookies and POST-only state changes provide the
current CSRF boundary; future cross-site deployments must add explicit Origin
checking or CSRF tokens.

## Auditing

Session creation, revocation, and deletion are written to `audit_log` without
the session token hash. Hourly `last_seen_at` and idle-expiry updates are
intentionally excluded to avoid high-volume audit churn.
