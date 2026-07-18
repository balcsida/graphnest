# Milestones 0-1 implementation report

## Delivered phases

| Phase | Delivered files and decision | Verification |
| --- | --- | --- |
| 0 | `go.mod`, `Makefile`, `docs/adr/0001-go.md` through `0010-no-jvm.md`, `deploy/compose/compose.yml` | Go, pinned Zoekt, Compose, and no JVM dependency established. |
| 1 | `internal/config`, `internal/authn`, `internal/authz`, `internal/repository`, `internal/observability` | Configuration rejects missing or equal tokens; static repository authorization and metrics are covered by tests. |
| 2 | `pkg/api/search.go`, `internal/search`, `internal/zoekt`, `test/integration/zoekt_test.go` | One shared service authorizes before server-selected `RepoIDs`; the integration test proves only authorized ID `7` reaches Zoekt. |
| 3 | `internal/httpapi`, `cmd/grepnest-server` | Bearer-protected `POST /v1/search`, health, readiness, and metrics are wired through the server. |
| 4 | `internal/mcpserver`, `cmd/grepnest-mcp`, `docs/adr/0011-mcp-go-sdk.md` | Hosted bearer-protected `/mcp` exposes `search_code` and `find_files`; stdio proxy uses only server URL and token. |
| 5 | `test/e2e/search_test.go`, fixture repository, Compose fixture index | Real local Zoekt indexes the fixture; REST, MCP, and authorization isolation are exercised. |

## Decisions

- Zoekt remains behind the server; clients never receive direct access.
- Repository IDs are selected after authorization, not accepted from clients.
- JSON HTTP remains the adapter pending ADR-0003's identical-query 10% threshold.
- PostgreSQL is intentionally unused until Milestone 2.
- Pinned Zoekt runs as `linux/amd64`; Apple-silicon hosts use Docker emulation
  because the pinned image has no arm64 variant.

## Verification commands and results

The documentation pass ran the following commands after the final edit:

```sh
go clean -testcache
make fmt
make lint
make test
make test-race
make integration
make e2e
make build
docker compose -f deploy/compose/compose.yml config
docker compose -f deploy/compose/compose.yml up -d --wait
docker compose -f deploy/compose/compose.yml ps
rg -n 'github\\.com|latest|Authorization|token|RepoIDs|java|jvm|maven|gradle' --glob '!go.sum' .
git diff --check HEAD
git status --short
```

Results: all listed quality gates, uncached race/integration/E2E/build, Compose
configuration, and live Compose health checks exited 0. The scope scan found
expected module, documentation, test, and authorization references; it found no
runtime hard-coded host, `latest` image tag, credential logging, JVM dependency,
or client-controlled Zoekt `RepoIDs`. `make image` and `make helm-lint` were
not run as success gates: they intentionally return nonzero with
`image: milestone not implemented` and `helm-lint: milestone not implemented`.

## Risks and next milestone

Local E2E and live Compose prove the vertical slice only; they do not establish
production readiness. Missing capabilities include GitHub App ingestion,
webhook validation, durable PostgreSQL-backed coordination, secret delivery,
network policy, images, Helm, OpenShift deployment, and production observability.

Next: Milestone 2 adds GitHub App repository discovery and durable PostgreSQL
coordination before any production packaging work.
