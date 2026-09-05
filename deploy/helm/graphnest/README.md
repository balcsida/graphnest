# GraphNest Helm chart

This chart models the generic Kubernetes single-node pilot. The source-tree
chart is generic: it requires operator-supplied image repositories and
digests. A released OCI chart is a copied chart with its version and both
release image digests filled in.

The chart requires an operator-managed PostgreSQL database and never installs
PostgreSQL or creates Secrets. Supply image repositories and immutable
`sha256:` digests in `images.application` and `images.node`; rendered images
always use `repository@digest`. `images.pullSecrets` is an optional list of
existing image-pull Secret names. Tags are metadata only and cannot replace a
digest.

## Existing Secret contracts

Create these Secrets before installation and set their names and keys in the
corresponding values. The key names below are the defaults.

| Values | Required keys | Purpose |
| --- | --- | --- |
| `secrets.runtime.name` | `database-url` | PostgreSQL DSN |
| `secrets.githubApp.name` | `private-key.pem`, `webhook-secret` | GitHub App private key and webhook secret |
| `secrets.customCA.name` | `ca.crt` | Optional GitHub CA bundle; set the key with `secrets.customCA.key` |
| `secrets.oidc.name` | `client-secret` | OIDC client secret; set `secrets.oidc.clientSecretKey` to override |
| `secrets.githubOAuth.name` | `client-secret` | GitHub OAuth client secret; set `secrets.githubOAuth.clientSecretKey` to override |
| `secrets.mcpOAuth.name` | `sealing-key` | 32-byte key sealing GitHub user tokens kept for MCP OAuth refresh; required only with `server.sso.githubOAuth.accessSync`; set `secrets.mcpOAuth.keyKey` to override |
| `secrets.oidcCA.name` | `ca.crt` | Optional IdP CA bundle; set `secrets.oidcCA.key` to override |
| `secrets.scim.name` | `token` | Optional SCIM bearer token; set `secrets.scim.tokenKey` to override |
| `images.pullSecrets[]` | Kubernetes pull-secret contract | Optional private-registry credentials |
| `ingress.tls[].secretName` | Ingress-controller TLS contract | Optional existing TLS Secret for the listed hosts |

Override the runtime key name with `databaseURLKey`, and
the GitHub App key names with `privateKeyKey` and
`webhookSecretKey`. The chart never accepts plaintext credentials in values.
Referenced object names must be Kubernetes DNS subdomains. Secret data keys
may contain letters, digits, `-`, `_`, and `.`.

## Validate and install

Deployments created before the GraphNest rename require fresh GraphNest configuration
and a fresh GraphNest installation. This is not an in-place upgrade; GraphNest does not automatically discover or mutate previous deployment state.

For a release, replace `sha256:RELEASE_DIGEST` with values copied from the
GitHub Release; they are placeholders, not literal digest values. The OCI chart
already embeds both copied release digests, so pull and install it directly:

```sh
docker pull ghcr.io/balcsida/graphnest/application@sha256:RELEASE_DIGEST
docker pull ghcr.io/balcsida/graphnest/node@sha256:RELEASE_DIGEST
helm pull oci://ghcr.io/balcsida/graphnest/charts/graphnest --version 0.1.0
helm upgrade --install graphnest graphnest-0.1.0.tgz -n graphnest --create-namespace -f my-values.yaml --wait --timeout 15m
```

Use the `gh attestation verify` commands copied from the GitHub Release to
verify the images and packaged chart. Source-tree users must provide their own
image values:

Start from `values.yaml`, provide every required image, Secret, GitHub App ID,
installation ID, and repository ID value, then run:

```sh
helm lint deploy/helm/graphnest -f my-values.yaml
helm template graphnest deploy/helm/graphnest -n graphnest -f my-values.yaml
helm upgrade --install graphnest deploy/helm/graphnest -n graphnest --create-namespace -f my-values.yaml --wait --timeout 15m
```

For upgrades between GraphNest releases, use the same `helm upgrade --install`
command and the complete values file. Roll back with:

```sh
helm rollback graphnest <REVISION> -n graphnest --wait --timeout 15m
```

Helm rollback does not execute the pre-install/pre-upgrade migration hook.
Before rolling the application image back after a schema-changing upgrade,
verify database backward compatibility and follow the release-specific database
rollback or restore procedure.

The `graphnest-migrate` pre-install/pre-upgrade hook must succeed before the
release proceeds. A migration failure blocks install or upgrade, and the failed
Job remains inspectable because only successful and superseded hook Jobs are
deleted. Inspect it with `kubectl get job -n graphnest` and
`kubectl logs -n graphnest job/graphnest-migrate` (adjust the generated name when
using name overrides). Correct the database or migration problem before retrying.

## Optional integrations and networking

`ingress.enabled` renders a standard Kubernetes Ingress; configure
`className`, `hosts`, and optional existing TLS Secret references. Keep the
Zoekt Service internal: it is deliberately ClusterIP-only and has no Ingress.

The indexer and Zoekt share only the durable shard PVC at
`node.paths.indexes`. Archive extraction uses a separate bounded `emptyDir` at
`node.paths.workspace`; size it with `node.indexer.workspaceSizeLimit`. The
chart derives Zoekt's index and listen arguments from `node.paths.indexes` and
`node.zoekt.port`; `node.service.port` is the internal Service port.

`node.indexer.maxRepositoryBytes` defaults to 5 GiB and rejects oversized
GHES repositories before the indexer mints credentials or downloads an
archive. The 6 GiB workspace keeps a 1 GiB free-space floor, leaving the full
5 GiB repository allowance usable without making the default self-rejecting.

`monitoring.serviceMonitor.enabled` requires the
`monitoring.coreos.com/v1/ServiceMonitor` CRD. Rendering fails clearly if that
CRD is unavailable. It scrapes the server and indexer through internal
Services. Configure the monitoring namespace selector
in the ingress policy when Prometheus runs outside the release namespace.

Ingress isolation is enabled by default. External egress CIDR isolation is
optional because portable NetworkPolicy cannot select DNS names. Before
enabling `networkPolicy.externalEgress.enabled`, ensure its DNS selectors and
ports reach cluster DNS and its GitHub and PostgreSQL CIDRs cover every endpoint
the deployment resolves. CIDR changes and DNS answers must remain aligned.

Enable OIDC with `server.sso.oidc.enabled=true`, `server.sso.publicURL`,
`sessionIdle`, `sessionTTL`, `loginFlowTTL`, and OIDC `issuerURL`, `clientID`,
`scopes`, `linkClaim`, and `displayNameClaim`. Register
`<publicURL>/auth/oidc/callback` at the IdP. Reference `secrets.oidc` and,
when needed, `secrets.oidcCA`; never put their values in values files. With
external egress enabled, configure the IdP CIDRs and HTTPS port in
`networkPolicy.externalEgress.identityProvider`.

Enable GitHub OAuth with `server.sso.githubOAuth.enabled=true`, the same HTTPS
`server.sso.publicURL`, and `server.sso.githubOAuth.clientID`. Register
`<publicURL>/auth/oauth/github/callback` at GitHub and reference the existing
`secrets.githubOAuth` Secret. Its secret is mounted read-only at
`/var/run/secrets/graphnest/oauth-github/client-secret`; it is never placed in
values or a ConfigMap. GitHub OAuth uses the existing GitHub CA and egress.
Set `server.sso.githubOAuth.accessSync=true` to provision users on first
sign-in and mirror the repositories they can access through the configured
GitHub App; the referenced OAuth Secret must then hold that GitHub App's own
OAuth client secret. See the repository operations guide for the access model.

Enable the MCP authorization server with `server.sso.mcpOAuth.enabled=true`
alongside OIDC or GitHub OAuth. MCP clients then discover it from `/mcp`,
register themselves, sign the user in through the browser provider, and obtain
hour-long access tokens after an explicit consent page; no client configuration
or shared secret is needed. With GitHub access sync the server keeps each
grant's GitHub user token encrypted at rest so refreshes re-derive repository
access; that requires an existing `secrets.mcpOAuth` Secret holding 32 random
bytes (`head -c 32 /dev/urandom`), mounted read-only at
`/var/run/secrets/graphnest/mcp-oauth/sealing-key`. Rotating the key
invalidates stored GitHub tokens (grants keep their last-synced access until
the user signs in again); it does not revoke grants.

Enable SCIM with `server.scim.enabled=true`, the same HTTPS
`server.sso.publicURL`, and an existing `secrets.scim` Secret. The token is
mounted read-only at `/var/run/secrets/graphnest/scim/token`; it is never
rendered into a ConfigMap or environment value. Replace the Secret and restart
the server pods to rotate it. See the repository README for supported filters,
PATCH paths, limits, unsupported features, and the OIDC link-claim requirement.

`breakGlass.enabled=true` exposes only the disabled-by-default local recovery
routes. It provisions no user name, password, hash, salt, or Secret and never
activates because OIDC is unavailable. Provision and rotate the operator
password offline with `graphnest-admin` from the same digest-pinned application
image configured in `images.application`, then follow the repository
break-glass runbook. The chart requires OIDC or GitHub OAuth when break-glass
is enabled.

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
and do not automount Kubernetes API tokens. The node defaults `fsGroup` to
65532 so its non-root indexer can write the archive workspace and shard PVC;
set `node.podSecurityContext.fsGroup: null` where the platform assigns group
IDs itself, such as OpenShift's restricted security context constraints.
Writable paths use PVC or `emptyDir` volumes. The chart renders no host paths, privileged containers,
external Zoekt endpoint, Secret, or bundled database.
