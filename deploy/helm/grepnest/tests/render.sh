#!/bin/sh
set -eu

script_dir=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
chart=$(CDPATH= cd -- "$script_dir/.." && pwd)
minimal=$chart/ci/minimal-values.yaml
optional=$chart/ci/optional-values.yaml
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' 0 HUP INT TERM

require() {
  [ -f "$2" ] && [ -r "$2" ] || {
    echo "not a readable regular file: $2" >&2
    return 2
  }
  if rg -q -- "$1" "$2"; then
    return 0
  else
    status=$?
  fi
  [ "$status" -eq 1 ] || {
    echo "rg failed with status $status for $2" >&2
    return "$status"
  }
  echo "missing $1 in $2" >&2
  return 1
}

reject() {
  [ -f "$2" ] && [ -r "$2" ] || {
    echo "not a readable regular file: $2" >&2
    return 2
  }
  if rg -n -- "$1" "$2"; then
    echo "forbidden $1 in $2" >&2
    return 1
  else
    status=$?
  fi
  [ "$status" -eq 1 ] || {
    echo "rg failed with status $status for $2" >&2
    return "$status"
  }
}

expect_failure() {
  output=$1
  shift
  if "$@" >/dev/null 2>"$output"; then
    echo "expected failure passed: $*" >&2
    return 1
  else
    status=$?
  fi
  [ "$status" -gt 0 ] && [ -s "$output" ] || {
    echo "expected nonzero failure with diagnostics: $*" >&2
    return 1
  }
}

expect_failure "$tmp/require-missing.err" require anything "$tmp/missing"
expect_failure "$tmp/reject-missing.err" reject anything "$tmp/missing"
require 'not a readable regular file:' "$tmp/require-missing.err"
require 'not a readable regular file:' "$tmp/reject-missing.err"
: >"$tmp/probe"
expect_failure "$tmp/require-rg.err" require '[' "$tmp/probe"
expect_failure "$tmp/reject-rg.err" reject '[' "$tmp/probe"
require 'rg failed with status 2' "$tmp/require-rg.err"
require 'rg failed with status 2' "$tmp/reject-rg.err"

helm lint "$chart" -f "$minimal"
helm template pilot "$chart" -n grepnest -f "$minimal" >"$tmp/minimal.yaml"
helm template pilot "$chart" -n grepnest -f "$minimal" -f "$optional" \
  --api-versions monitoring.coreos.com/v1/ServiceMonitor >"$tmp/optional.yaml"
long_release=abcdefghijklmnopqrstuvwxyzabcdefghijklmnopqrstuvwxyzx
helm template "$long_release" "$chart" -n grepnest -f "$minimal" >"$tmp/long-release.yaml"

awk '
  /^[[:space:]]*(name|serviceName|serviceAccountName): [^{}]/ {
    value = $2
    gsub(/^"|"$/, "", value)
    if (length(value) > 63) {
      print "name exceeds 63 characters: " value > "/dev/stderr"
      failed = 1
    }
  }
  END { exit failed }
' "$tmp/long-release.yaml"
for suffix in server node zoekt indexer migrate deny-ingress allow-server-ingress \
  allow-zoekt-ingress allow-indexer-metrics-ingress; do
  [ "$(rg -c "^  name: .*$suffix\$" "$tmp/long-release.yaml")" -ge 1 ] || exit 1
done
[ "$(sed -n 's/^  name: //p' "$tmp/long-release.yaml" | sort -u | \
  rg -c -- '-(server|node|zoekt|indexer|migrate|deny-ingress|allow-server-ingress|allow-zoekt-ingress|allow-indexer-metrics-ingress)$')" \
  -eq 9 ] || exit 1

helm template paths "$chart" -n grepnest -f "$minimal" \
  --set-string=node.paths.data=/srv/grepnest-data \
  --set-string=node.paths.indexes=/srv/grepnest-data/zoekt/index \
  --set=node.zoekt.port=16070 --set=node.service.port=16071 \
  --set=node.indexer.metricsPort=19090 >"$tmp/node-contract.yaml"
for pattern in \
  'GREPNEST_DATA_DIR: "/srv/grepnest-data"' \
  'GREPNEST_INDEX_DIR: "/srv/grepnest-data/zoekt/index"' \
  'containerPort: 16070' 'port: 16071, targetPort: zoekt' \
  'GREPNEST_METRICS_LISTEN_ADDRESS: ":19090"' \
  'name: metrics, containerPort: 19090' \
  'name: metrics, port: 19090, targetPort: metrics' \
  'mountPath: "/srv/grepnest-data/zoekt/index", subPath: "zoekt/index", readOnly: true' \
  'mountPath: "/srv/grepnest-data"}'; do
  require "$pattern" "$tmp/node-contract.yaml"
done
sed -n '/name: zoekt-webserver$/,/name: grepnest-indexer$/p' \
  "$tmp/node-contract.yaml" >"$tmp/node-contract-zoekt.yaml"
require '^- -index$|^            - -index$' "$tmp/node-contract-zoekt.yaml"
require '^            - "/srv/grepnest-data/zoekt/index"$' \
  "$tmp/node-contract-zoekt.yaml"
require '^- -listen$|^            - -listen$' "$tmp/node-contract-zoekt.yaml"
require '^            - ":16070"$' "$tmp/node-contract-zoekt.yaml"
[ "$(rg -c 'tcpSocket: \{port: zoekt\}' "$tmp/node-contract-zoekt.yaml")" -eq 2 ] || exit 1

expect_failure "$tmp/disconnected-indexes.err" helm template bad "$chart" -f "$minimal" \
  --set-string=node.paths.data=/srv/grepnest-data \
  --set-string=node.paths.indexes=/srv/other/index
require 'node.paths.indexes must be a child of node.paths.data' "$tmp/disconnected-indexes.err"

helm template refs "$chart" -n grepnest -f "$minimal" \
  --set-string=secrets.runtime.name=runtime.team.example \
  --set-string=secrets.runtime.databaseURLKey=DB_URL.v1-key \
  --set-string=images.pullSecrets[0]=registry.team.example \
  --set-string=node.storage.storageClassName=ssd.storage.example >"$tmp/references.yaml"
for pattern in 'name: runtime.team.example' 'key: DB_URL.v1-key' \
  'name: registry.team.example' 'storageClassName: "ssd.storage.example"'; do
  require "$pattern" "$tmp/references.yaml"
done

helm template security "$chart" -n grepnest -f "$minimal" \
  --set=server.podSecurityContext.runAsNonRoot=false \
  --set=server.podSecurityContext.seccompProfile.type=Unconfined \
  --set=server.podSecurityContext.fsGroup=1234 \
  --set=node.podSecurityContext.runAsNonRoot=false \
  --set=node.podSecurityContext.seccompProfile.type=Unconfined \
  --set=node.podSecurityContext.fsGroup=2345 >"$tmp/pod-security.yaml"
sed -n '/^kind: Deployment$/,/^      containers:$/p' "$tmp/pod-security.yaml" \
  | sed -n '/^      securityContext:$/,$p' >"$tmp/server-pod-security.yaml"
sed -n '/^kind: StatefulSet$/,/^      containers:$/p' "$tmp/pod-security.yaml" \
  | sed -n '/^      securityContext:$/,$p' >"$tmp/node-pod-security.yaml"
for workload in server node; do
  security=$tmp/$workload-pod-security.yaml
  [ "$(awk '/^        runAsNonRoot: true$/ {count++} END {print count + 0}' "$security")" -eq 1 ] || exit 1
  [ "$(awk '/^        seccompProfile:/ {count++} END {print count + 0}' "$security")" -eq 1 ] || exit 1
  require 'type: RuntimeDefault' "$security"
  reject 'runAsNonRoot: false|type: Unconfined' "$security"
done
require '^        fsGroup: 1234$' "$tmp/server-pod-security.yaml"
require '^        fsGroup: 2345$' "$tmp/node-pod-security.yaml"

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

  images_file=$tmp/$(basename "$manifest").images
  if rg '^ *image:' "$manifest" >"$images_file"; then
    :
  else
    status=$?
    echo "image extraction failed with status $status for $manifest" >&2
    exit "$status"
  fi
  invalid_images=$tmp/$(basename "$manifest").invalid-images
  if rg -v '@sha256:[a-f0-9]{64}"?$' "$images_file" >"$invalid_images"; then
    echo "non-digest image in $manifest" >&2
    exit 1
  else
    status=$?
  fi
  [ "$status" -eq 1 ] || exit "$status"

  images=$(rg -c '^ *image:' "$images_file")
  for pattern in 'allowPrivilegeEscalation: false' 'capabilities: \{drop: \[ALL\]\}' \
    'readOnlyRootFilesystem: true'; do
    [ "$(rg -c "$pattern" "$manifest")" -eq "$images" ] || exit 1
  done
  [ "$(rg -c 'runAsNonRoot: true' "$manifest")" -eq "$((images + 2))" ] || exit 1
  [ "$(rg -c 'seccompProfile: \{type: RuntimeDefault\}' "$manifest")" -ge "$images" ] || exit 1
  [ "$(rg -c '^kind: StatefulSet$' "$manifest")" -eq 1 ] || exit 1
  [ "$(rg -c '^  replicas: 1$' "$manifest")" -eq 1 ] || exit 1
  [ "$(rg -c '^        - name: (zoekt-webserver|grepnest-indexer)$' "$manifest")" -eq 2 ] || exit 1
done

for pattern in '^kind: Ingress$' '^kind: ServiceMonitor$' \
  'name: custom-ca' 'secretName: grepnest-existing-ca' \
  'grepnest.example.invalid/pool: server' \
  'grepnest.example.invalid/pool: node' \
  'grepnest.example.invalid/pool: migration' \
  'nodeSelector:' 'affinity:' 'tolerations:' \
  'grepnest.example.invalid/tier' \
  'grepnest.example.invalid/dedicated' \
  '- frontend$' '- storage$' '- batch$' \
  'value: server' 'value: node' 'value: migration' \
  'topologySpreadConstraints:' \
  'cpu: 250m' 'memory: 256Mi' 'cpu: "8"' 'memory: 24Gi'; do
  require "$pattern" "$tmp/optional.yaml"
done
[ "$(rg -c '^kind: ServiceMonitor$' "$tmp/optional.yaml")" -eq 2 ] || exit 1
require 'app.kubernetes.io/component: indexer' "$tmp/optional.yaml"
require 'port: metrics' "$tmp/optional.yaml"

sed -n '/^kind: StatefulSet$/,/^# Source: grepnest\/templates\/migration-job.yaml$/p' \
  "$tmp/minimal.yaml" >"$tmp/node.yaml"
require '^kind: StatefulSet$' "$tmp/node.yaml"
[ "$(rg -c '^  volumeClaimTemplates:$' "$tmp/node.yaml")" -eq 1 ] || exit 1
sed -n '/^      containers:$/,/^      volumes:$/p' "$tmp/node.yaml" >"$tmp/node-containers.yaml"
require '^      containers:$' "$tmp/node-containers.yaml"
[ "$(rg -c '^        - name:' "$tmp/node-containers.yaml")" -eq 2 ] || exit 1
sed -n '/^        - name: zoekt-webserver$/,/^        - name: grepnest-indexer$/p' \
  "$tmp/node.yaml" >"$tmp/zoekt.yaml"
require '^        - name: zoekt-webserver$' "$tmp/zoekt.yaml"
sed -n '/^        - name: grepnest-indexer$/,/^      volumes:$/p' \
  "$tmp/node.yaml" >"$tmp/indexer.yaml"
require '^        - name: grepnest-indexer$' "$tmp/indexer.yaml"
reject 'secretKeyRef:|name: GREPNEST_DATABASE_URL|name: GREPNEST_(USER|ADMIN)_TOKEN' "$tmp/zoekt.yaml"
require 'name: GREPNEST_DATABASE_URL' "$tmp/indexer.yaml"
reject 'name: GREPNEST_(USER|ADMIN)_TOKEN' "$tmp/indexer.yaml"

sed -n '/^# Source: grepnest\/templates\/ingress.yaml$/,/^---$/p' \
  "$tmp/optional.yaml" >"$tmp/optional-ingress.yaml"
require '^kind: Ingress$' "$tmp/optional-ingress.yaml"
reject 'pilot-grepnest-zoekt|name: .*zoekt|backend:.*zoekt' "$tmp/optional-ingress.yaml"
reject 'host: "?\*|path: /\*|host: "?default([.]|"|$)' "$tmp/optional-ingress.yaml"

policies='deny-ingress allow-server-ingress allow-zoekt-ingress allow-indexer-metrics-ingress deny-egress allow-internal-egress allow-dns-egress allow-postgresql-egress allow-github-egress'
for policy in $policies; do
  sed -n "/^  name: pilot-grepnest-$policy\$/,/^---\$/p" \
    "$tmp/optional.yaml" >"$tmp/$policy.yaml"
  require "^  name: pilot-grepnest-$policy\$" "$tmp/$policy.yaml"
  sed -n '/^spec:$/,/^---$/p' "$tmp/$policy.yaml" >"$tmp/$policy-spec.yaml"
  require '^spec:$' "$tmp/$policy-spec.yaml"
  require 'app.kubernetes.io/name: grepnest' "$tmp/$policy-spec.yaml"
  require 'app.kubernetes.io/instance: pilot' "$tmp/$policy-spec.yaml"
done

require 'policyTypes: \[Ingress\]' "$tmp/deny-ingress-spec.yaml"
require 'ingress: \[\]' "$tmp/deny-ingress-spec.yaml"
require 'app.kubernetes.io/component: server' "$tmp/allow-server-ingress-spec.yaml"
require 'policyTypes: \[Ingress\]' "$tmp/allow-server-ingress-spec.yaml"
for peer in grepnest ingress-nginx monitoring; do
  require "kubernetes.io/metadata.name: $peer" "$tmp/allow-server-ingress-spec.yaml"
done
require 'protocol: TCP, port: 8080' "$tmp/allow-server-ingress-spec.yaml"

require 'app.kubernetes.io/component: node' "$tmp/allow-zoekt-ingress-spec.yaml"
require 'policyTypes: \[Ingress\]' "$tmp/allow-zoekt-ingress-spec.yaml"
require '^        - namespaceSelector:$' "$tmp/allow-zoekt-ingress-spec.yaml"
require '^          podSelector:$' "$tmp/allow-zoekt-ingress-spec.yaml"
require 'app.kubernetes.io/component: server' "$tmp/allow-zoekt-ingress-spec.yaml"
require 'protocol: TCP, port: 6070' "$tmp/allow-zoekt-ingress-spec.yaml"

require 'app.kubernetes.io/component: node' "$tmp/allow-indexer-metrics-ingress-spec.yaml"
require 'policyTypes: \[Ingress\]' "$tmp/allow-indexer-metrics-ingress-spec.yaml"
require 'kubernetes.io/metadata.name: monitoring' "$tmp/allow-indexer-metrics-ingress-spec.yaml"
require 'protocol: TCP, port: 9090' "$tmp/allow-indexer-metrics-ingress-spec.yaml"

require 'policyTypes: \[Egress\]' "$tmp/deny-egress-spec.yaml"
require 'egress: \[\]' "$tmp/deny-egress-spec.yaml"
require 'app.kubernetes.io/component: server' "$tmp/allow-internal-egress-spec.yaml"
require 'policyTypes: \[Egress\]' "$tmp/allow-internal-egress-spec.yaml"
require '^        - namespaceSelector:$' "$tmp/allow-internal-egress-spec.yaml"
require '^          podSelector:$' "$tmp/allow-internal-egress-spec.yaml"
require 'app.kubernetes.io/component: node' "$tmp/allow-internal-egress-spec.yaml"
require 'protocol: TCP, port: 6070' "$tmp/allow-internal-egress-spec.yaml"

require 'policyTypes: \[Egress\]' "$tmp/allow-dns-egress-spec.yaml"
require '^        - namespaceSelector:$' "$tmp/allow-dns-egress-spec.yaml"
require '^          podSelector:$' "$tmp/allow-dns-egress-spec.yaml"
require 'kubernetes.io/metadata.name: kube-system' "$tmp/allow-dns-egress-spec.yaml"
require 'k8s-app: kube-dns' "$tmp/allow-dns-egress-spec.yaml"
require 'protocol: UDP, port: 53' "$tmp/allow-dns-egress-spec.yaml"
require 'protocol: TCP, port: 53' "$tmp/allow-dns-egress-spec.yaml"

require 'values: \[server, node, migration\]' "$tmp/allow-postgresql-egress-spec.yaml"
require 'policyTypes: \[Egress\]' "$tmp/allow-postgresql-egress-spec.yaml"
require 'cidr: "192\.0\.2\.10/32"' "$tmp/allow-postgresql-egress-spec.yaml"
require 'protocol: TCP, port: 5432' "$tmp/allow-postgresql-egress-spec.yaml"
require 'values: \[server, node\]' "$tmp/allow-github-egress-spec.yaml"
require 'policyTypes: \[Egress\]' "$tmp/allow-github-egress-spec.yaml"
require 'cidr: "198\.51\.100\.0/24"' "$tmp/allow-github-egress-spec.yaml"
require 'cidr: "2001:db8:1234::/48"' "$tmp/allow-github-egress-spec.yaml"
require 'protocol: TCP, port: 443' "$tmp/allow-github-egress-spec.yaml"

reject '^ *- \{\}|^ *from: *\[?\]?$|^ *to: *\[?\]?$|^ *- (podSelector|namespaceSelector): *\{\}$' "$tmp/optional.yaml"
reject 'cidr: "?(0\.0\.0\.0/0|::/0)"?' "$tmp/optional.yaml"
reject '^ *namespaceSelector: *\{\}$|^ *podSelector: *\{\}$' "$tmp/optional.yaml"

expect_failure "$tmp/repository.err" helm template bad "$chart" -f "$minimal" \
  --set-string=images.application.repository=
expect_failure "$tmp/digest.err" helm template bad "$chart" -f "$minimal" \
  --set=images.node.digest=latest
for field in nameOverride fullnameOverride; do
  for value in Bad bad_name -bad bad- \
    aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa; do
    output=$tmp/$field-$(printf '%s' "$value" | tr -c 'a-zA-Z0-9' _).err
    expect_failure "$output" helm template bad "$chart" -f "$minimal" \
      --set-string="$field=$value"
    require "/$field" "$output"
  done
done
expect_failure "$tmp/runtime-secret-name.err" helm template bad "$chart" -f "$minimal" \
  --set-string='secrets.runtime.name=bad name'
expect_failure "$tmp/runtime-secret-key.err" helm template bad "$chart" -f "$minimal" \
  --set-string='secrets.runtime.databaseURLKey=bad/key'
expect_failure "$tmp/ingress-class-name.err" helm template bad "$chart" -f "$minimal" -f "$optional" \
  --set-string=ingress.className= --set=monitoring.serviceMonitor.enabled=false
expect_failure "$tmp/crd.err" helm template bad "$chart" -f "$minimal" -f "$optional"
expect_failure "$tmp/ipv4.err" helm template bad "$chart" -f "$minimal" \
  --set-json='networkPolicy.externalEgress.postgresql.cidrs=[{"address":"999.0.2.1","prefix":32}]'
expect_failure "$tmp/ipv6.err" helm template bad "$chart" -f "$minimal" \
  --set-json='networkPolicy.externalEgress.github.cidrs=[{"address":"2001:db8::zz","prefix":64}]'
expect_failure "$tmp/ipv4-prefix.err" helm template bad "$chart" -f "$minimal" \
  --set-json='networkPolicy.externalEgress.postgresql.cidrs=[{"address":"192.0.2.1","prefix":0}]'
expect_failure "$tmp/ipv6-prefix.err" helm template bad "$chart" -f "$minimal" \
  --set-json='networkPolicy.externalEgress.github.cidrs=[{"address":"2001:db8::1","prefix":0}]'
expect_failure "$tmp/optional-selector.err" helm template bad "$chart" -f "$minimal" -f "$optional" \
  --set-json='networkPolicy.serverIngress.ingressControllerNamespaceSelector.matchLabels=null'
expect_failure "$tmp/dns-selectors.err" helm template bad "$chart" -f "$minimal" \
  --set=networkPolicy.externalEgress.enabled=true \
  --set-json='networkPolicy.externalEgress.postgresql.cidrs=[{"address":"192.0.2.1","prefix":32}]' \
  --set-json='networkPolicy.externalEgress.github.cidrs=[{"address":"2001:db8::1","prefix":128}]' \
  --set-json='networkPolicy.externalEgress.dns.namespaceSelector=null' \
  --set-json='networkPolicy.externalEgress.dns.podSelector=null'

require "/images/application/repository.*minLength: got 0, want 1" "$tmp/repository.err"
require "/images/node/digest.*'latest'.*does not match pattern" "$tmp/digest.err"
require "/secrets/runtime/name.*'bad name'.*does not match pattern" "$tmp/runtime-secret-name.err"
require "/secrets/runtime/databaseURLKey.*'bad/key'.*does not match pattern" "$tmp/runtime-secret-key.err"
require '/ingress/className.*minLength: got 0, want 1' "$tmp/ingress-class-name.err"
require 'monitoring.serviceMonitor.enabled requires monitoring.coreos.com/v1/ServiceMonitor' "$tmp/crd.err"
require "/networkPolicy/externalEgress/postgresql/cidrs/0/address.*'999\.0\.2\.1'.*not valid ipv4" "$tmp/ipv4.err"
require "/networkPolicy/externalEgress/github/cidrs/0/address.*'2001:db8::zz'.*not valid ipv6" "$tmp/ipv6.err"
require '/networkPolicy/externalEgress/postgresql/cidrs/0/prefix.*minimum: got 0, want 1' "$tmp/ipv4-prefix.err"
require '/networkPolicy/externalEgress/github/cidrs/0/prefix.*minimum: got 0, want 1' "$tmp/ipv6-prefix.err"
require '/networkPolicy/serverIngress/ingressControllerNamespaceSelector/matchLabels.*got null, want object' "$tmp/optional-selector.err"
require "/networkPolicy/externalEgress/dns.*missing properties 'namespaceSelector', 'podSelector'" "$tmp/dns-selectors.err"

echo 'helm render tests passed'
