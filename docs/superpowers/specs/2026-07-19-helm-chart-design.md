# GrepNest Full-Pilot Helm Chart Design

**Date:** 2026-07-19
**Status:** Approved

## Goal

Add one generic Kubernetes Helm chart that models GrepNest's full single-node
pilot topology without adding container builds, OpenShift resources, a bundled
PostgreSQL database, or secret material. The chart is structurally complete,
but it is not a claim that the unfinished Milestone 2 binaries or images are
deployable.

## Scope

The chart lives at `deploy/helm/grepnest` and renders:

- a `grepnest-server` Deployment and ClusterIP Service;
- an optional standard Kubernetes Ingress;
- a one-replica `grepnest-node` StatefulSet containing `zoekt-webserver` and
  `grepnest-indexer` containers;
- one internal ClusterIP Service for Zoekt;
- a shared ReadWriteOnce volume claim template mounted by both node containers;
- a pre-install/pre-upgrade migration hook Job;
- release-managed server and node ServiceAccounts with API-token automounting
  disabled;
- non-secret ConfigMaps;
- default-deny ingress and narrowly scoped allow NetworkPolicies;
- an optional server PodDisruptionBudget; and
- an optional Prometheus Operator ServiceMonitor that fails clearly when the
  CRD is unavailable.

This pass does not add an OpenShift Route, SCC, BuildConfig, ImageStream,
Template, or any `*.openshift.io` API. It does not build or publish images,
install PostgreSQL, create Secrets, contact a cluster, or resume Milestone 2
application implementation.

## Chosen Structure

Use one Helm application chart rather than an umbrella chart or library chart.
All pilot components are one release and share a small labels/naming helper.
Server, node, migration, ingress, monitoring, storage, scheduling, and policy
settings remain independently configurable through values.

The alternatives were:

1. one application chart — selected because it is the smallest release unit
   matching the single-node pilot;
2. an umbrella chart with per-component subcharts — rejected because no
   component is independently reusable yet; and
3. raw manifests plus Kustomize — rejected because the master brief requires a
   Helm chart and operators need one values surface.

## Workload Topology

`grepnest-server` defaults to two replicas so a server PDB with
`minAvailable: 1` is useful. It listens on port 8080, exposes `/healthz`,
`/readyz`, and `/metrics`, uses JSON logging from the application, and reaches
PostgreSQL, GitHub, and the internal Zoekt Service.

`grepnest-node` is fixed at one replica. Its two containers share one RWO PVC.
The indexer mounts its root at `node.paths.data`; `node.paths.indexes` must be a
child of that path, and Zoekt mounts the corresponding PVC subpath there
read-only. The chart derives Zoekt's `-index` and `-listen` arguments from
`node.paths.indexes` and `node.zoekt.port`. The Zoekt Service is ClusterIP only
and selects only this StatefulSet. It has no Ingress or external service mode.
A TCP readiness/startup probe avoids inventing an undocumented Zoekt health
endpoint.

The migration Job uses the GrepNest application image and runs
`grepnest-migrate`. Helm hook annotations run it before install and upgrade and
retain failed jobs for diagnosis. A failed migration blocks rollout. Server and
node have release-managed ServiceAccounts; the pre-install migration hook uses
the namespace default ServiceAccount because it cannot depend on ordinary
release resources, and pod-level `automountServiceAccountToken: false` prevents
API token mounting.

## Images

The chart accepts two operator-supplied images:

- a GrepNest application image containing `grepnest-server` and
  `grepnest-migrate`; and
- a node image containing `zoekt-webserver`, `zoekt-git-index`,
  `grepnest-indexer`, Git, CA certificates, and minimal init behavior.

Repository and `sha256:` digest are required at render time. Tags are optional
metadata only and never override a digest. The default values contain no fake
deployable image and no `latest` tag. Pull secrets are references only.

## Configuration and Secrets

Non-secret values are split into server and node ConfigMaps and exposed as the
documented `GREPNEST_*` environment variables. Values cover GitHub web/API/
upload/Git endpoints and API version, App ID, principal installation and
repository ID scopes, search limits, worker identity, executable paths, data
paths, minimum free space, and internal service URLs.

Credentials come only from existing objects:

- a runtime Secret supplies the PostgreSQL DSN and user/admin bearer tokens;
- a GitHub App Secret is mounted read-only and supplies the private key and
  webhook secret as files; and
- an optional CA Secret is mounted read-only and supplies a configured CA file.

Values name each Secret and key. Templates never render a Kubernetes Secret or
accept plaintext credential values. Ingress TLS also references an existing
Secret.

## Security Defaults

Every container sets `allowPrivilegeEscalation: false`, drops all capabilities,
uses `seccompProfile: RuntimeDefault`, runs as non-root without a fixed UID, and
uses a read-only root filesystem. Dedicated `emptyDir` volumes provide `/tmp`
and a writable runtime home. Workloads do not mount host paths, use privileged
ports, or receive Kubernetes API tokens. Pod-level UID/GID settings remain
configurable but unset by default because they are cluster/storage specific.

Ingress is denied by default for release pods. Explicit policies allow:

- same-release server traffic from same-namespace clients and an optional
  configured ingress-controller namespace selector;
- same-release server traffic to the Zoekt pod; and
- optional monitoring traffic to server metrics.

External egress restrictions are opt-in because portable Kubernetes
NetworkPolicy cannot address GitHub or PostgreSQL by DNS name. When enabled,
operators must supply PostgreSQL and GitHub CIDRs plus DNS selector settings.
The node pod necessarily shares policy between Zoekt and the indexer because
NetworkPolicy operates at pod, not container, granularity; only the indexer is
given database credentials.

## Scheduling, Storage, and Capacity

Server and node values independently support resources, node selectors,
affinity, tolerations, and topology spread constraints. The node defaults to a
250Gi ReadWriteOnce PVC with an operator-selectable storage class. Pilot
resource defaults match the master brief and are documented as measurement
starting points, not guarantees:

- server: 250m/256Mi requests, 1 CPU/1Gi limits;
- Zoekt: 2 CPU/8Gi requests, 8 CPU/24Gi limits; and
- indexer: 1 CPU/2Gi requests, 4 CPU/8Gi limits.

Capacity decisions must use measured corpus size, index size, indexing time,
and query concurrency rather than repository count.

## Validation and Tooling

`values.schema.json` validates required names, numeric IDs, resource shapes,
storage size, ports, and digest syntax. A chart-local CI values file uses only
non-routable example registries and dummy Secret names.

`make helm-test` runs a shell harness that:

1. lints with the CI values;
2. renders the minimal chart;
3. renders optional Ingress and ServiceMonitor paths with a synthetic CRD;
4. asserts expected workload, service, policy, hook, PVC, probe, and security
   fields;
5. asserts images are digest-pinned; and
6. rejects Secret objects, `latest`, hostPath, privileged settings, external
   Zoekt exposure, and OpenShift API groups/kinds.

Repository Go tests remain a regression gate even though this pass changes no
Go code.

## Failure Behavior

Rendering fails with an actionable message when required images, digests,
Kubernetes object references, Secret data keys, or enabled optional-feature
inputs are absent or invalid. A
requested ServiceMonitor fails when its CRD is unavailable. Migration failure
blocks installation or upgrade. The chart does not silently create weaker
credentials, disable security boundaries, or substitute placeholder images.

## Completion Criteria

This slice is complete when the chart lints and renders in both minimal and
optional-feature modes; all structural and negative security assertions pass;
the repository test suite remains green; documentation states the current
binary/image dependency honestly; and an independent review finds no chart
scope, security, or Kubernetes correctness issue.
