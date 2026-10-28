# Proposal: advisory exposure checks

## Why

When a critical advisory lands across many products at once (the October 2026
Brotli decoder bug hit libbrotli, Python `brotli`/`brotlicffi`, Node.js, Bun,
Envoy, Istio, .NET, a Go port, and copies bundled inside pyarrow and duckdb),
finding exposure in GraphNest took an hour of chained `search_dependency_inventory`
calls, dozens of hand-written `search_code` regexes split into batches of fewer
than 100 matches, and ad-hoc version classification. The dependency inventory
cannot see container base images or runtime pins, `search_code` silently caps
and gives unhelpful errors, and nothing records which teams have already fixed
the problem. Responding to the next advisory should be one step, and its
progress should be visible until the list reaches zero.

## What Changes

- **Runtime inventory.** When `GRAPHNEST_SUPPLY_CHAIN=true`, the indexer
  extracts container base images and runtime pins from the commit it has just
  made searchable (Dockerfile `FROM` with `ARG` defaults, `.nvmrc`,
  `.node-version`, `.tool-versions`, `.bun-version`, `global.json`,
  `<TargetFramework>`, CI `node-version`/`bun-version`/`dotnet-version`,
  `engines`, image references in YAML/compose/Helm values, `build.env` and
  `docker-bake.hcl` variables, and CODEOWNERS). It publishes them as a
  `graphnest:runtime` supply-chain snapshot bound to that exact commit, with
  file and line evidence. Indirection it cannot resolve simply is marked
  unresolved, never guessed. Extraction failure never fails indexing.
- **Advisories as tracked records.** Administrators paste an advisory in a
  small YAML/JSON format shaped like OSV (or a plain OSV record for library
  packages), preview what it matches, and publish it. Edits create versions.
  Bundled copies are named in the advisory as the packages that bundle them.
- **Exposure classification.** GraphNest continuously re-checks every
  repository against open advisories across all inventory streams and
  classifies it as vulnerable, needs rebuild (floating tag), EOL, unresolved,
  fixed, or excepted, with per-line evidence, a path class (prod, ci, test,
  docs), and an owner (CODEOWNERS, then GitHub admin team, then direct admin,
  then the org). Test- and docs-only hits are listed but not counted.
- **Remediation tracking.** Repositories leave the list by a version change on
  the default branch or by a reasoned, expiring exception recorded by a holder
  of the repository's existing review grant. GraphNest keeps an append-only
  status history (burn-down) and shows read-only fix signals: PRs and branches
  mentioning the advisory, and evidence changes since publication.
- **Surfaces.** REST under `/v1/supply-chain/advisories`, MCP `list_advisories`
  and `check_advisory` (stored advisory or an inline dry run), a web view
  under `/supply-chain/advisories`, and CSV/Markdown exports. Every report
  states that it covers default branches only. Results are cursor-paginated
  with no 100-row cap.
- **Search errors.** `search_code`, `find_files` and `POST /v1/search` return
  Zoekt's own parse message naming the offending construct, with a hint for
  known gotchas. Inline case flags such as `(?i)`, which Zoekt mishandles
  without an error (verified: `(?i)brotli` and `(?i)BROTLI` return no
  matches where `case:no BROTLI` finds the line), are rejected up front with
  a pointer to `case:no`.
- New ADR-0020 amends ADR-0017's "the indexer shares nothing with supply
  chain" rule for this one stream and records that GraphNest tracks
  operator-supplied advisories without becoming a vulnerability database.
- Optional new GitHub App permissions: `Administration: read` (admin teams)
  and `Pull requests: read` (PR signals). Without them GraphNest says which
  signal is unavailable and why.

## Capabilities

### New Capabilities

- `runtime-inventory`: extraction of container images and runtime pins from
  the indexed commit into the supply-chain inventory, with evidence and
  unresolved handling.
- `advisory-definitions`: the advisory input format, OSV acceptance,
  validation, versions, lifecycle, and dry-run checks.
- `advisory-exposure`: matching inventory to advisories, status
  classification, path classes, owners, coverage, and the REST/MCP/web/export
  reports.
- `advisory-remediation`: exceptions, fix signals, and status history.
- `search-query-errors`: actionable invalid-query errors for code search.

### Modified Capabilities

None. No capability specs exist in `openspec/specs/` yet.

## Impact

- **Code:** `internal/indexer` (post-publication extraction hook),
  `internal/supplychain` (new `runtime` and `advisory` packages, CycloneDX
  occurrence locations, stream validation), `internal/postgres` (migration
  `040_supply_chain_advisories.sql`, store methods), `internal/githubapp`
  (teams, collaborators, matching refs, issue search, installation
  permissions), `internal/httpapi`, `internal/mcpserver`, `internal/zoekt`,
  `internal/search`, `internal/config`, `cmd/graphnest-indexer`,
  `cmd/graphnest-server`, `web/src/pages/supply-chain`.
- **APIs:** new `/v1/supply-chain/advisories…` routes and MCP tools (gated by
  `GRAPHNEST_SUPPLY_CHAIN`); `docs/openapi.yaml`. Search error messages become
  more specific and queries with inline case flags now fail with 400 instead
  of returning wrong results.
- **Data:** one additive migration (new tables, a `locations` column on
  components, the `graphnest` producer). Existing repositories gain runtime
  inventory on their next index job; a one-off reindex backfills them.
- **Operations:** the indexer reads `GRAPHNEST_SUPPLY_CHAIN`; Helm and Compose
  pass it through. No new required configuration.
- **Docs:** ADR-0020, a "Responding to a CVE" how-to, architecture,
  operations, threat model, README, and a CHANGELOG entry under Unreleased.
