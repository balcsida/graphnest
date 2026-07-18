# Architecture

GrepNest's first tested path is:

```text
fixture Git repository -> zoekt-git-index -> zoekt-webserver
  -> grepnest-server application service -> REST / MCP
```

Only `grepnest-server` may call Zoekt. REST and MCP share authentication,
authorization, limits, and normalized search models. See the accepted ADRs in
`docs/adr` and the Milestones 0-1 design in `docs/superpowers/specs`.

GitHub App indexing, PostgreSQL coordination, and OpenShift packaging begin in
later milestones only after this path passes with real Zoekt.
