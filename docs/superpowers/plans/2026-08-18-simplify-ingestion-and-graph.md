# Simplify Ingestion and Graph Implementation Plan

> **Required sub-skill:** Use `superpowers:subagent-driven-development` to
> execute this plan milestone by milestone with two-stage review.

**Goal:** Make exact-SHA archive snapshots, Zoekt, GitHub App access, and
PostgreSQL the default GraphNest runtime while removing persistent source,
LadybugDB, and optional scanner/tool dependencies from the core distribution.

**Architecture:** Preserve the current PostgreSQL queue, desired/indexed SHA
gates, authorization services, and Zoekt visibility checks. Replace the
Git-shaped worker seam with a job-scoped snapshot provider, index its ordinary
directory, reuse it for bounded optional enrichment, then query authoritative
graph rows directly from PostgreSQL. Switch and delete backends only after
parity checks pass.

**Tech stack:** Go 1.26.6, standard-library HTTP/tar/gzip/filesystem packages,
PostgreSQL/pgx, pinned Zoekt, Docker Compose, Helm.

**Spec:** User-approved migration brief and ADR-0013 through ADR-0015.

## Constraints and baseline

- Authorization filters before every backend call; exact-SHA checks remain
  before publication and response delivery.
- Installation tokens never enter argv, URLs, logs, metrics, filenames, or
  persistent configuration.
- Archive extraction is bounded and fail-closed; cleanup is observable and
  never hides the primary error.
- Search succeeds independently of optional enrichment.
- No target-repository pipeline changes, persistent archive cache, automatic
  GitHub fallback, generic query language, or unrelated UI/authentication work.
- Baseline at `dc7e8e3`: `make test-race`, `make openapi-check`,
  `make compose-test`, `make helm-lint`, and `make helm-test` pass. `make
  image-test` cannot start because the local Docker socket is unavailable.

### Current-HEAD audit

1. `internal/indexer.Git` keeps a persistent bare mirror per repository and a
   detached worktree per job. There is no source-neutral snapshot abstraction.
2. `internal/graphscanner.Worker` independently creates a second exact-SHA Git
   worktree and obtains a second installation token for enrichment.
3. `internal/indexer.ZoektIndexer` shells out to `zoekt-git-index` against Git
   metadata. The pinned source also provides `zoekt-index -meta` for ordinary
   directories, so no fake `.git` is needed.
4. `internal/repository.Service` reads GitHub Contents at `indexed_sha`, then
   reauthorizes and rejects a changed SHA before returning content.
5. `internal/githubapp.Client` already fetches the GitHub dependency SBOM, but
   that endpoint is repository-current rather than exact-SHA source evidence.
6. Migrations 007 and 008 plus `internal/postgres/graph.go` make graph uploads,
   nodes, edges, manifests, and snapshot IDs authoritative in PostgreSQL.
7. `internal/ladybug` synchronizes a derived query replica from PostgreSQL;
   embedded and separate graph-owner modes run the same runtime.
8. `internal/search.Service` authorizes before backend access but scopes Zoekt
   primarily through numeric IDs and suppresses hits whose Zoekt version differs
   from PostgreSQL's indexed SHA.
9. Root Make targets, all command builds, Docker images, integration, and E2E
   download/link Ladybug through CGO and the `system_ladybug` build tag.
10. The root module contains tree-sitter runtimes/grammars, SCIP/protobuf, and
    Buf/protoc generation tools. Tree-sitter remains CGO even after Ladybug is
    removed unless the scanner is isolated.

Baseline dependency counts are 236 modules, 1,005 module-graph edges, and 430
packages in `go list -deps ./...`; host `CGO_ENABLED=1`. Current unstripped
binaries are admin 17,059,314 bytes, graph 22,599,890, indexer 23,478,914, MCP
11,881,298, migrate 17,110,946, scanner 28,924,882, and server 24,287,042.
Dockerfile-equivalent stripped binaries are respectively 11,276,754,
14,957,922, 15,570,530, 8,066,530, 11,326,306, 22,182,482, and 16,206,322
bytes. A container-size baseline is unavailable until a Docker daemon is
present; this is an environment limitation, not a repository failure.

## Task 1: Milestone 1 — Add the snapshot seam

**Files:** Modify `internal/indexer/worker.go`, `worker_test.go`, `git.go`,
`git_test.go`, `cmd/graphnest-indexer/main.go`, and `main_test.go`.

1. Write failing worker tests for exact target propagation, provider workspace
   disk admission, desired-SHA checks before expensive work and publication,
   cleanup on success/failure/cancellation, joined cleanup errors, and unchanged
   lease/retry behavior.
2. Introduce `Snapshot`, `SnapshotProvider.Prepare`, `Cleanup`, `CleanupStale`,
   and `FreeSpacePath`; adapt the worker and publisher to accept the snapshot
   directory without exposing provider type.
3. Adapt current Git mirror/worktree behavior as `GitSnapshotProvider` and wire
   it as the explicit legacy provider.
4. Run `go test -race ./internal/indexer ./cmd/graphnest-indexer`, `make
   test-race`, and `git diff --check`.
5. Commit `refactor(indexer): add snapshot provider`.

## Task 2: Milestone 2 — Secure exact-SHA GitHub archives

**Files:** Add `internal/indexer/archive.go` and `archive_test.go`; modify
`internal/githubapp/client.go`, `client_test.go`, `internal/config/config.go`,
`config_test.go`, `internal/observability/metrics.go`, `metrics_test.go`,
`cmd/graphnest-indexer/main.go`, and `main_test.go`.

1. Write table and fuzz tests for safe tar.gz extraction: prefix validation,
   traversal/absolute/NUL paths, duplicate outputs, links/special files,
   executable bits, malformed input, every byte/file/count/path bound, timeout,
   cancellation, partial cleanup, and no host symlinks.
2. Implement the minimum standard-library streaming extractor with conservative
   permissions and configurable nonzero limits.
3. Write `httptest` failures for exact-SHA archive requests, GitHub/GHES
   redirects, HTTPS/origin/userinfo rules, authorization stripping, retry
   classification, redirect caps, and token/URL redaction.
4. Extend the existing GitHub App client with bounded non-JSON streaming and
   implement `ArchiveSnapshotProvider` using one unique job directory.
5. Add `archive|git` configuration, archive metrics without repo/SHA labels,
   inactive-job stale cleanup, and compatibility warnings for Git paths.
6. Run focused race tests, `make test-race`, `make staticcheck`, and
   `git diff --check`.
7. Commit `feat(indexer): add secure archive snapshots`.

## Task 3: Milestone 3 — Index ordinary directories with Zoekt

**Files:** Modify `internal/indexer/zoekt.go`, `zoekt_test.go`,
`cmd/graphnest-indexer/main.go`, `test/e2e/milestone2_test.go`, configuration,
deployment examples, and their tests.

1. Record the pinned Zoekt directory-index API and metadata fields; do not fake
   `.git` or guess flags.
2. Write failing command tests for numeric repository ID, name, URL, branch,
   exact visible version, metadata outside source, cancellation, limits, and
   redaction.
3. Implement directory indexing with stable metadata and preserve current shard
   publication plus exact `WaitVisible` behavior.
4. Add Git/archive parity fixtures covering literals, regex, Unicode, binary,
   empty/executable files, ordering, metadata, and SHA mismatch suppression.
5. Commit the parity fixture with the directory implementation, run it against
   both providers, and make archive the default only after that commit passes.
   Keep Git as an explicit
   rollback provider and add an E2E residue assertion for source, archives,
   mirrors, worktrees, credentials, and askpass files.
6. Run focused tests, `make integration`, `make e2e`, `make test-race`, and
   `git diff --check`.
7. Commit `feat(indexer): index archive directories`, including its parity
   fixture, then commit `refactor(indexer): default to archives` after the full
   parity and residue gates pass.

## Task 4: Milestone 4 — Reuse one snapshot for enrichment

**Files:** Add `internal/enrichment/runner.go`, `runner_test.go`, and
`protocol.go`; modify `internal/indexer/worker.go`, `worker_test.go`,
`internal/postgres/queue.go`, `queue_test.go`, `internal/graphscanner/worker.go`,
`cmd/graphnest-indexer/main.go`, `cmd/graphnest-scanner/main.go`, and the graph
job integration/E2E tests. Deployment removal waits for milestone 7.

**Interface:** `Enricher.Enrich(ctx, Snapshot, repository.Repository,
targetSHA) (Status, error)` runs a versioned length-bounded JSON artifact
protocol through the existing process runner; it receives no token or arbitrary
output path. `IndexQueue.PublishIndex` atomically exposes the exact SHA while
leaving the leased index job in its existing `running` state; `CompleteIndex`
closes the job after the optional phase. The existing active-job lease, renewal,
reaping, metrics, and one-running-job constraint therefore protect the snapshot
without adding a public job state or schema migration. A crashed running job is
requeued and may reacquire its archive; a normal run never downloads twice.
Existing `graph_jobs` remains the
durable enrichment result/status record but is no longer a separately claimed
checkout queue.

1. Write queue/worker failures for `ready` publication before enrichment,
   retained lease/activity during enrichment, crash requeue, independent
   timeout/status, stale-SHA rejection, bounded subprocess input/output, and
   cleanup after every result.
2. Split current completion into idempotent `PublishIndex` and terminal
   `CompleteIndex` transactions without changing search authorization, public
   job-state enums, or SHA gates.
3. Run the optional scanner locally against the existing snapshot. Remove its
   token source and `GitWorkspace` only after the inline crash-retry tests pass;
   keep SCIP upload-driven and exact-SHA behavior unchanged.
4. Run `go test -race ./internal/indexer ./internal/enrichment
   ./internal/graphscanner ./internal/postgres`, `make integration`, `make e2e`,
   and `git diff --check`.
5. Commit `refactor(indexer): track optional enrichment`, then
   `refactor(graph): reuse index snapshots`.

## Task 5: Milestone 5 — Query graph data in PostgreSQL

**Files:** Add `internal/graphquery/store.go` and `store_test.go`; add
`internal/postgres/graph_query.go` and `graph_query_test.go`; adapt
`internal/graphquery/{service,context,impact,trace}.go`, add golden fixtures in
`test/fixtures/graph/query/`, and extend `test/integration/graph_contract_test.go`.
After parity, remove `internal/graphquery/cypher.go`, `internal/ladybug`,
`internal/graphruntime`, `internal/graphclient`, `internal/graphtransport`,
`internal/graphcommand`, and `cmd/graphnest-graph`; remove Cypher together from
`pkg/api`, `internal/httpapi`, `internal/mcpserver`, `internal/webui`, and
`docs/openapi.yaml`. This milestone also removes Ladybug/graph-owner references
from Docker, Compose, Helm, CI, and image tests so every commit remains runnable;
milestone 7 owns the remaining archive/scanner/default-topology cleanup.

**Interface:** `graphquery.Store` exposes current manifest/health, selector
resolution, bounded context edges, and batched impact/trace frontiers scoped by
repository ID, upload ID, and commit. `graphquery.Service` owns traversal,
cycle detection, deterministic assembly, and all existing result bounds.

1. Introduce the store interface with a Ladybug adapter and unchanged public
   behavior; run graph query/unit/E2E tests and commit
   `refactor(graph): add query store` independently.
2. Convert current context/impact/trace results into backend-independent golden
   tests covering direction, relation/confidence filters, cycles, duplicates,
   bounds, stale/missing graphs, rename, cross-repository authorization, and
   deterministic ordering.
3. Implement parameterized, upload/commit-scoped PostgreSQL primitives with
   batched frontiers, cancellation, timeouts, and no N+1/unbounded graph load.
4. Compare normalized Ladybug/PostgreSQL results and capture representative
   `EXPLAIN (ANALYZE, BUFFERS)` evidence before adding any index.
5. Switch the storage-neutral service after parity. In a separate atomic commit,
   remove Cypher and all Ladybug synchronization/runtime/build/deployment code.
6. Run `go test -race ./internal/graphquery ./internal/postgres
   ./internal/graphservice`, `make integration`, `make e2e`, `make
   openapi-check`, deployment/image gates, and reference scans.
7. Commit `feat(graph): query PostgreSQL directly`, `test(graph): prove query
   parity`, then `refactor(graph): remove LadybugDB`.

## Task 6: Milestone 6 — Isolate optional dependencies

**Files:** Add `tools/go.mod` and `tools/tools.go`; add `scanner/go.mod`,
`scanner/go.sum`, `scanner/cmd/graphnest-scanner`, and `scanner/graphscan`; add
root `go.work` for development. Modify root `go.mod`, `go.sum`, `Makefile`,
`.github/workflows/ci.yml`, `.github/dependabot.yml`, `Dockerfile`,
`deploy/images/test.sh`, and `.github/workflows/release.yml`. The scanner module
may import stable root graph artifact/repository packages; the root module must
not import the scanner module.

1. Capture root/scanner/tools dependency, build, binary, image, CGO, and
   vulnerability baselines.
2. Move Buf/protoc tool directives and their sums to `tools/`, with
   `tools-check` running generation plus a clean-diff assertion. Move
   `internal/graphscan`, `internal/graphscanner`, and the scanner command to the
   scanner module after milestone 4 removes root imports. Keep SCIP in root
   unless a measured import audit justifies a separate `scip/` module.
3. Add `scanner-build`, `scanner-test`, `scanner-vulncheck`, and `tools-check`;
   CI and dependency updates cover modules independently.
4. Run per-module `go mod tidy`, `go mod verify`, dependency counts,
   vulnerability checks, core `CGO_ENABLED=0 go test ./...`, and binary sizes.
5. Commit `refactor(build): isolate generation tools` and `refactor(scanner):
   isolate tree-sitter dependencies`.

## Task 7: Milestone 7 — Clean the default deployment

**Files:** Update `README.md`, `docs/architecture.md`, `docs/operations.md`,
`docs/compatibility.md`, `docs/threat-model.md`, `docs/adr/README.md`,
`docs/openapi.yaml`, `.env.example`, `Dockerfile`, `deploy/images/test.sh`,
`deploy/compose/{durable,graph-embedded,graph-separate}.yml`,
`deploy/compose/test.sh`, Helm `values.yaml`, `values.schema.json`,
`templates/{configmaps,node,graph,networkpolicies,servicemonitor}.yaml`, chart
README/render tests, `.github/workflows/{ci,release}.yml`, and add
`docs/migrations/archive-postgres-graph.md`.

1. Remove default mirror/worktree storage, Git, graph-owner modes/secrets/ports,
   scanner binary, and liblbug while preserving private CAs, GHES, non-root
   containers, read-only roots, PostgreSQL, Zoekt, and dedicated ephemeral space.
2. Delete obsolete graph topology files/templates only in the same commit that
   removes every reference. Document archive migration, aliases/removals,
   first-index verification,
   manual old-storage deletion, graph/Cypher changes, scanner/SCIP enablement,
   rollback, disk failures, and SHA mismatch suppression.
3. Run all build, security, OpenAPI, Compose, Helm, and image gates.
4. Commit `chore(deploy): simplify default topology` and `docs: document
   ingestion migration`.

## Task 8: Milestone 8 — Add explicit degraded GitHub search

**Files:** Add `internal/search/scope.go`, `github.go`, and `github_test.go`;
modify `internal/search/service.go`, `service_test.go`,
`internal/githubapp/client.go`, `internal/config/config.go`, `pkg/api`,
`docs/openapi.yaml`, HTTP/MCP adapters, README, compatibility/operations docs,
Compose/Helm configuration, and their contract tests.

1. Proceed only after milestones 1-7 are green.
2. Define `RepositoryScope{ID, GitHubID, Name, IndexedSHA}` and
   `SearchConsistency{Backend, Exact, Revision, Partial}`. Test
   authorization-before-call, qualifier chunking, limits/rate errors,
   truncation/partial results, deterministic deduplication, exact-SHA claim
   absence, and no implicit fallback.
3. Add explicit `zoekt|github` selection with Zoekt default and exact-SHA file
   opening through the existing repository service.
4. Run `go test -race ./internal/search ./internal/githubapp ./internal/httpapi
   ./internal/mcpserver`, `make openapi-check`, `make test-race`, and E2E search
   contracts. Commit `feat(search): add degraded GitHub backend`.

## Final verification and report

Run every applicable repository gate from the brief, `go mod tidy`, `go mod
verify`, `git diff --check`, reference scans, and `git status --short`. Record
actual outcomes, before/after module graph counts, binaries, images, CGO needs,
remaining references, configuration/API changes, migration/rollback, known
limits, incomplete acceptance criteria, and ordered commits. Do not push or
open a pull request without separate authorization.
