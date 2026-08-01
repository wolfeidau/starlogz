# Dashboard operations controls

> Status: Current contract
> Last reviewed: 2026-07-30
> Authority: Behavioral and security contract; current code, migrations, and tests provide implementation evidence.

## Authorization and scope

Only a dashboard user allowed by `OPERATOR_GITHUB_IDS` may call an operations
mutation. The server enforces this boundary on every request. Organization
membership, OAuth scopes, and the UI capability hint do not grant access.

The initial control plane supports individual revocation only. Bulk actions,
user deletion, client deletion, token inspection, and credential display are
out of scope.

## Dashboard sessions

`RevokeOperationsWebSession` accepts the stable session UUID already present in
the operations read model. Revocation sets `revoked_at` and immediately makes
the opaque session cookie unusable. Repeating the mutation for an already
revoked session succeeds without creating another operator-action record.

The server rejects revocation of the operator's current session, and the UI
does not offer it as an action. Every other active session requires an explicit
inline confirmation.

## OAuth grants

Each refresh-capable grant has a stable UUID that is independent of its rotating
JWT ID. `RevokeOperationsOAuthGrant` uses this UUID to atomically:

1. revoke the current access-token JWT ID until its expiry;
2. delete the refresh-capable grant;
3. retain only the hashed Starlogz refresh token with bounded reason
   `operator_revoked`; and
4. record the operator action.

The raw Starlogz refresh token, GitHub credentials, ciphertext, and JWT ID are
never returned to the UI. A revoked client must authorize again.

Grant audit snapshots omit the raw Starlogz refresh token and encrypted GitHub
token fields. The migration also removes these fields from earlier grant audit
snapshots.

## Operator-action records

The database records the canonical actor user ID, bounded action, target UUID,
target user ID, optional client ID, and timestamp in the same transaction as
the credential change. The operations overview exposes recent records with
bounded display names resolved from the user table.

Wide events `operator.web_session_revoke.completed` and
`operator.oauth_grant_revoke.completed` record aggregate outcomes and bounded
failure reasons. They contain no actor, target, client, credential, or arbitrary
error attributes.

## Related contracts

- [Dashboard operations view](dashboard_operations.md)
- [Wide events](events.md)
- [OAuth2 refresh-token grant](refresh_tokens.md)
- [Web UI sessions](web_sessions.md)
