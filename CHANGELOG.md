# Changelog

Notable changes are recorded here. GraphNest is pre-1.0 pilot software; review
the compatibility and migration notes before upgrading.

## [Unreleased]

### Fixed

- `graph_capabilities`, `graph_discover`, `graph_callers`, `graph_callees`,
  `graph_files`, `graph_impact_radius` and `explore` failed with "graph is not
  ready" for every repository indexed with SCIP: they read only v2 graph
  generations, a SCIP upload built only the v1 fallback graph, and nothing in
  a default deployment published a v2 generation. A SCIP upload now also
  derives the v2 generation (producer `scip`): entities at their definitions
  with the indexer's kind, documentation and signature, `references` edges
  attributed to the enclosing definition, and `imports`, `implements` and
  `type_of` edges. The v1 fallback graph gains the same references, so
  `context`, `impact` and `trace` return relations instead of a bare symbol.
  Repositories indexed before this release need their SCIP index uploaded
  again for the current indexed commit.
- Graph readiness failures now say what happened instead of a single
  retryable `graph_not_ready`: `not_indexed`, `graph_missing` (names the
  indexed commit and what to upload; not retryable), `generation_changed`
  (retryable), `discovery_unavailable` and `graph_not_ready`, over REST and
  MCP alike.
- `context`, `impact` and `trace` no longer return one `graph_missing`
  boundary per unrelated authorized repository. Unrelated repositories without
  a graph collapse into one summary boundary per reason with a `count`.

### Added

- `graphnest doctor` checks an import environment without changing it: git,
  the repository commit, the CodeGraph index, the producer rules, the index
  state, freshness against HEAD and, when `GRAPHNEST_SERVER_URL` is set, the
  server and publication rights. `graphnest version` also prints the Go and
  SQLite versions.
- `--format text` for `graphnest graph import codegraph` and `graph upload`
  prints a readable report ending in a next step; JSON stays the default.
- Release binaries of `graphnest` for Linux and macOS (amd64 and arm64), with
  checksums, a dependency inventory and build provenance attestations,
  built with `make cli`.
- `graphnest`, a command-line tool. `graphnest graph import codegraph
  --dry-run` reads an existing CodeGraph index (schema versions 9, 10 and
  11; CodeGraph 1.6.0 to 1.6.2) through a pure-Go SQLite reader in one
  read-only transaction, converts it to the v2 graph artifact and reports
  counts by kind, diagnostics, the artifact hash and freshness as JSON.
  Freshness compares the index with the commit's content through
  `git archive`, using CodeGraph's own hashing, extension and ignore rules
  captured from the pinned builds; a matching HEAD is not proof. `--output`
  writes the artifact only for a fresh, complete index. `graphnest graph
  status` shows the repository and graph state the server holds. Server
  binaries do not link the SQLite reader. Without `--dry-run` or `--output`
  the command publishes a fresh, complete index, and `graphnest graph
  upload` publishes an artifact file: both run the publication preflight
  (indexed commit, grant, expected generation, explicit producer
  replacement), retry transport and server failures up to three times, and
  rely on the server's content-hash deduplication for safe retries.
- A second pinned CodeGraph reference, 1.6.2 (schema 11), under
  `test/fixtures/codegraph-1.6.2/`, and `producer-rules.json` for both pins.
- Repository status (`GET /v1/repositories/{id}`, MCP `get_repository_status`)
  reports `graph_status` (`current`, `stale`, `absent`, `unknown`),
  `graph_commit` and `graph_producer` next to `scip_status`, so a missing or
  stale graph generation is visible without calling a graph tool.

### Changed

- `graph_uploads` keeps one active v1 generation per repository (migration 038)
  and two active v2 generations, a published one and a SCIP-derived one
  (migration 039). A v2 publication no longer retires the v1 generation that
  `context`, `impact` and `trace` read, and a SCIP upload never touches a
  publisher's generation. `expected_generation` names the active published
  generation, `replace_producer=true` is needed only when the published
  generation's producer differs, and the graph tools use the published
  generation when it is at the indexed commit, otherwise the SCIP-derived one,
  so a stale publisher no longer causes `graph_missing`. Both migrations need
  writers drained first, as [graph storage](docs/graph-storage.md#rollout-and-recovery)
  describes, because old binaries cannot run against retained inactive
  generations.
- Release smoke tests, including the scan for fixable HIGH/CRITICAL
  vulnerabilities, now run on the amd64 images only. The arm64 images are
  still published with SBOMs and provenance, but are no longer smoke-tested or
  scanned before publishing.

## [0.9.2] - 2026-10-06

### Fixed

- Republished inventory snapshots stayed unassessed. Assessments were written
  only when a registry lookup finished, and coordinates with stored evidence
  are not looked up again, so a newly collected snapshot of the same
  dependencies had no assessments. Publishing a snapshot, and the startup
  backfill, now assess every component from the stored evidence without
  registry requests. ([#138])
- Retrying a negative registry lookup rebuilt the assessment without the
  human conclusion's precedence, turning a reviewed coordinate back into
  `declared`. The rebuild now keeps the reviewer's conclusion. Assessment no
  longer sorts the caller's evidence in place, which could make the reviewer's
  conclusion be missed when evidence arrived out of order. ([#138])

## [0.9.1] - 2026-10-06

### Changed

- Update the web console to React Router 8 and Recharts 3.10, and its build and
  test tooling to Vite 8, ESLint 10 and jsdom 30. TypeScript stays on 6.0 until
  typescript-eslint supports TypeScript 7. ([#130])
- Require Node 26.10.0 to build the web console, in CI and in the image build.
  Node 24 leaves active LTS on 2026-10-20 and Node 26 enters it on 2026-10-28.
  Published images do not contain Node and are unaffected. ([#136])

## [0.9.0] - 2026-10-06

### Added

- "Connect an agent" page at `/connect` with copy-ready MCP setup for Claude
  Code, Codex, OpenCode, Pi, Cursor, VS Code, Antigravity CLI and Claude
  Desktop, filled in with this server's address. When MCP OAuth is on, clients can
  sign in instead of using a token.

### Fixed

- With the sidebar collapsed to icons, the GraphNest wordmark spilled out of
  the icon rail over the header's sidebar toggle, so clicking the toggle
  followed the wordmark link instead of expanding the sidebar. The wordmark is
  now hidden while the sidebar is collapsed.

## [0.8.0] - 2026-10-04

The web console is rebuilt as a React application with a License dashboard
and a Dependency graph for directors. See
[ADR-0018](docs/adr/0018-react-shadcn-web-console.md). ([#128])

### Added

- License dashboard at `/supply-chain/licenses` for directors: key figures with
  named denominators, assessment status donut, top licenses, per-repository
  license mix by family, components per ecosystem, and a collection freshness
  strip. It is built from the existing supply-chain REST routes. ([#128])
- Dependency graph at `/supply-chain/graph`: onboarded repositories linked to
  the external dependencies they share, with filters, a detail sheet per node,
  and a table alternative. ([#128])
- `make ui`, `make ui-check`, `make ui-dev`, and `make ui-screenshots` build,
  check, run, and photograph the console. ([#128])

### Changed

- The console is one React single-page application on shadcn/ui, with a
  sidebar, client routes (`/`, `/repositories`, `/supply-chain`, `/admin`,
  `/account`), and a light, dark, or system theme, replacing the three
  separate pages. ([#128])
- The bearer token is kept in `sessionStorage` under `graphnest_token` and is
  migrated from `graphnest_admin_token` on first load. It is never stored in
  `localStorage` or a cookie.
- The Content-Security-Policy on HTML is now `script-src 'self'` with
  `style-src 'self' 'unsafe-inline'` instead of script and style hashes.
  Radix UI, Recharts, and Sonner inject style elements at runtime; inline
  scripts and `eval` remain forbidden.

### Removed

- The hand-written single-file pages and their 40 KiB budget, the Go contract
  tests and fake-DOM tests that read them, and the break-glass HTML stripping.
  The console reads `break_glass` from `GET /v1/auth/config`.

### Fixed

- The MCP `context` tool could not be loaded through OpenAI-compatible
  function calling (Azure OpenAI, LiteLLM): its input schema expressed the
  uid-or-name choice as a top-level `oneOf`, which those APIs reject with
  `invalid_function_parameters`, failing the whole request for every tool.
  The choice is now stated in the tool description and enforced by the
  service as before. ([#126])

### Upgrade guidance

- Building from source or images now requires Node 24. `make build`,
  `make test`, and the Docker image build run `make ui`; run it before building
  `Dockerfile.offline`. A Go binary built without it answers the console routes
  with `503`. Published images are unaffected.

## [0.7.0] - 2026-09-28

This release lets repositories publish their own v2 (CodeGraph) graph
generations under an administrator grant, and adds name-addressed callers,
callees and impact radius over REST and MCP.

### Added

- Name-addressed callers, callees and impact radius for v2 (CodeGraph)
  generations: `POST /v1/graph/callers`, `/v1/graph/callees` and
  `/v1/graph/impact-radius`, and the MCP tools `graph_callers`, `graph_callees`
  and `graph_impact_radius`. They follow CodeGraph's `codegraph_callers`,
  `codegraph_callees` and `codegraph_impact`: qualified names, per-definition
  sections, `file` narrowing and limits. They are compared with the pinned
  CodeGraph answers. Differences in definition order and non-exact name fallback
  are documented in `docs/graph-exploration.md`. ([#124])
- Repository-scoped graph publication (CodeGraph parity S1.08). ([#123])
  `POST /v1/graph/uploads` now accepts v2 artifacts
  (`application/vnd.graphnest.graph.v2+protobuf`). Administrators can publish
  them, and so can users whom an administrator has granted publication through
  `PUT /v1/graph/publication-grants`. Read access alone never publishes.
  - Each upload names the generation it replaces (`expected_generation`).
    Replacing another producer needs `replace_producer=true`.
  - An identical retry is deduplicated instead of rejected.
  - The token, grant and indexed commit are checked again after the upload is
    parsed.
  - Graph status gains a `publication` preflight block, and capabilities list
    upload versions `[1, 2]`.
  - v1 uploads remain administrator-only and unchanged.

### Fixed

- The search sidebar's repository and language filters overflowed past the
  sidebar border because a long example query widened the whole column. The
  filters now fit the sidebar and long examples are truncated. ([#122])

### Upgrade guidance

- Migration 037 adds the empty `graph_publication_grants` table. It runs
  automatically at startup, cascades from `repositories`, and touches nothing
  else. No configuration changes are required.

## [0.6.1] - 2026-09-27

Patch release with no migrations and no required configuration changes.

### Fixed

- Reconciliation stopped at the first repository whose default branch GitHub
  could not read (for example a renamed branch answering 404), skipping every
  later installation on every tick. The failing repository is now marked
  `error_code=default_branch`, no index job is queued for it, and
  reconciliation continues; the mark clears on the next successful read.
  Refresh and webhook reconcile failures now log the installation, repository,
  and HTTP status. ([#120], [#102])
- License enrichment ignored `HTTPS_PROXY`, so registry routes behind an
  egress proxy recorded `unavailable` for every package while the GitHub
  client worked. Registry requests now honour the standard proxy variables;
  the private-address policy is applied to the route host rather than the
  proxy address. ([#117])
- A registry route configured after inventories existed was never consulted
  for them: enrichment queued only on publication, and a repeat collection of
  an unchanged export publishes nothing. The enrichment worker now queues
  every stream's current snapshot at start-up (idempotent: fresh evidence and
  active jobs are skipped). ([#118])

### Added

- Helm: `server.extraEnv` renders plain variables into the server ConfigMap,
  for `HTTPS_PROXY`/`NO_PROXY` on clusters whose only egress is a proxy.
  ([#118])

## [0.6.0] - 2026-09-23

This release adds the opt-in Dependencies & Licenses module: a preserved
inventory of each managed repository's GitHub dependency-graph SBOM export,
exact-version license evidence from explicitly configured package registries,
portfolio queries and exports, standards-based SBOM imports, review workflows,
and read-only MCP tools. Everything is disabled by default; with
`GRAPHNEST_SUPPLY_CHAIN` unset the server behaves as in v0.5.0 apart from the
additive migrations below.

### Upgrade guidance

- Migrations 033 through 036 add the `supply_chain_*` tables; they run
  automatically at startup, cascade from `repositories`, and touch nothing
  else. No configuration changes are required. ([#105], [#108], [#110], [#111])
- The module is opt-in and durable-mode only: set `GRAPHNEST_SUPPLY_CHAIN=true`
  (Helm: `server.supplyChain.enabled`) to start collection. It never contacts
  a package registry unless a `GRAPHNEST_SUPPLY_CHAIN_REGISTRY_<ECOSYSTEM>_URL`
  route is configured, and a GitHub dependency-graph export is always reported
  as an unbound observation (`subject_assurance: unknown`) with `NOASSERTION`,
  `NONE`, and `UNLICENSED` never mapped to a license. ([#106], [#108])
- Snapshot retention defaults to ten snapshots per stream
  (`GRAPHNEST_SUPPLY_CHAIN_RETAIN_SNAPSHOTS`); the current snapshot and any
  snapshot referenced by a review record are always kept. ([#113])
- The GitHub dependency-graph collector has been exercised against recorded
  GHES fixtures only; live GHES and registry behaviour still require
  environment-specific validation (`docs/supply-chain-pilot-checklist.md`).

### Added

- Opt-in Dependencies & Licenses inventory (`GRAPHNEST_SUPPLY_CHAIN=true`,
  durable mode only). `graphnest-server` collects each managed repository's
  GitHub dependency-graph SBOM export on a jittered schedule, preserves the
  original SPDX 2.3 JSON byte-for-byte with its SHA-256, publishes an immutable
  snapshot of component occurrences and relationships in one fenced
  transaction, and serves it under `/v1/supply-chain/...` and the embedded
  `/supply-chain` page. Failed refreshes are recorded and never remove the last
  successful inventory; a GitHub export is reported as an unbound observation
  (`subject_assurance: unknown`) and license fields are preserved verbatim.
  Migration 033 adds the `supply_chain_*` tables; with the module disabled
  nothing else changes. See ADR-0017 and `docs/execplans/supply-chain.md`.
  ([#104], [#105], [#106], [#107])
- Exact-version license evidence for npm, NuGet, and Maven components from
  explicitly configured registry routes (`GRAPHNEST_SUPPLY_CHAIN_REGISTRY_*`),
  parsed with a bounded SPDX 2.3 expression parser against the pinned SPDX
  License List 3.27.0. Evidence rows are immutable and carry raw values,
  parse status, resolver and list versions, content hashes, and outcomes;
  per-occurrence assessments report resolved, declared, conflict, unlicensed,
  or unknown and are shown in the component table and a new evidence detail
  view (`GET /v1/supply-chain/repositories/{id}/component`). No route means no
  outbound license traffic. Migration 034 adds the evidence, enrichment-job,
  and assessment tables. ([#108])
- Portfolio read APIs over the caller's authorized repositories: an overview
  whose every count names its denominator, keyset-paginated unique
  coordinates with ecosystem, search, license, and assessment filters,
  bounded facets, a coordinate detail listing authorized occurrences, a CSV
  export with provenance columns and formula-safe cells, and a snapshot
  comparison that separates component, declared-license, and edge changes
  from document metadata changes. ([#109])
- Standards-based imports of SPDX 2.3 JSON and CycloneDX 1.6 JSON into
  declared `import:<subject>:<label>` streams (`POST /v1/supply-chain/imports`),
  with format detection from the document, explicit rejection of other
  formats and versions, byte-preserving storage, uploader identity recorded
  apart from the claimed producer, producer-asserted subject binding,
  idempotency, per-repository quotas, and administrator-managed upload grants
  (`PUT /v1/supply-chain/upload-grants`). A derived SPDX export
  (`GET /v1/supply-chain/exports/{id}/derived.spdx.json`) names GraphNest as
  creator, links the preserved original, and carries assessments as comments
  only. Migration 035 adds imports and upload grants. ([#110])
- Review workflows: a queue of occurrences needing review, human license
  conclusions recorded as immutable evidence, scoped approve/reject/exception
  decisions with optimistic concurrency on the evidence fingerprint
  (`409 stale_basis`), versioned policies evaluated over the SPDX expression
  tree with a clearly labelled example fixture and no auto-approval of
  unknowns, repository-scoped review grants, and an append-only audit trail
  under `/v1/supply-chain/review/*` and `/v1/supply-chain/policies`.
  Migration 036 adds the review tables. ([#111])
- Read-only MCP tools `search_dependency_inventory`,
  `find_component_repositories`, and `inspect_component_license` over the
  same authorized services as REST. ([#112])
- Retention for inventory snapshots (`GRAPHNEST_SUPPLY_CHAIN_RETAIN_SNAPSHOTS`)
  that always preserves the current snapshot and any snapshot referenced by
  a review record, plus bounded collection and job history. ([#113])

## [0.5.0] - 2026-09-18

This release adds repository-scoped graph discovery and exploration, introduces
delegation-only administrator tokens for CI upload brokers, and repairs the MCP
tools that failed against live deployments.

### Upgrade guidance

- Migrations 031 and 032 add two boolean columns to `api_tokens`; they run
  automatically at startup and default to `false`, so existing tokens keep
  their current behaviour. No configuration changes are required.
- Graph discovery, exploration, file listing, and capabilities require an
  existing v2 graph generation for the selected repository; repositories with
  only v1 uploads return an error that names the missing generation. ([#91])

### Added

- Repository-scoped graph discovery, exact-commit source exploration, indexed
  file listing, and capability reporting through `POST /v1/graph/discover`,
  `/v1/graph/explore`, `/v1/graph/files`, and `/v1/graph/capabilities`, and the
  matching MCP tools `graph_discover`, `explore`, `graph_files`, and
  `graph_capabilities`. Both transports recheck credentials and repository
  access before returning results. ([#91])
- Delegation-only administrator API tokens, minted by an interactive
  administrator through `POST /v1/account/delegation-tokens`. Such a token has
  no repository ceiling, so a CI upload broker no longer needs re-minting as
  repositories are onboarded, yet it is refused by every endpoint other than
  `POST /v1/admin/api-tokens`, so a leaked broker credential grants no direct
  read, search, or upload access. The account console labels these tokens.
  ([#94])

### Fixed

- MCP `search_code` returned "search service is unavailable" for common
  terms: Zoekt was asked for unbounded line matches and the payload exceeded
  the response budget. Requests now cap line matches at the page size, and a
  capped page reports `truncated: true` using Zoekt's match count rather than
  the display-limited result. ([#93])
- MCP `context`, `impact`, and `trace` returned "repository not found" for
  every OAuth and API-token principal: graph repository lookup required an
  installation match that non-installation principals never have. Such
  principals are now bounded only by their repository grants, while
  administrator-owned PATs keep their repository ceiling. ([#93], [#91])
- Digit-only repository selector strings such as `"825"` are treated as
  repository IDs rather than names. ([#93])
- Symbols in SCIP-fallback graphs are named from the SCIP descriptor instead of
  the raw symbol string and placed at their definition occurrence, so name-based
  `context` lookups (`Config`, `LoadConfig`, method names) resolve. ([#93])
- MCP OAuth consent could not be completed from a browser: `Referrer-Policy:
  no-referrer` made the consent form post arrive with `Origin: null`, and the
  `form-action` CSP directive blocked the post-consent redirect to the client's
  registered callback. ([#90])
- The admin console reports the HTTP status of non-JSON error responses, so an
  ingress proxy timeout during a long SCIP upload is distinguishable from a
  GraphNest failure. The Helm chart README documents raising the ingress
  timeout for large uploads. ([#92])

### Security

- A delegation-only token whose owner is later demoted from administrator is
  rejected outright instead of degrading into an ordinary token over all of the
  owner's repository grants. ([#94])
- Tokens minted by `POST /v1/admin/api-tokens` are recorded as delegated and
  may not delegate again, so delegation is one generation deep and a stolen
  job token cannot renew itself past its own expiry after the broker token is
  revoked. This closes a pre-existing gap. ([#94])
- Update the OTLP trace exporters bundled with the Zoekt tooling to v1.46.0
  (GHSA-8wmf-6v46-5gfg). ([#100])

### Changed

- Update `github.com/modelcontextprotocol/go-sdk` to v1.8.0 and `buf` to
  v1.73.0; update the `docker/setup-buildx-action` and
  `docker/setup-qemu-action` release workflow actions to v4.4.0. ([#99])

## [0.4.3] - 2026-09-13

### Security

- Apply Debian security updates while building the application and node runtime
  images, fixing PCRE2 vulnerabilities CVE-2026-86145 and CVE-2026-89161.
- Include the OpenTelemetry dependency fix and Go 1.27.1 upgrade documented in
  v0.4.2 below.

### Release notes

- v0.4.2 was tagged but not published: its image security gate detected the PCRE2
  vulnerabilities. v0.4.3 includes all changes since the last published release,
  v0.4.1.

## [0.4.2] - 2026-09-13

### Changed

- Require Go 1.27.1 for the service, scanner, developer tools, CI, and container
  builds. Update Staticcheck to v0.8.1 and govulncheck to v1.8.0 for Go 1.27
  compatibility. ([#87])

### Security

- Update Zoekt's OpenTelemetry OpenTracing bridge to v1.45.0 to fix concurrent
  baggage access that can crash the process (CVE-2026-45404 / GHSA-42cj-99w8-cp2p).
  ([#87])

## [0.4.1] - 2026-09-12

This release includes all changes since v0.3.0. The v0.4.0 tag did not produce
a published release: image scanning rejected its bundled Zoekt dependencies.
v0.4.1 rebuilds those tools with fixed dependencies.

This release introduces the GraphNest name, GitHub-derived repository access,
MCP client sign-in, and an expanded experimental graph-analysis foundation.

### Breaking changes and upgrade guidance

- **Deployments from v0.3.x and earlier require fresh GraphNest configuration
  and installation.** The product rename has no automatic in-place conversion
  or legacy-name aliases. Use `GRAPHNEST_*` configuration, `graphnest-*`
  commands and resources, and `graphnest_` metrics. Update Secret references,
  mounts, monitoring queries, and clients to the new names. See the
  [Helm installation guide](deploy/helm/graphnest/README.md). ([#49])
- Application and node images now publish under
  `ghcr.io/balcsida/graphnest/{application,node}`; the OCI chart is
  `oci://ghcr.io/balcsida/graphnest/charts/graphnest`. The Go module is
  `github.com/balcsida/graphnest`. Historical packages keep their original
  coordinates. ([#49])
- PostgreSQL replaces the LadybugDB graph replica and graph-owner service.
  Raw Cypher REST, MCP, and UI interfaces are removed; use the bounded context,
  impact, and trace operations. Existing current SCIP uploads are backfilled
  into PostgreSQL graph records by migration 019. ([#38])
- Default indexing uses bounded archives of the queued commit and temporary
  workspace instead of persistent Git mirrors/worktrees. Configure
  `GRAPHNEST_ZOEKT_INDEX`, keep temporary `GRAPHNEST_DATA_DIR` separate from
  durable `GRAPHNEST_INDEX_DIR`, and remove obsolete graph-owner deployment
  settings. Native enrichment is opt-in through a mounted scanner or the
  compatibility image target; default images, Compose, and Helm omit it.
  Follow the [archive and graph migration guide](docs/migrations/archive-postgres-graph.md),
  including backup, capacity, verification, and rollback steps. ([#38])
- Back up PostgreSQL before applying schema migrations. Drain graph, SCIP, and
  indexing traffic during the graph-storage transition; avoid mixed old/new
  binaries and do not assume a binary-only rollback is safe. See
  [graph-storage rollout and recovery](docs/graph-storage.md#rollout-and-recovery).
  Kubernetes 1.25 or newer is still required. ([#82])
- Repository listings now use cursor pagination. REST and MCP clients must
  follow `next_cursor` to retrieve all authorized repositories. ([#60])

### Added

- Optional GitHub-derived access (`GRAPHNEST_OAUTH_GITHUB_ACCESS_SYNC`): provision
  users at sign-in and synchronize repository grants from the GitHub App's
  installations. Administrator roles remain explicitly managed in GraphNest.
  The admin console shows derived grants. ([#58])
- Optional OAuth 2.1 authorization server for MCP clients
  (`GRAPHNEST_MCP_OAUTH`), with discovery, public-client registration, PKCE S256,
  browser consent, short-lived access tokens, rotating refresh tokens, and
  revocation. Users can view and disconnect connected MCP clients from their
  account. GitHub-backed grants require a 32-byte
  `GRAPHNEST_MCP_OAUTH_KEY_FILE`; see [MCP OAuth operations](docs/operations.md#mcp-oauth-authorization-server).
  For GitHub-backed authorization, the callback, consent POST, and client code
  exchange must reach the same replica; browser-cookie affinity alone is insufficient.
  ([#62])
- Administrator API-token delegation through `POST /v1/admin/api-tokens`, with
  explicit repository scope and a maximum one-hour lifetime. Delegated tokens
  cannot manage account tokens. ([#61])
- Optional GitHub search backend (`GRAPHNEST_SEARCH_BACKEND=github`) for
  low-volume deployments. Results are authorization-scoped and best-effort;
  they may be partial and do not promise the exact indexed revision. Zoekt
  remains the default, and backend selection is explicit. ([#38])
- Experimental graph v2 foundation: lossless, generation-scoped PostgreSQL
  storage; entity traversal and inspection; semantic discovery; bounded source
  exploration and task context; file-dependency analysis; aggregates; affected
  tests; and entity-impact analysis. The new operations are service-layer
  capabilities; their REST/MCP integration remains planned. Existing public
  graph operations remain available. Keep production v2 publication disabled
  until its rollout gate is implemented. See the
  [graph foundation checkpoint](docs/execplans/codegraph-parity.md). ([#82])

### Changed

- Redesigned search and administration consoles, with paginated repository
  inventories, repository totals, queue indicators, and improved file navigation.
  ([#60])
- Isolated optional native enrichment and generation tools into separate Go
  modules, reducing the dependencies of default application and node images.
  ([#38])
- Updated Go dependencies, pinned GitHub Actions, and Zoekt container images.
  Development and CI use Go 1.26.6. ([#84])

### Fixed

- GitHub access synchronization accepts full installation/repository list pages
  up to 1 MiB while retaining bounded response handling. ([#83])
- Accept scip-go's unspecified position encoding as UTF-8 and handle external
  build-cache document paths without rejecting valid in-project navigation.
  ([#61])
- GitHub OAuth sessions can manage account tokens and identities consistently
  with other supported browser sign-in methods. ([#36])
- Keep replica ports reserved during E2E proxy setup, and include workspace
  checksums required for reproducible generation checks. ([#83], [#84])
- Published release notes omit raw tag-signature blocks while retaining the
  signed tag and its compatibility/security notes.

### Security

- Build all bundled Zoekt tools from the pinned tools module, including gRPC
  1.83.2 and go-git 5.19.2, instead of installing the upstream module in isolation.
  This fixes the vulnerable transitive dependencies found by release image
  scanning in both the default node binaries and the legacy Git-indexing tool.
- MCP OAuth binds consent and provider handoffs to the initiating request and
  user, rejects refresh-token replay, encrypts retained GitHub credentials, and
  enforces registration/token-endpoint rate limits. OAuth access tokens are
  scoped to `/mcp`. ([#62])
- Archive ingestion rejects escaping paths, links, special files, conflicting
  outputs, and resource-limit violations. Redirect handling restricts targets
  and strips credentials when leaving the API origin. ([#38])
- Validate SCIP structure/counts before protobuf decoding and bound HTTP method
  metric labels to reduce resource-exhaustion exposure. ([#36])
- Release image smoke tests reject fixable HIGH/CRITICAL vulnerabilities.
  Published images retain SBOMs and provenance; images and charts use immutable
  digests and GitHub attestations. ([#36])

[Unreleased]: https://github.com/balcsida/graphnest/compare/v0.9.2...HEAD
[0.9.2]: https://github.com/balcsida/graphnest/compare/v0.9.1...v0.9.2
[0.9.1]: https://github.com/balcsida/graphnest/compare/v0.9.0...v0.9.1
[0.9.0]: https://github.com/balcsida/graphnest/compare/v0.8.0...v0.9.0
[0.8.0]: https://github.com/balcsida/graphnest/compare/v0.7.0...v0.8.0
[0.7.0]: https://github.com/balcsida/graphnest/compare/v0.6.1...v0.7.0
[0.6.1]: https://github.com/balcsida/graphnest/compare/v0.6.0...v0.6.1
[0.6.0]: https://github.com/balcsida/graphnest/compare/v0.5.0...v0.6.0
[0.5.0]: https://github.com/balcsida/graphnest/compare/v0.4.3...v0.5.0
[0.4.3]: https://github.com/balcsida/graphnest/compare/v0.4.1...v0.4.3
[0.4.2]: https://github.com/balcsida/graphnest/compare/v0.4.1...v0.4.2
[0.4.1]: https://github.com/balcsida/graphnest/compare/v0.3.0...v0.4.1
[#36]: https://github.com/balcsida/graphnest/pull/36
[#38]: https://github.com/balcsida/graphnest/pull/38
[#49]: https://github.com/balcsida/graphnest/pull/49
[#58]: https://github.com/balcsida/graphnest/pull/58
[#60]: https://github.com/balcsida/graphnest/pull/60
[#61]: https://github.com/balcsida/graphnest/pull/61
[#62]: https://github.com/balcsida/graphnest/pull/62
[#82]: https://github.com/balcsida/graphnest/pull/82
[#83]: https://github.com/balcsida/graphnest/pull/83
[#84]: https://github.com/balcsida/graphnest/pull/84
[#87]: https://github.com/balcsida/graphnest/pull/87
[#90]: https://github.com/balcsida/graphnest/pull/90
[#91]: https://github.com/balcsida/graphnest/pull/91
[#92]: https://github.com/balcsida/graphnest/pull/92
[#93]: https://github.com/balcsida/graphnest/pull/93
[#94]: https://github.com/balcsida/graphnest/pull/94
[#99]: https://github.com/balcsida/graphnest/pull/99
[#100]: https://github.com/balcsida/graphnest/pull/100
[#104]: https://github.com/balcsida/graphnest/pull/104
[#105]: https://github.com/balcsida/graphnest/pull/105
[#106]: https://github.com/balcsida/graphnest/pull/106
[#107]: https://github.com/balcsida/graphnest/pull/107
[#108]: https://github.com/balcsida/graphnest/pull/108
[#109]: https://github.com/balcsida/graphnest/pull/109
[#110]: https://github.com/balcsida/graphnest/pull/110
[#111]: https://github.com/balcsida/graphnest/pull/111
[#112]: https://github.com/balcsida/graphnest/pull/112
[#113]: https://github.com/balcsida/graphnest/pull/113
[#117]: https://github.com/balcsida/graphnest/pull/117
[#102]: https://github.com/balcsida/graphnest/issues/102
[#118]: https://github.com/balcsida/graphnest/pull/118
[#120]: https://github.com/balcsida/graphnest/pull/120
[#122]: https://github.com/balcsida/graphnest/pull/122
[#123]: https://github.com/balcsida/graphnest/pull/123
[#124]: https://github.com/balcsida/graphnest/pull/124
[#126]: https://github.com/balcsida/graphnest/pull/126
[#133]: https://github.com/balcsida/graphnest/pull/133
[#128]: https://github.com/balcsida/graphnest/pull/128
[#130]: https://github.com/balcsida/graphnest/pull/130
[#136]: https://github.com/balcsida/graphnest/pull/136
[#138]: https://github.com/balcsida/graphnest/pull/138
