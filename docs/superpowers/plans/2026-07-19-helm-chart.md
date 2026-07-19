# GrepNest Full-Pilot Helm Chart Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add one generic Kubernetes Helm chart that renders GrepNest's complete single-node pilot topology from operator-supplied digest-pinned images and existing Secrets.

**Architecture:** One dependency-free Helm application chart owns the server, one two-container Zoekt node, migration hook, internal services, configuration, policies, and optional integrations. A shell render harness treats `helm lint` and `helm template` output as the test boundary, validating both positive structure and forbidden security/scope regressions without contacting a cluster.

**Tech Stack:** Helm 3-compatible templates and commands, Kubernetes `apps/v1`, `batch/v1`, `networking.k8s.io/v1`, `policy/v1`, JSON Schema draft-07, POSIX shell, `awk`, `rg`, and existing GNU/BSD command-line tools.

## Global Constraints

- Implement only the generic Kubernetes full-pilot chart at `deploy/helm/grepnest`; do not add OpenShift APIs or resources.
- Do not build or publish images, install PostgreSQL, create Kubernetes Secrets, contact a cluster, or resume Milestone 2 application work.
- Use one Helm application chart with no subcharts, library chart, or new dependency.
- Require operator-supplied application and node image repositories plus `sha256:` digests; never render a tag-only image or `latest`.
- Treat image tags as optional metadata only; every rendered image is `<repository>@<digest>`.
- Read credentials only from named existing Secrets; values and templates accept no plaintext credential material.
- Fix `grepnest-node` at one replica with two containers sharing one `ReadWriteOnce` PVC mounted at `/data`.
- Keep Zoekt internal: one ClusterIP Service, no Ingress, NodePort, LoadBalancer, or external service mode.
- Run migrations as a blocking `pre-install,pre-upgrade` Helm hook using the application image.
- Disable service-account token automounting and apply the approved non-root, read-only-root-filesystem, capability-drop, RuntimeDefault security defaults to every pod and container.
- Default-deny ingress; make external egress restrictions opt-in and CIDR-based because portable NetworkPolicy cannot select DNS names.
- Render an optional standard Kubernetes Ingress and an optional ServiceMonitor; enabling ServiceMonitor without its CRD must fail clearly.
- Keep pilot resource defaults configurable and document them as measurement starting points, not capacity guarantees.
- Each task follows strict RED/GREEN, ends with an independently reviewable deliverable, and uses one atomic signed conventional commit with a single-line subject of at most 72 characters.

---

## File Map

- `deploy/helm/grepnest/Chart.yaml`: application chart metadata and Helm/Kubernetes compatibility.
- `deploy/helm/grepnest/values.yaml`: the complete public operator interface and safe defaults.
- `deploy/helm/grepnest/values.schema.json`: render-time validation and enabled-feature requirements.
- `deploy/helm/grepnest/ci/minimal-values.yaml`: non-routable image names and dummy existing-Secret references for lint/render tests.
- `deploy/helm/grepnest/ci/optional-values.yaml`: Ingress, PDB, monitoring, custom CA, scheduling, and external-egress test inputs.
- `deploy/helm/grepnest/templates/_helpers.tpl`: shared names, labels, image construction, and required-input helpers only.
- `deploy/helm/grepnest/templates/serviceaccounts.yaml`: release-managed server ServiceAccount with token automount disabled.
- `deploy/helm/grepnest/templates/configmaps.yaml`: non-secret server and node environment.
- `deploy/helm/grepnest/templates/server.yaml`: server Deployment and ClusterIP Service.
- `deploy/helm/grepnest/templates/node.yaml`: one-replica node StatefulSet, internal Zoekt Service, and RWO claim template.
- `deploy/helm/grepnest/templates/migration-job.yaml`: blocking migration hook using runtime Secret references.
- `deploy/helm/grepnest/templates/ingress.yaml`: optional generic Kubernetes Ingress only.
- `deploy/helm/grepnest/templates/pdb.yaml`: optional server PodDisruptionBudget.
- `deploy/helm/grepnest/templates/servicemonitor.yaml`: optional CRD-gated Prometheus Operator resource.
- `deploy/helm/grepnest/templates/networkpolicies.yaml`: ingress isolation and opt-in CIDR egress isolation.
- `deploy/helm/grepnest/tests/render.sh`: render, structure, optional-path, failure, and negative security assertions.
- `Makefile`: replace the Helm milestone stub with `helm-lint` and `helm-test` gates.
- `deploy/helm/grepnest/README.md`: values, existing-Secret contracts, installation, upgrade, and rollback.
- `docs/operations.md`: pilot capacity, storage, policies, and current binary/image limitation.
- `README.md`: advertise the chart without claiming Milestone 2 deployability.

## Complete Values Interface

All tasks use this exact values tree; later tasks may consume fields but must not rename or add aliases:

```yaml
nameOverride: ""
fullnameOverride: ""

images:
  application:
    repository: ""
    digest: ""
    tag: ""
    pullPolicy: IfNotPresent
  node:
    repository: ""
    digest: ""
    tag: ""
    pullPolicy: IfNotPresent
  pullSecrets: []

secrets:
  runtime:
    name: ""
    databaseURLKey: database-url
    userTokenKey: user-token
    adminTokenKey: admin-token
  githubApp:
    name: ""
    privateKeyKey: private-key.pem
    webhookSecretKey: webhook-secret
  customCA:
    name: ""
    key: ca.crt

server:
  replicas: 2
  port: 8080
  service:
    port: 8080
    annotations: {}
  config:
    githubWebURL: "https://github.example.invalid"
    githubAPIURL: "https://github.example.invalid/api/v3"
    githubUploadURL: "https://github.example.invalid/api/uploads"
    githubGitURL: "https://github.example.invalid"
    githubAPIVersion: "2022-11-28"
    githubAppID: ""
    userInstallationID: ""
    adminInstallationID: ""
    userRepositoryIDs: ""
    adminRepositoryIDs: ""
    defaultResults: 25
    maxResults: 100
    defaultContextLines: 3
    maxContextLines: 20
    defaultTimeout: 5s
    maxTimeout: 5s
    maxRequestBytes: 65536
    maxResponseBytes: 262144
  resources:
    requests: {cpu: 250m, memory: 256Mi}
    limits: {cpu: "1", memory: 1Gi}
  nodeSelector: {}
  affinity: {}
  tolerations: []
  topologySpreadConstraints: []
  podSecurityContext: {}
  podAnnotations: {}
  pdb:
    enabled: true
    minAvailable: 1

node:
  replicaCount: 1
  zoekt:
    port: 6070
    executable: /usr/local/bin/zoekt-webserver
    args: ["-index", "/data/index", "-listen", ":6070", "-rpc", "-html=false"]
    resources:
      requests: {cpu: "2", memory: 8Gi}
      limits: {cpu: "8", memory: 24Gi}
  indexer:
    executable: /usr/local/bin/grepnest-indexer
    gitExecutable: /usr/bin/git
    zoektGitIndexExecutable: /usr/local/bin/zoekt-git-index
    minFreeBytes: 10737418240
    resources:
      requests: {cpu: "1", memory: 2Gi}
      limits: {cpu: "4", memory: 8Gi}
  paths:
    data: /data
    indexes: /data/index
  service:
    port: 6070
    annotations: {}
  storage:
    size: 250Gi
    accessModes: [ReadWriteOnce]
    storageClassName: ""
    annotations: {}
  nodeSelector: {}
  affinity: {}
  tolerations: []
  topologySpreadConstraints: []
  podSecurityContext: {}
  podAnnotations: {}

migration:
  executable: /usr/local/bin/grepnest-migrate
  backoffLimit: 1
  activeDeadlineSeconds: 600
  resources:
    requests: {cpu: 100m, memory: 128Mi}
    limits: {cpu: 500m, memory: 512Mi}
  nodeSelector: {}
  affinity: {}
  tolerations: []

ingress:
  enabled: false
  className: ""
  annotations: {}
  hosts: []
  tls: []

monitoring:
  serviceMonitor:
    enabled: false
    interval: 30s
    scrapeTimeout: 10s
    labels: {}
    namespaceSelector: {}

networkPolicy:
  enabled: true
  serverIngress:
    ingressControllerNamespaceSelector: {}
    monitoringNamespaceSelector: {}
  externalEgress:
    enabled: false
    dns:
      namespaceSelector: {matchLabels: {kubernetes.io/metadata.name: kube-system}}
      podSelector: {matchLabels: {k8s-app: kube-dns}}
      ports: [53]
    postgresql:
      cidrs: []
      port: 5432
    github:
      cidrs: []
      ports: [443]
```

The chart maps these exact non-secret environment names:

```text
server ConfigMap:
GREPNEST_LISTEN_ADDRESS, GREPNEST_ZOEKT_URL,
GREPNEST_GITHUB_WEB_URL, GREPNEST_GITHUB_API_URL,
GREPNEST_GITHUB_UPLOAD_URL, GREPNEST_GITHUB_GIT_URL,
GREPNEST_GITHUB_API_VERSION, GREPNEST_GITHUB_APP_ID,
GREPNEST_USER_INSTALLATION_ID, GREPNEST_ADMIN_INSTALLATION_ID,
GREPNEST_USER_REPOSITORY_IDS, GREPNEST_ADMIN_REPOSITORY_IDS,
GREPNEST_DEFAULT_RESULTS, GREPNEST_MAX_RESULTS,
GREPNEST_DEFAULT_CONTEXT_LINES, GREPNEST_MAX_CONTEXT_LINES,
GREPNEST_DEFAULT_TIMEOUT, GREPNEST_MAX_TIMEOUT,
GREPNEST_MAX_REQUEST_BYTES, GREPNEST_MAX_RESPONSE_BYTES,
GREPNEST_GITHUB_PRIVATE_KEY_FILE, GREPNEST_GITHUB_WEBHOOK_SECRET_FILE,
GREPNEST_GITHUB_CA_FILE

node ConfigMap:
GREPNEST_ZOEKT_URL, GREPNEST_GITHUB_WEB_URL,
GREPNEST_GITHUB_API_URL, GREPNEST_GITHUB_UPLOAD_URL,
GREPNEST_GITHUB_GIT_URL, GREPNEST_GITHUB_API_VERSION,
GREPNEST_GITHUB_APP_ID, GREPNEST_GITHUB_PRIVATE_KEY_FILE,
GREPNEST_GITHUB_CA_FILE,
GREPNEST_DATA_DIR, GREPNEST_INDEX_DIR, GREPNEST_GIT_PATH,
GREPNEST_ZOEKT_GIT_INDEX, GREPNEST_MIN_FREE_BYTES

Secret-backed environment:
GREPNEST_DATABASE_URL, GREPNEST_USER_TOKEN, GREPNEST_ADMIN_TOKEN

Downward API:
GREPNEST_WORKER_ID = metadata.name
```

### Task 1: Chart Contract, Schema, and Shared Helpers

**Files:**
- Create: `deploy/helm/grepnest/Chart.yaml`
- Create: `deploy/helm/grepnest/values.yaml`
- Create: `deploy/helm/grepnest/values.schema.json`
- Create: `deploy/helm/grepnest/ci/minimal-values.yaml`
- Create: `deploy/helm/grepnest/templates/_helpers.tpl`

**Interfaces:**
- Consumes: Helm 3 and the Complete Values Interface above.
- Produces: chart `grepnest` version `0.1.0`, app version `0.0.0-pilot`, helpers `grepnest.name`, `grepnest.fullname`, `grepnest.labels`, `grepnest.selectorLabels`, `grepnest.serverName`, `grepnest.nodeName`, `grepnest.image`, and schema-validated operator inputs.

- [ ] **Step 1: RED — prove the chart does not exist**

Run:

```bash
helm lint deploy/helm/grepnest -f deploy/helm/grepnest/ci/minimal-values.yaml
```

Expected: FAIL with `no such file or directory` or `Chart.yaml file is missing`.

- [ ] **Step 2: GREEN — add metadata and the complete default values**

Create `Chart.yaml`:

```yaml
apiVersion: v2
name: grepnest
description: Generic Kubernetes chart for the GrepNest single-node pilot
type: application
version: 0.1.0
appVersion: 0.0.0-pilot
kubeVersion: ">=1.25.0-0"
```

Create `values.yaml` with the exact Complete Values Interface above. Do not add dependencies or a `dependencies:` key.

Create `ci/minimal-values.yaml`:

```yaml
images:
  application:
    repository: registry.example.invalid/grepnest/application
    digest: sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa
  node:
    repository: registry.example.invalid/grepnest/node
    digest: sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb
secrets:
  runtime: {name: grepnest-runtime}
  githubApp: {name: grepnest-github-app}
server:
  config:
    githubAppID: "12345"
    userInstallationID: "67890"
    adminInstallationID: "67890"
    userRepositoryIDs: "101,102"
    adminRepositoryIDs: "101,102,103"
```

- [ ] **Step 3: GREEN — add exact schema constraints**

Create a draft-07 object schema with `additionalProperties: false` at the root and every object, mirroring every key in `values.yaml`. Require these leaf values:

```json
{
  "$schema": "http://json-schema.org/draft-07/schema#",
  "type": "object",
  "required": ["images", "secrets", "server", "node", "migration", "ingress", "monitoring", "networkPolicy"],
  "properties": {
    "nameOverride": {"type": "string"},
    "fullnameOverride": {"type": "string"},
    "images": {
      "type": "object", "additionalProperties": false,
      "required": ["application", "node", "pullSecrets"],
      "properties": {
        "application": {"$ref": "#/definitions/image"},
        "node": {"$ref": "#/definitions/image"},
        "pullSecrets": {"type": "array", "items": {"type": "string", "minLength": 1}, "uniqueItems": true}
      }
    },
    "secrets": {
      "type": "object", "additionalProperties": false,
      "required": ["runtime", "githubApp", "customCA"],
      "properties": {
        "runtime": {"$ref": "#/definitions/runtimeSecret"},
        "githubApp": {"$ref": "#/definitions/githubSecret"},
        "customCA": {"$ref": "#/definitions/optionalSecret"}
      }
    }
  },
  "definitions": {
    "digest": {"type": "string", "pattern": "^sha256:[a-f0-9]{64}$"},
    "dnsLabel": {"type": "string", "minLength": 1, "maxLength": 253},
    "positiveInteger": {"type": "integer", "minimum": 1},
    "quantity": {"type": "string", "minLength": 1},
    "image": {
      "type": "object", "additionalProperties": false,
      "required": ["repository", "digest", "tag", "pullPolicy"],
      "properties": {
        "repository": {"type": "string", "minLength": 1, "not": {"pattern": "@|:latest$"}},
        "digest": {"$ref": "#/definitions/digest"},
        "tag": {"type": "string", "not": {"const": "latest"}},
        "pullPolicy": {"enum": ["Always", "IfNotPresent", "Never"]}
      }
    },
    "runtimeSecret": {
      "type": "object", "additionalProperties": false,
      "required": ["name", "databaseURLKey", "userTokenKey", "adminTokenKey"],
      "properties": {
        "name": {"$ref": "#/definitions/dnsLabel"},
        "databaseURLKey": {"$ref": "#/definitions/dnsLabel"},
        "userTokenKey": {"$ref": "#/definitions/dnsLabel"},
        "adminTokenKey": {"$ref": "#/definitions/dnsLabel"}
      }
    },
    "githubSecret": {
      "type": "object", "additionalProperties": false,
      "required": ["name", "privateKeyKey", "webhookSecretKey"],
      "properties": {
        "name": {"$ref": "#/definitions/dnsLabel"},
        "privateKeyKey": {"$ref": "#/definitions/dnsLabel"},
        "webhookSecretKey": {"$ref": "#/definitions/dnsLabel"}
      }
    },
    "optionalSecret": {
      "type": "object", "additionalProperties": false,
      "required": ["name", "key"],
      "properties": {"name": {"type": "string"}, "key": {"$ref": "#/definitions/dnsLabel"}}
    }
  }
}
```

Add these exact remaining root properties before `definitions`; every named
object has `additionalProperties: false` unless its type below is explicitly a
free-form map:

```json
"server": {
  "type": "object", "additionalProperties": false,
  "required": ["replicas", "port", "service", "config", "resources", "nodeSelector", "affinity", "tolerations", "topologySpreadConstraints", "podSecurityContext", "podAnnotations", "pdb"],
  "properties": {
    "replicas": {"type": "integer", "minimum": 1},
    "port": {"$ref": "#/definitions/port"},
    "service": {"$ref": "#/definitions/service"},
    "config": {
      "type": "object", "additionalProperties": false,
      "required": ["githubWebURL", "githubAPIURL", "githubUploadURL", "githubGitURL", "githubAPIVersion", "githubAppID", "userInstallationID", "adminInstallationID", "userRepositoryIDs", "adminRepositoryIDs", "defaultResults", "maxResults", "defaultContextLines", "maxContextLines", "defaultTimeout", "maxTimeout", "maxRequestBytes", "maxResponseBytes"],
      "properties": {
        "githubWebURL": {"$ref": "#/definitions/url"},
        "githubAPIURL": {"$ref": "#/definitions/url"},
        "githubUploadURL": {"$ref": "#/definitions/url"},
        "githubGitURL": {"$ref": "#/definitions/url"},
        "githubAPIVersion": {"type": "string", "minLength": 1},
        "githubAppID": {"$ref": "#/definitions/id"},
        "userInstallationID": {"$ref": "#/definitions/id"},
        "adminInstallationID": {"$ref": "#/definitions/id"},
        "userRepositoryIDs": {"type": "string", "pattern": "^[0-9]+(,[0-9]+)*$"},
        "adminRepositoryIDs": {"type": "string", "pattern": "^[0-9]+(,[0-9]+)*$"},
        "defaultResults": {"type": "integer", "minimum": 1, "maximum": 100},
        "maxResults": {"type": "integer", "minimum": 1, "maximum": 100},
        "defaultContextLines": {"type": "integer", "minimum": 0, "maximum": 20},
        "maxContextLines": {"type": "integer", "minimum": 0, "maximum": 20},
        "defaultTimeout": {"$ref": "#/definitions/duration"},
        "maxTimeout": {"$ref": "#/definitions/duration"},
        "maxRequestBytes": {"type": "integer", "minimum": 1, "maximum": 65536},
        "maxResponseBytes": {"type": "integer", "minimum": 1, "maximum": 262144}
      }
    },
    "resources": {"$ref": "#/definitions/resources"},
    "nodeSelector": {"$ref": "#/definitions/map"},
    "affinity": {"$ref": "#/definitions/map"},
    "tolerations": {"$ref": "#/definitions/array"},
    "topologySpreadConstraints": {"$ref": "#/definitions/array"},
    "podSecurityContext": {"$ref": "#/definitions/map"},
    "podAnnotations": {"$ref": "#/definitions/stringMap"},
    "pdb": {
      "type": "object", "additionalProperties": false,
      "required": ["enabled", "minAvailable"],
      "properties": {"enabled": {"type": "boolean"}, "minAvailable": {"const": 1}}
    }
  }
},
"node": {
  "type": "object", "additionalProperties": false,
  "required": ["replicaCount", "zoekt", "indexer", "paths", "service", "storage", "nodeSelector", "affinity", "tolerations", "topologySpreadConstraints", "podSecurityContext", "podAnnotations"],
  "properties": {
    "replicaCount": {"const": 1},
    "zoekt": {
      "type": "object", "additionalProperties": false,
      "required": ["port", "executable", "args", "resources"],
      "properties": {"port": {"$ref": "#/definitions/port"}, "executable": {"$ref": "#/definitions/path"}, "args": {"type": "array", "items": {"type": "string"}}, "resources": {"$ref": "#/definitions/resources"}}
    },
    "indexer": {
      "type": "object", "additionalProperties": false,
      "required": ["executable", "gitExecutable", "zoektGitIndexExecutable", "minFreeBytes", "resources"],
      "properties": {"executable": {"$ref": "#/definitions/path"}, "gitExecutable": {"$ref": "#/definitions/path"}, "zoektGitIndexExecutable": {"$ref": "#/definitions/path"}, "minFreeBytes": {"$ref": "#/definitions/positiveInteger"}, "resources": {"$ref": "#/definitions/resources"}}
    },
    "paths": {
      "type": "object", "additionalProperties": false,
      "required": ["data", "indexes"],
      "properties": {"data": {"$ref": "#/definitions/path"}, "indexes": {"$ref": "#/definitions/path"}}
    },
    "service": {"$ref": "#/definitions/service"},
    "storage": {
      "type": "object", "additionalProperties": false,
      "required": ["size", "accessModes", "storageClassName", "annotations"],
      "properties": {"size": {"$ref": "#/definitions/quantity"}, "accessModes": {"type": "array", "minItems": 1, "maxItems": 1, "items": {"const": "ReadWriteOnce"}}, "storageClassName": {"type": "string"}, "annotations": {"$ref": "#/definitions/stringMap"}}
    },
    "nodeSelector": {"$ref": "#/definitions/map"},
    "affinity": {"$ref": "#/definitions/map"},
    "tolerations": {"$ref": "#/definitions/array"},
    "topologySpreadConstraints": {"$ref": "#/definitions/array"},
    "podSecurityContext": {"$ref": "#/definitions/map"},
    "podAnnotations": {"$ref": "#/definitions/stringMap"}
  }
},
"migration": {
  "type": "object", "additionalProperties": false,
  "required": ["executable", "backoffLimit", "activeDeadlineSeconds", "resources", "nodeSelector", "affinity", "tolerations"],
  "properties": {"executable": {"$ref": "#/definitions/path"}, "backoffLimit": {"type": "integer", "minimum": 0}, "activeDeadlineSeconds": {"$ref": "#/definitions/positiveInteger"}, "resources": {"$ref": "#/definitions/resources"}, "nodeSelector": {"$ref": "#/definitions/map"}, "affinity": {"$ref": "#/definitions/map"}, "tolerations": {"$ref": "#/definitions/array"}}
},
"ingress": {
  "type": "object", "additionalProperties": false,
  "required": ["enabled", "className", "annotations", "hosts", "tls"],
  "properties": {"enabled": {"type": "boolean"}, "className": {"type": "string"}, "annotations": {"$ref": "#/definitions/stringMap"}, "hosts": {"type": "array", "items": {"$ref": "#/definitions/ingressHost"}}, "tls": {"type": "array", "items": {"$ref": "#/definitions/ingressTLS"}}}
},
"monitoring": {
  "type": "object", "additionalProperties": false,
  "required": ["serviceMonitor"],
  "properties": {"serviceMonitor": {"type": "object", "additionalProperties": false, "required": ["enabled", "interval", "scrapeTimeout", "labels", "namespaceSelector"], "properties": {"enabled": {"type": "boolean"}, "interval": {"$ref": "#/definitions/duration"}, "scrapeTimeout": {"$ref": "#/definitions/duration"}, "labels": {"$ref": "#/definitions/stringMap"}, "namespaceSelector": {"$ref": "#/definitions/map"}}}}
},
"networkPolicy": {
  "type": "object", "additionalProperties": false,
  "required": ["enabled", "serverIngress", "externalEgress"],
  "properties": {
    "enabled": {"type": "boolean"},
    "serverIngress": {"type": "object", "additionalProperties": false, "required": ["ingressControllerNamespaceSelector", "monitoringNamespaceSelector"], "properties": {"ingressControllerNamespaceSelector": {"$ref": "#/definitions/optionalSelector"}, "monitoringNamespaceSelector": {"$ref": "#/definitions/optionalSelector"}}},
    "externalEgress": {
      "type": "object", "additionalProperties": false,
      "required": ["enabled", "dns", "postgresql", "github"],
      "properties": {
        "enabled": {"type": "boolean"},
        "dns": {"type": "object", "additionalProperties": false, "required": ["namespaceSelector", "podSelector", "ports"], "properties": {"namespaceSelector": {"$ref": "#/definitions/optionalSelector"}, "podSelector": {"$ref": "#/definitions/optionalSelector"}, "ports": {"$ref": "#/definitions/ports"}}},
        "postgresql": {"type": "object", "additionalProperties": false, "required": ["cidrs", "port"], "properties": {"cidrs": {"$ref": "#/definitions/cidrs"}, "port": {"$ref": "#/definitions/port"}}},
        "github": {"type": "object", "additionalProperties": false, "required": ["cidrs", "ports"], "properties": {"cidrs": {"$ref": "#/definitions/cidrs"}, "ports": {"$ref": "#/definitions/ports"}}}
      }
    }
  }
}
```

Add these definitions alongside the earlier definitions:

```json
"port": {"type": "integer", "minimum": 1, "maximum": 65535},
"id": {"type": "string", "pattern": "^[0-9]+$"},
"url": {"type": "string", "pattern": "^https?://[^ ]+$"},
"duration": {"type": "string", "pattern": "^[0-9]+(ms|s|m|h)$"},
"path": {"type": "string", "pattern": "^/"},
"quantity": {"type": "string", "minLength": 1},
"map": {"type": "object"},
"stringMap": {"type": "object", "additionalProperties": {"type": "string"}},
"array": {"type": "array"},
"ports": {"type": "array", "minItems": 1, "uniqueItems": true, "items": {"$ref": "#/definitions/port"}},
"selectorLabels": {"type": "object", "minProperties": 1, "additionalProperties": {"type": "string"}},
"nonEmptySelector": {"type": "object", "additionalProperties": false, "required": ["matchLabels"], "properties": {"matchLabels": {"$ref": "#/definitions/selectorLabels"}}},
"optionalSelector": {"oneOf": [{"type": "object", "maxProperties": 0}, {"$ref": "#/definitions/nonEmptySelector"}]},
"cidrs": {"type": "array", "uniqueItems": true, "items": {"$ref": "#/definitions/network"}},
"network": {"oneOf": [
  {"type": "object", "additionalProperties": false, "required": ["address", "prefix"], "properties": {"address": {"type": "string", "format": "ipv4"}, "prefix": {"type": "integer", "minimum": 1, "maximum": 32}}},
  {"type": "object", "additionalProperties": false, "required": ["address", "prefix"], "properties": {"address": {"type": "string", "format": "ipv6"}, "prefix": {"type": "integer", "minimum": 1, "maximum": 128}}}
]},
"resources": {
  "type": "object", "additionalProperties": false,
  "required": ["requests", "limits"],
  "properties": {"requests": {"$ref": "#/definitions/resourcePair"}, "limits": {"$ref": "#/definitions/resourcePair"}}
},
"resourcePair": {
  "type": "object", "additionalProperties": false,
  "required": ["cpu", "memory"],
  "properties": {"cpu": {"$ref": "#/definitions/quantity"}, "memory": {"$ref": "#/definitions/quantity"}}
},
"service": {
  "type": "object", "additionalProperties": false,
  "required": ["port", "annotations"],
  "properties": {"port": {"$ref": "#/definitions/port"}, "annotations": {"$ref": "#/definitions/stringMap"}}
},
"ingressHost": {
  "type": "object", "additionalProperties": false,
  "required": ["host", "paths"],
  "properties": {"host": {"type": "string", "minLength": 1}, "paths": {"type": "array", "minItems": 1, "items": {"$ref": "#/definitions/ingressPath"}}}
},
"ingressPath": {
  "type": "object", "additionalProperties": false,
  "required": ["path", "pathType"],
  "properties": {"path": {"$ref": "#/definitions/path"}, "pathType": {"enum": ["Exact", "Prefix", "ImplementationSpecific"]}}
},
"ingressTLS": {
  "type": "object", "additionalProperties": false,
  "required": ["secretName", "hosts"],
  "properties": {"secretName": {"$ref": "#/definitions/dnsLabel"}, "hosts": {"type": "array", "minItems": 1, "items": {"type": "string", "minLength": 1}}}
}
```

Add root `allOf` conditions: when `ingress.enabled` is true require
`className` length 1 and `hosts` `minItems: 1`; when
`networkPolicy.externalEgress.enabled` is true require both PostgreSQL and
GitHub `cidrs` `minItems: 1` and require both DNS selectors to match
`nonEmptySelector`. Keep `server.pdb.minAvailable` fixed at 1; in the
PDB template call `fail "server.replicas must exceed server.pdb.minAvailable"`
when the PDB is enabled and `server.replicas` is 1, because JSON Schema
cannot compare numeric siblings.

- [ ] **Step 4: GREEN — add only shared naming and image helpers**

Use standard 63-character truncation for names and these exact helper contracts:

```gotemplate
{{- define "grepnest.image" -}}
{{- printf "%s@%s" (required "image repository is required" .repository) (required "image sha256 digest is required" .digest) -}}
{{- end }}
{{- define "grepnest.serverName" -}}{{ include "grepnest.fullname" . }}-server{{- end }}
{{- define "grepnest.nodeName" -}}{{ include "grepnest.fullname" . }}-node{{- end }}
```

`grepnest.labels` emits `helm.sh/chart`, `app.kubernetes.io/name`, `app.kubernetes.io/instance`, `app.kubernetes.io/version`, and `app.kubernetes.io/managed-by`. `grepnest.selectorLabels` emits only name and instance; workloads add `app.kubernetes.io/component: server|node|migration`.

- [ ] **Step 5: GREEN — verify valid and invalid contracts**

Run:

```bash
helm lint deploy/helm/grepnest -f deploy/helm/grepnest/ci/minimal-values.yaml
helm lint deploy/helm/grepnest --set images.application.repository=x --set images.application.digest=latest
```

Expected: first command PASS; second command FAIL and name `images.node.repository` plus invalid digest syntax.

- [ ] **Step 6: Commit the chart contract**

```bash
git status --short
git add deploy/helm/grepnest/Chart.yaml deploy/helm/grepnest/values.yaml deploy/helm/grepnest/values.schema.json deploy/helm/grepnest/ci/minimal-values.yaml deploy/helm/grepnest/templates/_helpers.tpl
git commit -S -m "feat(helm): define chart contract"
```

### Task 2: Server Workload and Existing-Secret Wiring

**Files:**
- Create: `deploy/helm/grepnest/templates/serviceaccounts.yaml`
- Create: `deploy/helm/grepnest/templates/configmaps.yaml`
- Create: `deploy/helm/grepnest/templates/server.yaml`
- Create: `deploy/helm/grepnest/templates/pdb.yaml`

**Interfaces:**
- Consumes: `images.application`, `secrets.*`, `server.*`, and helpers from Task 1.
- Produces: `<release>-grepnest-server` Deployment/Service/ServiceAccount/ConfigMap and optional PDB; Service port name `http`; selector component `server`.

- [ ] **Step 1: RED — assert the server contract before templates exist**

Run:

```bash
helm template pilot deploy/helm/grepnest -n grepnest -f deploy/helm/grepnest/ci/minimal-values.yaml > /tmp/grepnest-server.yaml
rg -n '^kind: Deployment$|name: pilot-grepnest-server$|automountServiceAccountToken: false|containerPort: 8080|path: /readyz|readOnlyRootFilesystem: true' /tmp/grepnest-server.yaml
```

Expected: render exits 0, then `rg` FAILS because no server resources exist.

- [ ] **Step 2: GREEN — render server configuration and ServiceAccount**

Add one server ConfigMap using the exact server ConfigMap environment mapping above. Derive `GREPNEST_ZOEKT_URL` as `http://<release>-grepnest-zoekt:<node.service.port>`, set the two secret file paths to `/var/run/secrets/grepnest/github/private-key.pem` and `/var/run/secrets/grepnest/github/webhook-secret`, and emit `GREPNEST_GITHUB_CA_FILE=/var/run/secrets/grepnest/ca/ca.crt` only when `secrets.customCA.name` is non-empty. Do not emit the Milestone 1 fixture registry or repository-name lists; the full pilot is database-backed.

Create the release-managed server ServiceAccount with:

```yaml
automountServiceAccountToken: false
```

Server and node have release-managed ServiceAccounts; the node ServiceAccount is
rendered with its workload in Task 3. The pre-install migration hook uses the
namespace default ServiceAccount because it cannot depend on ordinary release
resources, and pod-level `automountServiceAccountToken: false` prevents API
token mounting.

- [ ] **Step 3: GREEN — render the Deployment and ClusterIP Service**

Use this security/probe skeleton exactly in the server container:

```yaml
securityContext:
  allowPrivilegeEscalation: false
  capabilities: {drop: [ALL]}
  readOnlyRootFilesystem: true
  runAsNonRoot: true
  seccompProfile: {type: RuntimeDefault}
ports:
  - {name: http, containerPort: 8080, protocol: TCP}
livenessProbe:
  httpGet: {path: /healthz, port: http}
readinessProbe:
  httpGet: {path: /readyz, port: http}
```

Set pod `securityContext.seccompProfile.type: RuntimeDefault`, merge `server.podSecurityContext` without setting a UID/GID by default, set pod and ServiceAccount token automount false, and use the digest-only application image helper. Load non-secrets with `envFrom.configMapRef`; load the three runtime Secret keys with individual `secretKeyRef` entries. Mount only the private-key and webhook-secret keys from the GitHub App Secret using `items`, read-only. When configured, mount only the selected CA key read-only at `/var/run/secrets/grepnest/ca/ca.crt`. Add `emptyDir` volumes for `/tmp` and `/var/run/grepnest`; set `HOME=/var/run/grepnest`. Wire resources, image pull secrets, annotations, nodeSelector, affinity, tolerations, and topology spread directly from values.

The Service is always `ClusterIP`, selects only component `server`, maps `server.service.port` to named target `http`, and carries only `server.service.annotations`.

- [ ] **Step 4: GREEN — render the useful optional PDB**

When `server.pdb.enabled`, first fail with
`server.replicas must exceed server.pdb.minAvailable` unless replicas exceed
one, then render `policy/v1` PodDisruptionBudget selecting the server with
`minAvailable: 1`. Do not support `maxUnavailable` or a second PDB strategy.

- [ ] **Step 5: GREEN — verify server structure and isolation**

Run:

```bash
helm template pilot deploy/helm/grepnest -n grepnest -f deploy/helm/grepnest/ci/minimal-values.yaml > /tmp/grepnest-server.yaml
rg -n 'kind: Deployment|kind: Service|kind: PodDisruptionBudget|registry\.example\.invalid/grepnest/application@sha256:a{64}|automountServiceAccountToken: false|path: /healthz|path: /readyz|readOnlyRootFilesystem: true|drop:|RuntimeDefault' /tmp/grepnest-server.yaml
! rg -n '^kind: Secret$|runAsUser:|privileged: true|hostPath:|:latest' /tmp/grepnest-server.yaml
```

Expected: every positive pattern matches; forbidden scan exits 0 because `rg` finds nothing.

- [ ] **Step 6: Commit the server slice**

```bash
git status --short
git add deploy/helm/grepnest/templates/serviceaccounts.yaml deploy/helm/grepnest/templates/configmaps.yaml deploy/helm/grepnest/templates/server.yaml deploy/helm/grepnest/templates/pdb.yaml
git commit -S -m "feat(helm): add server workload"
```

### Task 3: Single-Replica Zoekt Node and Persistent Storage

**Files:**
- Modify: `deploy/helm/grepnest/templates/configmaps.yaml`
- Create: `deploy/helm/grepnest/templates/node.yaml`

**Interfaces:**
- Consumes: `images.node`, `secrets.runtime`, `secrets.githubApp`, `secrets.customCA`, and `node.*`.
- Produces: `<release>-grepnest-node` StatefulSet, headless governing Service named `<release>-grepnest-node`, internal ClusterIP Zoekt Service named `<release>-grepnest-zoekt`, and claim template `data`.

- [ ] **Step 1: RED — assert the node topology before implementation**

Run:

```bash
helm template pilot deploy/helm/grepnest -n grepnest -f deploy/helm/grepnest/ci/minimal-values.yaml > /tmp/grepnest-node.yaml
rg -n '^kind: StatefulSet$|name: zoekt-webserver$|name: grepnest-indexer$|claimName: data|storage: 250Gi|clusterIP: None|name: pilot-grepnest-zoekt$' /tmp/grepnest-node.yaml
```

Expected: FAIL because the StatefulSet and Zoekt Services do not exist.

- [ ] **Step 2: GREEN — add node non-secret configuration**

Extend the ConfigMap template with the exact node environment mapping. Use the internal Zoekt URL, configured GitHub endpoints, executable/path values, and a decimal string for `GREPNEST_MIN_FREE_BYTES`. Do not place the database URL, tokens, private keys, webhook secrets, or CA contents in the ConfigMap.

- [ ] **Step 3: GREEN — render the one-pod, two-container StatefulSet**

Set `replicas: 1`, `serviceName: <release>-grepnest-node`, `podManagementPolicy: OrderedReady`, and `updateStrategy.type: RollingUpdate`. Both containers use the same digest-pinned node image and common security context from Task 2.

`zoekt-webserver` runs `[node.zoekt.executable]` with `node.zoekt.args`, exposes named TCP port `zoekt`, mounts `data` read-only at `/data/index` using `subPath: index`, and has TCP startup/readiness probes on `zoekt`. It receives no runtime Secret env or GitHub App mount.

`grepnest-indexer` runs `[node.indexer.executable]`, loads the node ConfigMap, receives only `GREPNEST_DATABASE_URL` from the runtime Secret, obtains `GREPNEST_WORKER_ID` from `metadata.name`, mounts the GitHub private key plus optional CA read-only, and mounts `data` read-write at `/data`. The webhook secret remains server-only. Both containers get separate `/tmp` and runtime-home `emptyDir` mounts; no process supervisor is added.

Render the claim template exactly as:

```yaml
volumeClaimTemplates:
  - metadata:
      name: data
    spec:
      accessModes: [ReadWriteOnce]
      resources:
        requests:
          storage: 250Gi
```

Add `storageClassName` only when non-empty and merge configured PVC annotations. Wire node pod annotations, scheduling, resources, and pull secrets directly from values.

- [ ] **Step 4: GREEN — keep Zoekt internal**

Render a headless Service (`clusterIP: None`) selecting component `node` for StatefulSet identity. Render a separate ClusterIP-only Zoekt Service selecting only component `node`, with port name `zoekt` and target `zoekt`. Do not template any service-type value.

- [ ] **Step 5: GREEN — verify node storage, credentials, and exposure**

Run:

```bash
helm template pilot deploy/helm/grepnest -n grepnest -f deploy/helm/grepnest/ci/minimal-values.yaml > /tmp/grepnest-node.yaml
test "$(rg -c '^kind: StatefulSet$' /tmp/grepnest-node.yaml)" -eq 1
rg -n 'replicas: 1|name: zoekt-webserver|name: grepnest-indexer|readOnly: true|mountPath: /data|storage: 250Gi|ReadWriteOnce|tcpSocket:|clusterIP: None|name: pilot-grepnest-zoekt' /tmp/grepnest-node.yaml
test "$(rg -c 'name: GREPNEST_DATABASE_URL' /tmp/grepnest-node.yaml)" -eq 2
if sed -n '/name: zoekt-webserver/,/name: grepnest-indexer/p' /tmp/grepnest-node.yaml | rg -q 'GREPNEST_DATABASE_URL|github-app'; then exit 1; fi
! rg -n 'type: (NodePort|LoadBalancer)|kind: Ingress[[:space:]]*$.*zoekt' /tmp/grepnest-node.yaml
```

Expected: PASS; the two database URL occurrences are server plus indexer, never Zoekt.

- [ ] **Step 6: Commit the node slice**

```bash
git status --short
git add deploy/helm/grepnest/templates/configmaps.yaml deploy/helm/grepnest/templates/node.yaml
git commit -S -m "feat(helm): add single-node Zoekt workload"
```

### Task 4: Blocking Migration Hook

**Files:**
- Create: `deploy/helm/grepnest/templates/migration-job.yaml`

**Interfaces:**
- Consumes: application image, runtime Secret database URL key, `migration.*`, and the namespace default ServiceAccount.
- Produces: `<release>-grepnest-migrate` `batch/v1` Job with Helm hook lifecycle.

- [ ] **Step 1: RED — assert hook semantics before implementation**

Run:

```bash
helm template pilot deploy/helm/grepnest -n grepnest -f deploy/helm/grepnest/ci/minimal-values.yaml > /tmp/grepnest-migration.yaml
rg -n 'helm.sh/hook: pre-install,pre-upgrade|helm.sh/hook-delete-policy: before-hook-creation,hook-succeeded|helm.sh/hook-weight: "-10"|activeDeadlineSeconds: 600' /tmp/grepnest-migration.yaml
```

Expected: FAIL because no migration Job exists.

- [ ] **Step 2: GREEN — add the blocking hook Job**

Render one Job using the application image and `[migration.executable]`. Set:

```yaml
metadata:
  annotations:
    helm.sh/hook: pre-install,pre-upgrade
    helm.sh/hook-weight: "-10"
    helm.sh/hook-delete-policy: before-hook-creation,hook-succeeded
spec:
  backoffLimit: 1
  activeDeadlineSeconds: 600
  template:
    spec:
      restartPolicy: Never
```

Retaining failures is deliberate: do not include `hook-failed` in the delete policy. Give the container only `GREPNEST_DATABASE_URL` from the runtime Secret, `/tmp`, runtime home, resources, and the common security defaults. Do not mount the GitHub App Secret or custom CA and do not inject bearer tokens, webhook secret, or node PVC.

- [ ] **Step 3: GREEN — verify the failure-retention contract**

Run:

```bash
helm template pilot deploy/helm/grepnest -n grepnest -f deploy/helm/grepnest/ci/minimal-values.yaml > /tmp/grepnest-migration.yaml
rg -n 'kind: Job|pre-install,pre-upgrade|before-hook-creation,hook-succeeded|activeDeadlineSeconds: 600|GREPNEST_DATABASE_URL|readOnlyRootFilesystem: true' /tmp/grepnest-migration.yaml
! rg -n 'hook-failed|GREPNEST_USER_TOKEN|GREPNEST_ADMIN_TOKEN|GREPNEST_GITHUB_WEBHOOK_SECRET_FILE' /tmp/grepnest-migration.yaml
```

Expected: PASS; a failed hook remains for diagnosis and blocks install/upgrade naturally.

- [ ] **Step 4: Commit the migration hook**

```bash
git status --short
git add deploy/helm/grepnest/templates/migration-job.yaml
git commit -S -m "feat(helm): add migration hook"
```

### Task 5: Optional Generic Ingress and Monitoring

**Files:**
- Create: `deploy/helm/grepnest/ci/optional-values.yaml`
- Create: `deploy/helm/grepnest/templates/ingress.yaml`
- Create: `deploy/helm/grepnest/templates/servicemonitor.yaml`

**Interfaces:**
- Consumes: `ingress.*`, `monitoring.serviceMonitor.*`, and the server Service port `http`.
- Produces: optional `networking.k8s.io/v1` Ingress and optional `monitoring.coreos.com/v1` ServiceMonitor; no OpenShift resource.

- [ ] **Step 1: RED — add optional test inputs and prove templates are absent**

Create `ci/optional-values.yaml`:

```yaml
ingress:
  enabled: true
  className: nginx
  annotations: {nginx.ingress.kubernetes.io/proxy-body-size: 1m}
  hosts:
    - host: grepnest.example.invalid
      paths:
        - {path: /, pathType: Prefix}
  tls:
    - secretName: grepnest-existing-tls
      hosts: [grepnest.example.invalid]
monitoring:
  serviceMonitor:
    enabled: true
    labels: {monitoring: pilot}
secrets:
  customCA: {name: grepnest-existing-ca, key: ca.crt}
networkPolicy:
  serverIngress:
    ingressControllerNamespaceSelector:
      matchLabels: {kubernetes.io/metadata.name: ingress-nginx}
    monitoringNamespaceSelector:
      matchLabels: {kubernetes.io/metadata.name: monitoring}
```

Run:

```bash
helm template pilot deploy/helm/grepnest -n grepnest -f deploy/helm/grepnest/ci/minimal-values.yaml -f deploy/helm/grepnest/ci/optional-values.yaml --api-versions monitoring.coreos.com/v1/ServiceMonitor > /tmp/grepnest-optional.yaml
rg -n '^kind: Ingress$|^kind: ServiceMonitor$' /tmp/grepnest-optional.yaml
```

Expected: FAIL because neither optional template exists.

- [ ] **Step 2: GREEN — render only a standard Kubernetes Ingress**

Render `networking.k8s.io/v1` when enabled, including `ingressClassName`, annotations, every configured host/path, and TLS references. Backends always target the server Service and named port `http`. Schema each host as `{host: non-empty string, paths: non-empty array of {path, pathType enum Exact|Prefix|ImplementationSpecific}}`; schema TLS as existing `secretName` plus non-empty hosts. Do not render a Route or any `*.openshift.io` API.

- [ ] **Step 3: GREEN — fail clearly when ServiceMonitor CRD is absent**

Start `servicemonitor.yaml` with:

```gotemplate
{{- if .Values.monitoring.serviceMonitor.enabled }}
{{- if not (.Capabilities.APIVersions.Has "monitoring.coreos.com/v1/ServiceMonitor") }}
{{- fail "monitoring.serviceMonitor.enabled requires monitoring.coreos.com/v1/ServiceMonitor" }}
{{- end }}
```

Then render a ServiceMonitor selecting the server Service labels, endpoint port `http`, path `/metrics`, and configured interval/scrape timeout. Apply only configured labels and namespaceSelector.

- [ ] **Step 4: GREEN — verify disabled, failure, and success paths**

Run:

```bash
helm template pilot deploy/helm/grepnest -n grepnest -f deploy/helm/grepnest/ci/minimal-values.yaml | rg '^kind: (Ingress|ServiceMonitor)$'
helm template pilot deploy/helm/grepnest -n grepnest -f deploy/helm/grepnest/ci/minimal-values.yaml -f deploy/helm/grepnest/ci/optional-values.yaml
helm template pilot deploy/helm/grepnest -n grepnest -f deploy/helm/grepnest/ci/minimal-values.yaml -f deploy/helm/grepnest/ci/optional-values.yaml --api-versions monitoring.coreos.com/v1/ServiceMonitor > /tmp/grepnest-optional.yaml
rg -n 'apiVersion: networking.k8s.io/v1|kind: Ingress|secretName: grepnest-existing-tls|apiVersion: monitoring.coreos.com/v1|kind: ServiceMonitor|path: /metrics' /tmp/grepnest-optional.yaml
```

Expected: first command exits 1 because optional kinds are absent; second command FAILS with the exact CRD-required message; final render and scan PASS.

- [ ] **Step 5: Commit optional integrations**

```bash
git status --short
git add deploy/helm/grepnest/ci/optional-values.yaml deploy/helm/grepnest/templates/ingress.yaml deploy/helm/grepnest/templates/servicemonitor.yaml
git commit -S -m "feat(helm): add optional integrations"
```

### Task 6: Ingress Isolation and Opt-In External Egress

**Files:**
- Create: `deploy/helm/grepnest/templates/networkpolicies.yaml`
- Modify: `deploy/helm/grepnest/ci/optional-values.yaml`

**Interfaces:**
- Consumes: component labels, server/Zoekt/PostgreSQL ports, namespace `.Release.Namespace`, and `networkPolicy.*`.
- Produces: default-deny ingress, explicit server and Zoekt ingress allows, and opt-in default-deny plus DNS/PostgreSQL/GitHub egress policies.

- [ ] **Step 1: RED — assert the policy boundary before implementation**

Run:

```bash
helm template pilot deploy/helm/grepnest -n grepnest -f deploy/helm/grepnest/ci/minimal-values.yaml > /tmp/grepnest-policy.yaml
rg -n 'name: pilot-grepnest-deny-ingress|name: pilot-grepnest-allow-server-ingress|name: pilot-grepnest-allow-zoekt-ingress|policyTypes:|podSelector: {}' /tmp/grepnest-policy.yaml
```

Expected: FAIL because no NetworkPolicy exists.

- [ ] **Step 2: GREEN — add portable ingress policies**

When `networkPolicy.enabled`, render:

1. `deny-ingress`: selects all same-release pods by name/instance labels, `policyTypes: [Ingress]`, `ingress: []`.
2. `allow-server-ingress`: selects server; allows TCP `server.port` from the current namespace using `namespaceSelector.matchLabels.kubernetes.io/metadata.name: .Release.Namespace`; append a separate peer for configured ingress-controller namespace selector and another for monitoring selector.
3. `allow-zoekt-ingress`: selects node; allows TCP `node.zoekt.port` only from pods with same-release server selector labels in the same namespace.

Do not use cluster-specific labels beyond the standard namespace-name label and operator-supplied selectors.

- [ ] **Step 3: RED — enable external egress and prove CIDR policies are absent**

Append to optional values:

```yaml
networkPolicy:
  externalEgress:
    enabled: true
    postgresql:
      cidrs: [{address: 192.0.2.10, prefix: 32}]
      port: 5432
    github:
      cidrs: [{address: 198.51.100.0, prefix: 24}]
      ports: [443]
```

Run the optional render with the synthetic ServiceMonitor API and scan for both CIDRs. Expected: FAIL because egress policies are not implemented.

- [ ] **Step 4: GREEN — add explicit egress isolation**

When `externalEgress.enabled`, add:

1. `deny-egress` selecting all same-release pods with `policyTypes: [Egress]`, `egress: []`.
2. `allow-internal-egress` selecting server and permitting TCP to same-release node on the Zoekt port.
3. One DNS allow policy selecting server, node, and migration, using the configured namespace and pod selectors, and emitting UDP and TCP entries for every configured DNS port.
4. One PostgreSQL allow policy selecting server, node, and migration, emitting each configured `ipBlock.cidr` and TCP PostgreSQL port. The migration exception is required so pre-upgrade hooks remain usable while an earlier release's deny-egress policy exists. Document in the template comment that NetworkPolicy is pod-level: Zoekt shares the node policy, while only indexer receives the database URL in the long-running node pod.
5. One GitHub allow policy selecting server and node, emitting each configured CIDR and configured TCP port.

Do not infer IPs from hostnames, add broad `0.0.0.0/0`, or make egress restriction default-on.

- [ ] **Step 5: GREEN — verify default and opt-in policy sets**

Run:

```bash
helm template pilot deploy/helm/grepnest -n grepnest -f deploy/helm/grepnest/ci/minimal-values.yaml > /tmp/grepnest-policy-minimal.yaml
rg -n 'deny-ingress|allow-server-ingress|allow-zoekt-ingress' /tmp/grepnest-policy-minimal.yaml
! rg -n 'deny-egress|192.0.2.10/32|198.51.100.0/24' /tmp/grepnest-policy-minimal.yaml
helm template pilot deploy/helm/grepnest -n grepnest -f deploy/helm/grepnest/ci/minimal-values.yaml -f deploy/helm/grepnest/ci/optional-values.yaml --api-versions monitoring.coreos.com/v1/ServiceMonitor > /tmp/grepnest-policy-optional.yaml
rg -n 'deny-egress|allow-internal-egress|192.0.2.10/32|198.51.100.0/24|kube-system|k8s-app: kube-dns' /tmp/grepnest-policy-optional.yaml
```

Expected: PASS; default render contains ingress isolation only and optional render contains exact CIDR/DNS allows.

- [ ] **Step 6: Commit network boundaries**

```bash
git status --short
git add deploy/helm/grepnest/templates/networkpolicies.yaml deploy/helm/grepnest/ci/optional-values.yaml
git commit -S -m "feat(helm): enforce network boundaries"
```

### Task 7: Render and Security Regression Harness

**Files:**
- Create: `deploy/helm/grepnest/tests/render.sh`
- Modify: `Makefile`

**Interfaces:**
- Consumes: Helm, `rg`, minimal/optional CI values, and the full chart.
- Produces: `make helm-lint` and `make helm-test`; no cluster credentials or Kubernetes connection.

- [ ] **Step 1: RED — replace the milestone stub call with the intended gate**

Run:

```bash
make helm-test
```

Expected: FAIL with `No rule to make target 'helm-test'`.

- [ ] **Step 2: GREEN — add the smallest reusable render harness**

Create an executable POSIX shell script with `set -eu`, `mktemp -d`, and a trap. It must run these exact renders:

```sh
helm lint "$chart" -f "$minimal"
helm template pilot "$chart" -n grepnest -f "$minimal" > "$tmp/minimal.yaml"
helm template pilot "$chart" -n grepnest -f "$minimal" -f "$optional" \
  --api-versions monitoring.coreos.com/v1/ServiceMonitor > "$tmp/optional.yaml"
```

Implement only these two helpers:

```sh
require() { rg -q "$1" "$2" || { echo "missing $1 in $2" >&2; exit 1; }; }
reject() { ! rg -n "$1" "$2" || { echo "forbidden $1 in $2" >&2; exit 1; }; }
```

Use `require` to check Deployment, StatefulSet, Job, ConfigMap, ServiceAccount, ClusterIP Services, PDB, Ingress, ServiceMonitor, NetworkPolicy, hook annotations, two node containers, `replicas: 1`, RWO/250Gi, HTTP and TCP probes, every security field, `automountServiceAccountToken: false`, `/tmp` and runtime-home volumes, digest-pinned images, custom CA, scheduling/resources, and optional egress CIDRs.

Use `reject` on both renders for this exact combined expression:

```text
^kind: Secret$|apiVersion: .*openshift\.io|^kind: (Route|BuildConfig|ImageStream|Template|SecurityContextConstraints)$|:latest([[:space:]]|$)|hostPath:|privileged: true|allowPrivilegeEscalation: true|runAsUser: 0|type: (NodePort|LoadBalancer)
```

Also assert exactly one StatefulSet, exactly one `replicas: 1` node occurrence, exactly two node container names, no Zoekt Ingress backend, and every rendered `image:` line matches `@sha256:[a-f0-9]{64}$`.

Finally run expected-failure cases in subshells and fail if they unexpectedly pass:

```sh
helm template bad "$chart" --set images.application.repository=x 2> "$tmp/missing.err"
helm template bad "$chart" -f "$minimal" --set images.node.digest=latest 2> "$tmp/digest.err"
helm template bad "$chart" -f "$minimal" -f "$optional" 2> "$tmp/crd.err"
```

Require `missing.err` to name the missing node/application contract, `digest.err` to name digest validation, and `crd.err` to contain `monitoring.serviceMonitor.enabled requires monitoring.coreos.com/v1/ServiceMonitor`.

- [ ] **Step 3: GREEN — wire Make targets without dependencies**

Change `.PHONY` to include `helm-test`. Replace the existing Helm stub with:

```make
helm-lint:
	helm lint deploy/helm/grepnest -f deploy/helm/grepnest/ci/minimal-values.yaml

helm-test:
	sh deploy/helm/grepnest/tests/render.sh
```

Do not add chart-testing, kubeconform, yq, jq, or a Helm plugin.

- [ ] **Step 4: GREEN — run the complete chart harness**

Run:

```bash
make helm-lint
make helm-test
```

Expected: both commands exit 0; the script prints only Helm lint output and its final `helm render tests passed` line.

- [ ] **Step 5: Commit the test gate**

```bash
git status --short
git add deploy/helm/grepnest/tests/render.sh Makefile
git commit -S -m "test(helm): add render security gate"
```

### Task 8: Operator Documentation and Whole-Chart Verification

**Files:**
- Create: `deploy/helm/grepnest/README.md`
- Modify: `docs/operations.md`
- Modify: `README.md`

**Interfaces:**
- Consumes: complete values, existing-Secret key contracts, Helm commands, and current Milestones 0-1 implementation status.
- Produces: honest installation/upgrade/rollback guidance without claiming images or Milestone 2 binaries exist.

- [ ] **Step 1: RED — prove the operator contract is undocumented**

Run:

```bash
rg -n 'helm upgrade --install|grepnest-migrate|existing Secret|250Gi|ServiceMonitor' deploy/helm/grepnest/README.md docs/operations.md README.md
```

Expected: FAIL because the chart README does not exist and the current docs describe only Milestones 0-1.

- [ ] **Step 2: GREEN — document installation inputs and lifecycle**

Document these exact operator steps in the chart README:

```bash
helm lint deploy/helm/grepnest -f my-values.yaml
helm template grepnest deploy/helm/grepnest -n grepnest -f my-values.yaml
helm upgrade --install grepnest deploy/helm/grepnest -n grepnest --create-namespace -f my-values.yaml --wait --timeout 15m
helm rollback grepnest <REVISION> -n grepnest --wait --timeout 15m
```

List the exact runtime, GitHub App, optional CA, image pull, and Ingress TLS existing-Secret names/keys from the values interface. State that the chart never creates Secrets or PostgreSQL, migration failure blocks install/upgrade and remains inspectable, ServiceMonitor requires its CRD, Zoekt must remain internal, and external egress CIDRs must cover DNS, GitHub, and PostgreSQL endpoints before enabling isolation.

Warn directly after the rollback command that Helm rollback does not execute the
pre-install/pre-upgrade migration hook. Operators must verify database backward
compatibility and follow release-specific database rollback or restore
procedures before rolling the application image back after a schema-changing
upgrade. Do not invent a database downgrade command.

- [ ] **Step 3: GREEN — document scheduling, storage, and capacity truthfully**

Add the server/Zoekt/indexer resource defaults and 250Gi RWO default. State verbatim that actual capacity must be based on measured source corpus size, index size, indexing duration, and query concurrency rather than repository count alone. Explain independent server/node scheduling maps and that `node.storage.storageClassName` selects operator-provided SSD-backed RWO storage where available.

- [ ] **Step 4: GREEN — document the current implementation boundary**

In the root README and operations guide, state that the chart is structurally renderable but is not currently deployable: this change does not build images and the required Milestone 2 `grepnest-indexer`/`grepnest-migrate` behavior is unfinished. Do not add OpenShift install commands, Route instructions, image build steps, PostgreSQL installation, or a cluster test claim.

- [ ] **Step 5: GREEN — run documentation and repository regressions**

Run:

```bash
rg -n 'helm upgrade --install|existing Secret|migration failure|250Gi|ReadWriteOnce|measured source corpus size|not currently deployable' deploy/helm/grepnest/README.md docs/operations.md README.md
make test
make helm-test
```

Expected: all required documentation phrases match; Go tests and chart harness PASS.

- [ ] **Step 6: Commit operator documentation**

```bash
git status --short
git add deploy/helm/grepnest/README.md docs/operations.md README.md
git commit -S -m "docs(helm): add operator guidance"
```

- [ ] **Step 7: Verify branch and scope**

Run:

```bash
git status --short --branch
git diff --check main...HEAD
git diff --name-only main...HEAD
```

Expected: branch is `feat/helm-chart`; no whitespace errors; changed files are limited to `deploy/helm/grepnest/**`, `Makefile`, `README.md`, `docs/operations.md`, and this plan. No container, Go application, migration SQL, Compose, OpenShift, or Secret manifest file appears.

- [ ] **Step 8: Run every local regression gate**

Run:

```bash
make fmt
make lint
make test
make build
make helm-lint
make helm-test
```

Expected: every command exits 0.

- [ ] **Step 9: Inspect both rendered modes**

Run:

```bash
helm template pilot deploy/helm/grepnest -n grepnest -f deploy/helm/grepnest/ci/minimal-values.yaml > /tmp/grepnest-minimal-final.yaml
helm template pilot deploy/helm/grepnest -n grepnest -f deploy/helm/grepnest/ci/minimal-values.yaml -f deploy/helm/grepnest/ci/optional-values.yaml --api-versions monitoring.coreos.com/v1/ServiceMonitor > /tmp/grepnest-optional-final.yaml
rg '^kind:' /tmp/grepnest-minimal-final.yaml
rg '^kind:' /tmp/grepnest-optional-final.yaml
```

Expected: minimal mode contains ConfigMaps, two release-managed ServiceAccounts
for server and node, server Deployment/Service/PDB, node StatefulSet/headless
and Zoekt Services, migration Job, and NetworkPolicies. The migration Job uses
the namespace default ServiceAccount with pod-level token automount disabled.
Optional mode additionally contains one Ingress, one ServiceMonitor, and
external-egress policies; neither contains a Secret or OpenShift kind.

- [ ] **Step 10: Audit signed atomic history**

Run:

```bash
git log --show-signature --format='%h %G? %s' main..HEAD
git log --format='%s' main..HEAD | awk 'length($0) > 72 { print; bad=1 } END { exit bad }'
```

Expected: every implementation commit reports a good signature, every subject is conventional and at most 72 characters, and each task is independently reversible. Do not create a verification-only commit.
