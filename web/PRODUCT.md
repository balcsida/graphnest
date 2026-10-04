# Product

<!-- impeccable:product-schema 1 -->

<!-- Written without an interview (no one was available to answer); every fact below comes from README.md and the web console spec, 2026-10-04-shadcn-web-console-design.md. -->

## Platform

web

## Users
Engineers who search and navigate the code they are authorized to see, administrators who run the pilot (repositories, jobs, deliveries, GitHub, SCIP, users, groups, audit), and directors who review license usage and dependency sharing across onboarded repositories.

## Product Purpose
GraphNest is an experimental, self-hosted code search and context layer for engineering teams and AI agents. The embedded browser console is one of its interfaces, next to the REST API and MCP. It searches authorized repositories, opens files at the exact indexed commit and navigates symbols; administrators operate the service; the optional Dependencies & Licenses module shows an observed dependency inventory.

## Positioning
Self-hosted and scoped: clients never receive direct access to the search index, files open at the indexed commit, and the dependency inventory preserves each producer document as reported.

## Operating Context
A single embedded React application served by `graphnest-server`, signed in with OIDC or GitHub OAuth, or an API token. Administration appears only when the admin probe succeeds; Supply chain only when the module is enabled on the server.

## Capabilities and Constraints
- Pre-1.0 pilot software; not production-ready.
- Views: Search, Repositories, Supply chain (Overview, Repositories, Components, Licenses, Dependency graph, Compare), Administration, Account.
- Every view is built from the existing REST API; no client-side authorization rules, no analytics.
- Supply-chain figures name their denominators and describe evidence, not compliance.
- Browser-rendered API text is always plain text.

## Evidence on Hand
Screenshots in `docs/images/console-*.png`. No customer, benchmark or usage data exists; none may be invented.

## Product Principles
- Authorization is visible: show only what the caller may see, and say when a view is limited.
- Numbers carry their denominator.
- Evidence is not a verdict: assessments are never presented as compliance.
- Colour is never the only status signal.

## Accessibility & Inclusion
Visible focus, a label on every control, reduced motion respected, table alternatives for charts and the graph, light and dark themes.
