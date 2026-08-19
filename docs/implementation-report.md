# Milestones 0-2 implementation report

## Delivered phases

| Phase | Delivered files and decision | Verification |
| --- | --- | --- |
| 0 | `go.mod`, `Makefile`, `docs/adr/0001-go.md` through `0010-no-jvm.md`, `deploy/compose/compose.yml` | Go, pinned Zoekt, Compose, and no JVM dependency established. |
| 1 | `internal/config`, `internal/authn`, `internal/authz`, `internal/repository`, `internal/observability` | Configuration rejects missing or equal tokens; static repository authorization and metrics are covered by tests. |
| 2 | `pkg/api/search.go`, `internal/search`, `internal/zoekt`, `test/integration/zoekt_test.go` | One shared service authorizes before server-selected `RepoIDs`; the integration test proves only authorized ID `7` reaches Zoekt. |
| 3 | `internal/httpapi`, `cmd/grepnest-server` | Bearer-protected `POST /v1/search`, health, readiness, and metrics are wired through the server. |
| 4 | `internal/mcpserver`, `cmd/grepnest-mcp`, `docs/adr/0011-mcp-go-sdk.md` | Hosted bearer-protected `/mcp` exposes `search_code` and `find_files`; stdio proxy uses only server URL and token. |
| 5 | `test/e2e/search_test.go`, fixture repository, Compose fixture index | Real local Zoekt indexes the fixture; REST, MCP, and authorization isolation are exercised. |
| 6 | `internal/postgres`, `internal/githubapp`, `internal/webhook` | Embedded migrations, durable numeric identity, verified deliveries, reconciliation, coalescing, leases, and retention are covered against real PostgreSQL. |
| 7 | `internal/indexer`, `cmd/grepnest-indexer` | Bounded HTTPS Git fetches use fixed askpass, numeric paths, one leased worker, real Zoekt indexing, and exact `/api/list` publication checks. |
| 8 | repository REST/MCP services and `test/e2e/milestone2_test.go` | A GHES-compatible TLS fixture proves signed webhook through indexed-SHA search/read/list/status, including stale suppression, rename isolation, disablement, and an empty-tree repository. |
| 9 | `internal/scipgraph`, SCIP HTTP/MCP adapters, PostgreSQL graph storage, and `test/e2e/scip_test.go` | Pre-generated indexes provide exact-SHA cross-repository navigation while suppressing unauthorized targets; no managed indexer was added. |
| 10 | managed graph scanner, PostgreSQL graph storage and queries, and REST/MCP graph tools | PostgreSQL-backed contracts cover exact-checkout Go, JavaScript, TypeScript/TSX, Java, Kotlin, and Rust fixtures through managed scan, status, REST, and MCP. |

## Decisions

- PostgreSQL is authoritative for graph artifacts, upload state, nodes, edges,
  and bounded graph queries.
- `graphquery.Service` owns traversal behavior, cycle detection, confidence
  filtering, limits, partial-result boundaries, and backend-independent
  response semantics.
- The PostgreSQL store uses static parameterized SQL, repository/upload/commit
  scope, stable ordering, SQL limits, and one batched frontier query per depth
  and relation.
- Context, impact, and trace remain the public REST and MCP graph operations.
  The generic query language and internal owner protocol were removed.
- Native scanners remain job-scoped producers. No target repository workflow,
  separate graph database, synchronization loop, or native graph library is
  required.

## Verification commands and results

The graph-store change is covered by unit, integration, end-to-end, contract,
deployment-render, and image tests. The milestone gate set is:

```sh
go test -race ./internal/graphquery ./internal/postgres ./internal/graphservice
make test-race
make integration
make e2e
make staticcheck
make govulncheck
make openapi-check
make compose-test
make helm-lint
make helm-test
make image-test
git diff --check
```

PostgreSQL/legacy-backend response parity was captured before the old backend was
removed. The golden fixture covers deterministic ordering, cycles, confidence
filtering, duplicate names, stale and missing graphs, renamed-repository
scope, and hidden unauthorized repositories. Live PostgreSQL integration tests
exercise the final backend directly. `EXPLAIN (ANALYZE, BUFFERS)` confirmed
bounded plans using existing keys and indexes, so no speculative index was
added.

## Risks and next milestone

Local E2E and live Compose dependencies prove the implemented local contracts; the durable
Compose profile has configuration-only validation and does not establish
production readiness. The implementation uses the GHES 3.17-compatible default
REST API version `2022-11-28`; the version and CA bundle are configurable.
Indexing is default-branch-only. Images, secret delivery, cluster deployment,
backup/restore, capacity validation, and OpenShift testing remain incomplete.

Image, live-cluster, OpenShift, and storage-class recovery verification remain
outside this report. Compose and Helm coverage is render/configuration
validation only; no production image or cluster deployment was claimed.
