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
| 10 | managed graph scanner, PostgreSQL graph authority, LadybugDB runtime, REST/MCP graph tools, and deployment modes | Real PostgreSQL/LadybugDB contracts cover embedded and standalone ownership; exact-checkout Go, JavaScript, TypeScript/TSX, Java, Kotlin, and Rust fixtures reach managed scan, status, REST, and MCP. |

## Decisions

- Zoekt remains behind the server; clients never receive direct access.
- Repository IDs are selected after authorization, not accepted from clients.
- JSON HTTP remains the adapter pending ADR-0003's identical-query 10% threshold.
- PostgreSQL is the Milestone 2 metadata store and durable queue.
- External GHES IDs use `github_id`; full names are derived, the GHES host is
  deployment configuration, and shard placement remains a Milestone 5 concern.
- The indexer rejects GHES-reported oversized repositories before credentials
  or fetch and validates Zoekt with both exact `/api/list` metadata and a
  RepoID-scoped search.
- Repository-owned `.sourcegraph/ignore` is supported by pinned Zoekt. Global
  injected excludes are deferred because synthesizing the file would falsify
  the indexed commit SHA.
- Pinned Zoekt runs as `linux/amd64`; Apple-silicon hosts use Docker emulation
  because the pinned image has no arm64 variant.
- SCIP generation remains repository CI's responsibility. GrepNest accepts
  bounded protobuf uploads only for the exact currently indexed commit.
- PostgreSQL remains authoritative for managed and external graph artifacts.
  LadybugDB v0.18.3 is pinned derived storage with one embedded or standalone
  writer; public clients use the server's authorization and exact-SHA checks.

## Verification commands and results

The Milestone 2 completion pass runs these commands after the final edit:

```sh
go clean -testcache
make fmt
make lint
make test
make test-race
make postgres-integration
make integration
make e2e
make build
make helm-lint
make helm-test
make compose-test
docker compose -f deploy/compose/compose.yml --profile fixture config
rg -n 'github\\.com|latest|Authorization|token|RepoIDs|java|jvm|maven|gradle' --glob '!go.sum' .
git diff --check HEAD
git status --short
```

Results are recorded in the Task 17 report. The E2E gate requires PostgreSQL,
builds the pinned Zoekt tools, and exercises authenticated HTTPS smart-Git and
real Zoekt search/list processes. `make image` remains an intentionally failing
Milestone 3 boundary. Helm lint/render gates, fixture Compose rendering, and
durable Compose configuration validation pass, but the durable server container
and cluster deployment have not been tested because no application image is
built.

The graph completion pass additionally ran:

```sh
make native-link-test abi-test ladybug-test
make postgres-integration
make e2e
make compose-test helm-lint helm-test openapi-check
go mod tidy -diff
go mod verify
git diff --check
```

On Darwin/arm64, the pinned LadybugDB archive checksum passed, scanner,
indexer, and graph binaries built with cgo, and `otool -L` resolved
`@rpath/liblbug.0.dylib` at native version 0.18.3. The seven-variant ABI smoke
matrix parsed Go, JavaScript, TypeScript, TSX, Java, Kotlin, and Rust. Real
PostgreSQL/LadybugDB tests verified public REST/MCP parity across embedded and
standalone runtimes, administrator-only Cypher, authorized scope, current
commit reporting, result boundaries, and rejection after an indexed-SHA
change. Linux/x86_64 CI uses the separately pinned archive and checksum and
requires `ldd` to resolve `liblbug` from the pinned directory; that native
path is encoded but was not executed on this Darwin host.

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
