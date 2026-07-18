# Linked insights

> Status: Current contract
> Last reviewed: 2026-07-18
> Authority: Behavioral, compatibility, and security contract; current code, migrations, and tests provide implementation evidence.

Starlogz supports explicit, project-local relationships between insights. Links
supplement full-text search; they do not change search ranking or automatically
expand search results.

## Scope

Linked insights provide readable authoring syntax, structured outgoing links and
backlinks, bounded agent traversal, and dashboard navigation. They deliberately
exclude automatic link inference, typed relationships, cross-project links,
external-reference validation, graph ranking, and vector search.

Any live insight can be a source, including a keyless append-only entry. Only a
live keyed insight in the same project can be a resolved target.

## Syntax and parsing

Supported forms are:

```text
[[insight:<key>]]
[[insight:<key>|<display label>]]
```

The `insight:` namespace and prefix are case-sensitive. Bare `[[key]]` syntax is
not supported. Target-key lookup is also case-sensitive.

The server parses content as CommonMark using the pinned Goldmark dependency and
a Starlogz inline extension. The same AST is authoritative for relationship
extraction and dashboard rendering.

Parsing rules:

1. A candidate begins with exact `[[insight:` and ends at the next `]]` on the
   same line.
2. The body is split on the first `|`. Additional `|` characters belong to the
   label.
3. ASCII spaces and tabs are trimmed from the key and label. The key must be
   non-empty. An empty or omitted label falls back to the key.
4. A `]` or newline inside the body makes the candidate invalid.
5. Candidates inside code spans, fenced or indented code, raw HTML, and existing
   Markdown links or images do not become insight links.
6. Malformed candidates remain literal text. Parsing continues so later valid
   links can still be recognized.

Repeated links render at every occurrence but produce one stored relationship
per target key. Literal syntax can be placed in Markdown code.

These rules do not add validation to the existing insight `key` field. A valid
key that cannot be represented by this grammar remains valid but cannot be a
link target.

## Resolution and persistence

Relationships store the source insight ID and semantic target key, not a target
insight ID or cached resolution state. Resolution joins a live target by exact
key and the source project. Therefore:

- a target in another project is indistinguishable from a missing target;
- an unresolved link resolves automatically when a matching target appears;
- deleting a target makes the relationship unresolved;
- recreating a live target with the same key restores resolution without
  rewriting the source.

A source linking to its own key produces `self_insight_link` and no stored
relationship.

Content-bearing writes synchronize relationships in the same transaction as the
insight write. This includes insert, keyed upsert, content update, and import.
A keyed upsert or content-supplied update also repairs missing relationship rows
when the persisted insight fields are a semantic no-op; this repair does not
change the insight timestamp or revision. Tag-only updates do not touch
relationships. A synchronization failure rolls back the write.

Ordering is deterministic:

- outgoing links: target key using PostgreSQL `C` collation ascending;
- backlinks: source `updated_at` descending, then source ID descending;
- warnings: target key ascending, then warning code ascending.

Counts are calculated before limits. A truncated response always returns the
first entries in these orders.

Migration 17 did not backfill existing content. A later keyed upsert or
content-supplied update activates extraction even when the persisted content is
identical. Until then, legacy content may render a navigable link before its
structured outgoing relationship or backlink exists. The dashboard must not
fabricate the missing relationship metadata.

## Write warnings

`insight_write` and content-changing `insight_update` responses include an
additive `warnings` array:

```json
{
  "id": "019f...",
  "updated": false,
  "warnings": [
    {
      "code": "unresolved_insight_link",
      "target_key": "project-workflow"
    }
  ]
}
```

Supported codes are:

| Code | Meaning |
|---|---|
| `unresolved_insight_link` | No live target exists in the source project. |
| `self_insight_link` | The source points to its own key; no edge is stored. |

Warnings do not reject otherwise valid content. Repeated occurrences are
deduplicated by warning code and target key. A no-op keyed upsert regenerates
warnings while repairing relationships. Tag-only and semantic no-op
`insight_update` responses omit link warnings.

## `insight_get`

The read-scoped MCP tool retrieves one insight with bounded immediate
relationships. It requires a project and exactly one non-empty selector:

```json
{
  "project": "starlogz",
  "key": "project-workflow",
  "relation_limit": 50
}
```

`id` is the alternative selector and must be a UUID. Keys are matched exactly
and are not trimmed. `relation_limit` defaults to 50 and must be from 1 through
100. It applies independently to outgoing links and backlinks.

The response contains:

- the selected insight;
- outgoing references with target key and resolution state;
- target ID, category, and update time for resolved references;
- backlink source ID, optional key, category, and update time;
- exact outgoing and backlink totals;
- independent truncation flags.

Related insight content is intentionally omitted. Agents traverse a selected
reference with another `insight_get` call rather than receiving an unbounded
graph expansion. Missing, deleted, and inaccessible insights return the same
not-found behavior.

`insight_search` and `insight_list` retain their ranking, limits, and raw
content. Cursor pagination is additive and does not expand or attach
relationship data. The intended traversal is:

```text
insight_search -> select result -> insight_get -> traverse deliberately
```

## Server rendering and dashboard behavior

Connect responses render insight Markdown to a sanitized HTML fragment at read
time. Rendered HTML is derived from raw content and is not stored. MCP, import,
export, and write flows continue to use raw content.

Insight links render as same-origin anchors with a stable action contract:

```html
<a
  class="insight-link"
  href="?project=starlogz&amp;insight_key=project-workflow"
  data-starlogz-action="open-insight"
  data-insight-key="project-workflow"
>the project workflow</a>
```

The renderer URL-encodes project slugs and keys and HTML-escapes labels and
attributes. Raw HTML remains disabled. A narrow allowlist sanitizer removes
scripts, styles, embedded objects, event attributes, executable URLs, and
unknown Starlogz actions.

The dashboard renders only server-provided `rendered_html`. One delegated event
bridge maps the fixed `open-insight` action to local React behavior. It intercepts
only unmodified primary-button clicks; keyboard, modified, auxiliary, download,
and targeted navigation retain browser behavior. Unknown actions are not
executed.

Detail state uses `project` plus either `insight_key` or `insight_id` in the URL.
Initial deep links, browser history, close behavior, project changes, outgoing
navigation, backlinks, unresolved targets, not-found results, totals, and
truncation must remain coherent.

## Security and privacy

- Every lookup begins within the authenticated caller's organization and
  project.
- Resolution and backlinks never cross project or organization boundaries.
- Missing, deleted, and inaccessible targets are indistinguishable.
- Backlinks include only live source insights in the same project.
- Link keys, labels, IDs, warnings, and content are excluded from access logs
  and wide events.
- User content cannot register renderers or action handlers or derive arbitrary
  navigation URLs.
- Raw insight content is never passed to React's trusted-HTML boundary.

## Compatibility

Relationship storage, `insight_get`, Connect `GetInsight`, `rendered_html`, and
write warnings are additive. Existing MCP clients continue to receive raw
content from list and search operations. Stored insight content and timestamps
are never rewritten solely to create relationships.
