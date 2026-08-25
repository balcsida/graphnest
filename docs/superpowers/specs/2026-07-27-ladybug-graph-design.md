# LadybugDB Graph Analysis Design

## Purpose and success criteria

GraphNest will add a derived LadybugDB code graph beside its existing Zoekt
lexical index and PostgreSQL-backed SCIP navigation. The first graph release
will support four authorization-aware MCP tools, with matching REST behavior:
`context`, `impact`, `trace`, and administrator-only `cypher`.

Success means GraphNest can scan Go, TypeScript/JavaScript, Java, Kotlin, and
Rust repositories at their exact indexed commits; accept the same graph
artifact from external CI; rebuild LadybugDB entirely from PostgreSQL; run in
either embedded or standalone mode without changing public behavior; and never
serve graph data for a stale or unauthorized repository revision.

PostgreSQL remains authoritative. LadybugDB is a rebuildable query index, not a
replacement for repository metadata, queues, authentication, or SCIP storage.
Zoekt and existing SCIP navigation remain available when graph scanning or
LadybugDB is unavailable.

## Scope and non-goals

The initial graph contains repositories, files, symbols, and the relationships
needed for symbol context and call-graph traversal. It does not add hybrid
semantic search, process detection, framework-specific route or API analysis,
working-tree mutation, repository groups, PDG, or taint analysis. Those are
independent later capabilities built on the same artifact and graph boundary.

Only the currently indexed default branch is supported initially. The MCP
contracts accept an optional `branch` for compatibility, but reject any branch
other than the repository's indexed default branch with `branch_not_indexed`.

## Runtime architecture

One reusable internal graph runtime owns the LadybugDB schema, synchronization,
connection lifecycle, bounded queries, and internal HTTP handlers.

The default `embedded` mode starts this runtime inside `graphnest-indexer`. This
reuses the singleton node and its writable persistent volume. The optional
`separate` mode starts the identical runtime in a new `graphnest-graph` command
with its own deployment and volume. API servers always use the same internal
HTTP client and do not branch on the deployment mode.

LadybugDB is never opened by the horizontally scaled API servers. A runtime
process owns one writable database handle, one serialized writer, and a bounded
pool of read connections. This follows LadybugDB's single-process writer model
and keeps API replicas stateless.

The internal graph endpoint is authenticated with a dedicated secret, is not
published through ingress, and accepts an explicit repository allowlist for
every curated query. The API server remains responsible for user
authentication and repository authorization; the graph runtime enforces the
allowlist again.

## Graph production

Managed scanning and external analysis produce the same versioned graph
artifact.

The transaction that publishes a completed Zoekt index also enqueues a separate
graph job for that repository and commit. Scalable `graphnest-scanner` workers
claim these jobs, fetch only the exact commit through the existing GitHub App
and Git transport boundaries, parse bounded source inputs, resolve cross-file
symbols, and submit a graph artifact. Scanners parse source but never execute
repository code, compilers, build tools, package managers, or generated
scripts.

An administrator may upload the identical artifact for a repository's current
indexed commit. This supports external CI, unsupported analyzer environments,
and future local tooling. For the same repository and commit, an external
artifact takes precedence over a managed artifact; a late managed job cannot
overwrite it. A newer indexed commit supersedes either source.

When no enriched artifact exists, the graph synchronizer derives the smaller
available graph from current SCIP occurrences and relationships. This fallback
supports symbol context and explicit SCIP relationships without claiming
source-derived call accuracy.

## Scanner design

The managed scanner uses the official Tree-sitter Go binding and a pinned,
ABI-compatible grammar matrix for:

- Go;
- JavaScript, TypeScript, and TSX;
- Java;
- Kotlin;
- Rust.

Each language front end produces a shared intermediate representation:
declarations, imports, lexical scopes, reference and call sites, receiver and
type evidence, and inheritance or implementation facts. One cross-file
resolver converts those records into graph nodes and edges.

Language-specific code is limited to syntax queries and resolution rules:
Go modules, method sets, and implicit interface satisfaction; TypeScript module
aliases and JavaScript exports; Java and Kotlin package/import and heritage
rules; and Rust modules, traits, and `impl` blocks.

The binding, grammar versions, and supported Tree-sitter ABI are pinned
together. A compatibility test loads and parses one fixture with every grammar
so an incompatible dependency update fails before release.

The scanner walks regular files beneath its checked-out worktree without
following symbolic links or submodules. It skips Git metadata, binaries, and
configured generated/vendor directories and enforces per-file, file-count,
total-byte, parse-time, and artifact-count limits. Managed scanner containers
run non-root with a read-only root filesystem, an ephemeral worktree volume,
no service-account token, and only the network access required for GitHub and
PostgreSQL.

## Artifact and authoritative storage

The bounded protobuf artifact contains:

- schema version, analyzer identity and version;
- repository ID, exact 40-character commit, and content hash;
- files and symbols with normalized repository-relative paths and ranges;
- typed edges with source, target, location, confidence, and resolution reason.

The initial node kinds are `Repository`, `File`, and `Symbol`. The initial edge
kinds are `CONTAINS`, `IMPORTS`, `REFERENCES`, `CALLS`, `EXTENDS`, and
`IMPLEMENTS`.

SCIP symbol strings are the canonical symbol identity when available.
Otherwise the scanner uses a deterministic identity derived from language,
path, symbol kind, qualified name, and signature. SCIP facts and scanner facts
are merged by canonical identity, with exact SCIP identity preferred over
heuristic resolution.

The upload service validates authorization before reading a large body, then
validates size, schema version, commit, paths, ranges, identifiers, endpoints,
edge kinds, confidence values, duplicate records, and configured count limits.
It transactionally replaces the repository's current normalized graph rows in
PostgreSQL. Invalid or stale artifacts make no changes.

PostgreSQL stores only the current graph upload per repository, its normalized
nodes and edges, and scanner job state. This duplicates the derived query facts
deliberately so LadybugDB can be recreated without rescanning or introducing
object storage.

## LadybugDB synchronization

The graph runtime polls PostgreSQL graph upload IDs at a short configurable
interval. It compares those IDs and commits with a manifest stored in
LadybugDB, then transactionally replaces changed repository subgraphs. Deletes,
repository disablement, and a changed indexed commit remove the old repository
subgraph before it can be queried.

Queries filter on the synchronized manifest. A requested repository whose graph
commit differs from PostgreSQL's current indexed commit returns
`graph_not_ready`; the runtime never falls back to its older graph.
Cross-repository results identify excluded unauthorized or not-ready
repositories as boundaries rather than silently presenting a complete graph.

Schema or incompatible LadybugDB version changes build a new database from
PostgreSQL beside the live file, verify its manifest and focused queries, close
the old handles, and atomically swap the new file into service. Normal
repository updates use transactions rather than full rebuilds.

## Query contracts

REST and MCP adapters share one graph-analysis service and the existing result,
timeout, request, and response bounds.

### `context`

The caller selects a symbol by `uid`, or by `name` with optional `file_path`
and `kind`. The result is structured as `found`, `ambiguous`, or `not_found`.
A found result contains the symbol, categorized incoming and outgoing
relationships, bounded reference sites, graph commit, and completeness
boundaries. Optional source content is read through GraphNest's existing
exact-SHA repository file service rather than stored in LadybugDB.

### `impact`

The request supplies a target and `direction: upstream|downstream`. It supports
relationship filters, minimum confidence, test inclusion, per-depth
pagination, summary-only responses, and a bounded traversal depth with default
three. Results contain counts and locations grouped by depth, relationship
confidence, affected modules, graph commits, and completeness boundaries.

### `trace`

The request supplies source and target symbols. It finds the shortest directed
path through `CALLS` and symbol containment, with default depth ten and maximum
depth thirty. Results are `ok`, `ambiguous`, `not_found`, or `no_path`; a
successful path includes each symbol, source location, edge kind, and
confidence.

### `cypher`

Raw Cypher is administrator-only. It accepts one statement plus scalar
parameters and executes inside `BEGIN TRANSACTION READ ONLY`. The runtime uses
LadybugDB connection timeouts and interruption, then bounds returned rows and
encoded output. A connection that does not stop after interruption is discarded
and makes the graph runtime unhealthy.

Raw Cypher cannot provide row-level repository authorization, so it is not
available to ordinary repository-scoped principals. An optional repository
selector may be supplied for convenience and snapshot readiness checks, but it
does not weaken the administrator requirement.

## Initial agent skills

The first graph release ships four static skills: Guide, Exploring, Debugging,
and Impact Analysis. They teach agents the GraphNest graph schema and direct
them to the bounded `context`, `impact`, and `trace` tools; only Guide documents
administrator-only `cypher`.

`graphnest-mcp install-skills` installs the embedded skill files beneath
`.claude/skills/` in a selected repository root and mirrors them beneath
`.agents/skills/` when that repository already contains `.agents/`. It updates
only directories carrying a GraphNest-generated marker, rejects symbolic-link
destinations, and writes each directory through an atomic sibling rename.
Normal MCP proxy startup never writes to the working tree.

Working-tree-aware Plan, Work, Review, LFG, CLI, PDG, Taint, and generated area
skills remain follow-on capabilities because their required local analysis and
tool surfaces are outside this release.

## Repository and version selection

`repo` accepts a positive numeric repository ID or an exact repository name.
It may be omitted only when the caller can access exactly one repository.
Partial or fuzzy repository matching is intentionally excluded.

Every response reports the repository commits used. Curated multi-repository
queries include only repositories authorized to the principal and synchronized
to their current indexed commits. Trace endpoints must both be ready; impact
and context may return bounded partial results with explicit exclusions.

## Configuration and deployment

Helm and Compose expose `graph.mode: embedded|separate`, defaulting to
`embedded`. Both modes configure the same graph data path, internal listener,
internal secret, synchronization interval, query timeout, depth, row, node,
edge, upload, and response limits.

Managed scanning has independent enablement, replicas, resource requests,
repository/file/count limits, timeouts, and retry policy. Scanner workers and
the graph runtime can be scaled or disabled independently.

LadybugDB and Tree-sitter require cgo. The API server, MCP proxy, and migration
command remain free of these native dependencies. The node image contains the
indexer and standalone graph commands plus pinned LadybugDB runtime
requirements; the scanner image contains the scanner and compiled grammar
sources. Runtime images use a supported glibc base rather than musl or
`scratch`.

## Failure handling

A scanner failure retries through the graph queue and does not affect Zoekt,
SCIP, or the repository's indexed SHA. Exhausted jobs expose degraded graph
status while retaining SCIP fallback eligibility.

Artifact replacement and LadybugDB repository synchronization are
transactional. A failed replacement leaves the previous database transaction
intact, but that data is not served after PostgreSQL advances the indexed
commit. Corruption, missing files, or schema mismatch trigger a rebuild from
PostgreSQL.

The readiness endpoint distinguishes core search readiness from graph
readiness. Graph failure does not make lexical search unavailable, while the
standalone graph deployment has its own readiness and liveness checks.

Logs and metrics include repository ID, commit, job state, scan duration,
artifact counts, graph synchronization lag, query type, traversal counts,
timeouts, and rebuild status. They never include source content, credentials,
raw artifacts, or raw Cypher parameters.

## Verification

Focused unit tests cover artifact validation, canonical identities, ambiguity,
confidence, traversal limits, and the shared resolver. Table-driven parser
fixtures cover all supported languages, including cross-file calls, imports,
inheritance, and language-specific behavior.

PostgreSQL integration tests cover graph-job coalescing, lease behavior,
managed-versus-external precedence, stale-commit rejection, transactional
replacement, repository deletion, and authorization.

LadybugDB tests use temporary directories and cover schema creation, import,
repository replacement, concurrent readers, read-only Cypher, timeout and
interruption, manifest checks, corruption recovery, rebuild, and atomic swap.
The same contract suite runs against embedded and standalone runtimes.

REST and MCP tests cover compatible input aliases, structured ambiguity,
authorization, result bounds, branch rejection, exact-SHA enforcement, partial
boundaries, and output-byte limits. End-to-end fixtures exercise all supported
languages from exact checkout through scanning, PostgreSQL, LadybugDB, REST,
and MCP.

CI adds an explicit cgo build, a LadybugDB dynamic-link smoke test, the pinned
Tree-sitter ABI check, and Helm/Compose rendering checks for both graph modes.
The existing race, PostgreSQL integration, end-to-end, security, and Helm gates
remain required.

## Follow-on capabilities

The same artifact and graph runtime can later add hybrid BM25/semantic search,
process/community detection, route and MCP maps, shape and API-impact checks,
working-tree change detection and rename support, repository groups, PDG, and
taint analysis. Each requires its own accepted design because LadybugDB storage
alone does not provide those analyses.
