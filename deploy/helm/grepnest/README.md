# GrepNest Helm chart

This chart models the generic Kubernetes single-node pilot. The source-tree
chart is generic: it requires operator-supplied image repositories and
digests. A released OCI chart is a copied chart with its version and both
release image digests filled in.

The chart requires an operator-managed PostgreSQL database and never installs
PostgreSQL or creates Secrets. Supply both image repositories and immutable
`sha256:` digests in `images.application` and `images.node`; rendered images
always use `repository@digest`. `images.pullSecrets` is an optional list of
existing image-pull Secret names. Tags are metadata only and cannot replace a
digest.

## Existing Secret contracts

Create these Secrets before installation and set their names and keys in the
corresponding values. The key names below are the defaults.

| Values | Required keys | Purpose |
| --- | --- | --- |
| `secrets.runtime.name` | `database-url`, `user-token`, `admin-token` | PostgreSQL DSN and distinct user/admin bearer tokens |
| `secrets.githubApp.name` | `private-key.pem`, `webhook-secret` | GitHub App private key and webhook secret |
| `secrets.customCA.name` | `ca.crt` | Optional GitHub CA bundle; set the key with `secrets.customCA.key` |
| `images.pullSecrets[]` | Kubernetes pull-secret contract | Optional private-registry credentials |
| `ingress.tls[].secretName` | Ingress-controller TLS contract | Optional existing TLS Secret for the listed hosts |

Override the runtime key names with `databaseURLKey`, `userTokenKey`, and
`adminTokenKey`, and the GitHub App key names with `privateKeyKey` and
`webhookSecretKey`. The chart never accepts plaintext credentials in values.
Referenced object names must be Kubernetes DNS subdomains. Secret data keys
may contain letters, digits, `-`, `_`, and `.`.

## Validate and install

For a release, replace `sha256:RELEASE_DIGEST` with values copied from the
GitHub Release; they are placeholders, not literal digest values. The OCI chart
already embeds both copied release digests, so pull and install it directly:

```sh
docker pull ghcr.io/balcsida/grep-nest/application@sha256:RELEASE_DIGEST
docker pull ghcr.io/balcsida/grep-nest/node@sha256:RELEASE_DIGEST
helm pull oci://ghcr.io/balcsida/grep-nest/charts/grepnest --version 0.1.0
helm upgrade --install grepnest grepnest-0.1.0.tgz -n grepnest --create-namespace -f my-values.yaml --wait --timeout 15m
```

Use the `gh attestation verify` commands copied from the GitHub Release to
verify the images and packaged chart. Source-tree users must provide their own
image values:

Start from `values.yaml`, provide every required image, Secret, GitHub App ID,
installation ID, and repository ID value, then run:

```sh
helm lint deploy/helm/grepnest -f my-values.yaml
helm template grepnest deploy/helm/grepnest -n grepnest -f my-values.yaml
helm upgrade --install grepnest deploy/helm/grepnest -n grepnest --create-namespace -f my-values.yaml --wait --timeout 15m
```

Upgrade with the same `helm upgrade --install` command and the complete values
file. Roll back with:

```sh
helm rollback grepnest <REVISION> -n grepnest --wait --timeout 15m
```

Helm rollback does not execute the pre-install/pre-upgrade migration hook.
Before rolling the application image back after a schema-changing upgrade,
verify database backward compatibility and follow the release-specific database
rollback or restore procedure.

The `grepnest-migrate` pre-install/pre-upgrade hook must succeed before the
release proceeds. A migration failure blocks install or upgrade, and the failed
Job remains inspectable because only successful and superseded hook Jobs are
deleted. Inspect it with `kubectl get job -n grepnest` and
`kubectl logs -n grepnest job/grepnest-migrate` (adjust the generated name when
using name overrides). Correct the database or migration problem before retrying.

## Optional integrations and networking

`ingress.enabled` renders a standard Kubernetes Ingress; configure
`className`, `hosts`, and optional existing TLS Secret references. Keep the
Zoekt Service internal: it is deliberately ClusterIP-only and has no Ingress.

`node.paths.indexes` must be a child of `node.paths.data`. The indexer mounts
the PVC at the data path, while Zoekt mounts the matching child subpath
read-only at the indexes path. The chart derives Zoekt's index and listen
arguments from `node.paths.indexes` and `node.zoekt.port`; `node.service.port`
is the internal Service port.

`node.indexer.maxRepositoryBytes` defaults to 5 GiB and rejects oversized
GHES repositories before the indexer mints credentials or fetches Git data.

`monitoring.serviceMonitor.enabled` requires the
`monitoring.coreos.com/v1/ServiceMonitor` CRD. Rendering fails clearly if that
CRD is unavailable. It scrapes the server and the indexer's internal metrics
Service on `node.indexer.metricsPort`. Configure the monitoring namespace
selector in the ingress policy when Prometheus runs outside the release
namespace.

Ingress isolation is enabled by default. External egress CIDR isolation is
optional because portable NetworkPolicy cannot select DNS names. Before
enabling `networkPolicy.externalEgress.enabled`, ensure its DNS selectors and
ports reach cluster DNS and its GitHub and PostgreSQL CIDRs cover every endpoint
the deployment resolves. CIDR changes and DNS answers must remain aligned.

## Scheduling, storage, and capacity

Server and node workloads have independent `nodeSelector`, `affinity`,
`tolerations`, and `topologySpreadConstraints` maps. Migration scheduling is
also independently configurable. The node is a singleton StatefulSet: one
Zoekt container and one indexer container share a 250Gi `ReadWriteOnce` PVC.
`node.storage.storageClassName` selects operator-provided SSD-backed RWO storage
where available.

Default resource starting points are:

| Component | Requests | Limits |
| --- | --- | --- |
| Server | 250m CPU, 256Mi memory | 1 CPU, 1Gi memory |
| Zoekt | 2 CPU, 8Gi memory | 8 CPU, 24Gi memory |
| Indexer | 1 CPU, 2Gi memory | 4 CPU, 8Gi memory |

Actual capacity must be based on measured source corpus size, index size,
indexing duration, and query concurrency rather than repository count alone.
Measure the pilot, then tune resources and storage; these defaults are not
capacity guarantees.

## Security defaults

Workloads run as non-root without a fixed UID, drop all capabilities, disable
privilege escalation, use `RuntimeDefault` seccomp and read-only root filesystems,
and do not automount Kubernetes API tokens. Writable paths use PVC or
`emptyDir` volumes. The chart renders no host paths, privileged containers,
external Zoekt endpoint, Secret, or bundled database.
