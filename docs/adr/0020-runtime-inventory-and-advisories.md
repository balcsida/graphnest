# ADR-0020: Runtime Inventory From the Indexed Tree and Tracked Advisories

- Status: Accepted
- Date: 2026-10-28
- Amends: [ADR-0017](0017-supply-chain-inventory.md)

## Decision

When `GRAPHNEST_SUPPLY_CHAIN=true`, the **indexer** extracts container base
images and runtime pins from the exact tree it has just indexed, after the
commit becomes searchable and before the index job completes. Sources include
Dockerfile `FROM` lines with `ARG` defaults, `.nvmrc`, `global.json`,
`TargetFramework`, CI versions, `engines`, and image references in YAML, env,
and bake files. It publishes them as the `graphnest:runtime` supply-chain
stream: a CycloneDX 1.6 document with file and line evidence, normalized by
the existing importer path, bound to the indexed commit with subject
assurance `verified`. Extraction is best-effort: a failure, timeout, or budget
overrun is recorded as a failed collection, never publishes a partial
inventory, and never changes the outcome of the index job. Supply-chain
*collection* (the GitHub dependency-graph SBOM and imports) stays in the
server, as ADR-0017 decided. Only this stream, which describes the indexed
tree itself, comes from the indexer.

GraphNest also keeps **advisories** as tracked records. Administrators paste
them in a small OSV-shaped format, or as plain OSV records. The server checks
every repository against open advisories across all inventory streams, keeps
an append-only status history, records reasoned and expiring exceptions under
the existing review grant, and reads optional fix signals and owners from
GitHub. GraphNest is not a vulnerability database. It fetches no advisory
feeds, and it makes no outbound request for advisory content.

## Rationale

Three placements were considered for the runtime extraction:

- **The indexer (chosen).** The tree is already on local disk at the exact
  commit, so there are no extra GitHub calls. Every push refreshes the
  inventory, and the result is bound to the commit.
- **A server collector reading files through the GitHub contents API.** This
  stays inside ADR-0017's boundary, but costs one API call per candidate file
  per refresh, and GitHub truncates very large trees.
- **A server collector reading files from Zoekt.** This avoids GitHub calls,
  but needs new Zoekt modes and inherits the search response caps. It would
  also make inventory depend on an implementation detail (ADR-0002).

Classifying exposure at query time with code search was rejected. It keeps no
history, cannot resolve `ARG` or environment indirection without whole files,
and is bounded by the 100-match search cap.

## Consequences

- An additive migration (`040_supply_chain_advisories.sql`) adds the advisory
  tables and `repository_owners`, a `locations` column on components, and
  producer `graphnest` in the snapshot, collection, and stream check
  constraints.
- The indexer reads `GRAPHNEST_SUPPLY_CHAIN`. The Helm chart and Compose pass
  it to the indexer as well as the server.
- Existing repositories gain runtime inventory on their next index job. Until
  then, advisory reports list them as not checked rather than unaffected. A
  one-off reindex through the administrator API backfills them.
- GitHub App permissions `Administration: read` (admin teams) and
  `Pull requests: read` (pull request signals) become optional. Without them,
  GraphNest reports which signal is unavailable and why.
- The change is designed in
  `openspec/changes/add-advisory-exposure-checks/`.
