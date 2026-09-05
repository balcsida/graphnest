# Operations

Milestones 0-2 are local pilot development only. Start the pinned fixture stack with:

```sh
docker compose -f deploy/compose/compose.yml --profile fixture up -d --wait
docker compose -f deploy/compose/compose.yml --profile fixture ps
```

The isolated Compose fixture index uses repository ID `7` and the checked-in
registry at `deploy/compose/repositories.json`. Static mode does not connect to
PostgreSQL; durable server and indexer modes require it. Stop the stack with:

```sh
docker compose -f deploy/compose/compose.yml down
```

Run the server with the environment in the [local quick start](../README.md).
Use `GET /healthz` for process liveness. `GET /readyz` performs a bounded Zoekt
health query and returns 503 with `{"error":"unavailable"}` when Zoekt is not
ready. `GET /metrics` exposes Prometheus metrics. Search and readiness are

For an explicit low-volume degraded path, set `GRAPHNEST_SEARCH_BACKEND=github`
on the durable server. GitHub code search is authorization-scoped but eventual,
rate-limited, and reported as partial without an exact-SHA claim. Restore the
default `zoekt` value to roll back; no automatic fallback occurs.
bounded by the configuration caps documented in the README.

Keep Zoekt private. Compose keeps Zoekt and PostgreSQL on an internal network
and publishes Zoekt only to `127.0.0.1:6070` for the host processes. The pinned Zoekt
image runs as `linux/amd64`; this is deliberate for Apple-silicon hosts, where
Docker's emulation is needed because the pinned image has no arm64 variant.

## Durable local indexer

Durable Compose runs PostgreSQL, Zoekt, the server, and one indexer from
`compose.yml` and `durable.yml`:

```sh
docker compose \
  -f deploy/compose/compose.yml \
  -f deploy/compose/durable.yml \
  --profile durable up -d --wait
```

PostgreSQL is authoritative for repositories, authorization, jobs, indexed
SHAs, graph artifacts, and graph query data. Zoekt remains internal and is
published only on `127.0.0.1:6070` for local diagnostics. The indexer downloads
archives into a bounded ephemeral workspace separate from durable Zoekt
shards. No checkout volume, graph topology, or internal graph secret is
required.

For a source-tree run, start PostgreSQL and Zoekt, apply migrations, and run
the indexer with the documented GitHub App, database, data-directory, index
directory, Zoekt URL, worker, repository-size, free-space, and metrics
settings. `GRAPHNEST_DATA_DIR` must point to job-scoped ephemeral storage, while
`GRAPHNEST_INDEX_DIR` points to durable Zoekt shards. The indexer handles SIGINT and SIGTERM by cancelling active work and
releasing PostgreSQL leases. Keep checkout data job-scoped and logs free of
credentials.

Use `GET /healthz`, `GET /readyz`, and `GET /metrics` for service
diagnostics. A non-ready private backend returns a bounded `503` response.
Stop the local stack with:

```sh
docker compose -f deploy/compose/compose.yml down
```

## Graph operation and recovery

PostgreSQL stores graph artifacts, upload metadata, job state, nodes, and
edges. The server queries that state directly for context, impact, and trace;
there is no separate graph owner, transport secret, synchronization loop, or
derived graph volume.

A graph answer is available only when the repository's current indexed SHA has
a completed graph upload. Missing, stale, or failed graph state returns the
documented graph status rather than falling back to another revision. Inspect
repository status and graph jobs in PostgreSQL when diagnosing readiness.

Recovery uses the normal durable pipeline:

1. Restore PostgreSQL according to the database backup policy.
2. Requeue indexing for repositories whose current indexed SHA has no completed
   upload. For native enrichment, install `graphnest-scanner` in the indexer and
   set `GRAPHNEST_SCANNER_PATH` to that binary.
3. Confirm the indexer's `graphnest-scanner enrich` invocation or an exact-SHA
   SCIP upload stores a completed artifact for that repository ID and commit.
4. Retry the bounded graph operation.

Do not hand-edit graph nodes or edges. Rebuild them from the indexer's exact
source snapshot or an accepted exact-SHA SCIP upload. Running the scanner
without the indexer's `enrich` invocation only idles for compatibility; it does
not lease or complete graph work.

Graph request limits are configured with `GRAPHNEST_GRAPH_*` query settings.
Context, impact, and trace enforce traversal depth, fanout, node, edge, row,
request-byte, and response-byte caps. PostgreSQL queries are parameterized,
repository/upload/commit scoped, stable-ordered, and batch each relation
frontier.

## Break-glass administrator recovery

SSO remains the primary sign-in method. Use the offline command only when an
authorized operator has direct access to the same PostgreSQL database used by
every server replica:

```sh
export GRAPHNEST_DATABASE_URL='postgres://...'
docker run --rm -it --network <database-network> \
  --env GRAPHNEST_DATABASE_URL \
  "$GRAPHNEST_APPLICATION_IMAGE" \
  graphnest-admin break-glass set-password recovery-admin
```

Use the same digest-pinned application image deployed by the server and a
network path to its PostgreSQL database. The command reads and confirms the
password from `/dev/tty`; without a usable TTY it accepts exactly two
newline-delimited standard-input values. Do not put the password in arguments,
environment variables, files, shell history, or logs. It creates only a local
administrator, or rotates that same eligible account, forces password
rotation, revokes its sessions and API tokens, and records
`break_glass_password_set`.

Creating the credential does not expose local login. An external-provider outage never
enables it automatically. Set `GRAPHNEST_BREAK_GLASS_ENABLED=true` (Compose) or
`breakGlass.enabled=true` (Helm) only for the recovery window, apply the
configuration, and wait for every replica to restart. All replicas must share
the same PostgreSQL database, which holds throttles and sessions. The first
sign-in must use `/auth/local/rotate`; it replaces the operator password,
clears forced rotation, revokes older sessions and API tokens, and issues a
new session.

After external sign-in is restored, first verify OIDC or GitHub OAuth sign-in.
If the recovery account must remain, rerun `graphnest-admin` to replace its
password and revoke its
credentials; otherwise suspend it through identity administration and revoke
its credentials. Set the Compose switch to `false` or Helm
`breakGlass.enabled=false`, apply the configuration, and verify
`/v1/auth/config` reports `break_glass:false` on every replica before closing
the incident. Configuration is read only at process startup, so a partial
rollout can temporarily leave different route availability across replicas.

Security audit events record bounded actor, target, authentication method,
operation, outcome, request ID, and creation time fields. They never store
passwords, session tokens, request bodies, or OIDC claims. The API returns a
bounded newest-first page and reports truncation; this release has no automatic
audit-retention or deletion mechanism, and the database trigger rejects updates
deletes, and truncation. Operators must account for that growth in PostgreSQL
retention and backup policy.

## Production control gates

Treat the following as deployment prerequisites, not settings supplied by this
repository:

- During every break-glass window, publish `/auth/local` and
  `/auth/local/rotate` only through a trusted edge. Configure that edge to
  derive a real client address from its trusted proxy chain and rate-limit both
  routes by it. Never trust `Forwarded` or `X-Forwarded-*` supplied by an
  arbitrary peer; GraphNest itself throttles the connection peer address.
- Before allowing a `v*` release tag, enable GitHub protected-tag rules, or
  require reviewers on the release environment that publishes the release.
  Confirm the live repository rule or environment gate; the checked-in release
  workflow validates signed tags but cannot enforce either GitHub setting.
- Enable Helm external egress isolation only after a reviewed, current allow
  list covers PostgreSQL and GitHub CIDRs and the DNS resolver path. When OIDC
  is enabled, it must also cover the configured identity-provider CIDRs for
  discovery, JWKS, and token requests. The chart takes PostgreSQL, GitHub, and
  conditional identity-provider CIDRs plus DNS namespace/pod selectors; record
  the DNS resolver CIDRs with that review where the cluster policy/CNI requires
  them. Confirm the installed CNI enforces `NetworkPolicy` and verify the
  rendered allow list against the live endpoint addresses before rollout.

## Optional OIDC operations

Permit server egress only to the configured IdP discovery, JWKS, and token
endpoints. The callback is `/auth/oidc/callback`; `GRAPHNEST_PUBLIC_URL` is the
authoritative HTTPS origin. Browser clients send same-origin credentials. Unsafe
session requests require that exact Origin, and GraphNest persists no refresh
tokens.

## Optional GitHub OAuth operations

GitHub OAuth requires durable PostgreSQL, an HTTPS `GRAPHNEST_PUBLIC_URL`, and
both `GRAPHNEST_OAUTH_GITHUB_CLIENT_ID` and
`GRAPHNEST_OAUTH_GITHUB_CLIENT_SECRET_FILE`. The secret must be a non-empty
regular file mounted read-only; there is no plaintext secret variable. Both
absent disables this provider and either one alone is a startup error. Use a
dedicated GitHub OAuth App for each environment and register exactly
`<GRAPHNEST_PUBLIC_URL>/auth/oauth/github/callback` as its callback URL. GitHub
OAuth may run alone or beside OIDC; the combined browser list puts OIDC first.
With neither browser provider, browser sign-in is unavailable but bearer REST
and MCP credentials remain supported. Local break-glass requires either
external provider, never enables automatically, and recovery must verify an
external login before closure.

OAuth derives its authorization/token endpoints from `GRAPHNEST_GITHUB_WEB_URL`
and its user endpoint from `GRAPHNEST_GITHUB_API_URL`; it reuses their existing
GitHub egress, `GRAPHNEST_GITHUB_CA_FILE`, timeout, and redirect policy. Do not
add OAuth-specific endpoint, CA, or network-policy controls. GitHub Enterprise
Server OAuth is explicitly unverified. This OAuth App is only browser identity:
the separate GitHub App remains the repository credential. The request sends
no scope; granted scope is rejected. The access token is used only once for
the authenticated-user request, then is neither persisted nor refreshed.

Replace the mounted client-secret file and restart every server replica to
rotate it or revoke a compromised client; configuration and secrets are read at
process startup. Revoke the OAuth App credential at GitHub as appropriate, then
complete the restart. MCP has no browser OAuth mode and rejects session cookies;
use a bearer token for `/mcp`.

### GitHub-derived access

`GRAPHNEST_OAUTH_GITHUB_ACCESS_SYNC=true` (Helm
`server.sso.githubOAuth.accessSync=true`) replaces SCIM provisioning with
GitHub's own permission model. It requires GitHub OAuth and changes two
things:

- The OAuth client must be the **GitHub App's own OAuth credential** (the
  client ID and secret shown on the App's settings page), not a separate OAuth
  App, and "Request user authorization (OAuth) during installation" may stay
  off. GraphNest cannot verify this pairing; a foreign OAuth client simply
  yields no installations and therefore no access.
- After `GET /user`, the same one-time token calls `GET /user/installations`
  and, for every installation of the configured `GRAPHNEST_GITHUB_APP_ID`,
  `GET /user/installations/{id}/repositories`. The result is exactly the
  repositories GraphNest indexes that this user can already reach on GitHub.
  Any failure or over-bound response denies the login rather than narrowing
  or widening access.

On a successful sign-in GraphNest creates the user on first use with
`source=github`, `externalId` `github:<issuer>:<numeric-id>`, and the GitHub
login as user name (suffixed with the numeric ID on a collision), updates the
display name, and atomically replaces that user's GitHub-derived grants. Direct
administrator or repository grants set through the admin API are kept separate
and still apply. Suspending the user in GraphNest denies subsequent logins.
SCIM and local users are never taken over by this path.

Access revoked on GitHub takes effect at the user's next sign-in, bounded by
`GRAPHNEST_SSO_SESSION_TTL` and `GRAPHNEST_SSO_SESSION_IDLE`; shorten them when
that window is too long. Grant the first administrator by user ID:
`PUT /v1/admin/users/{id}/access` with `direct_administrator: true` from a
bootstrap credential, or the offline `graphnest-admin` break-glass account.

### MCP OAuth authorization server

`GRAPHNEST_MCP_OAUTH=true` (Helm `server.sso.mcpOAuth.enabled=true`) turns
GraphNest into an OAuth 2.1 authorization server for MCP clients so tools such
as pi, OpenCode, Claude Code or Cursor connect to `/mcp` with no configured
secret. It requires a browser sign-in provider (OIDC or GitHub OAuth) and the
durable store.

Flow: an unauthenticated `/mcp` request receives `WWW-Authenticate: Bearer
resource_metadata=".../.well-known/oauth-protected-resource"`; the client reads
that and `/.well-known/oauth-authorization-server`, registers itself at
`POST /oauth/register` (public clients only, PKCE S256 mandatory, loopback
`http://127.0.0.1:<any port>` or `https://` redirect URIs), and opens
`/oauth/authorize` in the browser. GraphNest signs the user in through the
configured provider when needed and then renders a **consent page** naming the
client and its loopback redirect; the user must click Allow. The code is
exchanged at `POST /oauth/token` for an access token valid **up to one hour** and a
refresh token; the grant itself expires **30 days** after consent and every
refresh rotates both tokens. `expires_in` reports the remaining token lifetime,
capped by the grant expiry. Every consumed refresh-token hash is retained for
the grant's lifetime. Presenting any consumed token again 30 seconds or more
after its rotation revokes the whole grant and records
`oauth_grant_reuse_detected`. Before that deadline, a consumed token still
returns `invalid_grant`, but does not revoke the grant. The grace window does
not replay or recover a successful refresh response: if that response is lost,
the client must authorize again because only token hashes are retained.

Access tokens (`gno_…`) carry the user's repository read access, including
GitHub-derived grants, without administrative privileges. They authenticate
only `/mcp` and cannot create or manage credentials. Users see and disconnect
clients under **Account → Connected MCP clients** at `/account`
(`GET`/`DELETE /v1/account/oauth-grants`); administrators' "revoke
credentials" also revokes grants. `scope` is accepted, persisted and echoed but
not yet enforced, so finer scopes can be introduced later with a
`WWW-Authenticate: Bearer error="insufficient_scope"` step-up rather than a
migration.

With `GRAPHNEST_OAUTH_GITHUB_ACCESS_SYNC`, every new authorization requires a
fresh GitHub sign-in, including users with an existing GraphNest session. Each
grant stores the user's GitHub
token encrypted with AES-256-GCM under `GRAPHNEST_MCP_OAUTH_KEY_FILE` (32
bytes unchanged, with no trailing newline; required in that combination) and every refresh queries GitHub before
rotating. A successful repository snapshot commits atomically with the token
rotation, so repository access removed on GitHub disappears from agents within
the hour without a browser round-trip. A rejected, expired, or missing stored
credential returns terminal `invalid_grant` and atomically revokes only that MCP
OAuth grant and clears its ciphertext; the user's shared GitHub-derived grants
are unchanged. If the revocation cannot be persisted, refresh returns HTTP 503
without changing the grant, so the same refresh token can retry the revocation.
GitHub outages and rate limits leave the credential and grants unchanged and do
not prevent token rotation. Refresh waits at most two seconds for this GitHub
synchronization. Revoking a new grant after encrypted-token storage
fails has a separate ten-second cleanup budget.
Unreadable stored GitHub credentials make refresh return HTTP 503 without
rotating tokens or changing grants. Restore the original encryption key or
authorize the client again.
Expired authorization requests, week-old dead grants and clients idle for 90
days are swept by the periodic cleanup.

PostgreSQL shares fixed one-minute request budgets across server replicas:
registration and authorization each allow 10 requests per source IP and 100
across the deployment;
token exchange and revocation each allow 60 per source and 1,000 across the
deployment. Registration also has an atomic deployment-wide cap of 10,000
clients. Limits return HTTP 429;
an unavailable limiter returns HTTP 503. Source limits use the socket peer IP,
ignoring forwarded headers, so clients behind the same ingress proxy share a
source budget. Idle-client cleanup releases registration capacity.

Migration 025 extends the shared budgets to `/oauth/authorize`; migration 024
added `/oauth/revoke`. Unknown, wrong-client and already-revoked tokens still
return HTTP 200 when within budget, but only a newly revoked grant records a
successful revocation audit.
Disconnecting a client through the account UI also records a revocation audit.

Migration 022 revokes grants already rotated by earlier MCP OAuth builds,
because their discarded token history cannot be recovered. Those clients must
authorize again; unrotated grants remain valid.

Not supported by design: confidential clients, `client_credentials`
(services keep using API tokens and `POST /v1/admin/api-tokens` delegation),
remembered consent, and client-ID metadata documents. The provider-token
hand-off is process-local: the GitHub callback, consent POST, and the MCP client's
code exchange must reach the same replica. Browser-cookie affinity alone does
not cover the client's exchange. Use one server replica or route the entire
flow consistently. A missing hand-off fails code exchange and requires fresh
authorization; it never issues an access-synced grant without GitHub credentials.

### GitHub.com smoke and negative procedure

1. Use a public HTTPS origin and create a dedicated GitHub.com OAuth App for
   that environment. Set its homepage to the public origin and its callback to
   `<GRAPHNEST_PUBLIC_URL>/auth/oauth/github/callback`.
2. Place the OAuth client secret in a read-only regular file. Configure
   `GRAPHNEST_DATABASE_URL`, `GRAPHNEST_PUBLIC_URL=https://<public-host>`,
   `GRAPHNEST_OAUTH_GITHUB_CLIENT_ID`, and
   `GRAPHNEST_OAUTH_GITHUB_CLIENT_SECRET_FILE`; retain the existing GitHub web,
   API, egress, and CA configuration. Restart every server replica.
3. Obtain the test account's numeric ID without using its mutable login name:
   `curl --fail-with-body https://api.github.com/user -H "Authorization: Bearer $GITHUB_TOKEN" | jq .id`.
   Provision an active SCIM user with `externalId` exactly
   `github:https://github.com:<numeric-id>`, then grant it access to a known
   repository.
4. `curl --fail-with-body https://<public-host>/v1/auth/config` must list the
   GitHub provider; with OIDC also enabled, it must follow OIDC. In a new
   browser session, choose **Sign in with GitHub**, complete GitHub.com login,
   and confirm `GET /v1/auth/session` returns `{"method":"oauth"}`. Search
   and read an authorized repository, then `POST /auth/logout` and confirm the
   session no longer authenticates requests.
5. Repeat the browser flow with an unprovisioned user, an inactive SCIM user,
   and a user with a wrong `externalId`; each must be denied. Cancel consent to
   confirm denial is safe, replay the callback URL to confirm rejection, send a
   browser session cookie to `/mcp` without bearer credentials to confirm 401,
   and, when enabled, complete an OIDC login as well to confirm simultaneous
   providers remain distinct.

## Optional SCIM operations

Publish only the HTTPS `<GRAPHNEST_PUBLIC_URL>/scim/v2` endpoint and configure
`GRAPHNEST_SCIM_TOKEN_FILE` from a read-only secret mount. Use a dedicated
high-entropy token; it cannot access REST, MCP, or admin APIs. The OIDC link
claim must exactly equal each SCIM user's `externalId`.

Supported reconciliation filters are Users `id`, `userName`, or `externalId`
and Groups `id`, `displayName`, or `externalId`, all with `eq`. PATCH accepts
user `active`, `userName`, `displayName`, `name`, and `emails`; group PATCH
accepts `members` and `members[value eq "USER_ID"]`. Limits are 1 MiB per
body, 8 KiB per query, 16 KiB per URL, 100 PATCH operations, and the configured
maximum result count.

Rotate the token by replacing the mounted secret and restarting every server
replica; the process reads it only at startup. Deactivation and deletion deny
existing sessions and API tokens on their next request. Bulk, sorting, ETags,
passwords, `/Me`, `/.search`, root search, enterprise extensions, custom
schemas/resources, roles, and entitlements are unsupported.

See [Archive ingestion and PostgreSQL graph migration](migrations/archive-postgres-graph.md)
before upgrading a deployment that used Git mirrors, persistent worktrees, or
the former graph topology.

## Kubernetes chart boundary

Releases publish multi-architecture images and an OCI chart. Replace each
`sha256:RELEASE_DIGEST` below with the digest copied from that GitHub Release;
it is a placeholder, not a literal digest.

```sh
docker pull ghcr.io/balcsida/graphnest/application@sha256:RELEASE_DIGEST
docker pull ghcr.io/balcsida/graphnest/node@sha256:RELEASE_DIGEST
helm pull oci://ghcr.io/balcsida/graphnest/charts/graphnest --version 0.1.0
```

Verify the copied artifacts with the commands included in the GitHub Release:

```sh
gh attestation verify "oci://ghcr.io/balcsida/graphnest/application@sha256:RELEASE_DIGEST" --repo "balcsida/graphnest"
gh attestation verify "oci://ghcr.io/balcsida/graphnest/node@sha256:RELEASE_DIGEST" --repo "balcsida/graphnest"
```

The pulled OCI chart already embeds both release image digests. The source-tree
chart remains generic, so its users must supply their own image repositories
and digests. `make helm-lint helm-test` verifies source chart structure;
`make image-test` builds and smoke-tests local images.

An operator must provide external PostgreSQL, digest-pinned application and
node images, and every existing Secret documented by the chart. The chart does
not create Secrets or PostgreSQL. Its migration failure blocks install or
upgrade and leaves the failed hook Job inspectable.

Keep the singleton Zoekt Service internal. The node's Zoekt and indexer
containers share a 250Gi `ReadWriteOnce` PVC only for Zoekt shards. Archive
extraction uses a bounded `emptyDir`. Select operator-managed
SSD-backed RWO storage with `node.storage.storageClassName`. Server and node
scheduling maps are independent, as are their resource settings; use them to
place the storage-heavy node separately from stateless server replicas.

The resource defaults in the chart README are measurement starting points, not
guarantees. Actual capacity must be based on measured source corpus size, index
size, indexing duration, and query concurrency rather than repository count
alone.
