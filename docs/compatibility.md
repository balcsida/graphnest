# Compatibility

Milestones 0-2 are pre-pilot and make no release compatibility promise.
Milestone 2 targets GitHub Enterprise Server 3.17 with REST API version
`2022-11-28` by default and remains compatible with configurable HTTPS web,
API, upload, and Git endpoints plus a custom CA bundle. GitHub Enterprise Cloud
uses the same configurable contract. Only default branches are indexed.

The local GHES-compatible fixture is not a certification against a live GHES
instance. Kubernetes, OpenShift, published images, upgrades, backup/restore,
and production-scale compatibility remain unverified.

## Graph analysis

Graph analysis is available only in durable mode. PostgreSQL is the
authoritative graph store and serves bounded graph queries directly; builds no
longer require a separate graph runtime, native graph ABI, or shared library.

The native scanner currently recognizes Go, TypeScript, JavaScript, Java,
Kotlin, and Rust. This does not promise language-indexer equivalence. Direct
`.scip` uploads remain supported independently for code navigation and can
supply exact-SHA graph data when native graph scanning is unavailable.

Graph queries target only the current indexed default branch. Repository
selectors resolve to stable numeric repository IDs, and every result is scoped
by repository ID, upload ID, and commit. Authorization and freshness checks
apply to bounded context, impact, and trace operations; see
[OpenAPI](openapi.yaml) for the exact contract.
