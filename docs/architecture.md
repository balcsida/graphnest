# Architecture

```text
fixture Git repository -> zoekt-git-index -> zoekt-webserver
  -> grepnest-server -> REST (/v1/search) and MCP (/mcp)
```

`grepnest-server` is the sole Zoekt client. It authenticates a single bearer
credential, selects repositories permitted to that principal, converts those
repositories to Zoekt `RepoIDs`, applies bounded search limits, and normalizes
the response. A request that selects no authorized repositories returns no
matches without calling Zoekt.

REST and MCP call the same search service. `/mcp` is hosted Streamable HTTP
MCP behind bearer authentication. `grepnest-mcp` is a stdio proxy: it connects
to `<GREPNEST_SERVER_URL>/mcp` with `GREPNEST_TOKEN`, lists the hosted tools,
and forwards calls. It does not call Zoekt.

The local Compose topology keeps the indexer, Zoekt, and PostgreSQL on the
internal network. Zoekt alone additionally joins the loopback-published
network at `127.0.0.1:6070`; it is not public ingress. Both Zoekt containers
pin `linux/amd64`: the pinned Zoekt image is amd64-only, so this lets Docker
emulate it on Apple silicon while preserving the identical pinned artifact.

PostgreSQL is not used by the application in these milestones. GitHub App
indexing, durable coordination, OpenShift packaging, and production ingress
are later milestones. See `docs/adr` for accepted decisions.
