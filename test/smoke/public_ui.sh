#!/bin/sh
set -eu

root=$(CDPATH= cd -- "$(dirname -- "$0")/../.." && pwd)
tmp=$(mktemp -d "${TMPDIR:-/tmp}/grepnest-ui-smoke.XXXXXX")
hello_sha=7fd1a60b01f91b314f59955a4e4d4e80d8edf11d
spoon_sha=d0dd1f61b33d64e29d8bc1372a94ef6a2fee76a9
zoekt_pid=
server_pid=

cleanup() {
  status=$?
  trap - 0 HUP INT TERM
  [ -z "$server_pid" ] || kill "$server_pid" 2>/dev/null || true
  [ -z "$zoekt_pid" ] || kill "$zoekt_pid" 2>/dev/null || true
  if [ "$status" -ne 0 ]; then
    [ ! -f "$tmp/zoekt.log" ] || sed -n '1,240p' "$tmp/zoekt.log" >&2
    [ ! -f "$tmp/server.log" ] || sed -n '1,240p' "$tmp/server.log" >&2
  fi
  rm -rf "$tmp"
  exit "$status"
}
trap cleanup 0 HUP INT TERM

fetch_repository() {
  url=$1 sha=$2 branch=$3 id=$4 name=$5 path=$6
  git init -q "$path"
  git -C "$path" -c credential.helper= -c core.askPass= -c core.hooksPath=/dev/null fetch -q --depth=1 "$url" "$sha"
  git -C "$path" -c core.hooksPath=/dev/null checkout -q -b "$branch" FETCH_HEAD
  test "$(git -C "$path" rev-parse HEAD)" = "$sha"
  git -C "$path" config zoekt.repoid "$id"
  git -C "$path" config zoekt.name "$name"
}

wait_http() {
  url=$1 attempts=0
  until curl -fsS "$url" >/dev/null; do
    attempts=$((attempts + 1))
    [ "$attempts" -lt 60 ] || return 1
    sleep 1
  done
}

wait_zoekt() {
  attempts=0
  until curl -fsS -H 'Content-Type: application/json' \
    --data '{"Q":"file:.","RepoIDs":[],"Opts":{"MaxDocDisplayCount":1}}' \
    http://127.0.0.1:16070/api/search >/dev/null; do
    attempts=$((attempts + 1))
    [ "$attempts" -lt 60 ] || return 1
    sleep 1
  done
}

cd "$root"
export GIT_TERMINAL_PROMPT=0
unset GH_TOKEN GITHUB_TOKEN GIT_ASKPASS
test -x ./node_modules/.bin/playwright
test -x .cache/bin/zoekt-git-index
test -x .cache/bin/zoekt-webserver

fetch_repository https://github.com/octocat/Hello-World.git "$hello_sha" master 101 octocat/Hello-World "$tmp/hello"
fetch_repository https://github.com/octocat/Spoon-Knife.git "$spoon_sha" main 102 octocat/Spoon-Knife "$tmp/spoon"
mkdir "$tmp/index"
.cache/bin/zoekt-git-index -index "$tmp/index" -branches master -submodules=false -incremental=false "$tmp/hello"
.cache/bin/zoekt-git-index -index "$tmp/index" -branches main -submodules=false -incremental=false "$tmp/spoon"

cat >"$tmp/repositories.json" <<EOF
[
  {"id":101,"zoekt_id":101,"name":"octocat/Hello-World","branch":"master","indexed_sha":"$hello_sha","web_url":"https://github.com/octocat/Hello-World"},
  {"id":102,"zoekt_id":102,"name":"octocat/Spoon-Knife","branch":"main","indexed_sha":"$spoon_sha","web_url":"https://github.com/octocat/Spoon-Knife"}
]
EOF

.cache/bin/zoekt-webserver -index "$tmp/index" -listen 127.0.0.1:16070 -rpc -html=false >"$tmp/zoekt.log" 2>&1 &
zoekt_pid=$!
wait_zoekt

GREPNEST_LISTEN_ADDRESS=127.0.0.1:18080 \
GREPNEST_ZOEKT_URL=http://127.0.0.1:16070 \
GREPNEST_REPOSITORIES_FILE="$tmp/repositories.json" \
GREPNEST_USER_TOKEN=grepnest-public-user \
GREPNEST_ADMIN_TOKEN=grepnest-public-admin \
GREPNEST_USER_REPOSITORIES=octocat/Hello-World,octocat/Spoon-Knife \
GREPNEST_ADMIN_REPOSITORIES=octocat/Hello-World,octocat/Spoon-Knife \
  go run ./cmd/grepnest-server >"$tmp/server.log" 2>&1 &
server_pid=$!
wait_http http://127.0.0.1:18080/healthz

GREPNEST_UI_SMOKE_URL=http://127.0.0.1:18080 \
GREPNEST_UI_SMOKE_TOKEN=grepnest-public-user \
  ./node_modules/.bin/playwright test test/smoke/public-ui.spec.mjs --workers=1
