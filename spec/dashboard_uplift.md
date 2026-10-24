# Dashboard uplift: routing, layout, and structure

> Status: Proposed
> Last reviewed: 2026-10-24
> Authority: Proposed design; not an implementation commitment until accepted.
> Current behavior remains defined by [insight_links.md](insight_links.md),
> [dashboard_operations.md](dashboard_operations.md), and current code.

## Problem

The dashboard grew from one page into an insights view, an insight detail
modal, and an operator view without a routing or application-shell layer:

- `DashboardView` owns every query and mutation for every view, selects the
  view from `window.location.pathname`, and renders `TopBar` in four branches.
- Project and detail state live in query parameters through a hand-rolled
  `pushState`/`popstate` hook. Search text and tag filter are React state only,
  so results cannot be linked, bookmarked, or restored with Back.
- Insight rows are not clickable. Detail opens only through insight links
  inside rendered content, so unlinked and keyless insights are unreachable.
- Summary tiles and charts precede the insight list, pushing the primary task
  below the fold. Category and source counts carry little information, and the
  tag count is capped by the `ListTags` limit.
- Search issues a request on every keystroke.
- `bun build` strips types without checking them; there is no `tsconfig.json`
  and CI does not typecheck.
- One 1,150-line stylesheet repeats about a dozen selectors, and there is no
  dark theme.
- The single unhashed 410 KB bundle ships d3 and the operations view to every
  user.
- The server's `/` handler is a catch-all, so unknown paths return 200 with an
  empty shell.

## Decision

### Router

Adopt TanStack Router with code-based route definitions. It integrates with the
existing TanStack Query client, validates typed path and search parameters, and
supports lazy route components for code splitting. The hand-rolled
`useDashboardNavigation` hook and `dashboardURL`/`readDashboardLocation`
helpers are removed.

Each route owns its queries and its loading and error states. A root route
loads the session once and renders a single application shell.

### Routes

| Path | View |
|------|------|
| `/dashboard` | Redirects (replace) to the most recently used project if it still exists, otherwise the first project, otherwise an empty state. |
| `/dashboard/$project` | Insight list. Search params: `q` (search text), `tag` (tag filter). |
| `/dashboard/$project/insights/$key` | Insight list with the detail drawer open for the insight with that key. |
| `/dashboard/$project/insights/by-id/$id` | Insight list with the detail drawer open for a keyless insight. |
| `/dashboard/$project/overview` | Project summary: total insights, category, source, and tag distributions, recent activity. |
| `/admin/operations` | Operations view; unchanged content and authorization boundary. |

The detail routes are children of the list route, so the list and its `q` and
`tag` search params remain rendered and preserved beneath the drawer. Closing
the drawer navigates to the parent route with search params retained.

Path parameters are encoded per segment, so keys containing `/`, `?`, `#`, or
spaces round-trip. A key segment cannot collide with `by-id/$id`, which has a
different depth.

The most recently used project slug is a per-browser convenience stored in
`localStorage`; read and write failures fall back to the first project.

### URL compatibility

Existing links use `/dashboard?project=<slug>[&insight_key=<key>|&insight_id=<id>]`.
The `/dashboard` route recognizes these parameters and redirects (replace) to
the equivalent path route. This compatibility redirect stays indefinitely,
because such URLs exist in bookmarks and in content agents have copied.

### Insight-link rendering

Server-rendered insight links change from a relative query href to an absolute
path:

```html
<a
  class="insight-link"
  href="/dashboard/starlogz/insights/project-workflow"
  data-starlogz-action="open-insight"
  data-insight-key="project-workflow"
>the project workflow</a>
```

The renderer path-escapes the project slug and key as single segments. Because
`rendered_html` is derived at read time, no stored content changes. The
`data-starlogz-action` contract, sanitizer allowlist, and delegated click
bridge are unchanged; the bridge navigates through the router instead of
`pushState`. Unmodified-primary-click interception rules are unchanged, so
modified clicks open the absolute href in a new tab correctly.

[insight_links.md](insight_links.md) is updated in the same change: the example
href and the "Detail state uses `project` plus either `insight_key` or
`insight_id`" rule are replaced by the path routes above.

### Server routing

- `/{$}` serves the landing page, with the existing signed-in redirect.
- `/dashboard` and `/dashboard/` subtree paths serve the application shell.
- `/admin/operations` serves the application shell.
- Any other unmatched path returns 404.

The client router renders a not-found view for unknown subpaths under served
prefixes.

### Layout

```
┌───────────────────────────────────────────────────────────────┐
│ starlogz   Insights  Operations*            avatar  name  Logout│
├──────────────┬────────────────────────────────────────────────┤
│ Project ▾    │ [ Search insights…            ]   List | Overview │
│              │                                                │
│ Tags         │  key · category · source · tags      updated   │
│  #auth   12  │  first lines of rendered content…              │
│  #ui      8  │ ────────────────────────────────────────────── │
│  #db      5  │  …                                             │
│  …           │                       [ Load more ]            │
└──────────────┴────────────────────────────────────────────────┘
                                   ┌──────────── detail drawer ─┐
                                   │ key            [Close]     │
                                   │ metadata, content, links,  │
                                   │ backlinks, history         │
                                   └────────────────────────────┘
```

- **Shell:** one top bar with primary navigation; operator navigation only when
  `is_operator` is true.
- **Left rail:** project selector and tag facets as toggle chips with counts
  from `ListTags`. Selecting a chip sets `tag`; selecting it again clears it.
  At narrow widths the rail collapses above the list.
- **List:** the insight list is the default project view. Each row shows key
  (or a short ID), category pill, source, tags, updated time, and rendered
  content clamped to three lines. The whole row is a link to its detail route;
  insight links inside content keep their own behavior. The heading shows
  "Insights" or "Search results" and a loaded-count indicator.
- **Overview:** the existing bucket charts and activity strip move here. The
  low-value category-count, source-count, and capped tag-count tiles are
  removed; total insights remains.
- **Search:** input is debounced (300 ms) before updating `q` with
  `replace` navigation, so typing does not create history entries.
- **Detail drawer:** a right-side drawer built on the native `<dialog>` element
  opened with `showModal()`, giving focus containment, Escape-to-close, an
  inert background, and focus return. This replaces the hand-written focus
  trap. Content, relationships, history, and not-found behavior are unchanged.
- **Operations:** revoke confirmation moves into the shared dialog so it is
  visible from any table row. Mutations invalidate the operations query instead
  of calling `refetch()`.
- **Session errors:** only `Code.Unauthenticated` from `GetSession` renders the
  login view. Other failures render a retryable error state.
- **Theme:** colour tokens gain dark values under `prefers-color-scheme: dark`.

### Source structure

```
ui/
  main.tsx               entry: transport, query client, router provider
  router.tsx             route tree and typed search-param validation
  shell/                 AppShell, TopBar, LoginView, error and not-found views
  routes/
    insights/            list route, left rail, debounced search, row
    insight_detail/      drawer, relationships, history
    overview/            bucket list, activity strip
    operations/          telemetry chart, tables, revoke dialog (lazy)
  components/            Dialog, Panel, Metric, Pill, Tag, LoadMore, DataTable
  lib/                   time formatting, rendered markdown and click bridge
  styles/                tokens.css, base.css; feature CSS beside its components
```

Shared helpers are defined once; `timestampToDate` currently exists in two
files. Feature CSS is co-located and imported by its component, and duplicate
selectors are merged.

### Build and delivery

- Add a strict `tsconfig.json` and a `typecheck` script running
  `tsc --noEmit`. CI and lefthook run it alongside test and lint.
- Build with `bun build --splitting` and content-hashed output names. The
  operations route is lazy, so d3 loads only for operators.
- The build emits a manifest mapping entry points to hashed files. The Go page
  template reads the manifest from the embedded filesystem to emit script and
  stylesheet tags.
- Hashed assets are served with `Cache-Control: public, max-age=31536000,
  immutable`. The HTML shell is served with `Cache-Control: no-cache`.

## Non-goals

- No new Connect RPCs. Category and source filtering, a server-provided plain
  snippet, and a total distinct-tag count would each require API changes and
  are deferred.
- No session-authenticated insight writes from the dashboard. Detail and
  history remain read-only, as required by
  [insight_revisions.md](insight_revisions.md).
- No component library or CSS framework. Existing tokens and plain CSS remain.
- No server-side rendering.

## Security and privacy

- Authorization boundaries are unchanged; every route uses the existing
  session-authenticated Connect RPCs.
- Path parameters are treated as untrusted input and are only passed to
  Connect requests as values. They are never used as HTML.
- Search text, project slugs, and insight keys move into the URL path and
  search params, so they appear in browser history. Server access logs record
  only the matched mux pattern (for example `/dashboard/`), never the raw path
  or query string. Shell routes must be registered as prefix patterns rather
  than per-value patterns, so these values stay out of logs and wide events.
- `localStorage` holds only the last-used project slug.

## Delivery

Each phase is a separate PR that keeps existing UI tests passing or ports them.

1. **Foundation:** no visible change. Typecheck in CI and lefthook, source
   restructure, shared `lib/`, CSS de-duplication, and the native-dialog swap
   for the detail panel.
2. **Routing:** TanStack Router, path routes, URL-backed `q` and `tag`, legacy
   query-param redirect, insight-link href change with the `insight_links.md`
   contract update, and server route and 404 changes.
3. **Layout:** clickable rows, left rail with tag chips, Overview route,
   debounced search, and the right-side drawer.
4. **Polish:** dark theme, operations dialog and query invalidation,
   session-error handling, code splitting, hashed assets, and caching headers.

## Acceptance

- Every route above deep-links on a fresh page load, and Back/Forward restore
  list, search, tag, and drawer state.
- Legacy `?project=&insight_key=` and `?project=&insight_id=` URLs reach the
  same insight.
- Every live insight, including keyless ones, opens from the list.
- Insight links in rendered content, outgoing links, and backlinks open the
  drawer, and modified clicks open a new tab at a working URL.
- Keyboard-only users can open, traverse, and close the drawer, and focus
  returns to the originating row.
- Unknown paths return 404 from the server.
- Non-operators never download the operations chunk.
- `typecheck`, `test`, `lint`, `go test ./...`, and `proto:check` pass.

On completion, the routing, URL, and link-rendering parts are promoted into
current contracts, and this document is condensed into an implemented decision.

## Related contracts

- [Insight links](insight_links.md)
- [Insight revisions](insight_revisions.md)
- [Dashboard operations view](dashboard_operations.md)
- [Web UI sessions](web_sessions.md)
