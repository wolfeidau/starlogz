# OAuth Client ID Metadata Documents

> Status: Proposed
> Last reviewed: 2026-07-23
> Authority: Experimental design and development-rollout record informed by the MCP authorization specification, the OAuth Client ID Metadata Document Internet-Draft, and current repository evidence. This is not a stable contract.

## Summary

When enabled, Starlogz supports OAuth Client ID Metadata Documents (CIMD) as an
optional client identification path alongside the existing Dynamic Client
Registration (DCR) flow. A compatible MCP client can use an HTTPS
metadata-document URL as its `client_id`; Starlogz resolves and validates that
document when authorization starts.

The initial implementation supports public clients only, requires exact
redirect URI matching, uses the shared post-GitHub client confirmation, and treats
metadata retrieval as an SSRF-sensitive operation. DCR and the first-party
dashboard client remain supported without behavior changes.

The `feat_cimd_support` implementation adds this path for the current
development deployment. Starlogz is still a 0.x project, so CIMD remains
experimental and may evolve without a production compatibility window.
`CIMD_ENABLED` provides a quick rollback to the baseline DCR flow.

## Motivation

Without CIMD, Starlogz requires MCP clients to register through DCR before
starting authorization. CIMD lets a client publish stable OAuth metadata at a
URL it controls and use that URL directly as its client identifier. This avoids
a separate registration record for every Starlogz deployment while retaining
DCR for clients that do not support CIMD.

The feature is additive. It must not weaken the existing authorization-code,
PKCE, redirect URI, token, or refresh-token contracts in [auth.md](auth.md).

## Standards basis

The proposal is based on these sources as reviewed on 2026-07-19:

- [MCP Authorization, protocol revision 2025-11-25](https://modelcontextprotocol.io/specification/2025-11-25/basic/authorization)
  recommends CIMD support and defines client selection, user-interface, SSRF,
  and redirect-validation expectations for MCP implementations.
- [OAuth Client ID Metadata Document draft-ietf-oauth-client-id-metadata-document-02](https://www.ietf.org/archive/id/draft-ietf-oauth-client-id-metadata-document-02.html)
  defines HTTPS URL client identifiers, document retrieval and validation,
  exact identifier matching, caching, and security considerations. It is an
  Internet-Draft, not an RFC; implementation must pin its tested behavior to a
  reviewed draft and reassess later revisions before release.
- [OAuth 2.0 Authorization Server Metadata, RFC 8414](https://www.rfc-editor.org/rfc/rfc8414)
  defines the discovery document extended by the CIMD capability flag.
- [OAuth 2.0 Dynamic Client Registration, RFC 7591](https://www.rfc-editor.org/rfc/rfc7591)
  defines the client metadata fields shared by DCR and CIMD.
- [Special-Purpose IP Address Registries, RFC 6890](https://www.rfc-editor.org/rfc/rfc6890)
  provides the basis for blocking non-public network destinations.

These are external protocol requirements. Current implementation evidence is
recorded below.

## Current repository evidence

As reviewed on 2026-07-23:

- authorization-server discovery always advertises the DCR
  `registration_endpoint` and advertises
  `client_id_metadata_document_supported` only when CIMD is enabled;
- `/oauth2/authorize` resolves persisted clients first, then eligible HTTPS
  client ID metadata documents when CIMD is enabled;
- the post-GitHub confirmation flow is now implemented for non-first-party
  registered and CIMD clients, with `pending_auths`,
  `authorization_confirmations`, and `auth_codes` carrying trusted client,
  redirect, scope, PKCE, refresh eligibility, and upstream token state through
  single-use PostgreSQL records;
- authorization-code exchange uses refresh eligibility bound during
  authorization rather than reloading the metadata document; and
- the first-party dashboard still uses the fixed `starlogz-ui` client ID and
  bypasses confirmation only by exact client ID, not by client-supplied name.

The current [auth.md](auth.md) contract documents the baseline DCR flow. This
proposal records the experimental CIMD behavior used by the development
deployment.

## Experimental behavior

### Client resolution

At the authorization endpoint, Starlogz resolves a client in this order:

1. A pre-registered first-party or DCR client is loaded from PostgreSQL.
2. An otherwise unknown client ID that is an eligible HTTPS URL is resolved as
   a CIMD client.
3. Any other unknown client is rejected.

This order preserves existing registrations and ensures that server-issued
client IDs are never reinterpreted as remotely hosted metadata. Starlogz keeps
an internal, bounded client kind such as `registered`, `cimd`, or
`first_party`; it does not infer the kind again from untrusted text after
resolution.

A resolved client value should contain only the authorization decisions needed
by later stages:

- exact client ID;
- validated redirect URI set;
- allowed scope set;
- whether the refresh-token grant is allowed;
- bounded client kind; and
- escaped display name and client-ID hostname for the confirmation response.

CIMD documents are not copied into `oauth_clients`. Their stable identifier is
the URL, and authorization-time decisions are bound to the pending request and
authorization code instead of depending on a later network fetch.

### Eligible client identifier URLs

The initial implementation accepts a CIMD client ID only when all of these are
true:

- the value is no longer than 2048 bytes;
- it is an absolute HTTPS URL with a hostname and a path;
- it has no userinfo, fragment, dot path segments, or query component;
- it uses the default HTTPS port;
- its hostname is not an IP literal; and
- DNS resolution produces only public, globally routable addresses.

Rejecting query components and non-default ports is an intentional initial
Starlogz restriction even where the draft permits or discourages rather than
forbids them. Support can be widened later with specific interoperability
evidence.

Development mode does not exempt localhost, loopback, private, link-local, or
other special-purpose destinations. Tests use an injected resolver and HTTP
transport rather than weakening production validation.

### Metadata retrieval

Add a resolver in `internal/oidc`, with network behavior isolated behind a
small interface so deterministic tests do not require external hosts. The
production resolver must:

- use a dedicated HTTP client that ignores environment proxy settings;
- resolve DNS itself, reject the request if any answer is non-public, and dial
  an already validated address while preserving the original TLS server name;
- reject IPv4 and IPv6 special-purpose ranges comprehensively, including
  loopback, private, link-local, documentation, benchmarking, multicast,
  unspecified, and IPv4-mapped forms;
- never follow redirects;
- require a successful `200` JSON response;
- apply a short end-to-end timeout, initially three seconds;
- read at most 5 KiB and reject an oversized or trailing JSON payload; and
- return a generic OAuth error without exposing network details to the client.

DNS checks must apply when each new connection is established so a hostname
cannot pass validation and then be redialled to an unchecked address. The
resolver must not reuse the application's general-purpose HTTP client.

### Metadata validation

The document must include:

- `client_id`, exactly equal to the requested URL using simple string
  comparison;
- a non-empty `client_name`; and
- one to ten valid `redirect_uris`.

The initial implementation supports only public clients using
`token_endpoint_auth_method=none`, the authorization-code grant, response type
`code`, and existing Starlogz scopes. Shared-secret client authentication is
rejected. Key-based client authentication, `jwks`, `jwks_uri`, signed metadata,
software statements, and CIMD service endpoints are deferred.

Redirect URI syntax uses the same validation policy as DCR, then authorization
requires an exact string match against the validated set. Supplied scopes must
be a subset of Starlogz-supported scopes. The implementation must define and
test normalization for omitted `grant_types`, `response_types`,
`token_endpoint_auth_method`, and `scope` rather than inheriting unsafe generic
OAuth defaults accidentally.

Remote presentation fields such as `logo_uri`, `client_uri`,
`policy_uri`, and `tos_uri` are ignored initially. Starlogz does not cause the
browser to load resources from those URLs.

### Cache

Valid documents may be cached in a bounded, per-process cache:

- at most 256 entries;
- concurrent requests for one client ID are coalesced;
- `Cache-Control: no-store` is honored;
- explicit freshness metadata is honored with a Starlogz maximum of 24 hours;
- responses without explicit freshness are not cached initially; and
- network errors, non-success responses, oversized responses, and invalid
  documents are never cached.

The cache is an availability and latency optimization, not a source of durable
client registration. A process restart can require a new fetch. Metadata
changes apply only to new authorization attempts and never mutate an existing
pending request, authorization code, or grant.

### Authorization confirmation

CIMD clients use the shared post-GitHub confirmation specified in
[auth.md](auth.md), at the same point as DCR clients. Client resolution binds
the validated display name, exact redirect URI, scopes, PKCE request, client
kind, and refresh eligibility into pending authorization state before the
GitHub redirect. After GitHub authentication, Starlogz renders the existing
confirmation page using those bound values; it does not fetch the metadata
document again.

The page shows the escaped resolved client name, full exact redirect URI, and
requested Starlogz scopes with server-owned descriptions. It does not render
remote HTML, images, or links from client metadata. The form carries only the
same hashed, expiring, one-time capability used by DCR confirmations. Approval
atomically creates the authorization code; denial deletes the transient state.
Repeated, expired, cancelled, or tampered submissions fail closed.

The explicitly configured first-party dashboard continues directly after
GitHub. CIMD display names can never select that bypass.

### Binding authorization decisions

Starlogz must not fetch the CIMD document again at the token or refresh
endpoints. The authorization-time decision is carried through server-side
state:

1. Resolution validates the redirect, scope, response type, PKCE request, and
   refresh eligibility.
2. Pending GitHub authorization and post-GitHub confirmation state retain the bounded
   client kind and refresh eligibility.
3. The authorization code retains those values with the exact client ID,
   redirect URI, scope, and PKCE challenge.
4. Code exchange uses the value bound to the code rather than querying
   `oauth_clients` to rediscover refresh eligibility.
5. A created refresh grant remains bound to the exact client ID under the
   existing refresh-token contract.

Migration 22 adds `refresh_allowed` to `pending_auths`,
`authorization_confirmations`, and `auth_codes`. This carries the
authorization-time decision through code exchange without another metadata
fetch. A bounded `client_kind` should be added only if later confirmation or
observability work needs it.

In-flight requests created before deployment may lose refresh eligibility but
must remain safe and able to complete an access-token exchange where otherwise
valid.

### Discovery and rollout

Starlogz advertises `client_id_metadata_document_supported` as `true` when
CIMD is enabled. Application configuration defaults it to false; the current
Terraform development deployment defaults it to true.

The development rollout is intentionally lightweight:

1. deploy the code and migration together;
2. complete a real MCP client authorization and confirm DCR fallback;
3. inspect failures during normal development use; and
4. set `CIMD_ENABLED=false` if interoperability or security issues require a
   quick rollback.

There is no separate production rollout contract while Starlogz remains a 0.x
development service. Production stability and compatibility gates can be added
when the project introduces a production environment.

## Security requirements

- Treat metadata retrieval as server-side request forgery exposure. URL
  parsing, DNS validation, address pinning, redirect rejection, response size,
  and timeout limits are required controls.
- Preserve exact client ID and redirect URI comparisons. Do not canonicalize a
  value and then use the canonical form as an authorization identity.
- Treat fetched metadata as untrusted input in HTML, logs, errors, metrics, and
  events.
- Do not support a shared client secret for a URL client identifier. Future
  proof-of-possession methods require a separate proposal.
- Bind resolved decisions to single-use server-side state so metadata changes
  or fetch failures cannot alter an authorization already in progress.
- Fail closed when resolution, validation, persistence, or state transition
  fails.

## Privacy and observability requirements

CIMD metadata is public, but copying it indiscriminately creates separate
privacy and operational risks:

- the full client ID URL, redirect URI, and client name create uncontrolled
  log and metric cardinality and can contain identifying path text;
- metadata fetch timing and frequency can reveal authorization activity to the
  metadata host; and
- browser retrieval of a remote logo or other presentation URL can let a
  client correlate the authorizing user.

Logs, traces, metrics, Sentry data, and EventBridge events therefore record only
bounded outcome fields such as client kind, success or failure stage, and a
small error reason taxonomy. They do not record raw CIMD URLs, document
metadata, redirect URIs, DNS answers, authorization state, or query values.
Normal HTTP access logging must be checked because `client_id` arrives in the
authorization query string.

These controls do not classify the metadata document itself as secret. They
limit unnecessary propagation and prevent unbounded observability dimensions.

## Implementation summary

1. A dedicated `internal/oidc` CIMD resolver applies URL, DNS,
   transport, size, timeout, and metadata validation rules.
2. `/oauth2/authorize` resolves registered clients first, then
   eligible CIMD URL client IDs, and rejects all other unknown clients.
3. Migration 22 persists authorization-time refresh eligibility through the
   existing single-use server-side state.
4. CIMD reuses the current post-GitHub confirmation page and one-time
   completion flow.
5. Discovery advertises `client_id_metadata_document_supported` only when the
   feature flag is enabled.

## Verification backlog

The items below guide hardening as the experimental implementation evolves.
They are not deployment gates for the current development environment.

### Resolver tests

- accepted HTTPS URL and exact `client_id` self-match;
- userinfo, fragment, query, dot segment, non-default port, IP literal, and
  overlong client ID rejection;
- every denied IPv4 and IPv6 special-purpose class, IPv4-mapped IPv6, mixed DNS
  answers, DNS rebinding, and dial-time revalidation;
- redirect rejection, TLS hostname preservation, proxy bypass, timeout,
  non-200 response, content type, 5 KiB limit, trailing JSON, and malformed
  JSON;
- required and unsupported metadata fields, exact redirect matching, scopes,
  public-client authentication, and grant defaults;
- positive cache freshness, `no-store`, no implicit freshness, eviction,
  concurrency coalescing, and no caching of errors or invalid documents.

### Authorization tests

- registered clients resolve without a network request;
- eligible unknown HTTPS clients use CIMD and other unknown clients fail;
- confirmation escapes metadata and displays the required host, destination,
  scopes, and warnings;
- confirmation state is opaque, expiring, tamper-resistant, and single-use;
- metadata mutation after confirmation cannot change the pending request;
- code exchange preserves exact client, redirect, PKCE, scope, and refresh
  eligibility bindings without a second fetch;
- old pending rows default safely when the migration is deployed; and
- DCR, first-party dashboard login, and existing refresh flows remain
  unchanged.

### Privacy and end-to-end tests

- raw client metadata, URLs, redirect URIs, DNS answers, and query parameters do
  not appear in logs, traces, Sentry payloads, or EventBridge events;
- discovery omits the capability while disabled and advertises it while
  enabled;
- an MCP client completes authorization from a controlled public HTTPS CIMD
  document;
- the MCP Inspector still completes the DCR fallback flow; and
- failure, cancellation, restart, and multi-instance state transitions fail
  closed without replay.

## Development rollout checks

Before leaving CIMD enabled in the shared development environment:

- the server starts after migration 22 and discovery advertises the capability;
- the existing automated suite passes;
- one current MCP client completes authorization through a public HTTPS CIMD
  document; and
- DCR and the dashboard remain usable.

Failures discovered during development are fixed in place or handled by
disabling `CIMD_ENABLED`; they do not require a formal compatibility or release
process.

## Deferred decisions

- private-key JWT or other proof-of-possession authentication;
- signed metadata and trust marks;
- `jwks`, `jwks_uri`, or a CIMD service endpoint;
- remote logos or other presentation resources;
- persistent or distributed metadata caching;
- grant invalidation when a document changes;
- non-default HTTPS ports or query-bearing client identifiers;
- development-only localhost metadata.
