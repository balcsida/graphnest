#!/bin/sh
set -eu

base_config=$(env \
  GREPNEST_APPLICATION_IMAGE= \
  GREPNEST_GITHUB_PRIVATE_KEY_FILE= \
  GREPNEST_GITHUB_WEBHOOK_SECRET_FILE= \
  GREPNEST_GITHUB_WEB_URL= \
  GREPNEST_GITHUB_API_URL= \
  GREPNEST_GITHUB_UPLOAD_URL= \
  GREPNEST_GITHUB_GIT_URL= \
  GREPNEST_GITHUB_APP_ID= \
  GREPNEST_USER_TOKEN= \
  GREPNEST_USER_INSTALLATION_ID= \
  GREPNEST_USER_REPOSITORY_IDS= \
  GREPNEST_ADMIN_TOKEN= \
  GREPNEST_ADMIN_INSTALLATION_ID= \
  GREPNEST_ADMIN_REPOSITORY_IDS= \
  docker compose -f deploy/compose/compose.yml --profile fixture config --format json)

printf '%s' "${base_config:?missing fixture Compose config}" |
  jq -e '
    (.services | has("grepnest-server") | not)
    and (.services | has("postgres"))
    and (.services | has("zoekt"))
    and (.services | has("zoekt-index"))
  ' >/dev/null

render_files() {
  overlay=$1
  shift
  overlay_args=
  [ -z "$overlay" ] || overlay_args="-f $overlay"
  env \
    GREPNEST_NODE_IMAGE=registry.example/grepnest/node:test \
    GREPNEST_SCANNER_IMAGE=registry.example/grepnest/scanner:test \
    GREPNEST_APPLICATION_IMAGE= \
    GREPNEST_GITHUB_CA_FILE= \
    GREPNEST_PUBLIC_URL= \
    GREPNEST_SSO_SESSION_IDLE= \
    GREPNEST_SSO_SESSION_TTL= \
    GREPNEST_SSO_LOGIN_FLOW_TTL= \
    GREPNEST_OIDC_ISSUER_URL= \
    GREPNEST_OIDC_CLIENT_ID= \
    GREPNEST_OIDC_CLIENT_SECRET_FILE= \
    GREPNEST_OIDC_CA_FILE= \
    GREPNEST_OIDC_SCOPES= \
    GREPNEST_OIDC_LINK_CLAIM= \
    GREPNEST_OIDC_DISPLAY_NAME_CLAIM= \
    GREPNEST_GITHUB_PRIVATE_KEY_FILE=/tmp/private-key.pem \
    GREPNEST_GITHUB_WEBHOOK_SECRET_FILE=/tmp/webhook-secret \
    GREPNEST_GITHUB_WEB_URL=https://github.example \
    GREPNEST_GITHUB_API_URL=https://github.example/api/v3 \
    GREPNEST_GITHUB_UPLOAD_URL=https://github.example/api/uploads \
    GREPNEST_GITHUB_GIT_URL=https://github.example \
    GREPNEST_GITHUB_APP_ID=1 \
    GREPNEST_USER_TOKEN=user-token \
    GREPNEST_USER_INSTALLATION_ID=2 \
    GREPNEST_USER_REPOSITORY_IDS=3 \
    GREPNEST_ADMIN_TOKEN=admin-token \
    GREPNEST_ADMIN_INSTALLATION_ID=4 \
    GREPNEST_ADMIN_REPOSITORY_IDS=5 \
    "$@" \
    docker compose \
      -f deploy/compose/compose.yml \
      -f deploy/compose/durable.yml \
      $overlay_args \
      --profile durable \
      config \
      --format json
}

render() {
  render_files "" "$@"
}

render_graph() {
  overlay=$1
  shift
  render_files "$overlay" \
    GREPNEST_APPLICATION_IMAGE=registry.example/grepnest/application:test \
    GREPNEST_GRAPH_INTERNAL_SECRET_FILE=/tmp/graph-secret \
    "$@"
}

assert_graph_mode() {
  config=$1
  owner=$2

  printf '%s' "$config" | jq -e --arg owner "$owner" '
    .services as $services
    | $services["grepnest-server"] as $server
    | $services["grepnest-indexer"] as $indexer
    | $services["grepnest-scanner"] as $scanner
    | ($services["grepnest-graph"] != null) as $has_graph
    | ($services["zoekt-durable"].volumes[] | select(.target == "/data/index") | .source) as $zoekt_index
    | $services[$owner] as $graph_owner
    | ($services | to_entries | map(select(.value.environment.GREPNEST_GRAPH_DATA_DIR? != null)) | length) == 1
    and (($services | to_entries | map(select(.value.environment.GREPNEST_GRAPH_DATA_DIR? != null))[0].key) == $owner)
    and ($server.environment.GREPNEST_GRAPH_URL == "http://grepnest-graph:8081")
    and ($server.environment.GREPNEST_GRAPH_SECRET_FILE == "/run/secrets/grepnest/graph-secret")
    and ([ $server.volumes[] | select(.target == "/run/secrets/grepnest/graph-secret") | .read_only ] == [true])
    and ($graph_owner.image == "registry.example/grepnest/node:test")
    and ($graph_owner.environment.GREPNEST_GRAPH_SECRET_FILE == "/run/secrets/grepnest/graph-secret")
    and ([ $graph_owner.volumes[] | select(.target == "/run/secrets/grepnest/graph-secret") | .read_only ] == [true])
    and ($indexer.image == "registry.example/grepnest/node:test")
    and ($indexer.entrypoint == ["grepnest-indexer"])
    and ($scanner.image == "registry.example/grepnest/scanner:test")
    and ($scanner.entrypoint == ["/bin/sh", "-ec"])
    and ($scanner.command == ["GREPNEST_WORKER_ID=$$(hostname) exec grepnest-scanner"])
    and ($scanner.deploy.replicas >= 1)
    and ([ $services[] | .ports[]? | select(.target == 8081) ] | length == 0)
    and ([ $services[] | .volumes[]? | select(.target == "/var/lib/grepnest/graph" and (.read_only // false | not)) ] | length == 1)
    and ([ $graph_owner.volumes[] | select(.target == "/var/lib/grepnest/graph" and (.read_only // false | not)) ] | length == 1)
    and ([ $server.volumes[] | select(.target == "/var/lib/grepnest/graph") ] | length == 0)
    and ([ $services[] | .volumes[]? | select(.source == $zoekt_index and (.read_only // false | not)) ] | length == 1)
    and (($owner == "grepnest-graph") == $has_graph)
  ' >/dev/null
}

assert_rejects_extra_graph_writer() {
  config=$1
  owner=$2
  extra_writer=$(printf '%s' "$config" | jq '
    .services["unexpected-graph-writer"] = {
      volumes: [{type: "volume", source: "grepnest-data", target: "/var/lib/grepnest/graph"}]
    }
  ')

  if assert_graph_mode "$extra_writer" "$owner"; then
    echo "expected graph mode assertion to reject an extra graph writer" >&2
    exit 1
  fi
}

embedded=$(render_graph deploy/compose/graph-embedded.yml)
assert_graph_mode "$embedded" grepnest-indexer
assert_rejects_extra_graph_writer "$embedded" grepnest-indexer

separate=$(render_graph deploy/compose/graph-separate.yml)
assert_graph_mode "$separate" grepnest-graph

config=$(render \
  GREPNEST_APPLICATION_IMAGE=registry.example/grepnest/application:test \
  GREPNEST_GITHUB_CA_FILE=/tmp/github-ca.pem \
  GREPNEST_PUBLIC_URL=https://grepnest.example \
  GREPNEST_SSO_SESSION_IDLE=30m \
  GREPNEST_SSO_SESSION_TTL=8h \
  GREPNEST_SSO_LOGIN_FLOW_TTL=10m \
  GREPNEST_OIDC_ISSUER_URL=https://id.example \
  GREPNEST_OIDC_CLIENT_ID=grepnest \
  GREPNEST_OIDC_CLIENT_SECRET_FILE=/tmp/oidc-client-secret \
  GREPNEST_OIDC_CA_FILE=/tmp/oidc-ca.pem \
  GREPNEST_OIDC_SCOPES=openid,profile,email \
  GREPNEST_OIDC_LINK_CLAIM=sub \
  GREPNEST_OIDC_DISPLAY_NAME_CLAIM=name)

printf '%s' "$config" | jq -e '
  .services["grepnest-server"] as $server
  | if $server == null then false else
    $server.profiles == ["durable"]
  and $server.image == "registry.example/grepnest/application:test"
  and $server.entrypoint == ["grepnest-server"]
  and $server.depends_on.postgres.condition == "service_healthy"
  and $server.depends_on["zoekt-durable"].condition == "service_healthy"
  and ($server.environment | keys | sort) == [
    "GREPNEST_ADMIN_INSTALLATION_ID", "GREPNEST_ADMIN_REPOSITORY_IDS", "GREPNEST_ADMIN_TOKEN",
    "GREPNEST_DATABASE_URL", "GREPNEST_GITHUB_API_URL", "GREPNEST_GITHUB_APP_ID",
    "GREPNEST_GITHUB_CA_FILE", "GREPNEST_GITHUB_GIT_URL", "GREPNEST_GITHUB_PRIVATE_KEY_FILE", "GREPNEST_GITHUB_UPLOAD_URL",
    "GREPNEST_GITHUB_WEBHOOK_SECRET_FILE", "GREPNEST_GITHUB_WEB_URL", "GREPNEST_OIDC_CA_FILE", "GREPNEST_OIDC_CLIENT_ID", "GREPNEST_OIDC_CLIENT_SECRET_FILE", "GREPNEST_OIDC_DISPLAY_NAME_CLAIM", "GREPNEST_OIDC_ISSUER_URL", "GREPNEST_OIDC_LINK_CLAIM", "GREPNEST_OIDC_SCOPES", "GREPNEST_PUBLIC_URL", "GREPNEST_SCIP_MAX_UPLOAD_BYTES", "GREPNEST_SSO_LOGIN_FLOW_TTL", "GREPNEST_SSO_SESSION_IDLE", "GREPNEST_SSO_SESSION_TTL",
    "GREPNEST_USER_INSTALLATION_ID", "GREPNEST_USER_REPOSITORY_IDS", "GREPNEST_USER_TOKEN", "GREPNEST_ZOEKT_URL"
  ]
  and ($server.ports | any(.host_ip == "127.0.0.1" and .target == 8080 and .published == "8080"))
  and ($server.networks | keys | sort) == ["internal", "loopback"]
  and ([ $server.volumes[] | select(.target == "/run/secrets/grepnest/private-key.pem" or .target == "/run/secrets/grepnest/webhook-secret") | .read_only ] | length == 2 and all(. == true))
  and ([ $server.volumes[].bind.create_host_path ] | all((. // false) == false))
  and $server.environment.GREPNEST_GITHUB_CA_FILE == "/run/secrets/grepnest/github-ca.pem"
  and ($server.volumes | any(.source == "/tmp/github-ca.pem" and .target == "/run/secrets/grepnest/github-ca.pem" and .read_only))
  and $server.environment.GREPNEST_OIDC_CLIENT_SECRET_FILE == "/run/secrets/grepnest/oidc-client-secret"
  and $server.environment.GREPNEST_OIDC_CA_FILE == "/run/secrets/grepnest/oidc-ca.pem"
  and ($server.volumes | any(.source == "/tmp/oidc-client-secret" and .target == "/run/secrets/grepnest/oidc-client-secret" and .read_only))
  and ($server.volumes | any(.source == "/tmp/oidc-ca.pem" and .target == "/run/secrets/grepnest/oidc-ca.pem" and .read_only))
  and $server.healthcheck.test == ["CMD", "wget", "-q", "--spider", "http://127.0.0.1:8080/readyz"]
  end
' >/dev/null

without_ca=$(render GREPNEST_APPLICATION_IMAGE=registry.example/grepnest/application:test)

printf '%s' "${without_ca:?missing Compose config without private CA}" |
  jq -e '
    .services["grepnest-server"] as $server
    | $server.image == "registry.example/grepnest/application:test"
    and $server.environment.GREPNEST_GITHUB_CA_FILE == ""
    and ($server.volumes | any(.source == "/dev/null" and .target == "/run/secrets/grepnest/github-ca.pem" and .read_only and (.bind.create_host_path // false) == false))
  ' >/dev/null

if render >/dev/null 2>&1; then
  echo "expected GREPNEST_APPLICATION_IMAGE to be required" >&2
  exit 1
fi
