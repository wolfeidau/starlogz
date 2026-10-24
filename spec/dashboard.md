# Dashboard application

> Status: Current contract
> Last reviewed: 2026-10-24
> Authority: Behavioral, compatibility, and security contract; current code and tests provide implementation evidence.

The dashboard is one React application served by the Go server and backed by
the session-authenticated `starlogz.v1.UIService` Connect API. This document
covers its URLs, server routing, session handling, and asset delivery. Insight
link rendering is defined in [insight_links.md](insight_links.md); operator
views in [dashboard_operations.md](dashboard_operations.md).

## Routes

| Path | View |
|------|------|
| `/dashboard` | Redirects (replace) to the most recently used accessible project, otherwise the first project, otherwise an empty state. |
| `/dashboard/<project>` | Insight log (see below). Optional search params: `q` (search text) and `tag` (tag filter). |
| `/dashboard/<project>/insights/<key>` | Insight list with the detail drawer open for that key. |
| `/dashboard/<project>/insights/by-id/<id>` | Insight list with the detail drawer open for a keyless insight. |
| `/dashboard/<project>/overview` | Project summary and distributions. |
| `/admin/operations` | Operator view; see [dashboard_operations.md](dashboard_operations.md). |

- Path parameters are single escaped segments, so keys containing `/`, `?`,
  `#`, or spaces round-trip.
- `q` and `tag` are always strings, and they are preserved when the drawer
  opens, closes, or navigates between insights.
- Search input updates `q` with replace navigation after a short debounce, so
  typing does not create history entries.
- A project that is not in the caller's project list redirects to
  `/dashboard`.
- The most recently used project slug is a per-browser convenience in
  `localStorage`. Storage failures fall back to the first project.
- Unknown subpaths under served prefixes render a client-side not-found view.

## URL compatibility

`/dashboard?project=<slug>` redirects to `/dashboard/<slug>`. Adding
`insight_key=<key>` or `insight_id=<id>` redirects to the matching detail
route. These redirects are permanent compatibility behavior, because such URLs
exist in bookmarks and in content agents have copied.

## Server routing

- `/` (exact) serves the landing page and redirects to `/dashboard` when a
  valid web session exists.
- `/dashboard`, the `/dashboard/` subtree, and `/admin/operations` serve the
  application shell.
- Every other unmatched path returns 404.
- Shell routes are registered as prefix patterns. Access logs record only the
  matched mux pattern, so project slugs, insight keys, and search text never
  appear in server logs or wide events.
- Edge routing must forward the same shell paths. API Gateway routes
  (`infra/terraform/apigw.tf`) include `GET /dashboard/{proxy+}`; a missing
  edge route returns 404 before the request reaches the server.

## Session handling

- The login view is shown only when `GetSession` fails with `Unauthenticated`.
  The UI auth middleware returns HTTP 401 for missing, expired, or revoked
  sessions, and Connect maps that to `Unauthenticated`.
- Any other session failure renders a retryable error state and leaves the
  session cookie in place.

## Insight log

The insight list is a recency-first project log.

- List mode (no `q`) orders by `updated_at DESC` and groups consecutive entries
  under local-day headings: Today, Yesterday, then dated headings. Entries
  without a valid timestamp fall under an "Unknown date" heading.
- Search mode (non-blank `q`) keeps server relevance order and is not grouped.
- All displayed times use the browser's local zone and 24-hour format.

### New-since marker

A divider labelled "New since <time>" separates entries updated after the
caller's previous visit from older ones.

- The baseline is the project's last-visit time in `localStorage` under
  `starlogz.lastSeen.<project>`; each visit records the current time.
- The baseline is pinned in `sessionStorage` for the tab session, so
  navigating away and back (for example via Overview) keeps the divider in
  place. A new tab session reads the stored time again.
- The divider appears in list mode only, and only when the loaded list
  contains both newer and older entries. With no baseline, or when every
  loaded entry is on one side, no divider is shown.
- Storage failures disable the marker without affecting the list.

## Headings

Every page has exactly one `h1`, owned by the route or view (project title,
Operations, login wordmark). The top bar renders no heading.

## Detail drawer

Insight detail is a modal drawer on the native `<dialog>` element.

- Escape closes it regardless of where focus sits, including after in-drawer
  navigation has replaced the focused element.
- Closing it navigates to the list route with `q` and `tag` preserved.
- Focus returns to the list row of the insight being shown when that row is
  loaded, otherwise to the project title.
- Detail and history are read-only; see
  [insight_revisions.md](insight_revisions.md).

## Asset delivery

- `bun run build` writes code-split, content-hashed assets and a generated
  `internal/server/public/manifest.json` that names the entry script and
  every stylesheet. Generated assets and the manifest are never committed.
- The server embeds the build output and refuses to start without a valid
  manifest.
- The HTML shell and `manifest.json` are served with `Cache-Control:
  no-cache`. Hashed assets are served with `Cache-Control: public,
  max-age=31536000, immutable`; missing files are not marked cacheable.
- Fonts are self-hosted latin subsets (Bricolage Grotesque and JetBrains Mono
  variable fonts). The build emits them as content-hashed files under
  `internal/server/public/fonts/` and rewrites the stylesheet to reference
  them as `/public/fonts/<name>-<hash>.woff2`. They are served by the same
  public handler with immutable caching. They are not inlined as `data:` URIs,
  which CSP `font-src` (falling back to `default-src 'self'`) would block.
- Fonts are not listed in `manifest.json`. The hashed stylesheet names the
  hashed font files, so a font change changes the stylesheet name and the
  manifest comparison below still detects it.
- All stylesheets load up front. The operations route is loaded lazily, so
  its code and charting dependencies are not downloaded by other views.
- Verifying a deployment compares the deployed manifest's hashed file names
  with the build output.

## Related contracts

- [Linked insights](insight_links.md)
- [Insight revisions](insight_revisions.md)
- [Dashboard operations view](dashboard_operations.md)
- [Web UI sessions](web_sessions.md)
