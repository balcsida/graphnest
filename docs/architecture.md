# Architecture

```text
GitHub Enterprise -> indexer -> Zoekt shards
          |               |
          +-> PostgreSQL <-+-> server <- REST and MCP
```

`grepnest-server` is the sole Zoekt search client. It authenticates a single
bearer credential, selects repositories permitted to that principal, converts
those repositories to Zoekt `RepoIDs`, applies bounded search limits, and
normalizes the response. A request that selects no authorized repositories
returns no matches without calling Zoekt.

Browser sign-in may enable OIDC, GitHub OAuth, or both; the provider list is
deterministically OIDC then GitHub. Both use the same Authorization Code + PKCE
flow and store only a hashed opaque GrepNest session. Same-origin browser REST
requests use the HttpOnly session cookie. GitHub OAuth's metadata/flow provider
is `github`, but its identity and session method is `oauth`; its routes are
`/auth/oauth/github/login` and `/auth/oauth/github/callback`.

GitHub OAuth canonicalizes the HTTPS GitHub web origin as issuer and uses the
positive numeric GitHub user ID as subject. It links an active SCIM user only
when `externalId` exactly equals `github:https://github.com:<numeric-id>` on
GitHub.com. This immutable identity survives login or display-name changes.
The access token is used once for the authenticated-user request, never stored
or refreshed; the authorization request sends no scope and rejects a granted
scope. `/mcp` remains bearer-only and rejects browser-session cookies.

When enabled, `/scim/v2` uses a separate secret-file bearer credential and
writes the same PostgreSQL users, groups, and memberships used by browser
providers and authorization. OIDC binds its configured link claim to SCIM
`externalId`; GitHub OAuth uses its canonical `github:<issuer>:<subject>` link.
Sessions and API tokens resolve live directory state on every request, so SCIM
deactivation or deletion takes effect immediately.

REST and MCP call the same search service. `/mcp` is hosted Streamable HTTP
MCP behind bearer authentication. `grepnest-mcp` is a stdio proxy: it connects
to `<GREPNEST_SERVER_URL>/mcp` with `GREPNEST_TOKEN`, lists the hosted tools,
and forwards calls. It does not call Zoekt.

The embedded Web UI at `/` and `/index.html` is a thin, same-origin client of
the repository service at `GET /v1/repositories` and the search service at
`POST /v1/search`. It makes no authorization decisions: repository names are
only usability selectors, and the server authenticates every API request and
enforces the principal's repository scope.

Beginning in Milestone 2, PostgreSQL supplies repository metadata and the
durable index queue. `grepnest-server` verifies GitHub webhooks and reconciles
GitHub App installations. That GitHub App is separate from user OAuth and is
the only credential used for repository work. `grepnest-indexer` leases one
job at a time, fetches
only its default branch, and publishes the indexed SHA after Zoekt confirms
visibility through `/api/list`. Search suppresses a result when Zoekt's branch
version differs from PostgreSQL's committed indexed SHA. Runtime bearer scopes
bind to numeric GitHub repository IDs within an installation boundary; mutable
repository names are selectors only.

The local durable Compose profile keeps PostgreSQL and Zoekt on the internal
network and bind-mounts the host shard directory into Zoekt. Archive extraction
uses a bounded ephemeral workspace that is separate from those shards. Zoekt is
published only at `127.0.0.1:6070`; it is not public ingress. OpenShift
packaging and production ingress remain Milestone 3 work. See `docs/adr` for
accepted decisions.

## Derived graph analysis

PostgreSQL is authoritative for repository state, the indexed default-branch
SHA, graph artifacts, upload metadata, graph jobs, and graph queries. The
indexer may invoke an optional enrichment binary on its job-scoped snapshot;
SCIP uploads are the other artifact source. Server replicas query that same
state directly; there is no separate graph owner, transport, derived database,
or graph volume.

Before a graph query, the server resolves the authorized repository selector
(numeric GitHub ID or name) and the current indexed default-branch SHA. It
reauthorizes selected and returned repositories against that exact snapshot,
returning `graph_not_ready` if graph data is missing or stale and
`branch_not_indexed` for a non-indexed branch.

The public surface is limited to bounded `context`, `impact`, and `trace`
operations. Traversal limits, cycle detection, confidence filtering, stable
ordering, and partial-result boundaries remain in the graph query service.
The PostgreSQL store performs scoped, parameterized, batched reads by
repository ID, upload ID, and commit. Request and response contracts are in the
[OpenAPI contract](openapi.yaml).

Pre-generated `.scip` uploads remain an exact-SHA code-navigation input and a
fallback source when native scanning is unavailable; they do not turn SCIP
into a native scanner. PostgreSQL remains internal-only behind GrepNest's
authenticated REST and MCP services. See
[ADR-0014](adr/0014-postgresql-graph-queries.md) for the current query-store
decision. The ADR index records the superseded topology.
