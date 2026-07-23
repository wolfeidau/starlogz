# OAuth Client ID Metadata Documents

> Status: Proposed
> Last reviewed: 2026-07-23
> Authority: Design proposal informed by the MCP authorization specification, the OAuth Client ID Metadata Document Internet-Draft, and current repository evidence. This is not a current contract.

## Summary

Add OAuth Client ID Metadata Document (CIMD) support as an optional client
identification path alongside the existing Dynamic Client Registration (DCR)
flow. A compatible MCP client can use an HTTPS metadata-document URL as its
`client_id`; Starlogz resolves and validates that document when authorization
starts.

The initial implementation supports public clients only, requires exact
redirect URI matching, uses the shared post-GitHub client confirmation, and treats
metadata retrieval as an SSRF-sensitive operation. DCR and the first-party
dashboard client remain supported without behavior changes.

As of 2026-07-23, Starlogz still implements DCR-only client onboarding. This
document describes an additive design for CIMD that reuses the current
post-GitHub confirmation flow and existing single-use authorization state.

## Motivation

Starlogz currently requires MCP clients to register through DCR before starting
authorization. CIMD lets a client publish stable OAuth metadata at a URL it
controls and use that URL directly as its client identifier. This avoids a
separate registration record for every Starlogz deployment while retaining DCR
for clients that do not support CIMD.

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

These are external protocol requirements, not evidence that Starlogz currently
implements CIMD.

## Current repository evidence

As reviewed on 2026-07-23:

- authorization-server discovery advertises the DCR
  `registration_endpoint`, and repository tests assert the current metadata
  shape without a `client_id_metadata_document_supported` flag;
- `/oauth2/authorize` resolves clients only from persisted `oauth_clients` and
  rejects any unknown `client_id` before GitHub redirect;
- the post-GitHub confirmation flow is now implemented for non-first-party
  registered clients, with `pending_auths`, `authorization_confirmations`, and
  `auth_codes` carrying trusted client, redirect, scope, PKCE, and upstream
  token state through single-use PostgreSQL records;
- authorization-code exchange still decides refresh-token eligibility by
  reloading the registered client from `oauth_clients`; and
- the first-party dashboard still uses the fixed `starlogz-ui` client ID and
  bypasses confirmation only by exact client ID, not by client-supplied name.

The current behavior remains governed by [auth.md](auth.md) until this proposal
is implemented and that contract is updated.

## Proposed behavior

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

Current schema already persists the client ID, client name, redirect URI,
scope, PKCE challenge, and encrypted upstream tokens through
`pending_auths`, `authorization_confirmations`, and `auth_codes`. A CIMD
implementation still needs one trusted representation of refresh eligibility
and any bounded `client_kind` carried through those records so the token and
refresh endpoints do not re-fetch metadata or rediscover client type from
untrusted text. The implementation should prefer the smallest schema or state
change that preserves that binding.

In-flight requests created before deployment may lose refresh eligibility but
must remain safe and able to complete an access-token exchange where otherwise
valid.

### Discovery and rollout

Starlogz advertises `client_id_metadata_document_supported` as `true` only
when CIMD is enabled and all secure resolver and confirmation behavior is
active.

Introduce `CIMD_ENABLED`, defaulting to false for the initial deployment. DCR's
`registration_endpoint` remains advertised and operational. Rollout order is:

1. deploy disabled code and migrations;
2. enable CIMD in a development environment;
3. validate real MCP client authorization and DCR fallback;
4. review resolver and confirmation telemetry; and
5. enable production and update [auth.md](auth.md) in the release change.

The feature flag is not a substitute for secure defaults. Enabling it must not
activate partially configured network behavior.

## Security requirements

- Treat metadata retrieval as server-side request forgery exposure. URL
  parsing, DNS validation, address pinning, redirect rejection, response size,
  and timeout limits are release-blocking controls.
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

## Implementation outline

1. Add a dedicated `internal/oidc` CIMD resolver with strict URL, DNS,
   transport, size, timeout, and metadata validation rules.
2. Change `/oauth2/authorize` to resolve registered clients first, then
   eligible CIMD URL client IDs, and reject all other unknown clients.
3. Persist authorization-time client decisions needed after GitHub, including
   refresh eligibility and any bounded client classification, through the
   existing single-use server-side state.
4. Reuse the current post-GitHub confirmation page and one-time completion flow
   for CIMD clients.
5. Advertise `client_id_metadata_document_supported` only when the full secure
   path is enabled and [auth.md](auth.md) is updated to describe supported
   behavior.

## Verification plan

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

## Release gates

CIMD must not be advertised in production until all of these are true:

- the reviewed draft version and any deviations are recorded;
- SSRF, DNS rebinding, redirect, timeout, and response-bound tests pass;
- the confirmation screen and single-use transition are complete;
- DCR and dashboard compatibility suites pass;
- raw metadata has been excluded from observability paths;
- a real development OAuth flow succeeds with a public HTTPS CIMD fixture; and
- [auth.md](auth.md) describes the enabled client-selection and authorization
  behavior.

## Deferred decisions

- private-key JWT or other proof-of-possession authentication;
- signed metadata and trust marks;
- `jwks`, `jwks_uri`, or a CIMD service endpoint;
- remote logos or other presentation resources;
- persistent or distributed metadata caching;
- grant invalidation when a document changes;
- non-default HTTPS ports or query-bearing client identifiers;
- development-only localhost metadata.
