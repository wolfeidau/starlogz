# OAuth Client ID Metadata Documents

> Status: Proposed
> Last reviewed: 2026-07-19
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

Implementation should wait until the client-identity classification work
represented by closed, unmerged PR
[#69](https://github.com/wolfeidau/starlogz/pull/69) has an explicit
disposition. That work overlaps the OAuth handlers and privacy-safe event
fields affected by CIMD.

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

As reviewed on 2026-07-19:

- authorization-server discovery advertises a DCR `registration_endpoint` but
  does not advertise `client_id_metadata_document_supported`;
- authorization requires `client_id` to resolve to a persisted `oauth_clients`
  record before redirecting to GitHub;
- refresh-token eligibility is checked by looking up the persisted client again
  during authorization-code exchange;
- the first-party dashboard uses the fixed `starlogz-ui` client ID and registers
  only the authorization-code grant; and
- OAuth request state and authorization codes already bind the client ID,
  redirect URI, scope, and PKCE challenge in PostgreSQL.

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

This likely requires the next available migration to add a non-null
`refresh_allowed` field to pending authorizations and authorization codes,
defaulting to false for existing rows. A bounded `client_kind` field should be
added only if it is needed after confirmation or for privacy-safe events. The
implementation must prefer the smallest schema change that preserves the
binding.

In-flight requests created before deployment may lose refresh eligibility but
must remain safe and able to complete an access-token exchange where otherwise
valid.

### Discovery and rollout

The Go MCP SDK already models the authorization-server metadata member
`client_id_metadata_document_supported`. Starlogz advertises it as `true` only
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

## Delivery plan

### 0. Resolve overlapping work

- Decide whether to restore, replace, or abandon the client-identity
  classification design from closed PR #69.
- If retained, land its bounded classification and privacy rules first, then
  rebase the CIMD work on that contract.
- Reconcile the reviewed IETF draft version immediately before implementation.

### 1. Specify and implement secure resolution

- Add CIMD configuration and an `internal/oidc` resolver.
- Share validation rules with DCR only where semantics are actually identical.
- Implement dedicated network controls, document validation, caching, and
  privacy-safe failure classification.
- Add unit tests before integrating the resolver with authorization.

Suggested commit: `feat: add secure CIMD metadata resolution`

### 2. Bind CIMD clients into authorization

- Add registered-first client resolution at `/oauth2/authorize`.
- Persist authorization-time refresh eligibility and any required bounded
  client kind through pending state and authorization codes.
- Remove the token endpoint's assumption that every valid client must be
  reloaded from `oauth_clients`.
- Preserve DCR and first-party dashboard behavior.

Suggested commit: `feat: resolve CIMD clients during authorization`

### 3. Extend shared client confirmation

- Carry resolved CIMD display values and bounded client classification into
  the existing post-GitHub confirmation state.
- Reuse the existing escaped response, server-owned scope descriptions,
  one-time capability, CSRF controls, expiry, and atomic completion.
- Add CIMD-specific coverage without introducing a pre-GitHub confirmation.

Suggested commit: `feat: confirm CIMD authorization requests`

### 4. Advertise and verify support

- Set `client_id_metadata_document_supported` only behind the enabled feature.
- Add discovery, compatibility, privacy, and end-to-end coverage.
- Validate the flow with a controlled public HTTPS fixture and the MCP
  Inspector, then with current clients that expose CIMD configuration.
- Update [auth.md](auth.md) when enabling the behavior as a supported contract.

Suggested commit: `feat: advertise CIMD client metadata support`

This proposal itself can be committed separately as
`docs: propose CIMD client metadata support`.

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
