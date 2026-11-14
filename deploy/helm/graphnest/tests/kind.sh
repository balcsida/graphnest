#!/bin/sh
set -eu

# Installs this chart on a throwaway kind cluster with the images built from
# this tree, then repeats it as an upgrade from the latest released chart.

root=$(CDPATH= cd -- "$(dirname -- "$0")/../../../.." && pwd)
chart=$root/deploy/helm/graphnest
values=$chart/tests/kind-values.yaml
cluster=graphnest-test
registry=$cluster-registry
node_image=${KIND_NODE_IMAGE:?}
registry_image=${KIND_REGISTRY_IMAGE:?}
application=${APPLICATION_IMAGE:-graphnest-application:dev}
node=${NODE_IMAGE:-graphnest-node:dev}
released=${RELEASED_CHART:-oci://ghcr.io/balcsida/graphnest/charts/graphnest}

tmp=$(mktemp -d)
# Leave the caller's kubeconfig and current context alone.
export KUBECONFIG="$tmp/kubeconfig"

cleanup() {
  status=$?
  trap - 0
  if [ "$status" -ne 0 ]; then
    kubectl get pods --all-namespaces -o wide || true
    kubectl get events --all-namespaces --sort-by=.lastTimestamp | tail -n 40 || true
    for namespace in graphnest-fresh graphnest-upgrade; do
      kubectl -n "$namespace" logs -l app.kubernetes.io/instance=graphnest \
        --all-containers --prefix --tail=40 || true
    done
  fi >&2
  if [ -n "${KEEP_CLUSTER:-}" ]; then
    echo "kept cluster $cluster: KUBECONFIG=$KUBECONFIG" >&2
  else
    kind delete cluster --name "$cluster" >/dev/null 2>&1 || true
    docker rm -f -v "$registry" >/dev/null 2>&1 || true
    rm -rf "$tmp"
  fi
  exit "$status"
}
trap cleanup 0
trap 'exit 1' HUP INT TERM

kind delete cluster --name "$cluster" >/dev/null 2>&1 || true
docker rm -f -v "$registry" >/dev/null 2>&1 || true
kind create cluster --name "$cluster" --image "$node_image" --wait 120s
# The migration hook resolves the database by name and retries only once.
kubectl -n kube-system rollout status deployment/coredns --timeout=120s

# The chart accepts only digest-pinned images, and a locally built image gets
# a digest the nodes can pull only from a registry.
docker run -d --name "$registry" --network kind -p 127.0.0.1::5000 "$registry_image" >/dev/null
registry_host=localhost:$(docker port "$registry" 5000/tcp | sed 's/.*://')
tries=0
until curl -fsS -o /dev/null "http://$registry_host/v2/" 2>/dev/null; do
  tries=$((tries + 1))
  [ "$tries" -lt 30 ] || { echo 'the registry did not start' >&2; exit 1; }
  sleep 1
done
for name in $(kind get nodes --name "$cluster"); do
  docker exec "$name" mkdir -p "/etc/containerd/certs.d/$registry_host"
  printf '[host."http://%s:5000"]\n' "$registry" |
    docker exec -i "$name" cp /dev/stdin "/etc/containerd/certs.d/$registry_host/hosts.toml"
done

push() { # LOCAL_IMAGE NAME: prints the pushed digest
  docker tag "$1" "$registry_host/graphnest/$2:test"
  docker push "$registry_host/graphnest/$2:test" | sed -n 's/.*digest: \(sha256:[a-f0-9]*\).*/\1/p'
}
application_digest=$(push "$application" application)
node_digest=$(push "$node" node)
[ -n "$application_digest" ] && [ -n "$node_digest" ] || { echo 'image push printed no digest' >&2; exit 1; }

openssl genrsa -out "$tmp/app.pem" 2048 2>/dev/null
postgres_image=$(sed -n 's/^ *image: \(postgres:.*\)/\1/p' "$root/deploy/compose/compose.yml")

prepare() { # NAMESPACE: what the chart expects an operator to provide
  kubectl create namespace "$1"
  kubectl -n "$1" create secret generic graphnest-runtime \
    --from-literal=database-url='postgres://graphnest:graphnest@postgres:5432/graphnest?sslmode=disable'
  kubectl -n "$1" create secret generic graphnest-github-app \
    --from-file=private-key.pem="$tmp/app.pem" --from-literal=webhook-secret="$(openssl rand -hex 32)"
  # Probing over TCP skips the socket-only server the image runs while it initializes.
  kubectl -n "$1" apply -f - <<EOF
apiVersion: apps/v1
kind: Deployment
metadata: {name: postgres}
spec:
  selector: {matchLabels: {app: postgres}}
  template:
    metadata: {labels: {app: postgres}}
    spec:
      containers:
        - name: postgres
          image: $postgres_image
          env:
            - {name: POSTGRES_USER, value: graphnest}
            - {name: POSTGRES_PASSWORD, value: graphnest}
          readinessProbe:
            exec: {command: [pg_isready, -h, 127.0.0.1, -U, graphnest]}
            periodSeconds: 2
---
apiVersion: v1
kind: Service
metadata: {name: postgres}
spec:
  selector: {app: postgres}
  ports: [{port: 5432}]
EOF
  kubectl -n "$1" rollout status deployment/postgres --timeout=180s
}

install_tree() { # NAMESPACE: installs or upgrades to the chart and images of this tree
  helm upgrade --install graphnest "$chart" -n "$1" -f "$values" --wait --timeout 10m \
    --set-string "images.application.repository=$registry_host/graphnest/application" \
    --set-string "images.application.digest=$application_digest" \
    --set-string "images.node.repository=$registry_host/graphnest/node" \
    --set-string "images.node.digest=$node_digest"
}

token() { # NAMESPACE: stores an API token the way the server does and prints it
  value=gnp_$(openssl rand -base64 32 | tr '+/' '-_' | tr -d '=\n')
  hash=$(printf %s "$value" | openssl dgst -sha256 -r | cut -d' ' -f1)
  kubectl -n "$1" exec -i deploy/postgres -- psql -q -v ON_ERROR_STOP=1 -U graphnest >/dev/null <<EOF
with created as (
  insert into users (external_id, user_name, source) values ('kind-test', 'kind-test', 'local') returning id
)
insert into api_tokens (token_hash, prefix, user_id)
select decode('$hash', 'hex'), '$(printf %s "$value" | cut -c1-12)', id from created;
EOF
  printf %s "$value"
}

request() { # NAMESPACE WGET_ARGUMENTS: from a server pod, so the call crosses the Service and the network policies
  namespace=$1
  shift
  # After an upgrade the old pods are still shutting down; skip those.
  pod=$(kubectl -n "$namespace" get pods -l app.kubernetes.io/component=server \
    -o jsonpath='{range .items[*]}{.metadata.deletionTimestamp}{" "}{.metadata.name}{"\n"}{end}' |
    awk 'NF == 1 { print $1; exit }')
  # A connection opened just after a rollout can still land on a pod that has gone, hence the retries.
  kubectl -n "$namespace" exec "$pod" -- wget -qO- -T 5 -t 6 "$@"
}

smoke() { # NAMESPACE TOKEN
  request "$1" http://graphnest-server:8080/readyz >/dev/null
  request "$1" http://graphnest-server:8080/v1/auth/config | jq -e '.token_login' >/dev/null
  request "$1" -S http://graphnest-server:8080/v1/repositories 2>&1 | grep -q 'HTTP/1.1 401'
  request "$1" --header "Authorization: Bearer $2" http://graphnest-server:8080/v1/repositories |
    jq -e '.repositories == []' >/dev/null
  # The database pod is in the namespace but is neither server nor node: it may reach the API, never Zoekt.
  tries=0
  until kubectl -n "$1" exec deploy/postgres -- timeout 5 bash -c 'exec 3<>/dev/tcp/graphnest-server/8080' 2>/dev/null; do
    tries=$((tries + 1))
    [ "$tries" -lt 6 ]
    sleep 2
  done
  dropped=0
  kubectl -n "$1" exec deploy/postgres -- timeout 5 bash -c 'exec 3<>/dev/tcp/graphnest-zoekt/6070' \
    2>/dev/null || dropped=$?
  # 124 is the timeout: the policy dropped the connection, nothing refused it.
  [ "$dropped" -eq 124 ]
}

prepare graphnest-fresh
install_tree graphnest-fresh
fresh_token=$(token graphnest-fresh)
smoke graphnest-fresh "$fresh_token"

# The released chart carries the digests of the released images. It takes the
# same values file: a value it rejects is one an upgrading operator must add.
helm pull "$released" --destination "$tmp"
prepare graphnest-upgrade
helm upgrade --install graphnest "$tmp"/graphnest-*.tgz -n graphnest-upgrade -f "$values" --wait --timeout 10m
upgrade_token=$(token graphnest-upgrade)
smoke graphnest-upgrade "$upgrade_token"
install_tree graphnest-upgrade
# The token written before the upgrade still works after it.
smoke graphnest-upgrade "$upgrade_token"

echo 'helm install and upgrade tests passed'
