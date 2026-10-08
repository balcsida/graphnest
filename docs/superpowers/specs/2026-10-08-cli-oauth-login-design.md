# CLI OAuth Login Design

## Goal

`graphnest login` signs a developer in through the browser with the OAuth 2.0
authorization-code flow, PKCE and a loopback redirect, following RFC 8252
(OAuth 2.0 for Native Apps). The resulting token is good enough for every
command that talks to the server, including publication, so a laptop no longer
needs a hand-copied API token. CI keeps using API tokens.

GraphNest is already the authorization server (ADR-0016): RFC 8414 metadata,
RFC 7591 public-client registration, S256-only PKCE, any-port loopback
redirects (RFC 8252 §7.3), rotating refresh tokens with replay detection,
RFC 7009 revocation and a mandatory consent page. What is missing is a client
in the CLI and permission for its tokens to reach the CLI's REST routes.

## Decision: a scope, not an audience

OAuth access tokens become valid on the routes the CLI calls, and publication
by an OAuth principal requires a new `graph:write` scope. Binding tokens to an
audience with RFC 8707 resource indicators was rejected: it needs a migration
and an audience check on two paths, while the CLI's read routes expose nothing
an MCP token cannot already read. ADR-0016 persisted `scope` for exactly this
step. The parity plan's "no administrator credential on an ordinary developer
laptop" and "any audience/scope extension is a separately tested security
change" both bind this work.

## Server

- With `GRAPHNEST_MCP_OAUTH` on, the bearer router that `/mcp` uses
  (API tokens, plus `gno_` OAuth access tokens) also authenticates
  `GET /v1/repositories`, `GET /v1/repositories/{id}`,
  `GET /v1/graph/repositories/{id}/status` and `POST /v1/graph/uploads`
  (including the administrator-only `PUT /v1/graph/publication-grants` and v1
  uploads registered beside them, which OAuth principals fail because they are
  never administrators). Account, admin, search, file reads, SCIP, graph
  queries and everything else keep rejecting OAuth tokens, so an OAuth token
  still cannot mint or manage credentials.
- `authn.ScopeGraphWrite = "graph:write"`. `authn.Principal` gains
  `Scope string`, the space-delimited scope of an OAuth grant (empty for every
  other credential), and `Principal.HasScope(scope string) bool`.
  `postgres.Store.OAuthPrincipal` fills `Scope` from `oauth_grants.scope`.
  No migration. Refresh keeps the scope; it cannot be raised.
- `graphingest.Service.mayPublish` refuses a principal whose `Method` is
  `oauth_token` without `graph:write`, before the per-repository publication
  grant check. Upload and the status `publication.permitted` share it, so
  `graph status` and `doctor` predict the upload. Publications record
  `oauth_token:<user id>` as the publisher.
- Consent page, only when the request's scope contains `graph:write`: an extra
  list item "publish code graphs to repositories where you hold a publication
  grant", and "It will not be able to change anything else or create further
  credentials." Requests without it render exactly as today.
- Metadata `scopes_supported` stays `[]` so MCP clients, which request every
  advertised scope, do not ask for write access by default (RFC 8414 §2 allows
  not advertising a supported scope). The scope is documented instead.
- ADR-0019 records this and amends ADR-0016's "authenticates only `/mcp`".

## CLI login (RFC 8252)

`graphnest login [--timeout 5m]` reads `GRAPHNEST_SERVER_URL` and the optional
`GRAPHNEST_CA_FILE`; token variables are ignored (a note on stderr says that a
set `GRAPHNEST_TOKEN`/`GRAPHNEST_TOKEN_FILE` takes precedence over the login).

1. **Discovery (RFC 8414).** `GET <origin>/.well-known/oauth-authorization-server`,
   where origin is the server URL's scheme and lower-cased host. Require
   `issuer` == origin (§3.3), `S256` in `code_challenge_methods_supported`, and
   authorization, token and registration endpoints. A 404 fails with
   "server does not offer OAuth sign-in (GRAPHNEST_MCP_OAUTH is off); set GRAPHNEST_TOKEN instead".
2. **Loopback listener (§7.3, §8.3).** Listen on `127.0.0.1:0`, falling back
   to `[::1]:0`: an IP literal, never `localhost`, on the loopback interface
   only. The port is open only for this login and closed once the callback has
   been answered.
3. **Registration (RFC 7591; §8.4, §8.5).** A public client named
   `graphnest CLI` with the exact redirect `http://<listener address>/callback`,
   grant types `authorization_code` and `refresh_token`, response type `code`,
   `token_endpoint_auth_method: none`. A new client per login; no secret.
4. **PKCE and state (§8.1, §8.9).** A 32-byte random verifier (43 base64url
   characters) sent as an S256 challenge, and a separate 32-byte random state.
5. **External user-agent (§4.1, §8.12).** The authorization URL, with
   `scope=graph:write` and no `resource`, is printed to stderr and then opened
   in the system browser: `open` on macOS, `rundll32 url.dll,FileProtocolHandler`
   on Windows, `xdg-open` elsewhere. A failed launch is not fatal. Never an
   embedded view.
6. **Callback.** Only `GET /callback` is served; anything else is 404. A wrong
   `state` gets 400 and the login keeps waiting. An `error` ends the login with
   `authorization failed: <error>: <error_description>`. A code ends it with a
   plain-text page "Signed in to GraphNest. You can close this tab and return
   to the terminal." Ctrl-C or `--timeout` (default 5 minutes; the server keeps a
   pending request for 10) aborts.
7. **Exchange.** `grant_type=authorization_code` with code, redirect_uri,
   client_id and code_verifier. The response must carry an access token, a
   refresh token, `token_type` Bearer (any case) and a positive `expires_in`.
8. Save the login, revoke the previous login's grant for that server
   (best-effort; a failure is a warning), print `Signed in to <origin>.` on
   stderr. Nothing is written to stdout.

Mix-up (§8.10): each login talks to one authorization server whose issuer was
verified, and the fresh port and state bind the response to this request. The
server does not send RFC 9207 `iss`, so it is not checked.

## Stored logins, refresh and logout

- One JSON file per server at
  `<os.UserConfigDir()>/graphnest/credentials/<hex sha256(origin)>.json`
  holding `server`, `client_id`, `access_token`, `refresh_token`, `expires_at`,
  `token_endpoint` and `revocation_endpoint`. The directory is created 0700 and
  the file 0600, written to a temporary file, synced and renamed into place. A
  file whose `server` differs from the origin is ignored. No OS keychain: it
  would need a new dependency.
- Precedence for `graph status`, `graph upload`, `graph import codegraph` and
  `doctor`: `GRAPHNEST_TOKEN`/`GRAPHNEST_TOKEN_FILE` (unchanged, both set is an
  error), else the stored login for the server URL's origin, else
  "GRAPHNEST_TOKEN or GRAPHNEST_TOKEN_FILE is required, or run graphnest login".
  `doctor`'s server check names the source it used.
- The REST client asks for a token before every request and refreshes first
  when less than the request timeout plus 30 seconds remain, because the server
  re-authenticates the credential in the middle of a publication. A mutex
  serializes refresh within a process. The rotated pair is saved before it is
  used; if saving fails the command fails (the next run's stale refresh token
  then trips replay detection, which revokes the grant).
- `invalid_grant` on refresh: re-read the file once. If another `graphnest`
  process already rotated the tokens (the server's 30-second grace keeps that
  race from revoking the grant), continue with them. Otherwise fail with
  "the stored login for <origin> has expired or was revoked; run graphnest login".
- A 401 from the server adds "check the token or run graphnest login" to the
  error, which covers the up-to-an-hour window after a grant is revoked from
  the account page.
- `graphnest logout` revokes the grant (RFC 7009, refresh token and
  `client_id`) and deletes the file. The file is deleted even when revocation
  fails; the command then exits 1 and says to disconnect the client under
  Account → Connected MCP clients. Without a stored login it says so and exits 0.

## Testing

- Unit: `mayPublish` with and without the scope; consent page with and without
  `graph:write`; the production handler accepts OAuth tokens on exactly the CLI
  routes; the client's discovery, registration, exchange, refresh rotation,
  reload after a concurrent rotation, revocation and the credential file's
  permissions against a fake authorization server; the CLI's login against that
  fake server with a test browser (state mismatch ignored, `access_denied`,
  issuer mismatch, timeout), logout, and precedence.
- PostgreSQL integration: `OAuthPrincipal` returns the grant's scope; an
  in-process `graphnest login` against the real authorization server over TLS
  with a real session consents with `graph:write`, then `graph status` and
  `graph import codegraph` publish with the stored login, an expired access
  token refreshes and rotates the file, a token without the scope reads but
  cannot publish, and `logout` revokes the grant.
- The MCP OAuth end-to-end harness mirrors the new production wiring, so its
  OAuth token now reads `/v1/repositories/101` and still cannot reach account
  routes.

## Documentation

ADR-0019 and the ADR index; `docs/operations.md` (CLI credentials and the MCP
OAuth section); `README.md` (command table and CLI paragraph);
`docs/openapi.yaml` (`scope` on `/oauth/authorize`, which routes accept OAuth
access tokens); `docs/threat-model.md` (stored CLI credentials, loopback
callback); `CHANGELOG.md` under Unreleased.

## Out of scope

The device authorization grant (RFC 8628) and a fixed `--port` for machines
without a local browser, which keep using API tokens; `graphnest-mcp` reading
the stored login; OS keychain storage; RFC 9207 `iss` checking; enforcing
other scopes.
