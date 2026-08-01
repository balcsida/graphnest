# GrepNest

GrepNest is a pre-pilot code-search service. Milestones 0-2 provide a pinned
Zoekt-backed search path, bearer authorization, REST and MCP, PostgreSQL-backed
repository state, GitHub App reconciliation, verified webhooks, indexed-SHA
file reads, and sequential default-branch indexing. The local GHES-compatible
HTTPS smart-Git-to-Zoekt proof passes.
It is not production-ready.

## Interface

Search indexed code, open the exact indexed revision, and follow SCIP
definitions, references, and implementations without leaving the console.

![GrepNest code search with SCIP navigation](docs/images/grepnest-ui.png)

Graph analysis is available in durable mode. PostgreSQL remains authoritative;
LadybugDB is a rebuildable derived store owned by one embedded (default) or
separate graph runtime. The server is always its internal authenticated client.
Native scanners cover Go, TypeScript, JavaScript, Java, Kotlin, and Rust.
The currently exposed graph tools are `context`, `impact`, `trace`, and
administrator-only read-only Cypher. See [architecture](docs/architecture.md),
[operations](docs/operations.md), and [compatibility](docs/compatibility.md).

The [Helm chart](deploy/helm/grepnest/README.md) supports Kubernetes 1.25 or
newer. Releases publish multi-architecture images and an OCI chart; the
released chart embeds immutable image digests.

## Local images

Build and smoke-test the local images with:

```sh
make image-test
```

The local tags are `grepnest-application:dev` and `grepnest-node:dev`. They
are for local use only; a released chart uses immutable multi-architecture
image digests.

## Local quick start

Prerequisites: Go 1.26, Git, Docker Compose, `jq`, and an internet connection
for `make tools` and Docker image pulls. Run the checked-in fixture with
Compose:

```sh
make tools
docker compose -f deploy/compose/compose.yml --profile fixture up -d --wait
```

Compose copies `test/fixtures/repository`, initializes it as a Git repository,
sets Zoekt repository ID `7`, and indexes it. Start the server in another
terminal with development-only, distinct tokens:

```sh
GREPNEST_LISTEN_ADDRESS=127.0.0.1:8080 \
GREPNEST_ZOEKT_URL=http://127.0.0.1:6070 \
GREPNEST_REPOSITORIES_FILE=deploy/compose/repositories.json \
GREPNEST_USER_TOKEN=grepnest-dev-user-token \
GREPNEST_ADMIN_TOKEN=grepnest-dev-admin-token \
GREPNEST_USER_REPOSITORIES=fixture/repository \
GREPNEST_ADMIN_REPOSITORIES=fixture/repository \
go run ./cmd/grepnest-server
```

Open `http://127.0.0.1:8080/` and enter the development user token
`grepnest-dev-user-token`. The console keeps the bearer token only for the
current browser session. Static fixture mode exposes the repository inventory
and links search results to the exact indexed external source revision.

For an explicit local index instead of Compose, create a temporary Git
repository from `test/fixtures/repository`, configure `zoekt.repoid` to `7` and
`zoekt.name` to `fixture/repository`, commit it, then run:

```sh
.cache/bin/zoekt-git-index -index /tmp/grepnest-index -branches main -submodules=false -incremental=false /tmp/grepnest-fixture
.cache/bin/zoekt-webserver -index /tmp/grepnest-index -listen 127.0.0.1:6070 -rpc -html=false
```

Search the fixture:

```sh
curl -sS http://127.0.0.1:8080/v1/search \
  -H 'Authorization: Bearer grepnest-dev-user-token' \
  -H 'Content-Type: application/json' \
  --data '{"query":"GrepNestFixtureNeedle","repositories":["fixture/repository"]}'
```

## SCIP code navigation

GrepNest stores pre-generated SCIP indexes; it does not run or manage language
indexers. Generate a `.scip` file in each repository's CI at the same 40-character
lowercase commit SHA that GrepNest reports as `indexed_sha`, then upload it with
an administrator token:

```sh
scip-go
curl --fail-with-body -X POST \
  "https://grepnest.example/v1/scip/uploads?repository_id=101&commit=$GITHUB_SHA" \
  -H "Authorization: Bearer $GREPNEST_ADMIN_TOKEN" \
  -H 'Content-Type: application/vnd.scip+protobuf' \
  --data-binary @index.scip
```

The upload is rejected with `409` when `commit` differs from the repository's
exact indexed SHA. `GREPNEST_SCIP_MAX_UPLOAD_BYTES` defaults to 67108864 (64
MiB) and is capped at 268435456 (256 MiB). Indexes may use SCIP UTF-8, UTF-16,
or UTF-32 code-unit positions; navigation lines are one-based and characters
are zero-based in the index's declared unit.

Navigate from an indexed occurrence with a token authorized for the origin and
any returned target repositories:

```sh
curl --fail-with-body https://grepnest.example/v1/scip/navigation \
  -H "Authorization: Bearer $GREPNEST_TOKEN" \
  -H 'Content-Type: application/json' \
  --data '{"repository_id":102,"path":"main.go","line":12,"character":4,"operation":"definitions"}'
```

Targets outside the caller's authorized repositories are omitted. Administrative
upload and metadata requests return `403` for a non-administrator; an unknown or
unauthorized repository may return `404` without revealing whether it exists.

Cross-repository navigation can use manually supplied package URLs:

```sh
curl --fail-with-body -X PUT https://grepnest.example/v1/scip/dependencies \
  -H "Authorization: Bearer $GREPNEST_ADMIN_TOKEN" \
  -H 'Content-Type: application/json' \
  --data '{"repository_id":101,"provides":["pkg:golang/example.com/acme/lib@v1.0.0"],"depends_on":[]}'
```

Or refresh package metadata from GitHub's dependency graph:

```sh
curl --fail-with-body -X POST https://grepnest.example/v1/scip/dependencies/github \
  -H "Authorization: Bearer $GREPNEST_ADMIN_TOKEN" \
  -H 'Content-Type: application/json' \
  --data '{"repository_id":101}'
```

The GitHub App must have read access to repository metadata and the dependency
graph. GitHub deployments without dependency-graph data degrade gracefully:
the refresh reports `available: false`; inaccessible repositories return 403 or
404 and existing manual metadata remains usable.

An unlisted repository is deliberately not searched; the response is a normal
empty result:

```sh
curl -sS http://127.0.0.1:8080/v1/search \
  -H 'Authorization: Bearer grepnest-dev-user-token' \
  -H 'Content-Type: application/json' \
  --data '{"query":"GrepNestFixtureNeedle","repositories":["other/repository"]}'
```

## MCP

The hosted Streamable HTTP MCP endpoint is `http://127.0.0.1:8080/mcp` and
requires the same `Authorization: Bearer <token>` header. It offers
`search_code` (`query`, optional `repositories`, `limit`, `context_lines`, and
`max_output_bytes`) and `find_files` (`pattern`, optional `repositories`,
`limit`, and `max_output_bytes`). Durable mode also exposes `navigate_symbol`
with `repository_id`, `path`, one-based `line`, zero-based `character`, and
`definitions`, `references`, or `implementations` as the `operation`.

For a stdio MCP client, build the proxy and configure only these two variables:

```sh
go build -o /tmp/grepnest-mcp ./cmd/grepnest-mcp
GREPNEST_SERVER_URL=http://127.0.0.1:8080 \
GREPNEST_TOKEN=grepnest-dev-user-token \
/tmp/grepnest-mcp
```

The proxy appends `/mcp`; do not set Zoekt or server configuration on the proxy.

Install GrepNest's graph skills only when wanted; ordinary proxy startup does
not write to the current repository:

```sh
/tmp/grepnest-mcp install-skills --root /path/to/repository
```

The installer writes `.claude/skills/` and mirrors to `.agents/skills/` only
when `.agents/` already exists. It updates only its marked destinations.

## Server environment

All modes require `GREPNEST_ZOEKT_URL` (HTTP(S)) and distinct non-empty
`GREPNEST_USER_TOKEN` and `GREPNEST_ADMIN_TOKEN`.
`GREPNEST_LISTEN_ADDRESS` defaults to `:8080`. Repository lists are
comma-separated: `GREPNEST_USER_REPOSITORIES` and
`GREPNEST_ADMIN_REPOSITORIES`.

Static fixture mode additionally requires `GREPNEST_REPOSITORIES_FILE` and uses
the name-based repository lists above. Durable mode is selected by
`GREPNEST_DATABASE_URL` and does not read the static repository file or any
indexer-only setting. It requires these server settings:

- `GREPNEST_GITHUB_WEB_URL`, `GREPNEST_GITHUB_API_URL`,
  `GREPNEST_GITHUB_UPLOAD_URL`, and `GREPNEST_GITHUB_GIT_URL` as HTTPS URLs;
- `GREPNEST_GITHUB_APP_ID`, `GREPNEST_GITHUB_PRIVATE_KEY_FILE`, and
  `GREPNEST_GITHUB_WEBHOOK_SECRET_FILE`;
- OIDC configuration: `GREPNEST_PUBLIC_URL`, `GREPNEST_OIDC_ISSUER_URL`,
  `GREPNEST_OIDC_CLIENT_ID`, and `GREPNEST_OIDC_CLIENT_SECRET_FILE`.
- Optional SCIM configuration: `GREPNEST_SCIM_TOKEN_FILE`; SCIM also uses the
  HTTPS public URL and durable PostgreSQL directory.

The shipped application image includes `grepnest-admin` for last-resort
administrator recovery. Run that exact image and follow the
[break-glass runbook](docs/operations.md#break-glass-administrator-recovery).
The offline command only creates or rotates a local credential in PostgreSQL;
it does not enable a server login route or replace SSO.

`GREPNEST_GITHUB_API_VERSION` defaults to `2022-11-28` and
`GREPNEST_GITHUB_CA_FILE` optionally extends system trust. Startup pings and
migrates PostgreSQL, records the singleton Zoekt node as `primary`, reconciles
GitHub synchronously, then refreshes reconciliation and queue metrics every five
minutes. `POST /webhooks/github` is public but requires a valid GitHub HMAC;
search, repository, file-read, and MCP routes require bearer authentication.

### Durable Compose

The durable Compose overlay runs the server, indexer, scalable scanners,
PostgreSQL, and Zoekt. Set `GREPNEST_APPLICATION_IMAGE` and
`GREPNEST_NODE_IMAGE` to existing images plus the GitHub, graph, and OIDC
variables listed above. The application image must provide `grepnest-server`
and `wget` on `PATH`; the node image must provide `grepnest-indexer`,
`grepnest-scanner`, `grepnest-graph`, `git`, and `zoekt-git-index`. The overlay
also requires
`GREPNEST_GITHUB_PRIVATE_KEY_FILE` and `GREPNEST_GITHUB_WEBHOOK_SECRET_FILE`
to be readable host-file paths; Compose mounts both read-only into the server.
Set `GREPNEST_GITHUB_CA_FILE` to an optional private-CA host file; Compose mounts
it read-only.

Choose one graph overlay. Both modes keep the server URL at the internal
`http://grepnest-graph:8081` and require a read-only
`GREPNEST_GRAPH_INTERNAL_SECRET_FILE`. Embedded mode runs the graph owner in
the singleton indexer; separate mode runs one `grepnest-graph` service. Neither
publishes a graph port. Set `GREPNEST_SCANNER_REPLICAS` to scale scanners
(default `2`).

```sh
GREPNEST_APPLICATION_IMAGE=registry.example/grepnest/application:2026-07-22 \
GREPNEST_NODE_IMAGE=registry.example/grepnest/node:2026-07-28 \
GREPNEST_GITHUB_PRIVATE_KEY_FILE=$PWD/github-app-private-key.pem \
GREPNEST_GITHUB_WEBHOOK_SECRET_FILE=$PWD/github-webhook-secret \
GREPNEST_GITHUB_CA_FILE=$PWD/github-ca.pem \
GREPNEST_GRAPH_INTERNAL_SECRET_FILE=$PWD/graph-internal-secret \
GREPNEST_GITHUB_WEB_URL=https://github.example \
GREPNEST_GITHUB_API_URL=https://github.example/api/v3 \
GREPNEST_GITHUB_UPLOAD_URL=https://github.example/api/uploads \
GREPNEST_GITHUB_GIT_URL=https://github.example \
GREPNEST_GITHUB_APP_ID=123 \
GREPNEST_PUBLIC_URL=https://grepnest.example \
GREPNEST_OIDC_ISSUER_URL=https://id.example \
GREPNEST_OIDC_CLIENT_ID=grepnest \
GREPNEST_OIDC_CLIENT_SECRET_FILE=$PWD/oidc-client-secret \
GREPNEST_SCIM_TOKEN_FILE=$PWD/scim-token \
docker compose \
  -f deploy/compose/compose.yml \
  -f deploy/compose/durable.yml \
  -f deploy/compose/graph-embedded.yml \
  --profile durable \
  up -d --wait
```

Replace `graph-embedded.yml` with `graph-separate.yml` for standalone graph
ownership.

Optional limits are positive and cannot exceed their server caps:

### Optional OIDC browser sign-in

OIDC requires durable mode and an HTTPS `GREPNEST_PUBLIC_URL`. Configure
`GREPNEST_OIDC_ISSUER_URL`, `GREPNEST_OIDC_CLIENT_ID`,
`GREPNEST_OIDC_CLIENT_SECRET_FILE`, and `GREPNEST_OIDC_LINK_CLAIM`; optional
settings are `GREPNEST_OIDC_CA_FILE`, `GREPNEST_OIDC_SCOPES`,
`GREPNEST_OIDC_DISPLAY_NAME_CLAIM`, `GREPNEST_SSO_SESSION_IDLE`,
`GREPNEST_SSO_SESSION_TTL`, and `GREPNEST_SSO_LOGIN_FLOW_TTL`. Register
`https://<public-host>/auth/oidc/callback` as the Authorization Code + PKCE
redirect URI. The client secret and optional CA are readable files, never
environment values or ConfigMap data.

### Optional SCIM 2.0 provisioning

Set `GREPNEST_SCIM_TOKEN_FILE` to a readable file containing a dedicated,
high-entropy bearer token and expose
`https://<public-host>/scim/v2`. SCIM is default-off, durable-mode only, and
every discovery and resource request requires that token. The OIDC
`GREPNEST_OIDC_LINK_CLAIM` value must exactly match the SCIM user's
`externalId`; directory attributes are not authorization claims.

Users support `eq` filters on `id`, `userName`, and `externalId`; groups
support `id`, `displayName`, and `externalId`. PATCH supports user `active`,
`userName`, `displayName`, `name`, and `emails`, plus group `members` and
`members[value eq "USER_ID"]`. Requests are limited to 1 MiB bodies, 8 KiB
queries, 16 KiB URLs, 100 PATCH operations, and `GREPNEST_MAX_RESULTS`.
Replacing the secret file does not hot-reload it: restart server replicas to
rotate the token. Deactivation or deletion denies existing browser sessions
and API tokens on their next request.

Bulk, sorting, ETags, passwords, `/Me`, `/.search`, root search, enterprise
extensions, custom schemas/resources, roles, and entitlements are unsupported.

| Variable | Default | Maximum |
| --- | ---: | ---: |
| `GREPNEST_DEFAULT_RESULTS` | 25 | `GREPNEST_MAX_RESULTS` (100) |
| `GREPNEST_MAX_RESULTS` | 100 | 100 |
| `GREPNEST_DEFAULT_CONTEXT_LINES` | 3 | `GREPNEST_MAX_CONTEXT_LINES` (20) |
| `GREPNEST_MAX_CONTEXT_LINES` | 20 | 20 |
| `GREPNEST_DEFAULT_TIMEOUT` | 5s | `GREPNEST_MAX_TIMEOUT` (5s) |
| `GREPNEST_MAX_TIMEOUT` | 5s | 5s |
| `GREPNEST_MAX_REQUEST_BYTES` | 65536 | 65536 |
| `GREPNEST_MAX_RESPONSE_BYTES` | 262144 | 262144 |
| `GREPNEST_SCIP_MAX_UPLOAD_BYTES` | 67108864 | 268435456 |

Run `make fmt lint staticcheck govulncheck test test-race postgres-integration
integration e2e build compose-test` before proposing a change. `make e2e` starts its pinned
PostgreSQL dependency and runs real TLS smart-Git and Zoekt processes. `make
helm-lint helm-test` validates the chart structure without contacting a
cluster. `make image-test` builds and smoke-tests the two local images.

## Policies

- [Architecture](docs/architecture.md)
- [Operations](docs/operations.md)
- [Threat model](docs/threat-model.md)
- [OpenAPI](docs/openapi.yaml)
- [Benchmarking](docs/benchmarking.md)
- [Implementation report](docs/implementation-report.md)
- [Contributing](CONTRIBUTING.md)
- [Security](SECURITY.md)
- [Code of conduct](CODE_OF_CONDUCT.md)
- [Support](SUPPORT.md)
- [Release process](docs/release-process.md)
- [Compatibility](docs/compatibility.md)
- [Dependency pinning](docs/dependency-pinning.md)
