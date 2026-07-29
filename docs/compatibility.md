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

Graph analysis is durable-mode only. PostgreSQL is the authoritative source;
LadybugDB is derived and rebuildable. The runtime uses the native LadybugDB
ABI v0.18.3 through `github.com/LadybugDB/go-ladybug` v0.17.0, so builds need
cgo and the matching native shared library. The Makefile downloads the pinned
artifact only for Darwin/arm64 and Linux/x86_64 (glibc). Those build targets
are packaging constraints, not a claim of native-platform certification.

The native scanner currently recognizes Go, TypeScript, JavaScript, Java,
Kotlin, and Rust. It does not promise language-indexer equivalence. Direct
`.scip` uploads remain supported independently for code navigation; at the
current indexed SHA they can provide graph fallback/compatibility data when a
native graph is unavailable. They do not enable native scanning.

The graph implementation is compatible only with the current indexed default
branch. Name and integer repository selectors are accepted by graph requests;
the server resolves both to the authorized repository and current exact SHA.
Graph results are rejected as `graph_not_ready` if that authorization/freshness
check no longer holds. The externally observable tools are context, impact,
trace, and administrator-only read-only Cypher; see [OpenAPI](openapi.yaml)
for their bounded request and response contract.
