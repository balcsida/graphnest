# GrepNest Search Workspace Redesign

**Date:** 2026-07-20
**Status:** Draft for written-spec review

## Goal

Replace the unstable card layout with a dense, predictable code-search
workspace inspired by Sourcegraph's information hierarchy. Preserve GrepNest's
identity, dependency-free delivery, same-origin APIs, session-scoped bearer
token, and sub-40 KiB embedded document.

The page has one job: help engineers search authorized repositories, scan code
matches, and open a result at its indexed revision.

## Diagnosed Failures

The redesign must fix these confirmed root causes rather than only restyle the
page:

- author display rules override the browser's native `[hidden]` behavior, so
  authentication and workspace states render together;
- the mobile search form's ID-level flex rule defeats the media-query layout;
- grid and code min-content widths create page-level horizontal overflow;
- code-result screenshots can race the reveal animation; and
- current tests inspect source strings but do not verify rendered geometry.

## Chosen Direction

Use one stable shell with this hierarchy:

```text
application bar
search strip
summary row
context rail | repository groups
             | file header
             | line-number gutter + code preview
```

This transfers Sourcegraph's dense, predictable geometry without copying its
branding, navigation, colors, or full product chrome.

Rejected alternatives:

1. a single-column results feed, because it loses persistent query context and
   is slower to scan on wide screens; and
2. a larger multi-page application shell, because GrepNest has no additional
   destinations that justify tabs or global navigation.

## Application Shell

The top bar is 52 pixels tall on desktop and contains the GrepNest wordmark and
session action. The search strip sits immediately below it. Its query field is
the dominant control, with repository scope and the Search action adjacent.

Before authentication, the shell remains visible but the search controls are
inactive. A compact, maximum-400-pixel bearer-token panel appears directly
below the top bar. It explains that the token is retained only for the current
browser session. Authentication hides this panel completely and reveals the
search workspace; signing out reverses those states.

The `hidden` attribute is authoritative through a global
`[hidden]{display:none!important}` rule. No component-level display rule may
override it.

## Search Workspace

Desktop widths use a 232-pixel context rail and a flexible
`minmax(0, 1fr)` results column. The rail shows match count, repository count,
query examples, and repository scope. It does not become a decorative card.

The results column begins with a compact summary row. Repository results are
grouped in arrival order. Each repository group contains file sections:

- a muted file header with repository and path;
- a fixed-width line-number gutter;
- a monospace code viewport with approximately 20-pixel line rhythm; and
- one compact indexed-source link in the file header.

File sections use thin borders and neutral surfaces rather than floating cards,
large shadows, or large gaps. Match emphasis remains warm amber. Code never
wraps merely to fit the viewport; horizontal scrolling belongs only to the code
viewport.

## Responsive Layout

At widths below 760 pixels:

- the permanent context rail collapses into a compact summary block above the
  results;
- the search strip becomes an explicit two-row grid: query on the first row,
  repository scope and Search action on the second;
- every flex and grid child that contains results has `min-width:0`;
- repository and path labels may wrap or truncate, while code retains its line
  structure; and
- the document itself has no horizontal overflow at 320 or 390 pixels.

The compact token panel uses the same shell and remains within 20 pixels of
both viewport edges.

## Visual System

Keep the existing performance-minded system and monospace font stacks. Use a
quiet engineering palette:

- **Ink `#172033`** for primary text and the wordmark;
- **Canvas `#F6F8FA`** for the page background;
- **Surface `#FFFFFF`** for controls and code bodies;
- **Border `#D8DEE8`** for structure;
- **Signal `#2563EB`** for actions, links, and focus; and
- **Match `#FFE08A`** for code-hit emphasis.

The distinctive element is the rigid file-and-line geometry: repository and
path context stay attached to contiguous source lines. Spacing and borders
carry the hierarchy. Animation is removed from result opacity so content is
immediately stable; reduced-motion remains supported.

## Behavior and Data Flow

Existing behavior remains authoritative:

1. the bearer token is stored only in `sessionStorage`;
2. `GET /v1/repositories` populates authorized repository scope when
   available;
3. explicit form submission sends the raw query to `POST /v1/search`;
4. new searches abort stale requests;
5. results remain bounded and grouped by repository and path; and
6. links target the repository host at indexed SHA, path, and line.

No new endpoint, client-side authorization rule, router, framework, font,
icon package, or build pipeline is introduced.

## Accessibility

All controls retain programmatic labels, 44-pixel touch targets where users
tap, visible focus, semantic forms and headings, and live status messages.
Color is not the sole status signal. Source code remains readable with browser
zoom, forced colors, and keyboard-only navigation.

## Testing

Use TDD to replace brittle visual string checks with layout invariants where
possible. Browser verification must prove:

- unauthenticated search/workspace content is not visible;
- authenticated token content is not visible;
- signing out restores only the token panel;
- `document.documentElement.scrollWidth === clientWidth` at 320, 390, 768,
  and 1440 pixels;
- search controls stay inside the viewport without overlap;
- the results column starts immediately below the shell;
- long code scrolls inside its code viewport without widening the page; and
- desktop and mobile screenshots are captured only after stable rendering.

Focused Go tests continue to prove security headers, exact routing, credential
lifecycle, safe DOM rendering, and the 40 KiB document budget. The full Go
suite, race tests, static analysis, vulnerability scan, and build remain final
gates.

## Completion Criteria

The redesign is complete when authentication and workspace states are mutually
exclusive, search geometry remains stable from 320 pixels through desktop,
real fixture results read as grouped code rather than cards, the page never
scrolls horizontally, all existing behavior and security contracts pass, and
fresh desktop and mobile screenshots show the settled UI.
