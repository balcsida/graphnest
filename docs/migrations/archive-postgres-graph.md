# Archive ingestion and PostgreSQL graph migration

This runbook upgrades a deployment that used persistent Git mirrors,
worktrees, scanner pods, or LadybugDB. Back up PostgreSQL and record the
current image digests, Helm revision or Compose files, indexed SHAs, and volume
names before changing the deployment.

## Configuration changes

Archive ingestion is the default. The packaged node image, Compose overlay,
and Helm chart no longer contain Git, `zoekt-git-index`, `grepnest-scanner`, a
persistent checkout volume, graph-owner modes, graph secrets, graph ports,
LadybugDB data, or `liblbug`.

Use `GREPNEST_ZOEKT_INDEX` instead of the deprecated
`GREPNEST_ZOEKT_GIT_INDEX` alias. The alias remains accepted during the
pre-1.0 compatibility window only when its basename is `zoekt-git-index`.
Packaged deployments set archive ingestion directly; remove
`GREPNEST_SOURCE_PROVIDER=git`, `GREPNEST_GIT_PATH`, scanner values, and former
`GREPNEST_GRAPH_URL`, `GREPNEST_GRAPH_SECRET_FILE`, `GREPNEST_GRAPH_MODE`,
`GREPNEST_GRAPH_DATA_DIR`, and `GREPNEST_GRAPH_LISTEN_ADDRESS` settings.
`GREPNEST_GRAPH_*` query and upload limits remain valid.

Configure two distinct writable locations:

- `GREPNEST_DATA_DIR` is bounded ephemeral archive workspace. Compose uses a
  6 GiB tmpfs and Helm uses `node.paths.workspace` with
  `node.indexer.workspaceSizeLimit`.
- `GREPNEST_INDEX_DIR` is durable Zoekt shard storage. Do not place the archive
  workspace inside this volume.

Private GitHub CAs, configurable GitHub.com or GHES HTTPS origins, GitHub App
secret files, non-root execution, read-only roots, PostgreSQL, Zoekt, and
network-policy boundaries are unchanged.

## Upgrade and first-index verification

1. Back up PostgreSQL and snapshot, rather than delete, the old mirror,
   worktree, graph, and shard volumes.
2. Apply database migrations, then deploy the new application and node images.
   Do not mount the old mirror, worktree, or LadybugDB volume.
3. Confirm the server and indexer are healthy and the node can reach the
   configured GitHub archive endpoint through the private CA when applicable.
4. Trigger one repository index. Wait for its index job to complete, then
   confirm PostgreSQL records the expected 40-character default-branch
   `indexed_sha` and Zoekt `/api/list` reports that same revision.
5. Search for a known symbol and open one result. The response must use the
   same indexed SHA. Repeat on GHES/private-CA repositories before broadening
   the rollout.

GrepNest publishes a new indexed SHA only after Zoekt confirms visibility. If
PostgreSQL and Zoekt disagree, search results are intentionally suppressed and
file reads stay on PostgreSQL's committed SHA. Treat suppression as a failed or
incomplete index, not as permission to serve another revision.

## Graph data and API changes

Existing PostgreSQL graph artifacts, uploads, nodes, edges, jobs, and completed
exact-SHA data are retained. Server replicas query PostgreSQL directly; no
export to a graph volume or synchronization step is required.

The raw Cypher REST endpoint, MCP tool, UI, graph-owner service, and LadybugDB
query path are removed. Clients must use the bounded context, impact, and trace
operations in the OpenAPI contract. Missing or stale graph data returns the
documented readiness status instead of querying an older commit.

Native scanning is optional and is no longer installed by Compose or Helm.
Operators that still need it during the compatibility window may explicitly
build Docker's `legacy-node` target and run the scanner module as a separately
managed worker. The default `node` target remains archive-only. Prefer an
exact-SHA `.scip` upload when a language-specific indexer already exists;
uploads continue to work without the native scanner.

## Disk failures and retry

An archive that exceeds download, extracted-byte, file-byte, file-count, path,
repository, free-space, or workspace limits fails the job. A full or evicted
ephemeral workspace must not modify durable shards or publish an indexed SHA.
Increase the bounded workspace or configured archive limit only after checking
the repository and available capacity, then retry the job. Clean orphaned
ephemeral pod or container state normally; do not edit Zoekt shards by hand.

## Rollback

Keep the prior image digests and deployment definition. Roll back the
application and node together if the new indexer cannot fetch or publish. The
legacy runtime may remount its old storage, but first verify its schema is
compatible with the already-applied PostgreSQL migrations. Do not restore an
older PostgreSQL backup merely to restore LadybugDB: PostgreSQL graph data is
authoritative and should be preserved.

Rollback does not make a mismatched Zoekt revision safe. Reindex until Zoekt
and PostgreSQL agree, or restore a matching shard snapshot and its corresponding
PostgreSQL backup. Exact-SHA mismatch suppression remains active in either
direction.

## Manual cleanup after acceptance

No upgrade automatically deletes old storage. After the first-index checks,
graph operations, backup retention, and rollback window have passed, identify
the exact obsolete mirror, worktree, and LadybugDB volumes from the recorded
old deployment. Unmount them, take a final recoverable snapshot if policy
requires it, and delete only those explicit volumes. Retain the PostgreSQL
database and current Zoekt shard volume. Never use a wildcard cleanup command
or remove a volume solely because its name contains `graph` or `data`.
