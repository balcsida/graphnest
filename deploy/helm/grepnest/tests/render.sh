#!/bin/sh
set -eu

chart=deploy/helm/grepnest
minimal=$chart/ci/minimal-values.yaml
optional=$chart/ci/optional-values.yaml
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT HUP INT TERM

require() { rg -q "$1" "$2" || { echo "missing $1 in $2" >&2; exit 1; }; }
reject() { ! rg -n "$1" "$2" || { echo "forbidden $1 in $2" >&2; exit 1; }; }

helm lint "$chart" -f "$minimal"
helm template pilot "$chart" -n grepnest -f "$minimal" > "$tmp/minimal.yaml"
helm template pilot "$chart" -n grepnest -f "$minimal" -f "$optional" \
  --api-versions monitoring.coreos.com/v1/ServiceMonitor > "$tmp/optional.yaml"

for manifest in "$tmp/minimal.yaml" "$tmp/optional.yaml"; do
  for pattern in \
    '^kind: Deployment$' '^kind: StatefulSet$' '^kind: Job$' \
    '^kind: ConfigMap$' '^kind: ServiceAccount$' '^kind: Service$' \
    '^kind: PodDisruptionBudget$' '^kind: NetworkPolicy$' \
    'type: ClusterIP' 'helm.sh/hook: pre-install,pre-upgrade' \
    'replicas: 1' 'ReadWriteOnce' 'storage: 250Gi' \
    'httpGet:' 'tcpSocket:' 'automountServiceAccountToken: false' \
    'allowPrivilegeEscalation: false' 'capabilities: \{drop: \[ALL\]\}' \
    'readOnlyRootFilesystem: true' 'runAsNonRoot: true' \
    'seccompProfile: \{type: RuntimeDefault\}' \
    'mountPath: /tmp' 'mountPath: /var/run/grepnest' \
    'requests:' 'limits:'; do
    require "$pattern" "$manifest"
  done
  reject '^kind: Secret$|apiVersion: .*openshift\.io|^kind: (Route|BuildConfig|ImageStream|Template|SecurityContextConstraints)$|:latest([[:space:]]|$)|hostPath:|privileged: true|allowPrivilegeEscalation: true|runAsUser: 0|type: (NodePort|LoadBalancer)' "$manifest"
  reject 'runAsNonRoot: false' "$manifest"
  if rg '^ *image:' "$manifest" | rg -v '@sha256:[a-f0-9]{64}"?$' >/dev/null; then
    echo "non-digest image in $manifest" >&2
    exit 1
  fi
  images=$(rg -c '^ *image:' "$manifest")
  for pattern in 'allowPrivilegeEscalation: false' 'capabilities: \{drop: \[ALL\]\}' \
    'readOnlyRootFilesystem: true' 'runAsNonRoot: true'; do
    [ "$(rg -c "$pattern" "$manifest")" -eq "$images" ] || exit 1
  done
  [ "$(rg -c 'seccompProfile: \{type: RuntimeDefault\}' "$manifest")" -ge "$images" ] || exit 1
  [ "$(rg -c '^kind: StatefulSet$' "$manifest")" -eq 1 ] || exit 1
  [ "$(rg -c '^  replicas: 1$' "$manifest")" -eq 1 ] || exit 1
  [ "$(rg -c '^        - name: (zoekt-webserver|grepnest-indexer)$' "$manifest")" -eq 2 ] || exit 1
done

for pattern in '^kind: Ingress$' '^kind: ServiceMonitor$' \
  'name: custom-ca' 'secretName: grepnest-existing-ca' \
  'cidr: "192\.0\.2\.10/32"' 'cidr: "198\.51\.100\.0/24"'; do
  require "$pattern" "$tmp/optional.yaml"
done
for pattern in 'nodeSelector' 'affinity' 'tolerations' 'topologySpreadConstraints'; do
  require "$pattern" "$chart/templates/server.yaml"
  require "$pattern" "$chart/templates/node.yaml"
done
require '"format": "ipv4"' "$chart/values.schema.json"
require '"format": "ipv6"' "$chart/values.schema.json"

sed -n '/^kind: StatefulSet$/,/^# Source: grepnest\/templates\/migration-job.yaml$/p' "$tmp/minimal.yaml" > "$tmp/node.yaml"
[ "$(rg -c '^  volumeClaimTemplates:$' "$tmp/node.yaml")" -eq 1 ] || exit 1
sed -n '/^      containers:$/,/^      volumes:$/p' "$tmp/node.yaml" > "$tmp/node-containers.yaml"
[ "$(rg -c '^        - name:' "$tmp/node-containers.yaml")" -eq 2 ] || exit 1
sed -n '/^        - name: zoekt-webserver$/,/^        - name: grepnest-indexer$/p' "$tmp/node.yaml" > "$tmp/zoekt.yaml"
sed -n '/^        - name: grepnest-indexer$/,/^      volumes:$/p' "$tmp/node.yaml" > "$tmp/indexer.yaml"
reject 'secretKeyRef:|name: GREPNEST_DATABASE_URL|name: GREPNEST_(USER|ADMIN)_TOKEN' "$tmp/zoekt.yaml"
require 'name: GREPNEST_DATABASE_URL' "$tmp/indexer.yaml"
reject 'name: GREPNEST_(USER|ADMIN)_TOKEN' "$tmp/indexer.yaml"

sed -n '/^# Source: grepnest\/templates\/ingress.yaml$/,/^---$/p' "$tmp/optional.yaml" > "$tmp/optional-ingress.yaml"
reject 'pilot-grepnest-zoekt|name: .*zoekt|backend:.*zoekt' "$tmp/optional-ingress.yaml"
reject 'host: "?\*|path: /\*|host: "?default([.]|"|$)' "$tmp/optional-ingress.yaml"
reject '^ *- \{\}|^ *from: *\[?\]?$|^ *to: *\[?\]?$|^ *- (podSelector|namespaceSelector): *\{\}$' "$tmp/optional.yaml"

if helm template bad "$chart" --set images.application.repository=x >/dev/null 2> "$tmp/missing.err"; then exit 1; fi
if helm template bad "$chart" -f "$minimal" --set images.node.digest=latest >/dev/null 2> "$tmp/digest.err"; then exit 1; fi
if helm template bad "$chart" -f "$minimal" -f "$optional" >/dev/null 2> "$tmp/crd.err"; then exit 1; fi
if helm template bad "$chart" -f "$minimal" --set 'networkPolicy.externalEgress.postgresql.cidrs[0].address=not-an-ip' >/dev/null 2> "$tmp/ip.err"; then exit 1; fi
if helm template bad "$chart" -f "$minimal" --set 'networkPolicy.externalEgress.postgresql.cidrs[0].address=192.0.2.1' --set 'networkPolicy.externalEgress.postgresql.cidrs[0].prefix=0' >/dev/null 2> "$tmp/prefix.err"; then exit 1; fi
if helm template bad "$chart" -f "$minimal" \
  --set networkPolicy.externalEgress.enabled=true \
  --set 'networkPolicy.externalEgress.postgresql.cidrs[0].address=192.0.2.1' \
  --set 'networkPolicy.externalEgress.postgresql.cidrs[0].prefix=32' \
  --set 'networkPolicy.externalEgress.github.cidrs[0].address=2001:db8::1' \
  --set 'networkPolicy.externalEgress.github.cidrs[0].prefix=128' \
  --set-json 'networkPolicy.externalEgress.dns.namespaceSelector.matchLabels=null' \
  --set-json 'networkPolicy.externalEgress.dns.podSelector.matchLabels=null' \
  >/dev/null 2> "$tmp/selectors.err"; then exit 1; fi

require '/images/(application|node)|image (repository|sha256 digest)' "$tmp/missing.err"
require 'digest|sha256' "$tmp/digest.err"
require 'monitoring.serviceMonitor.enabled requires monitoring.coreos.com/v1/ServiceMonitor' "$tmp/crd.err"
require 'address|ipv4|ipv6' "$tmp/ip.err"
require 'prefix|greater than or equal to 1' "$tmp/prefix.err"
require 'namespaceSelector|podSelector|matchLabels' "$tmp/selectors.err"

echo 'helm render tests passed'
