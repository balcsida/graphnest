#!/bin/sh
set -eu

base_config=$(env \
  GRAPHNEST_APPLICATION_IMAGE= \
  GRAPHNEST_GITHUB_PRIVATE_KEY_FILE= \
  GRAPHNEST_GITHUB_WEBHOOK_SECRET_FILE= \
  GRAPHNEST_GITHUB_WEB_URL= \
  GRAPHNEST_GITHUB_API_URL= \
  GRAPHNEST_GITHUB_UPLOAD_URL= \
  GRAPHNEST_GITHUB_GIT_URL= \
  GRAPHNEST_GITHUB_APP_ID= \
  GRAPHNEST_USER_TOKEN= \
  GRAPHNEST_USER_INSTALLATION_ID= \
  GRAPHNEST_USER_REPOSITORY_IDS= \
  GRAPHNEST_ADMIN_TOKEN= \
  GRAPHNEST_ADMIN_INSTALLATION_ID= \
  GRAPHNEST_ADMIN_REPOSITORY_IDS= \
  docker compose -f deploy/compose/compose.yml --profile fixture config --format json)

printf '%s' "${base_config:?missing fixture Compose config}" |
  jq -e '
    (.services | has("graphnest-server") | not)
    and (.services | has("postgres"))
    and (.services | has("zoekt"))
    and (.services | has("zoekt-index"))
  ' >/dev/null

fixture=$(mktemp -d "${TMPDIR:-/tmp}/graphnest-fixture.XXXXXX")
case "$fixture" in
  "${TMPDIR:-/tmp}"/graphnest-fixture.*) ;;
  *) echo "unexpected temporary directory: $fixture" >&2; exit 1 ;;
esac
trap 'rm -rf "$fixture"' EXIT
cp -R test/fixtures/repository/. "$fixture"
git init --initial-branch=main "$fixture" >/dev/null
git -C "$fixture" config user.name "GraphNest Test"
git -C "$fixture" config user.email test@graphnest.invalid
git -C "$fixture" config commit.gpgsign false
git -C "$fixture" config zoekt.repoid 7
git -C "$fixture" config zoekt.name fixture/repository
git -C "$fixture" add .
GIT_AUTHOR_DATE=2000-01-01T00:00:00Z \
  GIT_COMMITTER_DATE=2000-01-01T00:00:00Z \
  git -C "$fixture" commit -m fixture >/dev/null
fixture_sha=$(git -C "$fixture" rev-parse HEAD)
pinned_sha=$(jq -r '.[] | select(.name=="fixture/repository") | .indexed_sha' deploy/compose/repositories.json)
if [ "$fixture_sha" != "$pinned_sha" ]; then
  echo "fixture commit $fixture_sha drifted from indexed_sha $pinned_sha in deploy/compose/repositories.json" >&2
  echo "update repositories.json after changing test/fixtures/repository" >&2
  exit 1
fi

render_files() {
  overlay=$1
  shift
  overlay_args=
  [ -z "$overlay" ] || overlay_args="-f $overlay"
  env \
    GRAPHNEST_NODE_IMAGE=registry.example/graphnest/node:test \
    GRAPHNEST_APPLICATION_IMAGE= \
    GRAPHNEST_GITHUB_CA_FILE= \
    GRAPHNEST_PUBLIC_URL= \
    GRAPHNEST_SSO_SESSION_IDLE= \
    GRAPHNEST_SSO_SESSION_TTL= \
    GRAPHNEST_SSO_LOGIN_FLOW_TTL= \
    GRAPHNEST_OIDC_ISSUER_URL= \
    GRAPHNEST_OIDC_CLIENT_ID= \
    GRAPHNEST_OIDC_CLIENT_SECRET_FILE= \
    GRAPHNEST_OIDC_CA_FILE= \
    GRAPHNEST_OIDC_SCOPES= \
    GRAPHNEST_OIDC_LINK_CLAIM= \
    GRAPHNEST_OIDC_DISPLAY_NAME_CLAIM= \
    GRAPHNEST_OAUTH_GITHUB_CLIENT_ID= \
    GRAPHNEST_OAUTH_GITHUB_CLIENT_SECRET_FILE= \
    GRAPHNEST_SCIM_TOKEN_FILE= \
    GRAPHNEST_BREAK_GLASS_ENABLED= \
    GRAPHNEST_GITHUB_PRIVATE_KEY_FILE=/tmp/private-key.pem \
    GRAPHNEST_GITHUB_WEBHOOK_SECRET_FILE=/tmp/webhook-secret \
    GRAPHNEST_GITHUB_WEB_URL=https://github.example \
    GRAPHNEST_GITHUB_API_URL=https://github.example/api/v3 \
    GRAPHNEST_GITHUB_UPLOAD_URL=https://github.example/api/uploads \
    GRAPHNEST_GITHUB_GIT_URL=https://github.example \
    GRAPHNEST_GITHUB_APP_ID=1 \
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

config=$(render \
  GRAPHNEST_APPLICATION_IMAGE=registry.example/graphnest/application:test \
  GRAPHNEST_GITHUB_CA_FILE=/tmp/github-ca.pem \
  GRAPHNEST_PUBLIC_URL=https://graphnest.example \
  GRAPHNEST_SSO_SESSION_IDLE=30m \
  GRAPHNEST_SSO_SESSION_TTL=8h \
  GRAPHNEST_SSO_LOGIN_FLOW_TTL=10m \
  GRAPHNEST_OIDC_ISSUER_URL=https://id.example \
  GRAPHNEST_OIDC_CLIENT_ID=graphnest \
  GRAPHNEST_OIDC_CLIENT_SECRET_FILE=/tmp/oidc-client-secret \
  GRAPHNEST_OIDC_CA_FILE=/tmp/oidc-ca.pem \
  GRAPHNEST_OIDC_SCOPES=openid,profile,email \
  GRAPHNEST_OIDC_LINK_CLAIM=sub \
  GRAPHNEST_OIDC_DISPLAY_NAME_CLAIM=name \
  GRAPHNEST_OAUTH_GITHUB_CLIENT_ID=graphnest-github \
  GRAPHNEST_OAUTH_GITHUB_CLIENT_SECRET_FILE=/tmp/oauth-github-client-secret \
  GRAPHNEST_SCIM_TOKEN_FILE=/tmp/scim-token)

printf '%s' "$config" | jq -e '
  .services["graphnest-server"] as $server
  | .services["graphnest-indexer"] as $indexer
  | if $server == null then false else
    (.services | has("graphnest-scanner") | not)
  and ($indexer.environment.GRAPHNEST_SOURCE_PROVIDER == "archive")
  and ($indexer.environment | has("GRAPHNEST_GIT_PATH") | not)
  and ($indexer.volumes | any(.target == "/var/lib/graphnest/work" and .type == "tmpfs"))
  and ($indexer.volumes | any(.target == "/var/lib/graphnest/index" and .type == "bind"))
  and ([.volumes // {} | keys[]] | length == 0)
  and
    $server.profiles == ["durable"]
  and $server.image == "registry.example/graphnest/application:test"
  and $server.entrypoint == ["graphnest-server"]
  and $server.depends_on.postgres.condition == "service_healthy"
  and $server.depends_on["zoekt-durable"].condition == "service_healthy"
  and ($server.environment | keys | sort) == [
    "GRAPHNEST_BREAK_GLASS_ENABLED", "GRAPHNEST_DATABASE_URL", "GRAPHNEST_GITHUB_API_URL", "GRAPHNEST_GITHUB_APP_ID",
    "GRAPHNEST_GITHUB_CA_FILE", "GRAPHNEST_GITHUB_GIT_URL", "GRAPHNEST_GITHUB_PRIVATE_KEY_FILE", "GRAPHNEST_GITHUB_UPLOAD_URL",
    "GRAPHNEST_GITHUB_WEBHOOK_SECRET_FILE", "GRAPHNEST_GITHUB_WEB_URL", "GRAPHNEST_OAUTH_GITHUB_CLIENT_ID", "GRAPHNEST_OAUTH_GITHUB_CLIENT_SECRET_FILE", "GRAPHNEST_OIDC_CA_FILE", "GRAPHNEST_OIDC_CLIENT_ID", "GRAPHNEST_OIDC_CLIENT_SECRET_FILE", "GRAPHNEST_OIDC_DISPLAY_NAME_CLAIM", "GRAPHNEST_OIDC_ISSUER_URL", "GRAPHNEST_OIDC_LINK_CLAIM", "GRAPHNEST_OIDC_SCOPES", "GRAPHNEST_PUBLIC_URL", "GRAPHNEST_SCIM_TOKEN_FILE", "GRAPHNEST_SCIP_MAX_UPLOAD_BYTES", "GRAPHNEST_SSO_LOGIN_FLOW_TTL", "GRAPHNEST_SSO_SESSION_IDLE", "GRAPHNEST_SSO_SESSION_TTL",
    "GRAPHNEST_ZOEKT_URL"
  ]
  and ($server.ports | any(.host_ip == "127.0.0.1" and .target == 8080 and .published == "8080"))
  and ($server.networks | keys | sort) == ["internal", "loopback"]
  and ([ $server.volumes[] | select(.target == "/run/secrets/graphnest/private-key.pem" or .target == "/run/secrets/graphnest/webhook-secret") | .read_only ] | length == 2 and all(. == true))
  and ([ $server.volumes[].bind.create_host_path ] | all((. // false) == false))
  and $server.environment.GRAPHNEST_GITHUB_CA_FILE == "/run/secrets/graphnest/github-ca.pem"
  and ($server.volumes | any(.source == "/tmp/github-ca.pem" and .target == "/run/secrets/graphnest/github-ca.pem" and .read_only))
  and $server.environment.GRAPHNEST_OIDC_CLIENT_SECRET_FILE == "/run/secrets/graphnest/oidc-client-secret"
  and $server.environment.GRAPHNEST_OIDC_CA_FILE == "/run/secrets/graphnest/oidc-ca.pem"
  and ($server.volumes | any(.source == "/tmp/oidc-client-secret" and .target == "/run/secrets/graphnest/oidc-client-secret" and .read_only))
  and ($server.volumes | any(.source == "/tmp/oidc-ca.pem" and .target == "/run/secrets/graphnest/oidc-ca.pem" and .read_only))
  and $server.environment.GRAPHNEST_OAUTH_GITHUB_CLIENT_ID == "graphnest-github"
  and $server.environment.GRAPHNEST_OAUTH_GITHUB_CLIENT_SECRET_FILE == "/run/secrets/graphnest/oauth-github-client-secret"
  and ($server.volumes | any(.source == "/tmp/oauth-github-client-secret" and .target == "/run/secrets/graphnest/oauth-github-client-secret" and .read_only))
  and $server.environment.GRAPHNEST_SCIM_TOKEN_FILE == "/run/secrets/graphnest/scim/token"
  and $server.environment.GRAPHNEST_BREAK_GLASS_ENABLED == "false"
  and ($server.volumes | any(.source == "/tmp/scim-token" and .target == "/run/secrets/graphnest/scim/token" and .read_only))
  and $server.healthcheck.test == ["CMD", "wget", "-q", "--spider", "http://127.0.0.1:8080/readyz"]
  end
' >/dev/null

enabled=$(render \
  GRAPHNEST_APPLICATION_IMAGE=registry.example/graphnest/application:test \
  GRAPHNEST_BREAK_GLASS_ENABLED=true)

printf '%s' "$enabled" | jq -e '
  .services["graphnest-server"].environment.GRAPHNEST_BREAK_GLASS_ENABLED == "true"
  and ([.services["graphnest-server"].environment | keys[] | select(test("PASSWORD|HASH|SALT"))] | length == 0)
' >/dev/null

without_ca=$(render GRAPHNEST_APPLICATION_IMAGE=registry.example/graphnest/application:test)

printf '%s' "${without_ca:?missing Compose config without private CA}" |
  jq -e '
    .services["graphnest-server"] as $server
    | $server.image == "registry.example/graphnest/application:test"
    and $server.environment.GRAPHNEST_GITHUB_CA_FILE == ""
    and $server.environment.GRAPHNEST_OAUTH_GITHUB_CLIENT_ID == ""
    and $server.environment.GRAPHNEST_OAUTH_GITHUB_CLIENT_SECRET_FILE == ""
    and $server.environment.GRAPHNEST_SCIM_TOKEN_FILE == ""
    and ($server.volumes | any(.source == "/dev/null" and .target == "/run/secrets/graphnest/github-ca.pem" and .read_only and (.bind.create_host_path // false) == false))
    and ($server.volumes | any(.source == "/dev/null" and .target == "/run/secrets/graphnest/oauth-github-client-secret" and .read_only and (.bind.create_host_path // false) == false))
  ' >/dev/null

if render >/dev/null 2>&1; then
  echo "expected GRAPHNEST_APPLICATION_IMAGE to be required" >&2
  exit 1
fi
