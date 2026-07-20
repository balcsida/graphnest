# GrepNest Web UI Design

**Date:** 2026-07-20
**Status:** Approved

## Goal

Add a fast, keyboard-friendly code-search console to the existing GrepNest
server. The page's single job is to search authorized repositories and make
matches easy to scan and open at the indexed revision.

The audience is engineers searching private GitHub or GHES repositories. The
interface borrows Sourcegraph's query-first workflow and GitHub's code-reading
conventions without copying either product's branding.

## Chosen Approach

Serve one embedded HTML document from the Go server. Its CSS and JavaScript are
inline, so the first usable screen needs one request and no frontend build,
package manager, framework, font download, or separate runtime.

The alternatives were:

1. an embedded static document — selected because it adds no runtime or build
   dependency and is the shortest path to first-class startup performance;
2. an Astro static build — rejected because this single interactive screen
   gains little from a component compiler while adding a Node toolchain; and
3. server-rendered HTML for every search — rejected because same-origin REST
   APIs already exist and full-page navigation would make repeated searches
   slower.

## Page Structure

The selected direction is a dense search console:

- a compact top bar with the GrepNest wordmark, persistent query input,
  repository picker, search action, and keyboard hint;
- a narrow context rail showing result count, active repository count, and
  concise query help;
- a main results column grouped by repository and path;
- code previews with line numbers, match emphasis, and links to the repository
  host at the indexed SHA and line; and
- responsive collapse to a single column on narrow screens.

The initial state explains `file:` and `case:` query terms with real examples.
The empty state gives a concrete next query. Errors state what happened and
whether retrying is useful.

## Visual System

The palette is deliberately closer to engineering instruments than a generic
dark dashboard:

- **Night navy `#132238`** — top command strip;
- **Paper blue `#F4F7FB`** — page canvas;
- **Panel white `#FFFFFF`** — result surfaces;
- **Steel `#66758A`** — secondary labels;
- **Signal blue `#2F6FEB`** — focus and links;
- **Match amber `#FFE08A`** — search-hit emphasis.

The interface uses the native system UI stack for controls and `ui-monospace`
for queries, paths, and code. Avoiding downloaded fonts is an explicit
performance choice. A nested command strip is the signature: repository scope
and keyboard affordances sit inside the same visual frame as the query, making
the search boundary immediately legible.

Motion is limited to one short result reveal and hover/focus transitions.
`prefers-reduced-motion` removes the reveal. Rounded corners are restrained and
the information density stays closer to Sourcegraph and GitHub than to a card
dashboard.

## Authentication and Data Flow

When no credential exists, the page shows a compact bearer-token gate. The
token is kept only in `sessionStorage`, survives refreshes in the same tab, and
is removed when the tab closes. It is never placed in URLs, logs, HTML, or
`localStorage`.

After authentication:

1. `GET /v1/repositories` populates the authorized repository picker.
2. Enter or Command/Ctrl+Enter submits `POST /v1/search` with the raw query and
   selected repository names.
3. A new search aborts the previous fetch.
4. Matches are grouped by repository and path in arrival order.
5. Match links use the result's `web_url`, indexed SHA, path, and line number.

Repository selection remains separate from the raw Zoekt query. This avoids a
client-side query parser and preserves advanced syntax unchanged. Searches are
explicit rather than search-as-you-type, preventing unnecessary backend work.

## Performance Budget

- One embedded HTML response for the complete application shell.
- No third-party JavaScript, CSS, fonts, icons, or frontend runtime.
- No client router, service worker, speculative prefetching, or hydration.
- At most the server's bounded 100 matches are rendered.
- DOM construction uses document fragments and one delegated result listener.
- An `AbortController` cancels stale searches.
- The server sends `Cache-Control: no-store` because the document handles a
  session credential and should update with the binary.

The implementation should remain small enough that minification is unnecessary.
Measure the final embedded document and keep it below 40 KiB uncompressed; add a
build/minification step only if measured transfer or parse cost becomes material.

## Accessibility

The page uses a real form, labels, buttons, checkbox-backed repository choices,
semantic headings, and an `aria-live` status region. Keyboard focus is visible,
result links have descriptive text, and color is never the only status signal.
Touch targets remain usable on narrow screens. Reduced-motion and high-contrast
preferences are respected.

## Failure Behavior

- `401` clears the rejected session token and returns focus to the token gate.
- Invalid queries keep the entered query visible and show the server's safe
  error message.
- Retryable errors expose one Retry action for the same request.
- Repository-list failure leaves manual query search available across all
  authorized repositories.
- An aborted stale request produces no error notice.
- Empty and truncated results are explicitly distinguished.

The UI never duplicates authorization decisions. The server remains the source
of truth for repository access and response limits.

## Server Integration

Add a small `internal/webui` package that embeds the document and returns an
`http.Handler`. Mount it at exact paths `/` and `/index.html`; API, MCP,
webhook, health, readiness, and metrics routes remain unchanged. Unknown paths
return the existing server's normal not-found behavior rather than an SPA
fallback.

The page uses same-origin relative API URLs. No CORS configuration is added.

## Testing

Use TDD for the server integration:

1. a handler test first proves exact routes, methods, content type, cache policy,
   security headers, and the presence of the application shell;
2. a server test first proves `/` is mounted without intercepting existing or
   unknown routes; and
3. focused browser verification exercises token persistence, repository loading,
   search submission, cancellation, empty/error states, outbound links,
   responsive layout, keyboard use, and reduced motion.

The existing Go suite and build remain regression gates. Sandbox-only IPv6
`httptest` binding failures are reported separately from Web UI failures.

## Completion Criteria

The feature is complete when the embedded console loads from the Go server,
authenticates with a session-scoped token, lists authorized repositories,
searches and groups results, links matches to the indexed revision, handles the
documented failure states, works by keyboard and on mobile widths, stays within
the 40 KiB document budget, and passes its focused tests plus the available
repository gates.
