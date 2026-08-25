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

The optional native enrichment binary recognizes Go, TypeScript, JavaScript,
Java, Kotlin, and Rust. It is not included in the default images, Compose, or
Helm chart and does not promise language-indexer equivalence. During the
pre-1.0 compatibility window, operators can explicitly use Docker's
`legacy-node` target, or mount `graphnest-scanner` into the default node, and set
`GRAPHNEST_SCANNER_PATH=/usr/local/bin/graphnest-scanner` on the indexer. The
indexer invokes its `enrich` subcommand on the same archive snapshot; it is not
a standalone worker. The compatibility image is not published or selected
automatically. Direct
`.scip` uploads remain supported independently for code navigation and can
supply exact-SHA graph data when native graph scanning is unavailable.

`GRAPHNEST_ZOEKT_GIT_INDEX` remains a deprecated alias for
`GRAPHNEST_ZOEKT_INDEX` when its basename is `zoekt-git-index`. New deployments
must use archive ingestion and `GRAPHNEST_ZOEKT_INDEX`. See the
[migration runbook](migrations/archive-postgres-graph.md).

Graph queries target only the current indexed default branch. Repository
selectors resolve to stable numeric repository IDs, and every result is scoped
by repository ID, upload ID, and commit. Authorization and freshness checks
apply to bounded context, impact, and trace operations; see
[OpenAPI](openapi.yaml) for the exact contract.
# Search backend compatibility

`GRAPHNEST_SEARCH_BACKEND=zoekt` remains the default and preserves exact indexed
SHA search/read semantics. Durable deployments can explicitly select `github`;
it uses GitHub App code search with the existing private-CA/GHES client, returns
best-effort partial results, and never claims an exact revision. GitHub text
matches include bounded previews but no absolute source line, so those results
omit line anchors. `github` is not available in static mode and neither backend
automatically falls back to the other.
