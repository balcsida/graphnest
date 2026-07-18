# GrepNest Implementation Plan

## Current Pass: Milestones 0 and 1

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

The executable task plan, exact file paths, APIs, tests, commands, and commits
are in `docs/superpowers/plans/2026-07-18-milestones-0-1.md`.

## Milestone 2 Notes: GitHub Enterprise and Durable Indexing

Start only after every Milestones 0-1 gate passes.

- Add embedded PostgreSQL migrations for installations, repositories,
  index jobs, webhook deliveries, and search nodes.
- Use `pgx` transactions and `SKIP LOCKED` for one leased job per repository;
  coalesce queued pushes to the newest desired SHA.
- Add configurable GitHub web, API, and upload URLs plus a shared custom CA
  trust source.
- Generate GitHub App installation tokens just in time; keep them in memory and
  use a temporary credential helper for Git without credential-bearing remotes.
- Authenticate webhook HMAC before parsing, bound payloads, deduplicate delivery
  IDs durably, and enqueue only default-branch work.
- Run one indexer beside Zoekt. It maintains mirrors and isolated worktrees,
  invokes pinned binaries with argument arrays, never executes repository code,
  and atomically records `indexed_sha` only after index visibility checks.
- Implement repository list/status APIs and indexed-SHA file reads through the
  authorized GitHub content API.
- Test with fake GHES HTTP, PostgreSQL-backed queue concurrency, webhook
  coalescing, token redaction, repository removal, and rename behavior.

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
