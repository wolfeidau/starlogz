# OAuth Client ID Metadata Documents

> Status: Implemented decision
> Last reviewed: 2026-07-24
> Authority: Historical architecture decision; [auth.md](auth.md), current code, migrations, and tests define supported behavior.

## Outcome

OAuth Client ID Metadata Document (CIMD) support merged in PR
[#98](https://github.com/wolfeidau/starlogz/pull/98) at merge commit `451b206`
and was deployed to the shared development environment on 2026-07-23.
Starlogz can resolve compatible public OAuth clients from an HTTPS metadata URL
while retaining Dynamic Client Registration (DCR) and the first-party
dashboard flow. `CIMD_ENABLED` remains the operational rollback control.

Current client-selection, validation, confirmation, token-binding, and
discovery behavior is specified in [auth.md](auth.md).

## Problem

DCR requires each previously unknown MCP client to create and persist a
registration for every Starlogz deployment. Clients with a stable public web
presence can already publish the same OAuth metadata at a URL they control.
CIMD uses that URL directly as the client identifier, avoiding deployment-local
registration without removing DCR for clients that need it.

The authorization server must fetch an attacker-selected URL before the user is
authenticated. This introduces server-side request forgery, availability,
privacy, and client-impersonation risks that do not exist in the same form for
persisted registrations.

## Decision

- Resolve persisted first-party and DCR clients before considering CIMD. An
  existing client ID is never reinterpreted as a remote document.
- Support HTTPS metadata documents for public authorization-code clients only.
  Keep the accepted URL and metadata surface intentionally narrower than the
  Internet-Draft where Starlogz has no interoperability need.
- Fetch metadata with a dedicated, bounded HTTP path that bypasses environment
  proxies, validates public DNS answers again at connection time, preserves TLS
  hostname verification, rejects redirects, and limits time and response size.
- Bind the resolved client ID, display data, redirect, scopes, PKCE challenge,
  client kind, and refresh eligibility into single-use server-side state.
  Token and refresh endpoints do not fetch the document again.
- Reuse the post-GitHub client confirmation. Prominently display the metadata
  hostname and explain that the client name came from that domain so a
  user-controlled display name cannot stand alone as identity.
- Advertise support only while `CIMD_ENABLED` is active. Keep DCR advertised
  and operational as the compatibility path.

## Rationale

The registered-first order preserves existing client identities and avoids
turning server-issued identifiers into network locations. Authorization-time
binding prevents a metadata change, outage, or second fetch from changing an
in-flight authorization decision. Reusing the existing confirmation and
single-use state preserves the established GitHub, PKCE, redirect, and approval
boundaries instead of creating a parallel OAuth flow.

The metadata hostname is a necessary trust signal because the remote
`client_name` is untrusted. The feature flag provides a low-cost rollback while
the 0.x service gains interoperability evidence in its development environment.

## Lasting tradeoffs

- The reviewed CIMD specification is an Internet-Draft. Later revisions require
  an explicit compatibility review before Starlogz widens or changes behavior.
- Strict URL, client-authentication, metadata, and redirect policies reject
  some clients that the draft could permit. Support should widen only for a
  demonstrated client requirement.
- CIMD adds an outbound network dependency to authorization startup. Documents
  are not durable registrations, and persistent or distributed caching is not
  implemented.
- Metadata presentation URLs, signed metadata, key-based client
  authentication, trust marks, and non-default client identifier ports or
  query components remain unsupported.
- DCR remains necessary for clients without a stable public HTTPS metadata
  document.

## Standards basis

- [MCP Authorization, protocol revision 2025-11-25](https://modelcontextprotocol.io/specification/2025-11-25/basic/authorization)
- [OAuth Client ID Metadata Document draft-ietf-oauth-client-id-metadata-document-02](https://www.ietf.org/archive/id/draft-ietf-oauth-client-id-metadata-document-02.html)
- [OAuth 2.0 Authorization Server Metadata, RFC 8414](https://www.rfc-editor.org/rfc/rfc8414)
- [OAuth 2.0 Dynamic Client Registration, RFC 7591](https://www.rfc-editor.org/rfc/rfc7591)
- [Special-Purpose IP Address Registries, RFC 6890](https://www.rfc-editor.org/rfc/rfc6890)
