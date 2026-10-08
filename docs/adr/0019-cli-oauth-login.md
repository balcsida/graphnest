# ADR-0019: CLI OAuth Login

- Status: Accepted
- Date: 2026-10-08

## Decision

`graphnest login` is an RFC 8252 native client of the authorization server in
ADR-0016. It opens the system browser, receives the authorization code on a
loopback `127.0.0.1` redirect, uses S256 PKCE, and registers a new public client
dynamically for each login.

OAuth access tokens (`gno_…`) now also authenticate the routes the CLI calls:
`GET /v1/repositories`, `GET /v1/repositories/{id}`, and the graph ingestion
routes (`POST /v1/graph/uploads`, `GET /v1/graph/repositories/{id}/status`).
Every other REST route still accepts only API tokens and sessions.

Publication by an OAuth principal needs the `graph:write` scope in addition to
the repository publication grant it already needed; the scope never replaces
the grant. The consent page lists the capability whenever the requested scope
contains `graph:write`. `scopes_supported` in the authorization-server metadata
stays empty, so MCP clients are not offered it.

## Rationale

A developer laptop should not hold an administrator credential or a hand-copied
API token to publish a graph. Reusing the OAuth grant gives the CLI a
revocable, expiring credential tied to the signed-in user.

A scope needs no migration: `scope` is already persisted on the grant, and the
read routes expose nothing `/mcp` cannot already read for the same user. RFC 8707
resource audiences would split tokens per resource, which needs a migration and
a second check on every route, for no gain over the scope.

## Consequences

This amends ADR-0016, where an access token "authenticates only `/mcp`".
Existing MCP tokens can now read the repository routes and the graph status, but
they never carry `graph:write` and so can never publish. A token cannot mint or
manage credentials. The CLI keeps its credentials in a 0600 file per server.
Headless machines and CI keep using API tokens, since the flow needs a browser.
