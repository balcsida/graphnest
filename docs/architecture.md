# Architecture

```text
GitHub Enterprise -> grepnest-server -> PostgreSQL <- grepnest-indexer
                          |                          |
                    REST and MCP               Git -> Zoekt
```

`grepnest-server` is the sole Zoekt search client. It authenticates a bearer
credential or, for user REST routes, an OIDC browser session, selects repositories permitted to that principal, converts
those repositories to Zoekt `RepoIDs`, applies bounded search limits, and
normalizes the response. A request that selects no authorized repositories
returns no matches without calling Zoekt.

REST and MCP call the same search service. `/mcp` is hosted Streamable HTTP
MCP behind bearer authentication. `grepnest-mcp` is a stdio proxy: it connects
to `<GREPNEST_SERVER_URL>/mcp` with `GREPNEST_TOKEN`, lists the hosted tools,
and forwards calls. It does not call Zoekt.

The embedded Web UI at `/` and `/index.html` is a thin, same-origin client of
the repository service at `GET /v1/repositories` and the search service at
`POST /v1/search`. It makes no authorization decisions: repository names are
only usability selectors, and the server authenticates every API request and
enforces the principal's repository scope.

When OIDC is enabled, the browser flow is:

```text
Browser -> OIDC IdP -> callback -> auth_sessions -> authn.Principal -> existing authz
```

The callback verifies the authorization code with PKCE, nonce, issuer, and
audience before mapping the identity. It creates a hashed opaque session record
with the existing user repository scope; the session has no administrator
privilege. The short-lived browser binding and login transaction live only for
the redirect flow. The regular session cookie is then used only by same-origin
REST requests; `/mcp` stays bearer-only.

Beginning in Milestone 2, PostgreSQL supplies repository metadata and the
durable index queue. `grepnest-server` verifies GitHub webhooks and reconciles
GitHub App installations. `grepnest-indexer` leases one job at a time, fetches
only its default branch, and publishes the indexed SHA after Zoekt confirms
visibility through `/api/list`. Search suppresses a result when Zoekt's branch
version differs from PostgreSQL's committed indexed SHA. Runtime bearer scopes
bind to numeric GitHub repository IDs within an installation boundary; mutable
repository names are selectors only.

The local durable Compose profile keeps PostgreSQL and Zoekt on the internal
network and bind-mounts the host indexer's shard directory into Zoekt. Zoekt is
published only at `127.0.0.1:6070`; it is not public ingress. OpenShift
packaging and production ingress remain Milestone 3 work. See `docs/adr` for
accepted decisions.
