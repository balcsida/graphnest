#!/bin/sh
set -eu

config=$(GREPNEST_GITHUB_PRIVATE_KEY_FILE=/tmp/private-key.pem \
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
  docker compose -f deploy/compose/compose.yml --profile durable config --format json)

printf '%s' "$config" | jq -e '
  .services["grepnest-server"] as $server
  | if $server == null then false else
    $server.profiles == ["durable"]
  and $server.image == "grepnest/application:local"
  and $server.depends_on.postgres.condition == "service_healthy"
  and $server.depends_on["zoekt-durable"].condition == "service_healthy"
  and ($server.environment | keys | sort) == [
    "GREPNEST_ADMIN_INSTALLATION_ID", "GREPNEST_ADMIN_REPOSITORY_IDS", "GREPNEST_ADMIN_TOKEN",
    "GREPNEST_DATABASE_URL", "GREPNEST_GITHUB_API_URL", "GREPNEST_GITHUB_APP_ID",
    "GREPNEST_GITHUB_GIT_URL", "GREPNEST_GITHUB_PRIVATE_KEY_FILE", "GREPNEST_GITHUB_UPLOAD_URL",
    "GREPNEST_GITHUB_WEBHOOK_SECRET_FILE", "GREPNEST_GITHUB_WEB_URL", "GREPNEST_SCIP_MAX_UPLOAD_BYTES",
    "GREPNEST_USER_INSTALLATION_ID", "GREPNEST_USER_REPOSITORY_IDS", "GREPNEST_USER_TOKEN", "GREPNEST_ZOEKT_URL"
  ]
  and ($server.ports | any(.host_ip == "127.0.0.1" and .target == 8080 and .published == "8080"))
  and ($server.networks | keys | sort) == ["internal", "loopback"]
  and ([ $server.volumes[] | select(.target == "/run/secrets/grepnest/private-key.pem" or .target == "/run/secrets/grepnest/webhook-secret") | .read_only ] | length == 2 and all(. == true))
  end
' >/dev/null
