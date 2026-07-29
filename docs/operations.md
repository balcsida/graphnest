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
secret or OIDC credentials.
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
Server readiness remains a separate Zoekt/PostgreSQL concern. Check a
repository's graph state through the authenticated
`GET /v1/graph/repositories/{id}/status`. `not_indexed` means the repository
has no indexed SHA. `pending` has an indexed SHA but no current native graph,
`fallback` is an exact-SHA SCIP fallback, `degraded` records a failed native
job, and `ready` is current native graph data. Queries can still return
`409 graph_not_ready` when the indexed SHA changes during selection or
reauthorization.

For an incompatible existing database, restart its sole owner first. Automatic
compatibility rebuild writes a candidate and replaces the live database only
after it succeeds, so a failed automatic rebuild preserves the existing file.
Manual recovery is different: after stopping the owner, deleting
`grepnest.lbug` and its explicitly rejected `grepnest.lbug.wal` forces a fresh
rebuild from PostgreSQL. If that rebuild fails, no graph database remains and
graph requests stay unavailable/not ready until a rebuild succeeds.

- In embedded Compose mode, stop `grepnest-indexer` first. Its shared
  `grepnest-data` volume is mounted as both data and graph storage: remove only
  `/var/lib/grepnest/graph/grepnest.lbug` and
  `/var/lib/grepnest/graph/grepnest.lbug.wal` from a maintenance mount, then
  restart the indexer with:

  ```sh
  docker compose -f deploy/compose/compose.yml -f deploy/compose/durable.yml \
    -f deploy/compose/graph-embedded.yml --profile durable stop grepnest-indexer
  # Delete only the two paths above through an approved maintenance mount.
  docker compose -f deploy/compose/compose.yml -f deploy/compose/durable.yml \
    -f deploy/compose/graph-embedded.yml --profile durable up -d grepnest-indexer
  ```

  Never remove `grepnest-data`: it also holds mirrors and worktrees.
- In embedded Helm mode, identify and stop the rendered node StatefulSet and
  its exact PVC with:

  ```sh
  NODE=$(kubectl -n "$NAMESPACE" get statefulset \
    -l app.kubernetes.io/name=grepnest,app.kubernetes.io/instance="$RELEASE",app.kubernetes.io/component=node \
    -o jsonpath='{.items[0].metadata.name}')
  kubectl -n "$NAMESPACE" scale statefulset/"$NODE" --replicas=0
  NODE_PVC="data-${NODE}-0"
  kubectl -n "$NAMESPACE" get pvc "$NODE_PVC"
  ```

  The chart's default mounts make the graph database relative path
  `graph/grepnest.lbug` (and `.wal`) on `"$NODE_PVC"`; do not replace that
  shared node PVC. Mounting a PVC for maintenance is storage-class-specific,
  so this chart supplies no universal deletion command. After deleting only
  those two paths through the operator's approved maintenance mount, restart
  with `kubectl -n "$NAMESPACE" scale statefulset/"$NODE" --replicas=1` and
  wait with `kubectl -n "$NAMESPACE" rollout status statefulset/"$NODE"`.
- Separate mode owns its graph volume/PVC. For Helm, discover it and stop only
  its rendered Deployment:

  ```sh
  GRAPH=$(kubectl -n "$NAMESPACE" get deployment \
    -l app.kubernetes.io/name=grepnest,app.kubernetes.io/instance="$RELEASE",app.kubernetes.io/component=graph \
    -o jsonpath='{.items[0].metadata.name}')
  kubectl -n "$NAMESPACE" scale deployment/"$GRAPH" --replicas=0
  kubectl -n "$NAMESPACE" get pvc "$GRAPH"
  ```

  On that owned PVC, the default graph-data mount-relative paths are
  `grepnest.lbug` and `grepnest.lbug.wal`; replacing this graph-only PVC is
  also safe when the operator wants a clean rebuild. Scale the Deployment back
  to one and wait with `kubectl -n "$NAMESPACE" rollout status deployment/"$GRAPH"`.
  Compose separate mode has the same ownership boundary in `grepnest-graph`;
  stop/start only `grepnest-graph` with:

  ```sh
  docker compose -f deploy/compose/compose.yml -f deploy/compose/durable.yml \
    -f deploy/compose/graph-separate.yml --profile durable stop grepnest-graph
  # Delete graph-only paths or replace only its owned graph volume.
  docker compose -f deploy/compose/compose.yml -f deploy/compose/durable.yml \
    -f deploy/compose/graph-separate.yml --profile durable up -d grepnest-graph
  ```

  Never use a Compose-wide volume deletion.

Do not delete PostgreSQL graph artifacts merely to clear a derived-store
problem. An administrator can upload a fresh external native artifact only at
the repository's exact current indexed SHA; `.scip` ingestion remains separate
and supplies compatibility fallback, not native scanning.

External native artifacts use the server route
`POST /v1/graph/uploads?repository_id=<GitHub ID>&commit=<40 lowercase hex>`.
It requires the administrator bearer token, exactly those two query parameters,
`Content-Type: application/vnd.grepnest.graph.v1+protobuf`, and the artifact as
the binary body. This is not the graph runtime's internal bearer endpoint: the
server authorizes and stores the artifact in PostgreSQL; the graph runtime
later synchronizes it. Internal bearer transport is for runtime/query calls.
For example:

```sh
curl --fail-with-body -X POST \
  "http://127.0.0.1:8080/v1/graph/uploads?repository_id=101&commit=$GITHUB_SHA" \
  -H "Authorization: Bearer $GREPNEST_ADMIN_TOKEN" \
  -H 'Content-Type: application/vnd.grepnest.graph.v1+protobuf' \
  --data-binary @graph.v1.pb
```

The commit must be the repository's current exact indexed SHA. The route is
administrator-only; exposing the server through ingress is a separate operator
choice, while the graph runtime remains internal-only.

After a restart or upload, verify the server-visible state with:

```sh
curl --fail-with-body "$GREPNEST_SERVER_URL/v1/graph/repositories/$REPOSITORY_ID/status" \
  -H "Authorization: Bearer $GREPNEST_ADMIN_TOKEN"
```

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

## Optional OIDC operations

Permit server egress only to the configured IdP discovery, JWKS, and token
endpoints. The callback is `/auth/oidc/callback`; `GREPNEST_PUBLIC_URL` is the
authoritative HTTPS origin. Browser clients send same-origin credentials. Unsafe
session requests require that exact Origin, and GrepNest persists no refresh
tokens.

## Optional SCIM operations

Publish only the HTTPS `<GREPNEST_PUBLIC_URL>/scim/v2` endpoint and configure
`GREPNEST_SCIM_TOKEN_FILE` from a read-only secret mount. Use a dedicated
high-entropy token; it cannot access REST, MCP, or admin APIs. The OIDC link
claim must exactly equal each SCIM user's `externalId`.

Supported reconciliation filters are Users `id`, `userName`, or `externalId`
and Groups `id`, `displayName`, or `externalId`, all with `eq`. PATCH accepts
user `active`, `userName`, `displayName`, `name`, and `emails`; group PATCH
accepts `members` and `members[value eq "USER_ID"]`. Limits are 1 MiB per
body, 8 KiB per query, 16 KiB per URL, 100 PATCH operations, and the configured
maximum result count.

Rotate the token by replacing the mounted secret and restarting every server
replica; the process reads it only at startup. Deactivation and deletion deny
existing sessions and API tokens on their next request. Bulk, sorting, ETags,
passwords, `/Me`, `/.search`, root search, enterprise extensions, custom
schemas/resources, roles, and entitlements are unsupported.

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
