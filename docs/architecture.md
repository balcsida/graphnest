# Architecture

```text
GitHub Enterprise -> grepnest-server -> PostgreSQL <- grepnest-indexer
                          |                          |
                    REST and MCP               Git -> Zoekt
```

`grepnest-server` is the sole Zoekt search client. It authenticates a single
bearer credential, selects repositories permitted to that principal, converts
those repositories to Zoekt `RepoIDs`, applies bounded search limits, and
normalizes the response. A request that selects no authorized repositories
returns no matches without calling Zoekt.

REST and MCP call the same search service. `/mcp` is hosted Streamable HTTP
MCP behind bearer authentication. `grepnest-mcp` is a stdio proxy: it connects
to `<GREPNEST_SERVER_URL>/mcp` with `GREPNEST_TOKEN`, lists the hosted tools,
and forwards calls. It does not call Zoekt.

Beginning in Milestone 2, PostgreSQL supplies repository metadata and the
durable index queue. `grepnest-server` verifies GitHub webhooks and reconciles
GitHub App installations. `grepnest-indexer` leases one job at a time, fetches
only its default branch, and publishes the indexed SHA after Zoekt confirms
visibility through `/api/list`. Search suppresses a result when Zoekt's branch
version differs from PostgreSQL's committed indexed SHA. Runtime bearer scopes
bind to numeric installation IDs; mutable repository names are selectors only.

The local Compose topology keeps the indexer, Zoekt, and PostgreSQL on the
internal network. Zoekt alone additionally joins the loopback-published
network at `127.0.0.1:6070`; it is not public ingress. OpenShift packaging and
production ingress remain Milestone 3 work. See `docs/adr` for accepted
decisions.
