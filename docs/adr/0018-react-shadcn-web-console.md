# ADR-0018: React and shadcn/ui Web Console

- Status: Accepted
- Date: 2026-10-04

## Context

The embedded console was three hand-written single-file pages (`index.html`,
`admin.html`, `supply-chain.html`), each with inline script and style blocks, a
40 KiB document budget, no framework, and a hash-based Content-Security-Policy
that had to be recomputed whenever a page changed. The feature surface kept
growing: search with SCIP navigation, administration, account tokens, and the
supply-chain inventory. Directors also need views that the page style cannot
reasonably deliver: license charts across repositories and a graph of which
repositories share which external dependencies. This supersedes the "no
framework, no build pipeline" rules of the earlier Web UI redesign.

## Decision

The console is one React 19 single-page application built with Vite 7,
Tailwind CSS v4, and shadcn/ui (Radix UI, Recharts, React Flow), written in
strict TypeScript under `web/`. The build is written to `internal/webui/dist`
and embedded into `graphnest-server` with `go:embed`.

`internal/webui` serves it with exact route patterns: `/`, `/index.html`,
`/repositories`, `/admin`, `/account`, `/supply-chain` (the last three also
with a trailing slash) return `index.html` with `Cache-Control: no-store`;
`/assets/` serves hashed files as immutable; everything else is 404. There is
no catch-all `GET /` pattern, because it would turn the 404 for unknown
non-GET paths into a 405. When the build is not embedded, the HTML routes
answer `503` so `go build` and `go test` keep working without Node.

The Content-Security-Policy keeps `script-src 'self'` with no inline script and
no `eval`, and allows `style-src 'self' 'unsafe-inline'`. Radix UI, Recharts,
and Sonner inject `<style>` elements at runtime, so a style hash or nonce is
not available. The relaxation is limited to styles; API-controlled text is
rendered as React text only, and a Vitest test fails if
`dangerouslySetInnerHTML` or `innerHTML` appears outside the chart component's
static style block.

Node 24 is required to run `make build`, `make test`, and to build the images.
The console adds no backend endpoints: every view reads the existing REST API
in `docs/openapi.yaml`, with the same authentication and session contract.

## Consequences

- The initial route loads about 475 KiB of JavaScript and 75 KiB of CSS
  (about 151 KiB and 13 KiB gzipped), and the search page adds about 17 KiB.
  Recharts and React Flow are split by route: the License dashboard chunk is
  about 380 KiB (112 KiB gzipped) and the Dependency graph chunk about 226 KiB
  (74 KiB gzipped), so search never loads them. Sizes come from the Vite build
  output in `web/` and move with dependency upgrades.
- The Node toolchain becomes a build input. CI sets up Node 24.10.0 and runs
  `make ui` before Go targets, `Dockerfile` builds the console in a pinned
  Node stage, and `Dockerfile.offline` copies the build context, so `make ui`
  must run before building it. Dependabot tracks `web/` npm dependencies.
- The 40 KiB page budget, the hash-based CSP, the Go contract tests that read
  the HTML sources, the fake-DOM tests, and the break-glass HTML marker
  stripping are removed. The SPA reads `break_glass` from
  `GET /v1/auth/config`, and `POST /auth/local` is already 404 when the feature
  is off. UI behavior is tested with Vitest and Testing Library against a
  stubbed `fetch`; the Playwright smoke test keeps covering the served build.
- The bearer token moves to `sessionStorage` under `graphnest_token`, with a
  one-time migration from `graphnest_admin_token`. It is never stored in
  `localStorage` or a cookie.
- A Go binary built without `make ui` answers the console routes with `503`.
