# Dashboard uplift: routing, layout, and structure

> Status: Implemented decision
> Last reviewed: 2026-10-24
> Authority: Historical design rationale and outcome; current contracts, code,
> and tests define implementation behavior.

## Outcome

Implemented in PR #195 (`608bb8b`). Current behavior is authoritative in:

- [Dashboard application](dashboard.md) for routes, URL compatibility, server
  routing, session handling, the detail drawer, and asset delivery; and
- [Linked insights](insight_links.md) for rendered link hrefs and the
  delegated click bridge.

## Problem

The dashboard grew from one page into an insights view, a detail modal, and an
operator view without a routing or application-shell layer:

- **One orchestrating component.** It owned every query for every view, picked
  the view from `window.location.pathname`, and rendered the top bar in four
  branches.
- **State outside the URL.** Project and detail state lived in hand-managed
  query parameters, and search and tag filter were React state only.
- **Unreachable insights.** Rows were not clickable, so insights with no
  inbound links, and keyless insights, could not be opened from the list.
- **Detail modal focus defect.** After in-modal navigation, focus fell to
  `<body>` and Escape stopped closing the modal.
- **Tooling and delivery gaps.**
  - No typecheck.
  - Duplicated CSS selectors and no dark theme.
  - One unhashed bundle shipped d3 to every user.
  - A catch-all `/` route returned 200 for unknown paths.

## Decision

- **TanStack Router.** Chosen for its integration with the existing TanStack
  Query client, typed path and search parameters, and lazy routes. Route
  definitions are code-based, so there is no file-based codegen to maintain.
- **Path URLs with search state in the URL.** These make every view linkable
  and give Back/Forward coherent behavior. Legacy query-parameter URLs redirect
  permanently.
- **Detail as a drawer over a mounted list.**
  - The list route has no path segment of its own, and the detail routes are
    its children. A `/` index route cannot have children, and this structure
    keeps the list and its search state rendered beneath the drawer.
  - The native `<dialog>` replaced the hand-written focus trap. Its `cancel`
    event is independent of focus, which fixes the Escape defect at its source.
- **Server-rendered Markdown unchanged.** Only the link href changed, to an
  absolute path route. React keeps the delegated click bridge rather than
  rendering router components inside server HTML, preserving the rendering
  decision in [insight_links.md](insight_links.md).
- **Plain string search parsing.** The router's default parser coerces values
  like `q=123` to numbers, which typed validation then dropped.
- **Hashed, code-split assets with a generated manifest.** This enables
  immutable caching and keeps operator-only dependencies out of the main
  bundle. The server fails at startup rather than serving a page without
  scripts.
- **Login only on `Unauthenticated`.** Infrastructure failures no longer look
  like a signed-out session.

## Tradeoffs

- **Lazy chunks across deploys.** Hashed files are replaced on deploy, so a tab
  left open across a deploy can fail to load the lazy operations chunk until it
  is refreshed. This was accepted for an operator-only view.
- **No API changes.** Category and source filtering, a server-provided plain
  snippet, and a total distinct-tag count were deferred because each needs a
  Connect API change. The list clamps rendered HTML instead of showing a
  snippet, and the overview drops the low-value count tiles.
- **Revocation refresh.** Operations mutations invalidate cached queries rather
  than refetching manually, so the table refreshes before the confirmation
  dialog closes.

## Related contracts

- [Dashboard application](dashboard.md)
- [Linked insights](insight_links.md)
- [Dashboard operations view](dashboard_operations.md)
- [Dashboard operations controls](dashboard_operations_controls.md)
