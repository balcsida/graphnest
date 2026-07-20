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
docker compose -f deploy/compose/compose.yml config
docker compose -f deploy/compose/compose.yml up -d --wait
docker compose -f deploy/compose/compose.yml ps
rg -n 'github\\.com|latest|Authorization|token|RepoIDs|java|jvm|maven|gradle' --glob '!go.sum' .
git diff --check HEAD
git status --short
```

Results are recorded in the Task 17 report. The E2E gate requires PostgreSQL,
builds the pinned Zoekt tools, and exercises authenticated HTTPS smart-Git and
real Zoekt search/list processes. `make image` remains an intentionally failing
Milestone 3 boundary. Helm lint/render gates pass, but no image or cluster
deployment has been tested.

## Risks and next milestone

Local E2E and live Compose prove Milestones 0-2 only; they do not establish
production readiness. The implementation uses the GHES 3.17-compatible default
REST API version `2022-11-28`; the version and CA bundle are configurable.
Indexing is default-branch-only. Images, secret delivery, cluster deployment,
backup/restore, capacity validation, and OpenShift testing remain incomplete.

Next: Milestone 3 validates images and the existing Helm chart on Kubernetes or
OpenShift. No image or OpenShift implementation was added here.
