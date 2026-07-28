# Operations

Milestones 0-2 are local pilot development only. Start the pinned fixture stack with:

```sh
docker compose -f deploy/compose/compose.yml --profile fixture up -d --wait
docker compose -f deploy/compose/compose.yml --profile fixture ps
```

The isolated Compose fixture index uses repository ID `7` and the checked-in
registry at `deploy/compose/repositories.json`. Static mode does not connect to
PostgreSQL; durable server and indexer modes require it. Stop the stack with:

```sh
docker compose -f deploy/compose/compose.yml down
```

Run the server with the environment in the [local quick start](../README.md).
Use `GET /healthz` for process liveness. `GET /readyz` performs a bounded Zoekt
health query and returns 503 with `{"error":"unavailable"}` when Zoekt is not
ready. `GET /metrics` exposes Prometheus metrics. Search and readiness are
bounded by the configuration caps documented in the README.

Keep Zoekt private. Compose keeps Zoekt and PostgreSQL on an internal network
and publishes Zoekt only to `127.0.0.1:6070` for the host processes. The pinned Zoekt
image runs as `linux/amd64`; this is deliberate for Apple-silicon hosts, where
Docker's emulation is needed because the pinned image has no arm64 variant.

## Durable local indexer

`make postgres-integration` runs the real queue/concurrency suites. `make e2e`
starts the same pinned PostgreSQL service, resolves its reachable Compose
address, requires the database connection, and runs the GHES-compatible HTTPS
smart-Git-to-Zoekt proof. Both commands fail rather than skip when their
required PostgreSQL service is unavailable.

Build the database-only migration and indexer commands, then
create the host directories shared with the durable Zoekt profile:

```sh
go build -o .cache/bin/grepnest-migrate ./cmd/grepnest-migrate
go build -o .cache/bin/grepnest-indexer ./cmd/grepnest-indexer
mkdir -p .cache/durable-data .cache/durable-index
docker compose -f deploy/compose/compose.yml --profile durable up -d --wait postgres zoekt-durable
```

Run `grepnest-migrate` with only `GREPNEST_DATABASE_URL`. Run
`grepnest-indexer` with the database URL, Zoekt URL, GitHub web/API/upload/Git
HTTPS URLs, App ID, private-key file, optional CA file, API version, Git and
`zoekt-git-index` executable paths, worker ID, positive free-space floor,
optional `GREPNEST_MAX_REPOSITORY_BYTES` (default 5 GiB), and
these shared host paths:

```sh
GREPNEST_DATA_DIR="$PWD/.cache/durable-data" \
GREPNEST_INDEX_DIR="$PWD/.cache/durable-index" \
GREPNEST_ZOEKT_URL=http://127.0.0.1:6070 \
GREPNEST_METRICS_LISTEN_ADDRESS=127.0.0.1:9090 \
.cache/bin/grepnest-indexer
```

The indexer rejects repositories whose GHES-reported size exceeds the configured
cap before minting credentials or fetching Git data. It never reads the webhook
secret or user/admin bearer configuration.
It runs migrations, records search node `primary`, reaps expired leases, prunes
retention and abandoned worktrees, then processes one leased job at a time.
SIGINT or SIGTERM cancels child process groups and waits for lease renewal and
cleanup goroutines before PostgreSQL closes. Git credentials exist only in the
allowlisted Git child environment and the executable's fixed askpass mode;
persisted remotes, Zoekt, logs, and process arguments remain credential-free.

The fixture and durable profiles use separate index storage and must not be run
at the same time because both publish loopback port 6070. The durable profile
includes containerized indexer and scanner services when combined with a graph
overlay. Use either `graph-embedded.yml` (one graph owner inside the indexer)
or `graph-separate.yml` (one standalone graph owner); both keep graph traffic
internal and mount `GREPNEST_GRAPH_INTERNAL_SECRET_FILE` read-only. The indexer
serves only Prometheus metrics on
`/metrics`; its listen address defaults to `:9090` and should remain internal.
Recover an interrupted worker by restarting it; PostgreSQL reaps expired leases
and the worker removes abandoned numeric-ID worktrees before claiming more work.

With the durable image and secret environment from the [README](../README.md),
start embedded graph ownership with:

```sh
docker compose -f deploy/compose/compose.yml -f deploy/compose/durable.yml \
  -f deploy/compose/graph-embedded.yml --profile durable up -d --wait
```

Replace `graph-embedded.yml` with `graph-separate.yml` for standalone ownership.

## Graph operation and recovery

Graph analysis requires durable PostgreSQL. Keep PostgreSQL backups for graph
artifacts and graph-job state; LadybugDB data files are derived cache and can
be rebuilt. Use one mode at a time: `embedded` is the default single owner in
the indexer, while `separate` starts one `grepnest-graph` owner. Do not run two
owners against the same graph data directory.

The graph process opens and synchronizes its derived database before serving.
Server readiness remains a separate Zoekt/PostgreSQL concern. Check a repository's graph state through
the authenticated `GET /v1/graph/repositories/{id}/status`. `pending` means
there is no current native graph, `fallback` is an exact-SHA SCIP fallback,
`degraded` records a native processing failure, and `ready` is current native
graph data. Queries can still return `409 graph_not_ready` when the indexed
SHA changes during selection or reauthorization.

To recover a damaged or incompatible LadybugDB file, stop its sole owner,
retain PostgreSQL, remove only that derived graph file/volume, then start the
same owner again. Startup rebuilds from stored snapshots and preserves an
incompatible live file if rebuild fails. Do not delete PostgreSQL graph
artifacts merely to clear a derived-store problem. A fresh native artifact can
also be uploaded by an administrator only for the repository's exact current
indexed SHA; `.scip` ingestion remains separate and supplies compatibility
fallback, not native scanning.

Graph REST/MCP queries are bounded: context categories and impact result lists
default and cap at 100, impact depth defaults to 3 and caps at 32, trace depth
defaults to 10 and caps at 30, and administrator Cypher caps at 100 rows and
262144 encoded bytes. Use the [OpenAPI contract](openapi.yaml) for the exact
schemas and response status discriminators; only context, impact, trace, and
administrator-only Cypher are currently exposed.

Enable independently scalable native scanners in Helm with
`scanner.enabled=true` and set `scanner.replicas`; their checkouts are
ephemeral. Compose's durable graph overlays include the scanner service. The
native scanner supports Go, TypeScript, JavaScript, Java, Kotlin, and Rust.

Install the four embedded agent skills explicitly; normal MCP proxy startup
makes no checkout changes:

```sh
go build -o /tmp/grepnest-mcp ./cmd/grepnest-mcp
/tmp/grepnest-mcp install-skills --root /path/to/repository
```

This creates or updates GrepNest-owned skills under `.claude/skills/` and,
only when `.agents/` already exists, `.agents/skills/`. It refuses symlink or
unowned destinations.

## Kubernetes chart boundary

Releases publish multi-architecture images and an OCI chart. Replace each
`sha256:RELEASE_DIGEST` below with the digest copied from that GitHub Release;
it is a placeholder, not a literal digest.

```sh
docker pull ghcr.io/balcsida/grep-nest/application@sha256:RELEASE_DIGEST
docker pull ghcr.io/balcsida/grep-nest/node@sha256:RELEASE_DIGEST
helm pull oci://ghcr.io/balcsida/grep-nest/charts/grepnest --version 0.1.0
```

Verify the copied artifacts with the commands included in the GitHub Release:

```sh
gh attestation verify "oci://ghcr.io/balcsida/grep-nest/application@sha256:RELEASE_DIGEST" --repo "balcsida/grep-nest"
gh attestation verify "oci://ghcr.io/balcsida/grep-nest/node@sha256:RELEASE_DIGEST" --repo "balcsida/grep-nest"
```

The pulled OCI chart already embeds both release image digests. The source-tree
chart remains generic, so its users must supply their own image repositories
and digests. `make helm-lint helm-test` verifies source chart structure;
`make image-test` builds and smoke-tests local images.

An operator must provide external PostgreSQL, digest-pinned application and
node images, and every existing Secret documented by the chart. The chart does
not create Secrets or PostgreSQL. Its migration failure blocks install or
upgrade and leaves the failed hook Job inspectable.

Keep the singleton Zoekt Service internal. The node's Zoekt and indexer
containers share a 250Gi `ReadWriteOnce` PVC by default. Select operator-managed
SSD-backed RWO storage with `node.storage.storageClassName`. Server and node
scheduling maps are independent, as are their resource settings; use them to
place the storage-heavy node separately from stateless server replicas.

The resource defaults in the chart README are measurement starting points, not
guarantees. Actual capacity must be based on measured source corpus size, index
size, indexing duration, and query concurrency rather than repository count
alone.

Ingress and ServiceMonitor are optional. ServiceMonitor requires its CRD.
External egress isolation is also optional; before enabling it, CIDRs and DNS
selectors must cover DNS, GitHub, and PostgreSQL endpoints. Security defaults
include non-root containers, read-only root filesystems, dropped capabilities,
disabled API-token automounting, default-deny ingress, and internal-only Zoekt.
They do not fix a UID: this permits OpenShift-style arbitrary UIDs, with the
image/root-group permissions and writable PVC/`emptyDir` mounts supplying
access. Kubernetes projects the source graph secret as group-readable `0440`;
an init container validates and atomically stages the bounded secret into an
`emptyDir` as `0600` before the server or graph owner reads it. Keep that
staged path private and do not mount the source Secret into those processes.
