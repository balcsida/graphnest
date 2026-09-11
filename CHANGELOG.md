# Changelog

Notable changes are recorded here. GraphNest is pre-1.0 pilot software; review
the compatibility and migration notes before upgrading.

## [0.4.0] - 2026-09-11

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
  ([#62])
- Administrator API-token delegation through `POST /v1/admin/api-tokens`, with
  explicit repository scope and a maximum 24-hour lifetime. Delegated tokens
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
  graph operations remain available. See the
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

[0.4.0]: https://github.com/balcsida/graphnest/compare/v0.3.0...v0.4.0
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
