# Privacy-safe client identity classification

## Status

Implemented. Deployment verification remains pending.

## Objective

Identify the bounded client product responsible for MCP and OAuth activity without logging raw `User-Agent` headers, MCP implementation metadata, OAuth client names, or stable fingerprints of unknown values.

The production user-agent classification deployed in July 2026 is privacy-safe but does not identify agent products. In a representative `starlogz-dev` sample, all `/mcp` requests mapped to `client_kind=other`. The existing browser classifier remains useful and is not replaced by this design.

## Goals

- Attribute MCP initialization, MCP tool calls, and recognized OAuth flows to a bounded client product where possible.
- Prefer protocol metadata over HTTP header inference.
- Keep output cardinality fixed and controlled by Starlogz.
- Process raw identity inputs in memory and discard them after classification.
- Keep old access tokens and unknown clients working.
- Make classifier changes reviewable, testable, and auditable in the repository.

## Non-goals

- Prove the identity of third-party software.
- Identify individual installations, users, or devices.
- Automatically discover the product behind an unknown value.
- Store raw or hashed user agents for later analysis.
- Use client classification for authentication, authorization, throttling, or billing.
- Build a general product analytics pipeline or dashboard.

## Trust model

MCP `clientInfo`, OAuth Dynamic Client Registration metadata, and HTTP `User-Agent` are controlled by the caller. They are useful for aggregate reporting but are not authenticated product identities.

Use these confidence values:

| Value | Meaning |
|---|---|
| `first_party` | Derived from a Starlogz-owned client identifier. |
| `declared` | Derived from MCP or OAuth protocol metadata. |
| `signature` | Derived from an approved HTTP user-agent signature. |
| `unknown` | No approved rule matched. |

## Output contract

Add product identity fields without changing the meaning of the existing browser fields:

| Field | Values | Notes |
|---|---|---|
| `client_product` | Approved product enum or `other` | Always bounded. |
| `client_product_major` | Integer `0` through `999` | Optional; omit when absent or invalid. |
| `client_identity_source` | `first_party`, `mcp_initialize`, `oauth_registration`, `user_agent`, `unknown` | Describes the selected input. |
| `client_identity_confidence` | `first_party`, `declared`, `signature`, `unknown` | Describes the trust level. |

Initial product enums:

- `starlogz_ui`;
- `codex`;
- `claude_code`;
- `cursor`;
- `vscode`;
- `mcp_inspector`;
- `other`.

Do not add a matching rule until its protocol name or minimal user-agent signature has been verified with a controlled client test or authoritative client documentation. An approved enum may exist before a verified rule uses it.

The initial implementation recognizes the verified Codex MCP name `codex-mcp-client` and OAuth names `Codex` and `codex-mcp-client`. The remaining product enums intentionally have no rule until a controlled fixture or authoritative source verifies their identifiers.

Example wide-event attributes:

```json
{
  "tool": "insight_search",
  "result_count_bucket": "1-10",
  "client_product": "codex",
  "client_identity_source": "oauth_registration",
  "client_identity_confidence": "declared"
}
```

Wide-event attributes remain strings because the existing event envelope uses `map[string]string`. Access logs may emit `client_product_major` as an integer.

## Classification package

Add `internal/clientclass` with a small, side-effect-free classifier.

```go
type Classification struct {
	Product    string
	Major      int
	HasMajor   bool
	Source     string
	Confidence string
}
```

Expose source-specific entry points rather than one ambiguous bag of values:

```go
func FromFirstParty(clientID string) Classification
func FromMCP(name, version string) Classification
func FromOAuth(clientName string) Classification
func FromUserAgent(userAgent string) Classification
```

Rules:

1. Normalize case and surrounding whitespace in memory.
2. Match exact protocol names or narrowly anchored user-agent product tokens.
3. Never use broad substring matches such as `contains("code")`.
4. Extract a major version only after the product rule matches.
5. Accept only a numeric major from `0` through `999`.
6. Return `other`, `unknown`, and no major when input is missing, malformed, or unmatched.
7. Never return caller-provided text.

Keep the registry in Go source initially. This provides compile-time constants, straightforward overlap tests, and a normal PR/deployment audit trail. Do not add runtime regex configuration or a database-managed registry in the first implementation.

Registry fixtures contain only verified protocol names and minimal synthetic user-agent signatures. Do not copy complete production headers into fixtures.

## Identity source precedence

Select the strongest source available to the event:

1. Starlogz first-party client ID.
2. MCP `initialize.clientInfo` for the initialization event.
3. OAuth registered `client_name` for OAuth events and token claims.
4. Approved HTTP user-agent signature.
5. `other`.

Do not merge partial results from different products. If a higher-precedence source matches, its complete classification wins.

## MCP implementation

### Stateless transport constraint

The server currently configures `mcp.StreamableHTTPOptions.Stateless=true`. The Go SDK creates a new logical server session for each HTTP request and supplies default initialization state on non-initialize calls. Consequently, `req.Session.InitializeParams().ClientInfo` is available during the `initialize` request but is not retained for later `tools/call` requests.

Do not change the transport to stateful mode for this feature. In-memory MCP sessions are not reliable across concurrent Lambda execution environments and would introduce a distributed session-storage requirement.

### Initialization event

Register MCP receiving middleware on the server. When `method == "initialize"`:

1. Read the typed `mcp.InitializeParams` from the request.
2. Classify `ClientInfo.Name` and `ClientInfo.Version` in memory.
3. Call the next MCP handler.
4. Emit one `mcp.client_initialized` wide event with the bounded classification and success or failure outcome.
5. Discard the raw metadata.

This event counts initialization attempts, not unique clients, installations, or users.

### Tool-call attribution

Later stateless tool requests obtain classification in this order:

1. Bounded classification copied from the verified Starlogz access token into `auth.TokenInfo.Extra`.
2. `req.Extra.Header.Get("User-Agent")` classified in memory with approved signatures.
3. `other`.

Extend `trackTool` to add the selected classification to `mcp.tool_call.completed`. Do not add client identity to tool results or MCP response metadata.

## OAuth implementation

### Registration

OAuth DCR already accepts and persists `client_name` as required registration metadata. Keep that behavior; it supports authorization UI and existing OAuth semantics. The privacy handler must continue to block it from logs.

Add `oauth.client_registration.completed`. After validation and normalization:

1. Classify `req.ClientName` in memory.
2. Fall back to the request user agent when the name is unknown.
3. Emit only bounded identity attributes.

Malformed bodies that cannot be assigned to a valid registration remain access-log-only.

### Authorization, callback, exchange, and refresh

For recognized OAuth flows, derive classification from the registered client record selected by `client_id`. Reuse a client record already loaded by the handler; otherwise perform a best-effort lookup. Classification lookup failure must not change the OAuth response and maps to `other`.

The existing wide-event HTTP wrapper emits after the wrapped handler and currently accepts no handler-derived attributes. Extend it with a request-scoped attribute collector:

- the wrapper creates the collector and places it in the request context;
- handlers add only validated bounded identity attributes;
- the wrapper reads the collector when emitting the completion event;
- raw values never enter the collector;
- the collector is request-scoped and is not retained after the response.

Keep the existing `HTTPHandler` behavior for callers that do not add attributes. Preserve exactly one completion event per recognized flow, including panic handling.

Add identity attributes to:

- `oauth.client_registration.completed`;
- `oauth.authorization.completed`;
- `oauth.github_callback.completed`;
- `oauth.token_exchange.completed`;
- `oauth.refresh.completed`.

The first-party dashboard OAuth client maps directly to `starlogz_ui` with `first_party` confidence.

### Token propagation

Propagate only the bounded OAuth-derived classification in Starlogz-issued access tokens so stateless MCP tool calls can reuse it.

Add private claims:

- `slz_client_product`;
- `slz_client_product_major`, when present;
- `slz_client_identity_confidence`.

Requirements:

- `IssueJWT` accepts a `clientclass.Classification` and writes only validated values.
- Authorization-code exchange derives the classification from the registered client before issuance.
- Refresh rotation and grace retries re-derive it from the registered client before issuing a token.
- `VerifyJWT` treats the claims as optional for backward compatibility.
- Missing or invalid identity claims map to `other`; they do not invalidate an otherwise valid token.
- `VerifyJWT` copies validated values into `auth.TokenInfo.Extra` for MCP handlers.
- Never include raw client names, user-agent values, client IDs, or redirect information in these claims.

Existing access tokens continue to work and report `other` until the client refreshes or reauthorizes.

## HTTP access logs

Retain the existing browser fields:

- `client_kind`;
- `client_family`;
- `client_major`;
- `os_family`;
- `device_class`.

In the access middleware, pass `r.UserAgent()` directly to `clientclass.FromUserAgent` and append the four derived product fields. Do not expose protocol-derived identity in access logs; the wide events are authoritative for OAuth and MCP product reporting.

The raw header must remain prohibited by `internal/logattr`. Tests must continue to prove that recognizable and unknown raw values are absent from encoded output.

## Wide-event contract

Keep envelope `schema_version=1`; the new event and optional bounded attributes are additive. Update `spec/events.md` with:

- the two new event names;
- approved identity attribute names and values;
- per-event required and optional attributes;
- the statement that identity is self-reported unless `first_party`;
- CloudWatch query examples.

Update wide-event validation so:

- only approved event names accept identity attributes;
- every identity value is selected from an allowlist;
- `client_product_major` is a canonical decimal string from `0` through `999`;
- a recognized product requires a non-`unknown` source and confidence;
- `other` may use `unknown`;
- arbitrary attribute names and values still reject the whole event before publishing.

## Privacy requirements

Never emit or persist for observability:

- raw `User-Agent` headers;
- raw MCP client name, title, version, URL, icons, or capabilities;
- raw OAuth client name in logs or events;
- client-name or user-agent hashes;
- stable fingerprints derived from unknown clients;
- full product versions;
- rules generated automatically from production values.

Raw user-agent and MCP implementation values may exist only in request memory long enough to classify them. OAuth `client_name` remains in the existing OAuth client table because it is registration metadata used by the authorization server; this design does not copy it into telemetry storage.

The existing privacy handler remains a defense-in-depth control. Add prohibited keys for plausible aliases such as `mcp_client_name`, `oauth_client_name`, and `raw_client_product` if they are not already blocked by suffix rules.

## Registry maintenance

Use this workflow when adding or changing a client rule:

1. Reproduce the client against a local or dev Starlogz server owned by the team.
2. Inspect its MCP metadata and user agent only in that controlled session.
3. Prefer an exact MCP or OAuth protocol name over a user-agent rule.
4. Reduce a user-agent sample to the smallest anchored product signature and synthetic fixture.
5. Add or update the product constant, rule, and table-driven tests in one PR.
6. Verify existing fixtures do not change classification unintentionally.
7. Deploy and compare `other` rates by route and event source.

Production telemetry can reveal that unknown traffic exists, but cannot reveal its product without retaining identifying data. Do not weaken the privacy model to automate that discovery. Use controlled compatibility testing or vendor documentation instead.

## Implementation phases

### Phase 1: classifier and access-log fallback

- Add `internal/clientclass` and its tests.
- Add verified first-party and controlled-client rules.
- Enrich access logs with bounded product fields.
- Extend privacy tests for known and unknown raw values.

### Phase 2: wide-event schema

- Add identity attribute constants and validation.
- Add `mcp.client_initialized` and `oauth.client_registration.completed`.
- Add the request-scoped HTTP event attribute collector.
- Update event unit tests and `spec/events.md`.

### Phase 3: OAuth propagation

- Classify registered OAuth clients in each recognized flow.
- Add bounded private JWT claims.
- Read validated claims into `auth.TokenInfo.Extra`.
- Cover authorization-code, refresh rotation, grace retry, old-token, and lookup-failure cases.

### Phase 4: MCP events

- Register receiving middleware for initialization events.
- Enrich `mcp.tool_call.completed` from token classification with user-agent fallback.
- Add stateless HTTP integration tests proving identity survives through OAuth token claims rather than MCP session memory.

### Phase 5: deployment verification

- Deploy to `starlogz-dev`.
- Exercise the first-party UI, MCP Inspector, and each verified agent client.
- Confirm no raw fields appear in Lambda logs, EventBridge events, Sentry, or OTLP output.
- Confirm old access tokens still authorize and classify as `other`.
- Compare product counts with aggregate request and tool-call counts.
- Review `other` rates before adding further rules.

## Testing

### Classifier tests

- exact protocol-name matches;
- case and whitespace normalization;
- anchored user-agent matches;
- malformed and out-of-range versions;
- overlapping-rule detection;
- unknown inputs;
- proof that output never contains input substrings.

### OAuth tests

- DCR emits bounded registration identity;
- authorization, callback, exchange, refresh, and grace retry use the registered client classification;
- first-party dashboard classification;
- optional JWT claims round-trip through `VerifyJWT` and `TokenInfo.Extra`;
- old JWTs without claims remain valid;
- invalid optional identity claims map to `other`;
- lookup failure does not alter OAuth responses;
- raw client names and user agents are absent from captured logs and events.

### MCP tests

- initialize middleware reads typed client information and emits one bounded event;
- initialization failure emits one failure event;
- tool events prefer verified token classification;
- tool events fall back to an approved user-agent signature;
- unknown tool clients emit `other`;
- stateless non-initialize requests do not assume `InitializeParams.ClientInfo` is retained;
- raw MCP names, versions, and headers are absent from logs and events.

### Wide-event tests

- approved identity attributes validate on the intended event names;
- invalid products, sources, confidence values, majors, and extra attributes reject publication;
- panic paths still emit exactly one failure event and repanic;
- identity attributes coexist with tool and result-count attributes.

Run:

```bash
mise exec -- go test ./internal/clientclass ./internal/middleware ./internal/wideevent ./internal/oidc ./internal/server
mise exec -- go test ./...
```

## CloudWatch verification queries

Count tool calls by product:

```text
fields detail.attributes.client_product, detail.attributes.tool
| filter detail.event_name = "mcp.tool_call.completed"
| stats count() as calls by detail.attributes.client_product, detail.attributes.tool
| sort calls desc
```

Measure unknown classifications by source:

```text
fields detail.event_name, detail.attributes.client_identity_source
| filter detail.attributes.client_product = "other"
| stats count() as events by detail.event_name, detail.attributes.client_identity_source
| sort events desc
```

Verify access-log product coverage without reading raw headers:

```text
fields client_product, client_identity_source, route
| filter msg = "http_request"
| stats count() as requests by client_product, client_identity_source, route
| sort requests desc
```

## Acceptance criteria

- Every `http_request` event contains bounded product classification fields.
- Every `mcp.client_initialized` event contains bounded product classification fields.
- Every `mcp.tool_call.completed` event contains bounded product classification fields.
- Recognized OAuth completion events contain bounded identity when a client can be resolved and `other` otherwise.
- No raw identity inputs or fingerprints appear in application logs or wide events.
- Old JWTs remain valid.
- The MCP transport remains stateless.
- Adding a client product requires only a registry rule, fixtures, tests, and a normal deployment.

## References

- [MCP lifecycle and `clientInfo`](https://modelcontextprotocol.io/specification/2025-11-25/basic/lifecycle)
- [OAuth 2.0 Dynamic Client Registration, RFC 7591](https://www.rfc-editor.org/rfc/rfc7591.html)
- [AWS logging and wide-event uplift](observability_uplift.md)
- [Wide-event contract](events.md)
