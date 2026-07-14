# Linked insights

Status: proposed

## Implementation status

The initial backend slice implements the Goldmark insight-link AST extension,
migration 17, atomic relationship synchronization for content mutations, and
write warnings for unresolved and self-links.

The following surfaces remain pending: `insight_get` relationship reads and
backlinks, the Connect `GetInsight` RPC and `rendered_html` field, HTML
sanitization, and dashboard detail/deep-link navigation. Until those land,
links are stored structurally and reported during writes but cannot yet be
traversed through MCP, Connect, or the dashboard.

## Summary

Starlogz will support explicit, project-local links between insights using a
small wiki-link extension:

```text
This rollout implements [[insight:observability-uplift-plan]].
```

The server parses links into structured relationships so agents and humans can
navigate outgoing links and backlinks without reparsing every insight. Links
supplement full-text search; they do not change search ranking or automatically
expand search results.

The design deliberately starts with explicit links. Automatic graph generation,
typed relationships, cross-project links, and external references remain out of
scope until usage demonstrates a need.

## Motivation

Insights currently form a flat collection. Tags and full-text search find
individually relevant entries, but they do not express that one insight depends
on, implements, or should be read with another. Agents often compensate by
repeating prior context in new insights, which makes entries longer and harder
to maintain.

Research on linked and graph-based agent memory suggests that relationships can
improve multi-hop and temporal retrieval in richer, automatically constructed
memory systems. Those systems are not direct evidence for manually curated
wiki-links, and independent evaluation also shows that graph retrieval can add
cost and lose relevant context. Starlogz therefore treats explicit links as a
small, high-confidence retrieval signal to validate through product usage, not
as a replacement for search.

Relevant research:

- [A-MEM](https://arxiv.org/html/2502.12110) uses Zettelkasten-style linked
  memories and reports material gains on multi-hop retrieval.
- [Zep](https://arxiv.org/html/2501.13956) combines episodic and semantic graph
  memory for bounded long-term retrieval.
- [Cost and Accuracy of Long-Term Memory](https://arxiv.org/abs/2601.07978)
  shows that graph memory is not automatically more accurate or efficient than
  simpler retrieval systems.

## Goals

- Let insight authors express durable relationships using readable syntax.
- Preserve relationships structurally for reliable backlinks and traversal.
- Warn authors about unresolved references without rejecting useful content.
- Keep traversal explicit and bounded for predictable agent context use.
- Preserve existing insight, search, and tenancy behavior.
- Provide deep-linked dashboard navigation for humans reviewing insights.

## Non-goals

- Automatic link generation or LLM-generated relationship inference.
- Typed relationships such as `supports`, `supersedes`, or `depends_on`.
- Cross-project or cross-organization links.
- Links to keyless insights.
- GitHub pull request, commit, issue, build, or URL validation.
- Vector search, graph ranking, or automatic search-result expansion.
- Client-side Markdown parsing or hydration of React components inside rendered
  Markdown.
- Runtime installation of user-provided Markdown extensions.
- Persisting or caching rendered HTML.
- Rewriting existing insight content.

## Link syntax

### Forms

The supported forms are:

```text
[[insight:<key>]]
[[insight:<key>|<display label>]]
```

Examples:

```text
Follow [[insight:project-workflow]].
Follow [[insight:project-workflow|the project workflow]].
```

`insight:` is a required namespace. Bare `[[key]]` syntax is not supported;
the namespace prevents future ambiguity with projects, tags, users, and other
resource types.

### Parsing rules

The server parses insight content as CommonMark 0.31.2 using the pinned
`github.com/yuin/goldmark` dependency. A Starlogz Goldmark extension adds an
insight-link AST node, relationship extraction, and HTML rendering. The
Goldmark AST is authoritative for both stored relationships and dashboard
output; the dashboard does not parse Markdown.

The extension applies these link-specific rules:

1. Recognize the exact, case-sensitive prefix `[[insight:` and the next `]]`
   before the end of the current line. Split the enclosed body on its first
   `|`; additional `|` characters belong to the display label.
2. Trim ASCII space (`U+0020`) and horizontal tab (`U+0009`) from the target key
   and display label. The target key must be non-empty and cannot contain `|`,
   `]`, or a newline. A display label cannot contain `]` or a newline. An absent
   or empty trimmed label falls back to the trimmed target key.
3. Do not recognize insight links inside code spans, fenced or indented code
   blocks, raw HTML, or existing Markdown links and images. Insight links may
   appear inside emphasis and other ordinary inline containers.
4. Malformed or unclosed candidates remain literal text and create no
   relationship. Parsing continues so a later valid link can still be
   recognized.

Goldmark dependency upgrades require the extension fixture suite to pass before
merge. This prevents an upstream CommonMark behavior change from silently
changing relationship extraction.

The namespace and target-key lookup are case-sensitive. Repeated links to the
same key render at every occurrence but produce one stored relationship.
Warnings are likewise deduplicated by `(code, target_key)`. Authors can show
literal link syntax by placing it inside Markdown code.

These rules do not add new validation to the existing insight `key` field.
Keys that cannot be represented by the link grammar remain valid insight keys
but cannot be link targets in this version.

## Resolution model

A well-formed link resolves only when all of the following are true:

- the source insight is live;
- the target is a live insight;
- the target has the referenced key;
- the source and target belong to the same project.

Resolution uses the target key rather than a stored target insight ID. This
preserves the semantic relationship when a target is soft-deleted and later
recreated with the same key.

An unresolved link remains stored. It automatically resolves when a matching
live target appears and becomes unresolved again when that target is deleted.
No reconciliation job is required.

Any insight, including a keyless append-only entry, can be a link source. Only
keyed insights can be targets. A source linking to its own key produces a
`self_insight_link` warning and no stored relationship.

## Persistence

Migration 17 adds an `insight_links` table:

| Field | Type | Notes |
|---|---|---|
| `source_insight_id` | UUID | FK to `insights(id)` with hard-delete cascade |
| `target_key` | text | Project-local semantic target |
| `created_at` | timestamptz | Defaults to `now()` |

The primary key is `(source_insight_id, target_key)`. An index beginning with
`target_key` supports backlink lookup. The table uses the existing audit
mechanism.

The target project is derived by joining through the source insight. Target
resolution joins live insights on both `project_id` and `key`; a target in
another project or organization is indistinguishable from a missing target.
The table does not store `target_insight_id`, resolution state, labels, or
rendered content.

Outgoing relationships are derived data owned by insight content. The server
synchronizes them in the same transaction as every content mutation:

- inserting an insight;
- updating an existing keyed insight through `insight_write`;
- changing content through `insight_update`;
- importing insights.

Synchronization inserts new relationships, preserves unchanged relationships,
and removes relationships no longer present in content. Tag-only updates do not
touch links. A failed synchronization rolls back the content mutation.
Relationship extraction walks the same Starlogz Goldmark nodes used by the HTML
renderer; it does not maintain a separate link scanner.

Relationship reads have a deterministic order:

- outgoing links sort by `target_key COLLATE "C" ASC`;
- backlinks sort by source `updated_at DESC`, then source `id DESC`;
- warnings sort by `target_key COLLATE "C" ASC`, then `code ASC`.

Counts are computed before limits are applied. A truncated response always
returns the first entries in these orders. Content occurrence order is not part
of the relationship contract and is not stored.

Existing rows are not backfilled because the syntax was not previously part of
the content contract. Updating or keyed-upserting an older insight activates
link extraction for that content. This creates one deliberate compatibility
edge case: pre-existing content that already contains a valid wiki-link-shaped
string can render it as navigation, but `insight_get` will not report the
outgoing relationship or backlink until that source insight is rewritten. The
dashboard treats the target as resolved or not found only after navigation and
does not imply that the structured backlink exists. No migration rewrites
insight content or timestamps.

## MCP contract

### Write warnings

`insight_write` and content-changing `insight_update` responses gain an additive
`warnings` array:

```json
{
  "id": "019f...",
  "updated": false,
  "warnings": [
    {
      "code": "unresolved_insight_link",
      "target_key": "observability-uplift-plan"
    }
  ]
}
```

Supported warning codes are:

| Code | Meaning |
|---|---|
| `unresolved_insight_link` | No live target exists in the source project |
| `self_insight_link` | The source links to its own key; no edge was stored |

Warnings do not reject or roll back otherwise valid content. Empty warning
arrays are returned as `[]`. Tag-only updates return no link warnings.
Repeated occurrences produce at most one warning for each `(code, target_key)`.
Warnings use the deterministic order defined under Persistence.

Resolution checks reveal information only within the caller's project. A key
that exists elsewhere produces the same unresolved warning as a nonexistent
key.

### `insight_get`

A new read-scoped MCP tool retrieves one insight and its immediate
relationships. Input requires `project` and exactly one selector:

```json
{
  "project": "starlogz",
  "key": "observability-uplift-plan",
  "relation_limit": 50
}
```

or:

```json
{
  "project": "starlogz",
  "id": "019f...",
  "relation_limit": 50
}
```

Validation is identical for MCP and Connect:

- `project` must be a non-empty project slug;
- exactly one of `id` or `key` must be present and non-empty;
- `id`, when present, must be a valid UUID;
- `key` is matched exactly and is not trimmed;
- omitted `relation_limit` defaults to 50;
- a supplied `relation_limit` must be an integer from 1 through 100 inclusive.

Invalid selector combinations, malformed IDs, and out-of-range limits are
invalid requests rather than not-found results. `relation_limit` applies
independently to outgoing links and backlinks. Relationship totals are exact
before the limit, and each `*_truncated` field is true exactly when its total is
greater than the number returned.

The response shape is:

```json
{
  "insight": {
    "id": "019f...",
    "key": "observability-uplift-plan",
    "content": "...",
    "tags": ["starlogz", "workflow"],
    "category": "decision",
    "source": "user",
    "updated_at": "2026-07-13T21:07:43Z"
  },
  "links": [
    {
      "target_key": "project-workflow",
      "resolved": true,
      "id": "019f...",
      "category": "preference",
      "updated_at": "2026-07-11T12:37:30Z"
    },
    {
      "target_key": "missing-target",
      "resolved": false
    }
  ],
  "backlinks": [
    {
      "id": "019f...",
      "key": "observability-uplift-progress",
      "category": "context",
      "updated_at": "2026-07-13T22:21:04Z"
    }
  ],
  "link_count": 2,
  "backlink_count": 1,
  "links_truncated": false,
  "backlinks_truncated": false
}
```

Relationship summaries intentionally omit related insight content. Agents
traverse a selected reference with another `insight_get` call instead of
receiving an unbounded graph expansion.

The tool returns not found when the selected insight is absent, deleted, or
outside the requested project and caller's organization. It requires
`insights:read` and is included in the existing privacy-safe MCP completion
events. Events must not include insight IDs, keys, content, link targets, or
warning details.

### Existing read tools

`insight_search` and `insight_list` retain their existing input, output,
ranking, limits, and result counts. They do not automatically include linked
insights or relationship metadata.

The intended agent flow is:

```text
insight_search -> select relevant result -> insight_get -> traverse deliberately
```

## Server-side rendering

The server renders dashboard Markdown through Goldmark for every Connect
response that contains insights. Rendered HTML is derived from the current raw
content and is not stored in PostgreSQL. MCP tools, import, export, and future
editing flows continue to use raw content.

The Connect `Insight` message gains an additive `rendered_html` field. It is a
sanitized HTML fragment, not a complete document. `GetProjectDashboard`,
`ListInsights`, `SearchInsights`, and `GetInsight` populate it for every insight
they return. Rendering failure fails the Connect request rather than returning
raw or partially sanitized HTML.

Goldmark extensions are registered at server construction. The initial
Starlogz extension renders an insight link as a normal same-origin anchor with a
real deep-link target and a stable action contract:

```html
<a
  class="insight-link"
  href="?project=starlogz&amp;insight_key=project-workflow"
  data-starlogz-action="open-insight"
  data-insight-key="project-workflow"
>the project workflow</a>
```

The renderer URL-encodes project slugs and target keys, HTML-escapes labels and
attribute values, and never derives an arbitrary URL from wiki-link content.
Ordinary Markdown links remain ordinary sanitized anchors. Future server-owned
resource extensions may add new Goldmark nodes and `data-starlogz-action`
values, but user content cannot register renderers or action handlers.

Raw HTML rendering remains disabled. After Goldmark rendering, a narrow
allowlist sanitizer permits only the Markdown elements and attributes used by
the dashboard, including the two Starlogz data attributes above. It rejects
event-handler attributes, executable URL schemes, scripts, styles, embedded
objects, and unexpected Starlogz action values. The sanitizer is a mandatory
boundary even though Goldmark also uses safe renderer defaults.

## Dashboard contract

The Connect UI service gains a read-only `GetInsight` RPC with the same project,
selector, relation-limit, and tenancy semantics as the MCP tool. Generated Go
and TypeScript clients expose compact `InsightReference` messages for outgoing
links and backlinks. The request represents `id` and `key` as a protobuf
`oneof`, and `relation_limit` as an optional scalar so omission remains distinct
from an invalid zero value. The MCP JSON Schema uses `oneOf`, `minLength`, and
integer minimum/maximum constraints for the same contract.

The dashboard renders only the server-provided `rendered_html` fragment. A
single `RenderedMarkdown` React component owns `dangerouslySetInnerHTML` and
the action bridge; no other component may pass raw insight content to that API.
The component attaches one delegated click handler to its wrapper and maps
recognized `data-starlogz-action` values to local React functions.

For `open-insight`, the handler finds the closest action anchor, reads its
`data-insight-key`, and opens the detail panel using the current project. It
intercepts only an unmodified primary-button click. Keyboard activation and
modified, auxiliary, download, or explicitly targeted navigation retain normal
anchor behavior, so deep links remain accessible and can open in a new tab.
Unknown actions are not intercepted.

Clicking a link opens an insight detail panel. Detail state is encoded in the
URL using `project` plus either `insight_key` or `insight_id`, providing stable
deep links without adding a routing dependency. The dashboard must support:

- loading a detail panel from an initial URL;
- browser back and forward navigation;
- closing the panel by removing the insight selector;
- clearing the selector when the project changes;
- navigation through outgoing links and backlinks;
- visible unresolved-target and not-found states;
- relationship totals and truncation indicators.

The list and search tables remain unchanged apart from rendering supported
Markdown and wiki-link occurrences from `rendered_html`. For legacy rows that
predate link extraction, navigation can reach a target even though the source
has no stored relationship; the detail panel does not fabricate missing
outgoing links or backlinks.

## Agent authoring guidance

The Codex and Claude compound-knowledge skills will instruct agents to:

- create links only when the relationship materially improves future recall;
- prefer links to durable decisions, preferences, facts, and procedures;
- avoid linking every insight that merely shares a tag or topic;
- use Markdown only for readability, not for semantic metadata;
- use `insight_get` after search when linked context is relevant;
- treat unresolved warnings as review feedback rather than write failures.

External evidence such as pull requests, commits, builds, and deployment URLs
remains ordinary content until a separate structured-reference contract is
designed.

## Security and privacy

- Every lookup begins from the authenticated caller's organization and project.
- Link resolution never searches across projects or organizations.
- Missing, deleted, and inaccessible targets return indistinguishable results.
- Backlinks include only live source insights visible in the same project.
- Soft-deleted source insights do not appear in backlinks.
- Link keys, targets, labels, IDs, content, and warnings are excluded from wide
  events and access logs.
- Goldmark unsafe HTML remains disabled. Server-side escaping, generated
  same-origin navigation, and allowlist sanitization prevent insight content or
  labels from becoming executable HTML or arbitrary external URLs.
- React treats only the server-produced `rendered_html` field as trusted HTML.
  Raw insight content is never passed to `dangerouslySetInnerHTML`.
- The action bridge executes only fixed client functions selected by known
  `data-starlogz-action` values; the server never emits executable code.

## Compatibility and rollout

The database migration, MCP tool, Connect RPC, `rendered_html`, and response
warning fields are additive. Existing MCP clients retain the current raw-content
search and list behavior. Connect clients can ignore `rendered_html`; the
first-party dashboard switches from its limited client renderer to CommonMark
rendered by the server. This intentionally expands Markdown presentation but
does not modify stored content or MCP output.

The implementation is deployed through `mise exec -- bin/deploy`. Development
validation must cover:

1. resolved links and labelled rendering;
2. unresolved warning responses;
3. automatic resolution after the target is created;
4. transition back to unresolved after target deletion;
5. backlinks from keyed and keyless sources;
6. cross-project and cross-organization isolation;
7. bounded `insight_get` responses;
8. dashboard deep links and browser history;
9. safe server-rendered HTML and React action bridging;
10. privacy-safe completion events.

## Test requirements

### Goldmark extension and rendering

- plain and labelled links;
- multiple and duplicate links;
- empty keys and labels;
- malformed or unclosed syntax;
- code span, fenced-code, indented-code, and raw-HTML exclusions;
- Markdown link and image exclusions;
- links nested inside emphasis and other ordinary inline containers;
- CRLF input and malformed candidates followed by valid links;
- case-sensitive resolution;
- self-links;
- identical relationship targets from AST extraction and rendered anchors;
- label and attribute escaping, URL encoding, and generated deep links;
- sanitizer removal of raw HTML, event attributes, executable URLs, embedded
  content, and unknown Starlogz actions;
- rendering failures returning Connect errors without partial HTML;
- fuzz coverage for the custom extension and renderer.

The Go fixture corpus asserts the Goldmark AST nodes, deduplicated relationship
targets, warnings, and final sanitized HTML. Goldmark upgrades run the same
fixtures.

### PostgreSQL

- atomic insert, keyed upsert, update, and import synchronization;
- removal of obsolete relationships after content changes;
- tag-only updates preserving relationships;
- unresolved-to-resolved transitions without reconciliation writes;
- target soft deletion and recreation;
- keyless sources and keyed targets;
- duplicate suppression;
- no backfill for rows that predate migration 17, followed by activation on
  content mutation;
- transaction rollback;
- project and organization isolation.

PostgreSQL tests use the existing `testcontainers-go` infrastructure.

### MCP and Connect

- generated schemas and exactly-one selector validation;
- warning response compatibility;
- raw content remaining unchanged and `rendered_html` being additive;
- read and write scope enforcement;
- get-by-key and get-by-ID behavior;
- relation limits, totals, and truncation;
- deterministic outgoing, backlink, and warning ordering;
- empty, conflicting, malformed, and out-of-range request fields;
- missing and deleted targets;
- backlink visibility;
- cross-organization access rejection;
- bounded, content-free wide events.

### Dashboard

- rendering only server-provided HTML;
- delegated `open-insight` handling from nested click targets;
- ordinary primary clicks opening the detail panel;
- keyboard, modified, auxiliary, download, and targeted links retaining browser
  behavior;
- unknown action values not invoking client functions;
- legacy navigation without fabricated relationship metadata;
- deep-link initialization;
- panel navigation and close behavior;
- browser history handling;
- project changes clearing detail state;
- unresolved and not-found presentation;
- outgoing-link and backlink traversal.

Final verification runs:

```bash
mise exec -- bun install --frozen-lockfile
mise exec -- bun run proto:generate
mise exec -- bun run lint
mise exec -- bun run build
mise exec -- go test ./...
```
