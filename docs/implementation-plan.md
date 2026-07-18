# GrepNest Implementation Plan

## Completed: Milestones 0 and 1

Work proceeds in this order, with a runnable check and atomic commit after each
logical change:

1. Initialize the Go module, project policy files, CI, configuration, logging,
   health, readiness, and metrics.
2. Define canonical repository and search models plus `SearchBackend`.
3. Implement static bearer authentication and server-selected repository
   authorization.
4. Implement and test the bounded Zoekt JSON adapter against `httptest`.
5. Add the shared search application service and `POST /v1/search`.
6. Add `search_code` and `find_files` over Streamable HTTP MCP.
7. Add the stdio MCP client proxy.
8. Add pinned Docker Compose services and a deterministic fixture repository.
9. Prove fixture indexing, REST search, MCP search, and authorization isolation
   with real Zoekt.

The completed task plan is in
`docs/superpowers/plans/2026-07-18-milestones-0-1.md`.

## Current Pass: Milestone 2

Milestones 0-1 passed every gate. Work now proceeds in this order:

1. Add `pgx/v5`, embedded PostgreSQL migrations, and transactional repository,
   delivery, and leased-job operations.
2. Add the standard-library GitHub App client, custom CA trust, installation
   token handling, and installation repository reconciliation.
3. Add bounded HMAC-verified webhook ingestion and transactional default-branch
   job coalescing.
4. Replace the runtime static repository registry with PostgreSQL while keeping
   the shared authorization and search service.
5. Add the one-at-a-time indexer using safe Git mirrors, temporary worktrees,
   the pinned Zoekt binary, and exact `/api/list` visibility checks.
6. Add repository list/status and authorized indexed-SHA file reads.
7. Prove fake-GHES, PostgreSQL concurrency, Git safety, Zoekt visibility, REST,
   MCP, and authorization behavior end to end.

The approved design is in
`docs/superpowers/specs/2026-07-18-milestone-2-design.md`. The executable TDD
plan will be written after this design document is reviewed.

## Milestone 3 Notes: OpenShift Pilot

Start only after Milestone 2 has its own approved design and passing tests.

- Build pinned multi-stage images containing only the required Go binaries,
  Zoekt binaries, Git, CA certificates, optional Ctags, and minimal init.
- Verify arbitrary numeric UID with root group, read-only root filesystem,
  writable `/tmp` and data mounts, dropped capabilities, RuntimeDefault seccomp,
  graceful SIGTERM, and no Java executable or runtime.
- Add `deploy/helm/grepnest` with a server Deployment, one-replica Zoekt/indexer
  StatefulSet sharing an RWO PVC, internal services, external PostgreSQL DSN,
  optional Route and ServiceMonitor, secret references, PDB, and migration Job.
- Default-deny network access where supported; allow only server-to-Zoekt and
  server/indexer-to-PostgreSQL paths.
- Make registry, pull secrets, CA secret, resources, storage, scheduling, and
  topology values configurable. Label pilot values as measurement starting
  points, not capacity guarantees.
- Add install, backup, restore, upgrade, rollback, failure-recovery, and capacity
  runbooks before calling the pilot deployable.

## Explicitly Deferred

Multi-node sharding, Go outlines, web UI, semantic search, SCIP, tree-sitter,
Snyk workflows, and Gradle discovery are outside this pass.
