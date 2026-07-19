# Operations

Milestones 0-1 are local development only. Start the pinned fixture stack with:

```sh
docker compose -f deploy/compose/compose.yml up -d --wait
docker compose -f deploy/compose/compose.yml ps
```

The Compose fixture index uses repository ID `7` and the checked-in registry at
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

Keep Zoekt private. Compose joins the indexer, Zoekt, and PostgreSQL to an
internal network, publishing Zoekt only to `127.0.0.1:6070`. The pinned Zoekt
image runs as `linux/amd64`; this is deliberate for Apple-silicon hosts, where
Docker's emulation is needed because the pinned image has no arm64 variant.

## Kubernetes chart boundary

The [Helm chart](../deploy/helm/grepnest/README.md) is structurally lintable and
renderable, but not currently deployable. Images are not built or published,
and required Milestone 2 `grepnest-indexer` and `grepnest-migrate` behavior is
unfinished. It has not been cluster-tested. `make helm-lint helm-test` verifies
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
