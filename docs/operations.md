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

## Dependencies & Licenses inventory

The supply-chain inventory ([ADR-0017](adr/0017-supply-chain-inventory.md))
is disabled by default. Enable it centrally on `graphnest-server` in durable
mode; ordinary repositories need no workflow or configuration file.

| Variable | Default | Meaning |
| --- | --- | --- |
| `GRAPHNEST_SUPPLY_CHAIN` | `false` | Enable routes, the `/supply-chain` page, the scheduler, and collection workers. Requires `GRAPHNEST_DATABASE_URL`. |
| `GRAPHNEST_SUPPLY_CHAIN_INTERVAL` | `24h` | Scheduled refresh interval per repository stream (minimum `1m`). A stream is due when its last attempt is older than this; new jobs receive up to 10% jitter. `collection: stale` is reported after twice this interval. |
| `GRAPHNEST_SUPPLY_CHAIN_WORKERS` | `1` | Collection workers per server process (maximum 8). Each leases one job at a time. |
| `GRAPHNEST_SUPPLY_CHAIN_MAX_DOCUMENT_BYTES` | `16777216` | Maximum SBOM export size (whole HTTP body, maximum 256 MiB). Larger exports record `too_large`. |
| `GRAPHNEST_SUPPLY_CHAIN_MAX_COMPONENTS` | `50000` | Maximum packages per document (maximum 500000). |
| `GRAPHNEST_SUPPLY_CHAIN_RETAIN_SNAPSHOTS` | `10` | Snapshots kept per repository stream in addition to the current one and any snapshot referenced by a policy result, decision, or conclusion; `0` disables pruning. Unreferenced documents are removed with their last snapshot. |

GitHub permissions: the GitHub App needs `Contents: read` on the repository,
the same permission indexing already requires. The collector calls only
`GET /repos/{owner}/{repo}/dependency-graph/sbom` on the configured API
endpoint with the installation token; no other outbound traffic is produced,
and no registry, license, or SBOM URL from a document is ever dereferenced.
Custom CAs and secret files are the existing `GRAPHNEST_GITHUB_*` settings.

Job lifecycle: the scheduler runs on the reconciliation tick and enqueues at
most one queued job per repository stream. A manual refresh
(`POST /v1/supply-chain/repositories/{id}/refresh`, administrator) raises that
job's priority or creates one and returns `202` immediately. Workers lease
jobs for two minutes with `for update skip locked`, renew the lease during the
fetch, and publish in one transaction fenced by the lease owner and a
monotonic fence, so a worker that lost its lease cannot overwrite a newer
publication. Retryable outcomes (`rate_limited`, `transient`, `error`) back off
exponentially (1m, 4m, 16m, ... up to 1h, or `Retry-After` when longer) for at
most five attempts; `forbidden`, `not_found`, `malformed`, `too_large`, and
`unavailable` fail the job immediately. Leases expired by a crash are reaped to
`queued` or `failed` on worker start and each idle loop. Every attempt is a
row in `supply_chain_collections`; a failure never changes the stream's latest
snapshot.

Interpreting outcomes: a `403` is `rate_limited` only when GitHub's
`X-RateLimit-Remaining: 0`, `Retry-After`, or a `429` says so; otherwise it is
`forbidden`, and a `404` is `not_found`. Neither proves the dependency graph is
disabled; the operator message lists the possible causes (dependency graph
disabled, no installation access, unsupported GitHub version).

Recovering a failed collection: fix the cause (enable the dependency graph,
grant the installation access, raise the size limit), then request a manual
refresh. A `projection_error` on a collection means the snapshot published but
the compatibility projection into SCIP package mappings failed; the inventory
is intact and the projection is rebuilt by the next successful collection.

Disabling: set `GRAPHNEST_SUPPLY_CHAIN=false` (or unset it) and restart the
server. Routes and the page disappear, workers stop, and existing
`supply_chain_*` tables are left in place. Backup and restore follow the
PostgreSQL policy for other durable state; documents are stored inline
(`supply_chain_documents.body`), so database size grows with the number of
distinct SBOM exports retained. Retention runs on the scheduler tick: it
keeps the newest `GRAPHNEST_SUPPLY_CHAIN_RETAIN_SNAPSHOTS` snapshots per
stream plus every snapshot a review record refers to, trims failed collection
attempts beyond the newest 50 per stream, and removes finished jobs after 30
days. Repository removal or a revoked installation removes inventory
visibility immediately through the authorization queries and cascades the
rows; shared registry evidence (which is not repository data) is retained.

Metrics: `graphnest_supply_chain_collections_total{outcome}`,
`graphnest_supply_chain_collection_duration_seconds{outcome}`,
`graphnest_supply_chain_queue_depth{state}`,
`graphnest_supply_chain_enrichment_total{outcome}`, and
`graphnest_supply_chain_enrichment_duration_seconds{outcome}`. Labels use
fixed vocabularies; no repository or component identity is exported.

### License enrichment routes

GitHub exports carry no license data. Exact-version license evidence comes
only from registry routes you configure; with none configured, GraphNest
produces no license traffic at all and components show only the producer's
(usually `NOASSERTION`) declaration.

| Variable (per ecosystem `NPM`, `NUGET`, `MAVEN`) | Meaning |
| --- | --- |
| `GRAPHNEST_SUPPLY_CHAIN_REGISTRY_<ECO>_URL` | HTTPS registry root, without credentials, query, or fragment. npm: the registry root (`https://npm.example/`); NuGet: the V3 flat container (`https://nuget.example/v3-flatcontainer/`); Maven: the repository root (`https://maven.example/repository/public/`). |
| `..._TOKEN_FILE` | Optional bearer token secret file (regular file, at most 64 KiB). |
| `..._BASIC_FILE` | Optional `user:password` secret file; mutually exclusive with the token file. |
| `..._CA_FILE` | Optional PEM bundle appended to the system roots for this route. |
| `..._ALLOW_PRIVATE` | `true` to permit a registry that resolves to a private, loopback, or link-local address (internal mirrors). Default `false`; cloud metadata ranges stay blocked regardless. |
| `..._NAMESPACES` | Optional comma-separated npm scopes / Maven groupId prefixes / NuGet id prefixes this route may answer for; anything else is rejected without a request. |

One route per ecosystem. A package the route does not know is recorded as
`not_found` at that route; GraphNest never retries it against a public
registry, so a private-registry deployment cannot leak package names. Requests
are pinned to the route's origin and base path (redirects elsewhere are
rejected), bodies are bounded after decompression (4 MiB), and credentials are
attached only to the route's own origin. Registry requests honour
`HTTPS_PROXY`/`NO_PROXY` like the GitHub client; the private-address policy is
applied to the route host, not to the proxy. A TLS-intercepting proxy needs
its CA in `..._CA_FILE`.

What each resolver reads and how it records it:

- **npm**: `GET {root}/{name}/{version}` for the exact version only, never
  dist-tags or the packument's `latest`. A string `license` is parsed as an
  SPDX expression; `SEE LICENSE IN <file>` is recorded as a license-file
  reference; legacy `{type,url}` objects and `licenses` arrays are kept as
  legacy metadata (an array of names has no SPDX AND/OR meaning and stays
  unparsed); `UNLICENSED` stays `unlicensed`. A document naming a different
  version is rejected.
- **NuGet**: the exact-version `.nuspec` from the flat container; the
  `.nupkg` is never downloaded. `<license type="expression">` is parsed,
  `<license type="file">` is a file reference, and a legacy `<licenseUrl>`
  alone is recorded as a URL, not a concluded license.
- **Maven**: the exact-version POM. `<licenses>` are names and URLs, not SPDX
  expressions; only unambiguous names (Apache 2.0, MIT, BSD, EPL, LGPL, MPL,
  ISC, CDDL, Unlicense, CC0, GPL-2.0 with Classpath) are normalized, and a
  name that is already a valid SPDX expression parses as such. Several
  `<license>` elements are kept as a list without invented structure. Missing
  `<licenses>` are inherited through `<parent>` on the same route only, at
  most eight levels, with cycle detection and bounded `${property}`
  expansion; anything unresolved stays `no_license_metadata`. Repository
  declarations inside POMs are never followed.

Evidence rows are immutable and carry the raw value, parse status, normalized
expression, unknown terms, resolver version, SPDX License List version
(3.27.0), content hash, fetch time, and outcome. A re-fetch that yields the
same facts is a new observation flagged as a duplicate; a change is a new
row. Negative results (`not_found`, `no_license_metadata`, `unavailable`)
expire after 24 hours and are retried; an outage keeps the earlier resolved
evidence visible with its age rather than replacing it with "no license".

Assessments are derived per occurrence from the producer's declaration and
the latest evidence per route: `resolved` (registry expression, consistent),
`declared` (only the producer's expression parsed), `conflict` (structurally
different expressions, or an expression against `UNLICENSED`/`NONE`),
`unlicensed`, `unknown` (nothing parseable), `pending`, or `not_applicable`.
An assessment is evidence, not approval; the review workflow records
conclusions and decisions separately.

Lookups are queued when a snapshot is published and, at every server start,
for every stream's current snapshot, so a route configured after inventories
exist is consulted for them without waiting for the exports to change.
Coordinates with a queued job or fresh evidence are skipped, so the start-up
pass is idempotent.

### Standards-based imports

`POST /v1/supply-chain/imports?repository_id=<github id>&subject=<source|artifact>&label=<stream label>`
accepts SPDX 2.3 JSON (`application/spdx+json`) and CycloneDX 1.6 JSON
(`application/vnd.cyclonedx+json`); `application/json` is accepted and the
format is detected from the document itself. Other formats and schema
versions (SPDX 2.2, CycloneDX 1.5, XML, tag-value) are rejected with
`415 unsupported_format`; GraphNest does not claim universal SBOM
compatibility. Each `(subject, label)` pair is its own stream
(`import:source:ort`, `import:artifact:syft-image`), separate from the GitHub
observation, so a container SBOM never replaces the repository's GitHub
snapshot and an older artifact upload never becomes the current source
inventory.

Permission: administrators may import into any authorized repository. Other
principals need a repository-scoped upload grant
(`PUT /v1/supply-chain/upload-grants` by an administrator, keyed by the
principal's subject). A grant never widens read access. Quota: 100 imports
per repository per 24 hours, counting rejected attempts. Identical bytes into
the same stream are idempotent (`repeated: true`). Documents are bounded by
`GRAPHNEST_SUPPLY_CHAIN_MAX_DOCUMENT_BYTES` and `..._MAX_COMPONENTS`.

Trust boundaries: the authenticated uploader is recorded on the snapshot
(`uploaded_by`) separately from the tool the document names, which is only
the document's own claim; a `Tool: ORT` string grants nothing. An optional
`subject_revision` (40-hex commit) is recorded as `producer_asserted`, never
`verified`. Download locations, license URLs, and external document
references inside uploads are never dereferenced.

What is and is not carried: original bytes are always preserved and
downloadable. From SPDX, GraphNest normalizes packages, versions, purls,
checksums, suppliers, declared/concluded license values (verbatim), and every
relationship with its direction. From CycloneDX, it flattens nested
components (with `CONTAINS` edges), keeps `dependencies` as `DEPENDS_ON`,
hashes, suppliers, purl qualifiers, and license choices as the format carries
them: one `expression` or SPDX `id` verbatim; a `name`/`url` as a name or
URL; several license objects as a semicolon list (CycloneDX defines no
AND/OR meaning for them, so none is invented). CycloneDX component
`evidence` (license findings, copyright, file occurrences), inline license
text, ORT's per-file scan results and curations, and Syft's file catalog stay
only in the stored original and are flagged by coverage warnings
(`evidence_not_carried`, `license_text_inline`). GraphNest has no native
ScanCode or Code Insight adapter; it imports what those tools export in the
two supported formats.

Producer examples (each writes a supported format; the fixtures under
`test/fixtures/supplychain/` are sanitized shapes of these outputs):

```sh
# Syft: CycloneDX 1.6 JSON of a built image (artifact subject)
syft registry.example.internal/acme/widgets:1.4.2 -o cyclonedx-json@1.6 > widgets-image.cdx.json
curl --fail-with-body -X POST "https://graphnest.example/v1/supply-chain/imports?repository_id=101&subject=artifact&label=syft-image" \
  -H "Authorization: Bearer $GRAPHNEST_TOKEN" -H 'Content-Type: application/vnd.cyclonedx+json' --data-binary @widgets-image.cdx.json

# ORT: SPDX 2.3 JSON report of the analyzed source tree (source subject)
ort report -i analyzer-result.yml -o reports -f SpdxDocument -O SpdxDocument=outputFileFormats=JSON
curl --fail-with-body -X POST "https://graphnest.example/v1/supply-chain/imports?repository_id=101&subject=source&label=ort&subject_revision=$GITHUB_SHA" \
  -H "Authorization: Bearer $GRAPHNEST_TOKEN" -H 'Content-Type: application/spdx+json' --data-binary @reports/bom.spdx.json
```

### Review workflows and policies

Three record kinds stay separate: a **conclusion** corrects license evidence
(stored as immutable `human` evidence that overrides, but never deletes,
automated evidence; disagreement stays visible in the assessment's conflict
detail); a **policy result** applies one versioned policy to one occurrence;
a **decision** approves or rejects usage of exact coordinates in one
repository, or grants an exception with an expiry within a year. Every
decision records the reviewer, reason, usage context, policy version and
verdict, and the assessment evidence fingerprint it was made against. A newer
record supersedes the previous one; nothing is edited, and
`supply_chain_review_events` is append-only.

Optimistic concurrency: the review queue (`GET /v1/supply-chain/review/queue`)
hands out each occurrence's `basis` fingerprint, and conclusions/decisions
must echo it back; a changed basis is refused with `409 stale_basis`, so a
reviewer cannot approve evidence they have not seen. When evidence changes
after a decision, or an exception expires, the occurrence returns to the queue
with the reason and history reports `current_stale`. Re-evaluation runs in the
background every minute and retains historical results.

Permissions: reading the queue and history needs only repository read access;
recording conclusions and decisions needs a repository-scoped review grant
(`PUT /v1/supply-chain/review/grants`, administrator-only; reviewers cannot
grant themselves) in addition to read access; creating or activating policies
(`POST /v1/supply-chain/policies`) is administrator-only. No pretend legal
policy ships: the embedded policy is labelled `kind: example` and is installed
only on request; `unknown_handling` must be `review_required` or
`prohibited`, never approve. Policies are evaluated over the SPDX expression
tree (AND takes the worst operand, OR the best, `WITH` pairs are their own
terms and are never approved by their base license); an acceptable OR branch
is reported, but choosing it is a separate recorded decision. Verdicts are
`approved`, `prohibited`, `review_required`, or `unknown` and are not legal
advice or release gates.

### MCP tools

With the module enabled, `/mcp` exposes three read-only tools through the
same authorized services as REST: `search_dependency_inventory`,
`find_component_repositories`, and `inspect_component_license`. Responses
include snapshot IDs, provenance, scope, and truncation, and state that
package, license, and evidence content is untrusted data. There are no
approval, import, or refresh tools over MCP.

Derived export: `GET /v1/supply-chain/exports/{id}/derived.spdx.json` returns
an SPDX 2.3 JSON document created by GraphNest that links the preserved
original by URL and SHA-256 and adds assessments as `licenseComments` only
(`licenseConcluded` is always `NOASSERTION`). It is validated with
GraphNest's own reader before it is served and is never presented as the
producer's document. `GET /v1/supply-chain/exports/{id}/components.csv` is
the tabular equivalent with provenance columns and formula-safe cells.

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

## Service credentials for CI upload brokers

A broker service (for example a GitHub App that hands each CI job a
single-repository SCIP upload token) authenticates to GraphNest with an
administrator API token and calls `POST /v1/admin/api-tokens`. Ordinary
administrator API tokens carry an explicit repository ceiling and may only
delegate inside it, so such a broker credential silently stops covering
repositories onboarded after it was minted; delegation for those returns 403.

Use a **delegation-only** token for the broker instead. Mint it as an
interactive administrator (session or OIDC sign-in; never from another API
token) with `POST /v1/account/delegation-tokens`, optionally passing
`expires_at`. The token:

- has no repository ceiling and needs no re-widening as repositories are
  onboarded;
- may delegate a one-hour, narrowed token for any repository that is enabled,
  not archived, and on an active installation; a request naming any other
  repository is refused as a whole, so the response does not enumerate
  repositories;
- mints children that cannot delegate again, so a leaked job token expires
  within the hour instead of renewing itself after the broker is revoked;
- is refused by every other endpoint: it cannot search, read, upload, list or
  revoke tokens, or manage OAuth grants. A leaked broker credential therefore
  yields only the ability to mint short-lived single-repository tokens until it
  is revoked.

Revoke it like any other token from the owner's session
(`DELETE /v1/account/api-tokens/{id}`); it is listed with
`"delegation_only": true`. The token is tied to the owner's administrator
role: if the owner is demoted, the token stops authenticating altogether
rather than degrading into an ordinary token over the owner's grants. Prefer a
dedicated local service user as the owner so revoking a person's access never
disables the broker.

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

Each browser authorization has a public 64-character `request_id` and a
request-specific `__Host-graphnest_oauth_req_<request_id>` secret cookie, so
multiple tabs may sign in and decide consent independently. The provider
callback resumes only the exact `request_id` supplied by GraphNest; malformed
or altered continuations require restarting that authorization.

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
