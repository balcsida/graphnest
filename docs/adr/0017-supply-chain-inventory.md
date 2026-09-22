# ADR-0017: Supply-Chain Inventory From Preserved SBOM Observations

- Status: Accepted
- Date: 2026-09-22

## Decision

GraphNest gains a native **Dependencies & Licenses** module, implemented as a
focused `internal/supplychain` package over the existing PostgreSQL store,
authentication stack, shared service layer, embedded Web UI, and MCP server.
The module is disabled by default (`GRAPHNEST_SUPPLY_CHAIN=true` enables it);
existing deployments keep working unchanged with it disabled.

The baseline producer is the GitHub Enterprise Server dependency-graph SBOM
export (`GET /repos/{owner}/{repo}/dependency-graph/sbom`). GraphNest treats
each fetch as a **timestamped dependency-graph observation**, never as an
exact-commit or release inventory: the endpoint has no ref selector and GHES
does not populate license fields. The original SPDX JSON member is preserved
byte-for-byte alongside its SHA-256; normalized component occurrences and
relationships are derived into an **immutable snapshot** that is published
atomically. A failed refresh never deletes the last successful snapshot.

Inventory eligibility depends only on repository authorization. It never
depends on a Zoekt index, an indexed SHA, a SCIP upload, or graph enrichment,
and inventory work never blocks lexical indexing. The existing GitHub-sourced
`repository_packages` projection used by SCIP cross-repository navigation is
kept as a **backward-compatible derived view** refreshed from published
snapshots; manual mappings are untouched and the flattened projection is not
the inventory source of truth.

License evidence is versioned, source-attributed, and kept distinct from
publisher declarations, human conclusions, policy evaluation, and usage
exceptions. SPDX expressions are parsed with a bounded grammar and pinned
license-list version; `NOASSERTION`, `NONE`, `UNLICENSED`, invalid text, and
`LicenseRef-*` values stay explicit and are never mapped to a permissive
license. External license enrichment produces **no outbound traffic** until a
registry route is explicitly configured, and configured routes never fall back
from a private registry to a public one.

Authorization is applied with the live principal before any aggregation:
totals, facets, pagination, exports, downloads, and MCP responses all resolve
back to the authorized repository set through the existing `authz` and
repository-store queries that honor installation boundaries and numeric
GitHub identities.

## Rationale

The existing `githubapp.DependencySBOM` reader keeps only SPDX IDs, PURLs, and
relationships, and `scipgraph.RefreshGitHubDependencies` flattens that into
package mappings; neither preserves the document, its producer metadata, its
components without PURLs, or its timestamps. A byte-preserving, snapshot-based
model gives engineers an auditable inventory and gives later milestones (license
evidence, imports, reviews) a stable basis, without a second database, a
plugin framework, a custom license scanner, or a SaaS dependency.

PostgreSQL is already authoritative for repository state, queues, and graph
artifacts (ADR-0004, ADR-0014) and its lease/fencing patterns are proven in
`index_jobs`/`graph_jobs`; reusing them keeps one operational model.

## Consequences

- Additive migrations (`033_supply_chain.sql` onward) add document, snapshot,
  occurrence, relationship, collection, job, and stream-pointer tables. All
  reference `repositories(id)` with `on delete cascade`, so repository removal
  removes visibility immediately.
- New REST routes live under `/v1/supply-chain/...` and are registered only
  when the module is enabled; MCP tools are read-only and registered under the
  same gate.
- The server's periodic loop enqueues refresh jobs with jitter; a worker in the
  server process leases and executes them. Collection runs outside request
  handlers; manual refresh only enqueues.
- The module adds bounded configuration (`GRAPHNEST_SUPPLY_CHAIN_*`) documented
  in `README.md` and `docs/operations.md`, and bounded Prometheus metrics with
  no component-level cardinality.
- Design decisions, schema, authorization matrix, and progress are recorded in
  the living plan `docs/execplans/supply-chain.md`.
