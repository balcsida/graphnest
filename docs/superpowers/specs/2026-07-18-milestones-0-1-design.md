# GraphNest Milestones 0-1 Design

## Scope

This pass delivers only the foundation and a manually indexed search path:

`fixture Git repository -> zoekt-git-index -> zoekt-webserver -> GraphNest search service -> REST and MCP`

GitHub App integration, PostgreSQL-backed indexing, file reading, outlines,
containers for OpenShift, and Helm resources remain design-only work for later
milestones.

## Architecture

One `graphnest-server` process owns authentication, repository authorization,
the application search service, `POST /v1/search`, health and metrics endpoints,
and Streamable HTTP MCP at `/mcp`. A separate `graphnest-mcp` executable exposes
stdio MCP and forwards authenticated requests to the server; it never talks to
Zoekt directly.

The application service depends on a GraphNest-owned `SearchBackend` interface.
The first adapter calls the pinned Zoekt JSON HTTP API and converts responses to
stable GraphNest models. Transport handlers are thin wrappers around the same
application service.

## Repository and Authorization Model

Development configuration contains a static repository registry with the
repository's public name, stable GraphNest ID, Zoekt repository ID, branch,
indexed SHA, and web URL. Static bearer tokens map to principals and allowed
repository IDs. Tokens are compared in constant time.

Every search follows one path:

1. Authenticate the bearer token.
2. Resolve the principal's allowed repositories.
3. Intersect them with optional repository names requested by the caller.
4. Pass only server-selected Zoekt repository IDs to the backend.
5. Normalize and clamp the response before returning it.

User query syntax cannot widen that repository set. An empty authorized
intersection returns no matches without issuing an unrestricted Zoekt query.

## Foundation

The empty repository becomes a Go module using the temporary documented module
path `github.com/balcsida/graphnest`, because no Git remote exists. Milestone 0
adds `log/slog` JSON logging, environment configuration with startup validation,
`/healthz`, dependency-aware `/readyz`, Prometheus metrics, graceful shutdown,
Make targets, CI, Apache-2.0 licensing, and the documentation skeleton required
by the master brief.

Only dependencies that directly supply required protocol or metrics behavior
are added. HTTP routing and configuration parsing use the Go standard library.

## Search and MCP Behavior

`POST /v1/search` accepts a bounded JSON body containing query, optional
repository names, result limit, context lines, timeout, and maximum response
bytes. Defaults and ceilings are server configuration. Errors use a stable JSON
envelope and never include secrets.

Milestone 1 exposes `search_code` and `find_files` through the official MCP Go
SDK. `find_files` uses the same search service with a path-oriented Zoekt query;
both tools return deterministic, bounded, structured output. Streamable HTTP MCP
is hosted by `graphnest-server`; the stdio command is a protocol proxy to that
endpoint.

## Zoekt Integration

The Zoekt version and container source are pinned. The adapter uses a bounded
HTTP client, context cancellation, response-size limits, and no retry loop. Its
request and response shapes are based on the pinned Zoekt source and command
help, not guessed fields. Backend failures, timeouts, decode failures, and
truncation remain explicit.

## Development Environment

Docker Compose starts pinned PostgreSQL and Zoekt services. PostgreSQL is
included to preserve the documented developer topology but is not an
application dependency until Milestone 2. A script creates a deterministic
fixture Git repository and invokes the pinned `zoekt-git-index` binary with an
argument array.

## Testing

Small unit tests cover configuration validation, authentication, authorization
intersection, limits, Zoekt request construction, response normalization, and
transport errors. An `httptest` Zoekt server proves adapter behavior.

The end-to-end test uses a real pinned `zoekt-git-index` and
`zoekt-webserver`, then verifies:

1. the fixture repository is searchable through `POST /v1/search`;
2. an MCP client receives the same normalized result from `search_code`;
3. a principal cannot search an unauthorized repository;
4. health, readiness, response bounds, and graceful cleanup work.

If the required container runtime is unavailable, the test fails with an
explicit prerequisite message; it does not silently replace real Zoekt with a
fake.

## Milestones 2 and 3 Notes

Milestone 2 will add PostgreSQL metadata and queue migrations, GitHub App
authentication, GHES webhooks and custom CA support, and the durable indexer.
Milestone 3 will package the proven services for arbitrary-UID OpenShift and add
the Helm deployment. Their detailed notes belong in the implementation plan,
but no code or manifests for either milestone are part of this pass.

## Success Criteria

Milestones 0 and 1 are complete only when formatting, linting, unit, race,
integration, build, and real fixture end-to-end checks pass, and the documented
manual commands reproduce both REST and MCP searches plus authorization denial.
