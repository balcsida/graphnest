# GraphNest Web Console on React and shadcn/ui

**Date:** 2026-10-04
**Status:** Approved

## Goal

Replace the three hand-written single-file pages under `internal/webui/`
(`index.html`, `admin.html`, `supply-chain.html`) with one React single-page
application styled with shadcn/ui and Tailwind CSS, served from the same
embedded `graphnest-server` binary. Keep every behavior, security contract and
API interaction of the current pages, and add two views for directors:

- a **License dashboard** with charts of license usage across the onboarded
  repositories; and
- a **Dependency graph** showing the connections between onboarded
  repositories and their external dependencies.

This supersedes the "no framework, no build pipeline, 40 KiB document" rules of
`2026-07-20-web-ui-sourcegraph-redesign-design.md`. ADR-0018 records the
decision.

## Non-goals

- No new backend endpoints. Every view is built from the existing REST API in
  `docs/openapi.yaml`.
- No review-queue, policy or import screens (the legacy pages never had them).
- No server-side rendering, no client-side authorization rules, no analytics.

## Decisions

### Stack

| Concern | Decision |
| --- | --- |
| Build | Vite 7, React 19, TypeScript (strict), Node 24 |
| Styling | Tailwind CSS v4 via `@tailwindcss/vite`, shadcn/ui (`new-york` style, `neutral` base colour, CSS variables), Lucide icons |
| Routing | `react-router` v7 in library mode (`BrowserRouter`) |
| Server state | `@tanstack/react-query` v5 |
| Charts | shadcn `chart` component (Recharts) |
| Graph | `@xyflow/react` with `@dagrejs/dagre` left-to-right layout |
| Toasts | shadcn `sonner` |
| Tests | Vitest + `@testing-library/react` + `@testing-library/user-event` + `jsdom`; `fetch` is stubbed with a small local helper, no MSW |
| Lint | the Vite react-ts ESLint flat config; `tsc --noEmit` is part of `npm run check` |

Pin every npm dependency to an exact version (`save-exact=true` in
`web/.npmrc`), matching the root `package.json` convention. Do not add
dependencies beyond this table without recording why in the final report.

### Repository layout

```text
web/                      Vite project (package.json, vite.config.ts, src/)
web/src/api/              typed fetch functions + TypeScript types mirroring docs/openapi.yaml
web/src/lib/              api client, auth provider, theme, formatting helpers
web/src/components/ui/    shadcn components (generated, lightly patched)
web/src/components/       app shell (sidebar, header, theme toggle), auth gate, shared widgets
web/src/pages/<area>/     route components: search, repositories, admin, account, supply-chain
web/src/test/             Vitest setup and fetch stub helper
internal/webui/dist/      Vite build output, git-ignored except .gitkeep
internal/webui/handler.go Go handler serving the embedded build
```

`vite.config.ts` sets `build.outDir` to `../internal/webui/dist`, empties it on
build, and re-creates `.gitkeep` afterwards so `go:embed all:dist` always has a
match and `git status` stays clean after a build. The dev server proxies
`/v1`, `/auth`, `/healthz` and `/readyz` to `http://127.0.0.1:8080`.

Route-level code splitting with `React.lazy`: the search page must not load
Recharts or React Flow.

### Go handler

`internal/webui` embeds `all:dist` and registers:

- `GET /{$}`, `GET /index.html`, `GET /repositories`, `GET /admin`,
  `GET /admin/`, `GET /account`, `GET /account/`, `GET /supply-chain`,
  `GET /supply-chain/` → `dist/index.html` with `Cache-Control: no-store`;
- `GET /assets/` → the matching file under `dist/assets` with
  `Cache-Control: public, max-age=31536000, immutable`; `GET /favicon.svg` →
  the icon with `no-store`; directories are never listed;
- everything else → 404. There is no catch-all `GET /` pattern: one would turn
  the 404 for unknown non-GET paths (for example `POST /auth/local` when
  break-glass is off) into a 405.

Every response keeps the current security headers (`Cross-Origin-Opener-Policy`,
`Permissions-Policy`, `Referrer-Policy`, `X-Content-Type-Options`,
`X-Frame-Options`) and sends this Content-Security-Policy on HTML:

```text
default-src 'none'; base-uri 'none'; frame-ancestors 'none'; form-action 'self';
connect-src 'self'; img-src 'self' data:; font-src 'self'; script-src 'self';
style-src 'self' 'unsafe-inline'
```

`script-src` stays `'self'` only: no inline scripts, no `eval`. `style-src`
allows inline styles because Radix UI, Recharts and Sonner inject `<style>`
elements at runtime; the console still never renders API-controlled text
through HTML sinks. A Vitest test fails if `dangerouslySetInnerHTML` or
`innerHTML` appears under `web/src` outside `components/ui/chart.tsx` (whose
style block is built from static chart configuration only).

When `dist/index.html` is absent (Go built without `make ui`), the HTML routes
answer `503 Service Unavailable` with a plain-text "web console not built; run
make ui" body so `go build ./...` and `go test ./...` keep working without
Node. `webui.Built()` reports whether the build is embedded; tests that need
the real build skip with that message when it is missing.

`webui.RegisterWithBreakGlass` and the break-glass marker stripping are
removed: the SPA reads `break_glass` from `GET /v1/auth/config` and renders the
recovery form only when it is `true`, and `POST /auth/local` is already 404
when the feature is off. `cmd/graphnest-server` calls `webui.Register(mux)`.

### Build, CI, images

- `make ui` runs `npm ci` and `npm run build` in `web/` only when
  `web/src`, `web/package-lock.json` or the Vite/Tailwind config changed since
  the last build (Make prerequisites, no shell loops). `make ui-check` runs
  `npm run check` (ESLint, `tsc --noEmit`) and `npm test`. `make ui-dev` runs
  the Vite dev server.
- `make build`, `make test`, `make test-race`, `make server` and `make ui-smoke`
  depend on `ui`.
- CI: the `verify`, `integration`, `e2e` and `ui-smoke` jobs add the pinned
  `actions/setup-node` step (same SHA and `node-version: "24.10.0"` as the
  existing `ui-smoke` job, `cache: npm` with `cache-dependency-path:
  web/package-lock.json`) and run `make ui` before Go targets; `verify` also
  runs `make ui-check`.
- `Dockerfile`: a `node:24.10.0-bookworm-slim` stage pinned by digest builds
  `web/` and the Go builder copies `internal/webui/dist` from it before
  `go build`. `.dockerignore` adds `web/node_modules`.
- `.github/dependabot.yml` adds an `npm` entry for `/web` in the existing
  weekly group.
- `.gitignore` adds `web/node_modules/`, `web/dist/`, `web/coverage/` and
  `internal/webui/dist/*` with `!internal/webui/dist/.gitkeep`.

### Authentication and session (unchanged contract)

1. On load call `GET /v1/auth/config` → `{token_login, break_glass, file_reads,
   providers[]}`. Provider buttons link to each `login_url`; labels come from
   the response and are rendered as text.
2. Call `GET /v1/auth/session` with same-origin credentials. `200` means a
   browser session (`method` is `oidc`, `oauth` or `local`); the stored bearer
   token is then ignored. `401` means no session.
3. Without a session, a bearer token is sent as `Authorization: Bearer`. The
   legacy console kept it in memory only; the admin and supply-chain pages
   used `sessionStorage` key `graphnest_admin_token`. The SPA keeps it in
   `sessionStorage` under `graphnest_token` (so reloads and in-app navigation
   keep the sign-in) and migrates `graphnest_admin_token` on first load. Never
   `localStorage`, never a cookie.
4. The token gate shows the bearer form only when `token_login` is `true`, the
   provider buttons when providers exist, and the administrator recovery form
   only when `break_glass` is `true` (`POST /auth/local`, rotation through
   `POST /auth/local/rotate`, same semantics as the legacy form).
5. Sign-out with a session: `POST /auth/logout` must succeed before the UI
   drops to the token gate; on failure show the error and stay signed in. With
   a bearer token: remove it from `sessionStorage` and clear every
   principal-scoped state (repositories, results, file viewer, admin tables).
6. A `401` from any API call clears the bearer token and shows the gate. A
   `403` shows the error and keeps the credential, except that the admin
   console treats a `401`, `403` or `404` from `GET /v1/admin/overview` as "not
   an administrator" and locks itself with a message.
7. Repository names, results and inventory are never cached across principals.

### Rendering and link safety (unchanged contract)

- API-controlled text is rendered as React text, never through HTML sinks.
- Outbound repository links require `https://`, URL-encode the SHA and path,
  use `rel="noopener noreferrer"` and `target="_blank"`.
- Query strings sent to search are raw user input; the client never rewrites
  them.
- Reduced motion is respected; focus is visible; every control has a label;
  colour is never the only status signal.

### Theme

Light and dark themes through the shadcn CSS variables, `.dark` on `<html>`,
a header toggle cycling light/dark/system, persisted in `localStorage` under
`graphnest-theme`, defaulting to the system preference.

## Information architecture

Shadcn `sidebar` (collapsible) + header with breadcrumb, theme toggle and a
sign-out button. Sidebar groups:

- **Code**: Search (`/`), Repositories (`/repositories`)
- **Supply chain** (shown when `GET /v1/supply-chain/overview` is not 404):
  Overview (`/supply-chain`), Repositories (`/supply-chain/repositories`),
  Components (`/supply-chain/components`), Licenses
  (`/supply-chain/licenses`), Dependency graph (`/supply-chain/graph`),
  Compare (`/supply-chain/compare`)
- **Administration** (shown when the admin probe succeeds): Overview
  (`/admin`), Repositories, Jobs, Deliveries, GitHub, SCIP, Users, Groups,
  Audit (under `/admin/<section>`; Users, Groups, Audit and account tokens are
  session-only exactly as today)
- **Account** (`/account`): API tokens, delegation tokens, connected MCP
  clients

Unknown client routes render a not-found view inside the shell.

## Page requirements

Each legacy page is the behavioral source of truth for its port. Scouts write
a functional inventory per page; workers read the inventory and the legacy
source, then port every listed behavior. Nothing listed may be dropped
silently; anything intentionally not ported is named in the worker report.

### Search and Repositories (from `index.html`)

Token gate; query input with syntax drawer and examples; repository scope
picker (all authorized, or a checked subset, with cursor "load more");
language filter; search submission to `POST /v1/search` with stale-request
abort; results grouped by repository then file with line-number gutter and
match emphasis; result counts with correct pluralization; structured API
errors shown without clearing the query; "Open indexed source" link to the
exact SHA, path and line; file viewer (`POST /v1/files/read`) opened in place
with focus management and back navigation; identifier navigation through
`POST /v1/scip/navigation` (definitions, references, implementations) with a
locations panel; static mode (`file_reads: false`) makes paths non-interactive
and keeps only the outbound link; repositories table with branch and short
indexed SHA, stacking on narrow screens.

### Admin and Account (from `admin.html`)

Admin probe and lock semantics; overview cards; repositories table with status
chips reporting server totals, cursor pagination with de-duplication, select
all, reindex selected and per-row Retry; jobs table with "load older" cursor
and retry; webhook deliveries; GitHub app cards, config and reconcile; SCIP
uploads and dependency mappings (upload form, GitHub-derived refresh); users
(suspend, restore, revoke credentials, direct access editor), groups (access
editor); account API tokens (create with expiry and repository ceiling, reveal
once, revoke), delegation tokens, connected MCP clients with cursor paging and
revoke; audit events. Bearer administrators see only operational inventory;
identity screens stay hidden in bearer mode.

### Supply chain (from `supply-chain.html`)

Stream selector; repository inventory table (collection state, freshness,
assessment summary chips, warnings, refresh for administrators, document
download, snapshots and collections lists, notes); portfolio overview cards
with named denominators; components list with ecosystem, assessment and
license facets, substring search, cursor "load more", and a detail drawer
listing occurrences; snapshot compare; CSV and derived SPDX export links.

### License dashboard (new, `/supply-chain/licenses`)

Audience: directors. Everything on the page names its denominator and carries
the note that assessments describe evidence, not compliance.

Controls: stream selector, optional repository scope (multi-select of
authorized repositories), "top N" selector for the per-repository chart.

Data sources (all existing):

- `GET /v1/supply-chain/overview` → repository and component totals,
  assessment status counts, ecosystems, freshness, denominators;
- `GET /v1/supply-chain/facets` → license expressions with counts, assessment
  statuses, ecosystems for the whole scope;
- `GET /v1/supply-chain/facets?repository_id=<id>` per repository (bounded to
  the first 40 repositories with inventory, in parallel, cached by React Query)
  → exact per-repository license counts;
- `GET /v1/supply-chain/components?license=<expr>` for drill-down links.

Widgets (shadcn `chart` + Recharts):

1. KPI cards: repositories with inventory / authorized, unique coordinates,
   assessed share, conflicts, unlicensed, never collected, stale.
2. Donut: assessment status distribution.
3. Horizontal bar: top 15 license expressions by component count; each bar
   links to the components list filtered by that license.
4. Stacked horizontal bar: per-repository license mix for the top N
   repositories, stacked by license family (permissive, weak copyleft, strong
   copyleft, other, unknown). The family map is a small static table of
   well-known SPDX identifiers in `web/src/lib/license-family.ts` with a unit
   test; unknown expressions fall into "other". Tooltips show the exact
   expressions.
5. Bar: components per ecosystem.
6. Freshness strip: oldest and newest collection times, failed and opted-out
   counts.

### Dependency graph (new, `/supply-chain/graph`)

Audience: directors and architects. Shows which onboarded repositories share
which external dependencies.

Data: paginate `GET /v1/supply-chain/components` (100 per page, at most 30
pages, honouring the stream, ecosystem, assessment and `q` filters); every
portfolio component carries `repositories[]`. Group coordinates by ecosystem +
namespace + name so one node represents a package across versions. If the
scan stops at the page cap, show "based on the first N of M unique
coordinates" using `overview.components.unique_coordinates` for M.

Graph: React Flow with a dagre left-to-right layout; repository nodes on the
left, dependency nodes on the right; edges from repository to dependency.
Default filter: dependencies used by at least two repositories, at most 150
dependency nodes ordered by repository count; controls for minimum shared
repositories, ecosystem, assessment status, text search, and a "top N"
limit. Hover or select a node to highlight its edges and dim the rest. Click a
dependency to open a side sheet with versions, license expressions,
assessment statuses, the repositories using it and a link to the components
list filtered to that package; click a repository to open a sheet listing its
dependencies in the graph with a link to its inventory. Node colour encodes
ecosystem; a dependency with any `conflict` or `unlicensed` occurrence gets a
destructive border. Minimap, zoom controls and a legend are present. Keyboard
users get a table alternative (toggle "Show as table") listing the same edges.

## Testing

- Vitest covers: auth gate state machine (session before bearer, logout must
  succeed, 401 clears token, 403 keeps it, admin lock), search result grouping
  and link building, repository picker paging, admin pagination and
  de-duplication, token reveal once, supply-chain component facets and detail,
  license family mapping and per-repository aggregation, graph data building
  (grouping across versions, filters, page cap notice), and the HTML-sink
  grep. Each page has at least one render test against stubbed `fetch`.
- Go: `internal/webui/handler_test.go` covers every route rule above, the
  headers, the CSP string, immutable caching for `/assets/`, 404 for unknown
  and directory paths, and the 503 fallback using an injected `fs.FS`.
  `cmd/graphnest-server` keeps `TestAPIHandlerMountsWebUIWithoutFallback`,
  skipping when the build is absent.
- The Playwright smoke test `test/smoke/public-ui.spec.mjs` is rewritten with
  role-based selectors against the new UI and keeps asserting the same
  pinned-repository facts.
- The legacy `*_contract_test.go`, `*_dom_test.mjs`, `node_test.go` and the
  three HTML files are deleted once their behaviors are ported and covered.

## Documentation

- `docs/adr/0018-react-shadcn-web-console.md` (accepted) and the index row.
- README: interfaces table, quick start, screenshot, and a "Building the web
  console" paragraph (Node 24, `make ui`, `make ui-dev`).
- `docs/operations.md`: the console section mentions the new routes and that
  images embed the build; `docs/threat-model.md`: the CSP paragraph.
- `CHANGELOG.md` Unreleased: Added (license dashboard, dependency graph),
  Changed (console rebuilt; CSP), Build (Node required for `make build` and
  images), Removed (40 KiB budget, break-glass HTML stripping).
- Fresh screenshots under `docs/images/` rendered with Playwright against the
  stubbed API, replacing the current ones.

## Verification

```sh
make ui ui-check
make fmt lint test staticcheck
make build
make ui-smoke
```

The work is complete when the commands above pass, every legacy behavior is
ported or explicitly listed as dropped with a reason, the two director views
render with fixture data, and the reviewer reports no P0 or P1 findings.
