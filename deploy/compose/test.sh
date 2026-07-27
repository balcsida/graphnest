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

render() {
  env \
    GREPNEST_APPLICATION_IMAGE= \
    GREPNEST_GITHUB_CA_FILE= \
    GREPNEST_PUBLIC_URL= \
    GREPNEST_SSO_SESSION_TTL= \
    GREPNEST_SSO_LOGIN_FLOW_TTL= \
    GREPNEST_OIDC_ISSUER_URL= \
    GREPNEST_OIDC_CLIENT_ID= \
    GREPNEST_OIDC_CLIENT_SECRET_FILE= \
    GREPNEST_OIDC_CA_FILE= \
    GREPNEST_OIDC_SCOPES= \
    GREPNEST_OIDC_GROUPS_CLAIM= \
    GREPNEST_OIDC_ALLOWED_GROUPS= \
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
      --profile durable \
      config \
      --format json
}

config=$(render \
  GREPNEST_APPLICATION_IMAGE=registry.example/grepnest/application:test \
  GREPNEST_GITHUB_CA_FILE=/tmp/github-ca.pem \
  GREPNEST_PUBLIC_URL=https://grepnest.example \
  GREPNEST_SSO_SESSION_TTL=12h \
  GREPNEST_SSO_LOGIN_FLOW_TTL=5m \
  GREPNEST_OIDC_ISSUER_URL=https://idp.example/realms/engineering \
  GREPNEST_OIDC_CLIENT_ID=grepnest \
  GREPNEST_OIDC_CLIENT_SECRET_FILE=/tmp/oidc-client-secret \
  GREPNEST_OIDC_CA_FILE=/tmp/oidc-ca.pem \
  GREPNEST_OIDC_SCOPES=openid,profile,email \
  GREPNEST_OIDC_GROUPS_CLAIM=roles \
  GREPNEST_OIDC_ALLOWED_GROUPS=engineering,security \
  GREPNEST_OIDC_DISPLAY_NAME_CLAIM=preferred_username)

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
    "GREPNEST_GITHUB_WEBHOOK_SECRET_FILE", "GREPNEST_GITHUB_WEB_URL",
    "GREPNEST_OIDC_ALLOWED_GROUPS", "GREPNEST_OIDC_CA_FILE", "GREPNEST_OIDC_CLIENT_ID",
    "GREPNEST_OIDC_CLIENT_SECRET_FILE", "GREPNEST_OIDC_DISPLAY_NAME_CLAIM",
    "GREPNEST_OIDC_GROUPS_CLAIM", "GREPNEST_OIDC_ISSUER_URL", "GREPNEST_OIDC_SCOPES",
    "GREPNEST_PUBLIC_URL", "GREPNEST_SCIP_MAX_UPLOAD_BYTES",
    "GREPNEST_SSO_LOGIN_FLOW_TTL", "GREPNEST_SSO_SESSION_TTL",
    "GREPNEST_USER_INSTALLATION_ID", "GREPNEST_USER_REPOSITORY_IDS", "GREPNEST_USER_TOKEN", "GREPNEST_ZOEKT_URL"
  ]
  and ($server.ports | any(.host_ip == "127.0.0.1" and .target == 8080 and .published == "8080"))
  and ($server.networks | keys | sort) == ["internal", "loopback"]
  and ([ $server.volumes[] | select(.target == "/run/secrets/grepnest/private-key.pem" or .target == "/run/secrets/grepnest/webhook-secret") | .read_only ] | length == 2 and all(. == true))
  and ([ $server.volumes[].bind.create_host_path ] | all((. // false) == false))
  and $server.environment.GREPNEST_GITHUB_CA_FILE == "/run/secrets/grepnest/github-ca.pem"
  and ($server.volumes | any(.source == "/tmp/github-ca.pem" and .target == "/run/secrets/grepnest/github-ca.pem" and .read_only))
  and $server.environment.GREPNEST_PUBLIC_URL == "https://grepnest.example"
  and $server.environment.GREPNEST_SSO_SESSION_TTL == "12h"
  and $server.environment.GREPNEST_SSO_LOGIN_FLOW_TTL == "5m"
  and $server.environment.GREPNEST_OIDC_ISSUER_URL == "https://idp.example/realms/engineering"
  and $server.environment.GREPNEST_OIDC_CLIENT_ID == "grepnest"
  and $server.environment.GREPNEST_OIDC_CLIENT_SECRET_FILE == "/run/secrets/grepnest/oidc-client-secret"
  and $server.environment.GREPNEST_OIDC_CA_FILE == "/run/secrets/grepnest/oidc-ca.pem"
  and $server.environment.GREPNEST_OIDC_SCOPES == "openid,profile,email"
  and $server.environment.GREPNEST_OIDC_GROUPS_CLAIM == "roles"
  and $server.environment.GREPNEST_OIDC_ALLOWED_GROUPS == "engineering,security"
  and $server.environment.GREPNEST_OIDC_DISPLAY_NAME_CLAIM == "preferred_username"
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
    and $server.environment.GREPNEST_PUBLIC_URL == ""
    and $server.environment.GREPNEST_OIDC_CLIENT_SECRET_FILE == ""
    and $server.environment.GREPNEST_OIDC_CA_FILE == ""
    and ($server.volumes | any(.source == "/dev/null" and .target == "/run/secrets/grepnest/github-ca.pem" and .read_only and (.bind.create_host_path // false) == false))
    and ($server.volumes | any(.source == "/dev/null" and .target == "/run/secrets/grepnest/oidc-client-secret" and .read_only and (.bind.create_host_path // false) == false))
    and ($server.volumes | any(.source == "/dev/null" and .target == "/run/secrets/grepnest/oidc-ca.pem" and .read_only and (.bind.create_host_path // false) == false))
  ' >/dev/null

if render >/dev/null 2>&1; then
  echo "expected GREPNEST_APPLICATION_IMAGE to be required" >&2
  exit 1
fi
