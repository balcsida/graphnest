# GrepNest

GrepNest is a pre-pilot code-search service. Milestones 0-2 provide a pinned
Zoekt-backed search path, bearer authorization, REST and MCP, PostgreSQL-backed
repository state, GitHub App reconciliation, verified webhooks, indexed-SHA
file reads, and sequential default-branch indexing. The local GHES-compatible
HTTPS smart-Git-to-Zoekt proof passes.
It is not production-ready.

The [Helm chart](deploy/helm/grepnest/README.md) is structurally lintable and
renderable, but not currently deployable. This repository does not build or
publish the required images, and the chart has not been cluster-tested.

## Local quick start

Prerequisites: Go 1.26, Git, Docker Compose, and an internet connection for
`make tools` and Docker image pulls. Run the checked-in fixture with Compose:

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
`limit`, and `max_output_bytes`).

For a stdio MCP client, build the proxy and configure only these two variables:

```sh
go build -o /tmp/grepnest-mcp ./cmd/grepnest-mcp
GREPNEST_SERVER_URL=http://127.0.0.1:8080 \
GREPNEST_TOKEN=grepnest-dev-user-token \
/tmp/grepnest-mcp
```

The proxy appends `/mcp`; do not set Zoekt or server configuration on the proxy.

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
- `GREPNEST_USER_INSTALLATION_ID`, `GREPNEST_USER_REPOSITORY_IDS`,
  `GREPNEST_ADMIN_INSTALLATION_ID`, and `GREPNEST_ADMIN_REPOSITORY_IDS`.

`GREPNEST_GITHUB_API_VERSION` defaults to `2022-11-28` and
`GREPNEST_GITHUB_CA_FILE` optionally extends system trust. Startup pings and
migrates PostgreSQL, records the singleton Zoekt node as `primary`, reconciles
GitHub synchronously, then refreshes reconciliation and queue metrics every five
minutes. `POST /webhooks/github` is public but requires a valid GitHub HMAC;
search, repository, file-read, and MCP routes require bearer authentication.

Optional limits are positive and cannot exceed their server caps:

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

Run `make fmt lint staticcheck govulncheck test test-race postgres-integration
integration e2e build` before proposing a change. `make e2e` starts its pinned
PostgreSQL dependency and runs real TLS smart-Git and Zoekt processes. `make
helm-lint helm-test` validates the chart structure without
contacting a cluster. `make image` intentionally fails with
`image: milestone not implemented`; no deployable image is produced.

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
