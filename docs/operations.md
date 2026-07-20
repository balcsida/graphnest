# Operations

Milestones 0-1 are local development only. Start the pinned fixture stack with:

```sh
docker compose -f deploy/compose/compose.yml --profile fixture up -d --wait
docker compose -f deploy/compose/compose.yml --profile fixture ps
```

The isolated Compose fixture index uses repository ID `7` and the checked-in registry at
`deploy/compose/repositories.json`. PostgreSQL must become healthy for Compose,
but the server does not connect to it until Milestone 2. Stop the stack with:

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

Build the database-only migration and listener-free indexer commands, then
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
`zoekt-git-index` executable paths, worker ID, positive free-space floor, and
these shared host paths:

```sh
GREPNEST_DATA_DIR="$PWD/.cache/durable-data" \
GREPNEST_INDEX_DIR="$PWD/.cache/durable-index" \
GREPNEST_ZOEKT_URL=http://127.0.0.1:6070 \
.cache/bin/grepnest-indexer
```

The indexer never reads the webhook secret or user/admin bearer configuration.
It runs migrations, records search node `primary`, reaps expired leases, prunes
retention and abandoned worktrees, then processes one leased job at a time.
SIGINT or SIGTERM cancels child process groups and waits for lease renewal and
cleanup goroutines before PostgreSQL closes. Git credentials exist only in the
allowlisted Git child environment and the executable's fixed askpass mode;
persisted remotes, Zoekt, logs, and process arguments remain credential-free.

The fixture and durable profiles use separate index storage and must not be run
at the same time because both publish loopback port 6070. The durable profile
does not run GrepNest containers or build images; the host commands provide the
shared index directory. The indexer exposes no HTTP listener. Recover an
interrupted worker by restarting it; PostgreSQL reaps expired leases and the
worker removes abandoned numeric-ID worktrees before claiming more work.

## Kubernetes chart boundary

The [Helm chart](../deploy/helm/grepnest/README.md) is structurally lintable and
renderable, but not currently deployable. Images are not built or published,
and it has not been cluster-tested. `make helm-lint helm-test` verifies
only rendered structure; `make image` remains an intentionally failing milestone
boundary.

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
