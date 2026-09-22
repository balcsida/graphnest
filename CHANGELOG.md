# Changelog

Notable changes are recorded here. GraphNest is pre-1.0 pilot software; review
the compatibility and migration notes before upgrading.

## [Unreleased]

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
- Exact-version license evidence for npm, NuGet, and Maven components from
  explicitly configured registry routes (`GRAPHNEST_SUPPLY_CHAIN_REGISTRY_*`),
  parsed with a bounded SPDX 2.3 expression parser against the pinned SPDX
  License List 3.27.0. Evidence rows are immutable and carry raw values,
  parse status, resolver and list versions, content hashes, and outcomes;
  per-occurrence assessments report resolved, declared, conflict, unlicensed,
  or unknown and are shown in the component table and a new evidence detail
  view (`GET /v1/supply-chain/repositories/{id}/component`). No route means no
  outbound license traffic. Migration 034 adds the evidence, enrichment-job,
  and assessment tables.

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

[Unreleased]: https://github.com/balcsida/graphnest/compare/v0.5.0...HEAD
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
