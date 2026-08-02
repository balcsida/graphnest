#!/bin/sh
set -eu

application=${APPLICATION_IMAGE:-grepnest-application:dev}
node=${NODE_IMAGE:-grepnest-node:dev}
root=$(CDPATH= cd -- "$(dirname -- "$0")/../.." && pwd)
container=grepnest-image-smoke-$$
trap 'docker rm -f "$container" >/dev/null 2>&1 || true' 0 HUP INT TERM

docker run --rm --read-only --tmpfs /tmp --tmpfs /var/run/grepnest \
  "$application" /bin/sh -ec '
    test "$(id -u)" -ne 0
    id -G | tr " " "\n" | grep -qx 0
    command -v grepnest-server
    command -v grepnest-admin
    command -v grepnest-migrate
    command -v grepnest-mcp
    command -v wget
  '

docker run -d --name "$container" --read-only --tmpfs /tmp \
  --tmpfs /var/run/grepnest \
  -e GREPNEST_ZOEKT_URL=http://127.0.0.1:9 \
  -e GREPNEST_REPOSITORIES_FILE=/etc/grepnest/repositories.json \
  -e GREPNEST_USER_TOKEN=user-token \
  -e GREPNEST_ADMIN_TOKEN=admin-token \
  -v "$root/deploy/compose/repositories.json:/etc/grepnest/repositories.json:ro" \
  "$application" >/dev/null

attempt=0
until docker exec "$container" wget -q --spider http://127.0.0.1:8080/healthz; do
  attempt=$((attempt + 1))
  [ "$attempt" -lt 20 ] || {
    docker logs "$container" >&2
    exit 1
  }
  sleep 1
done
docker rm -f "$container" >/dev/null

docker run --rm --read-only --tmpfs /tmp --tmpfs /var/run/grepnest \
  "$node" /bin/sh -ec '
    test "$(id -u)" -ne 0
    id -G | tr " " "\n" | grep -qx 0
    command -v grepnest-indexer
    command -v grepnest-scanner
    command -v grepnest-graph
    command -v git
    command -v zoekt-git-index
    command -v zoekt-webserver
    git --version
    ldd /usr/local/bin/grepnest-indexer | grep -q "liblbug.so.0 => /usr/lib/"
    ldd /usr/local/bin/grepnest-graph | grep -q "liblbug.so.0 => /usr/lib/"
  '

echo "image smoke tests passed"
