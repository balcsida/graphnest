# GraphNest: CodeGraph-only server and local CLI parity

## Execution brief

Implement this roadmap in `balcsida/graphnest`, preserving the user's sequence:

1. **CodeGraph parity on GraphNest Server:** an authorized engineer or agent can obtain equivalent code facts, source context, explanations, and browser workflows for a committed repository snapshot.
2. **GraphNest CLI imports local CodeGraph indexes:** reuse an existing index without re-indexing by default, then publish through GraphNest's authorized upload boundary.
3. **GraphNest CLI replaces the local CodeGraph experience:** indexing, incremental updates, working-tree queries, MCP, browser exploration, and offline operation.

Engineer experience is the release criterion. Adding relationship enum values, accepting an artifact, or wrapping remote requests does not by itself satisfy any parity gate.

This document is an implementation specification, not a report of completed work. All new GraphNest commands, package paths, permissions, test targets, and contracts below are proposed. Adapt locations to the checked-out repository without reducing behavior.

## 1. Baselines and first actions

Research snapshot: 6 September 2026.

| Component | Inspected source commit |
|---|---|
| GraphNest | `49e77d1bcd7f4be7198d8368e58e062778b68235` |
| CodeGraph, `colbymchenry/codegraph` | `b9ca4b7981116909900368cc1686a1074cd4d4c1` |

Re-read `AGENTS.md`, applicable nested instructions, accepted ADRs, existing tests, and the current default branch. Record actual checkout and upstream versions. Do not reset the user's checkout to these commits. These are source baselines, not assertions that corresponding released binaries exist. Reproducibly build the pinned engine when necessary; do not silently substitute an older release and call it parity.

The inspected GraphNest uses Go, PostgreSQL, Zoekt, separately uploaded SCIP indexes, an external graph-artifact upload, REST, browser, and MCP surfaces. Its documented deployment currently indexes default branches. The artifact model supports six relationships; `selectedRelations()` and PostgreSQL relation mapping expose four, and neighbor queries constrain returned entities to symbols. External graph uploads require an administrator. [S1–S5]

The inspected CodeGraph defines 13 relationships and 23 node kinds. Its SQLite schema includes richer symbol attributes, edge evidence, file records, and unresolved references. Its public workflows go beyond graph traversal to exploration, source viewing, affected-test discovery, auto-sync, and browser analysis. [S6–S9]

Create a living execution record at `docs/execplans/codegraph-parity.md`, unless repository conventions specify another location. Maintain Progress, Decisions, Discoveries, Validation Results, Remaining Gaps, and the native stack/PR identifiers. Never mark an unexecuted test as passed.

## 2. Architectural decisions

### CodeGraph-only scope

CodeGraph is the only new external analyzer and local-index format in this roadmap. Do not research, install, invoke, bundle, or test against other graph analyzers. Do not add their adapters, CLI commands, runtime integrations, fixtures, compatibility layers, feature flags, dedicated schema extensions, licensing gates, or deferred release requirements. Preserve existing GraphNest-managed enrichment and SCIP compatibility.

This revision supersedes earlier multi-analyzer plans. Remove superseded work items and draft-only integration scaffolding from this initiative where present; do not rewrite published history, delete user indexes, or uninstall software. No other analyzer's output is an input to this migration, and it must never be relabeled as CodeGraph output. Preserve provenance rather than attempting an indirect conversion.

Keep the import pipeline reusable between importing an existing CodeGraph index in Stage 2 and publishing GraphNest-managed CodeGraph indexes in Stage 3. A generic plugin framework or additional analyzer support is not a prerequisite for either stage. Standard CodeGraph/runtime dependency and redistribution checks remain required.

### Preserve the server boundary

PostgreSQL remains the server's graph store. Zoekt remains private search infrastructure. SCIP remains a separate precision/navigation contribution. Do not introduce another persistent graph database, a required Node.js runtime, or CodeGraph itself as a production server dependency. Do not add persistent clones of indexed repositories or require a new per-repository CI pipeline. Reuse existing exact-SHA source acquisition and optional ephemeral archive workflows.

Stage 1 implements equivalent behavior over imported committed-snapshot facts, including any required query-time derivation. It does not promise a live remote view of uncommitted edits. Stage 3 provides the latter locally. Remote branch/workspace publication is a separate, explicitly versioned future capability; do not weaken exact-SHA uploads to simulate it.

### Reuse CodeGraph for the local engine

For Stage 3, use a pinned CodeGraph engine behind a GraphNest-owned adapter rather than independently reimplementing its language and framework analyzers. GraphNest owns the CLI, configuration, output contracts, authentication, publishing, and user experience. A supported installation must not require users to separately install CodeGraph, Node.js, npm, PostgreSQL, Zoekt, or Docker.

Deliver the engine as a versioned optional local runtime bundle, with its required runtime/native components, or as an explicitly adopted compatible installation. Use upstream machine-readable interfaces where sufficient. Where they are insufficient, build a small pinned bridge against the upstream library; do not scrape terminal output. Keep engine upgrades explicit, versioned, and covered by conformance tests. CodeGraph's README describes standalone bundled distribution, but GraphNest must independently validate its own packaging and dependency licenses. [S8]

Existing `.codegraph/` directories are read-only integration inputs. GraphNest-managed indexing uses a GraphNest-owned workspace or engine data location. Do not silently take ownership of a colleague's database, configuration, watcher, or agent integration.

### Keep provenance and scope explicit

Every graph result identifies repository/workspace, producer, producer version, graph generation, and either an exact commit or a local content-manifest identity. Also return freshness, coverage, unresolved-analysis boundaries, and traversal/response truncation. Partial results must be distinguishable from a verified empty result.

Initially retain one active relationship provider per repository, plus separate SCIP. Uploads must not silently replace a different provider. Require explicit activation/replacement with an expected active-generation precondition. Multi-producer reconciliation is not part of this roadmap; do not merge symbols merely because their names match.

### Treat imported data as untrusted

A content hash and a matching SHA do not prove an analyzer's conclusions are correct. Preserve publisher identity and analyzer provenance, enforce repository-scoped publication grants, and audit activation. Treat source text, docstrings, paths, and metadata as data, never executable instructions. Apply bounds before parsing deeply, querying, rendering, or allocating large structures.

## 3. Native GitHub stacked-PR workflow

Use GitHub's **native stacked pull requests**, not merely dependent PR descriptions or similarly named third-party tools. The documented feature is in public preview. It requires same-repository branches, supports an explicit remote stack, and provides `github/gh-stack` for local management. [S10–S12]

Use one sequential stack per stage, rooted at `main` or the actual default branch. Start the next stage's stack after the preceding stage lands. Within a stage, continue building higher layers while lower layers await review. Never base a dependent layer directly on `main` merely to make it look independent.

Before work, inspect `git status`, remotes, authentication, existing branches/PRs, and `gh stack --help`. Reuse existing matching work. Install `github/gh-stack` only when absent and permitted by the execution environment. Confirm native stack availability with an actual operation when submitting. If unavailable, retain local work and report that specific blocker; do not claim an ordinary branch chain is a native stack.

Example workflow; comments indicate work that must actually happen, not empty placeholder commits:

```sh
# From a clean checkout; do not discard someone else's changes.
git fetch origin
git switch main
git pull --ff-only

gh auth status
# Only if the extension is absent:
gh extension install github/gh-stack

gh stack init --base main feat/codegraph/s1-01-contract
# Implement S1.01, run its tests, and commit the reviewed file set.

gh stack add feat/codegraph/s1-02-artifact
# Implement S1.02, run its tests, and commit.

# Add subsequent layers only after the preceding layer has real commits.
gh stack submit --auto --remote origin
gh stack view --json
```

`submit --auto` creates draft PRs and links the native stack. Verify remote membership, correct base/head branches, and each layer's delta after submission; an exit code alone is not sufficient evidence. Use `gh stack rebase` for cascading changes, resolve conflicts through its documented continuation flow, rerun affected-layer tests, and submit again. Use `gh stack sync` after lower layers merge. [S11]

Each PR must contain the layer's implementation, relevant tests, documentation, and migration notes together. Do not defer all tests to a final PR. Include stage/layer, parent dependency, behavior change, validation commands/results, compatibility impact, and remaining gaps in the description. Keep incomplete capabilities disabled until their supporting layers exist.

All existing required checks and reviews remain required. Native stacks apply trunk merge requirements across their layers, but verify actual Actions runs and check names in this repository. Never bypass protection or merge automatically without owner authorization. Merging an upper native-stack PR can also merge its lower dependencies; stage approval must cover that whole selected prefix. [S10, S12]

Layer identifiers below are work-item IDs, not future GitHub PR numbers. Split oversized layers into additional consecutive native-stack layers, each with its own tests, rather than combining unrelated work or reducing scope. In particular, analysis workflows and browser parity will likely need several focused PRs.

## 4. Stage 1 — CodeGraph parity on GraphNest Server

**Outcome:** For a pinned CodeGraph index and the same committed source, GraphNest serves equivalent useful facts and workflows through PostgreSQL-backed services, REST, MCP, and its browser. CodeGraph may run in the test harness, never as a required production server dependency.

### S1.01 — Parity inventory and executable reference harness

Branch suffix: `s1-01-contract`.

Create `docs/parity/codegraph.md`, `test/fixtures/codegraph/`, and a proposed `test/parity/` harness. Inventory upstream CLI commands, MCP schemas/tool-registration behavior, library queries, UI workflows, node/edge kinds, language/framework coverage, configuration-sensitive behavior, and metadata used at query time. Inspect actual source and tests, not only README claims.

For every behavior, record upstream reference, stage, GraphNest surface, fixture, comparison method, and completion status. Stage 1 owns committed-snapshot query/browser behavior; Stage 3 owns local indexing/watch/installation behavior. No item disappears as vaguely “out of scope.”

Generate reproducible sanitized CodeGraph reference databases and expected query answers from source fixtures. Store producer version, schema version, configuration, source hashes, and regeneration commands. A test-only conversion helper is permitted here; the user-facing importer arrives in Stage 2.

**Acceptance:** Enumerated behavior is tied to tests; all 13 relationships and all 23 entity kinds have fixtures; reference answers are reproducible. Record performance baselines and proposed release budgets before implementation changes them. Tests that are not yet supported are visible planned gaps, not disabled tests counted as passes.

### S1.02 — Versioned artifact, relationship registry, and identity

Branch suffix: `s1-02-artifact`.

Extend `internal/graphartifact/` with a v2 contract and retain the v1 reader. Add first-class `exports`, `type_of`, `returns`, `instantiates`, `overrides`, `decorates`, and `navigates`; retain the established numeric values of the existing six relationships. Do not copy CodeGraph's enum order. Centralize names, wire mappings, direction/display information, and API validation in one registry.

Preserve every upstream node kind, original/source identity, short and qualified names, documentation, modifiers, decorators, type parameters, return types, file-generation hints, edge metadata/provenance, unresolved references, and extraction diagnostics. Use typed common fields plus bounded namespaced extension data. Support virtual/external entities without inventing a local path or source location.

Represent absent location/confidence explicitly. Determine source-coordinate units from producer implementations and test non-ASCII/CRLF cases; subtracting one from line numbers is not a complete conversion. Preserve separate occurrences and use producer-scoped identity without collapsing overloads or distinct declarations. Separate volatile import timestamps from canonical semantic hashing.

**Acceptance:** v1 golden artifacts still validate identically; v2 round trips every fixture fact/evidence field; unknown versions fail clearly; identity, metadata limits, missing values, and deterministic hashing have unit/property/fuzz coverage.

### S1.03 — PostgreSQL persistence and atomic generation lifecycle

Branch suffix: `s1-03-storage`.

Update `internal/postgres/` using new migrations, never editing applied migrations. Extend version/kind constraints, metadata storage, file facts, unresolved diagnostics, and edge occurrence keys. Add indexes for repository/upload-scoped traversal in both directions and for supported metadata predicates. Do not require a new PostgreSQL extension without a measured need and an ADR.

Persist publisher/producer identity and capabilities with each generation. Validate before atomically making a generation active. Keep graph queries pinned to one committed generation so concurrent replacement cannot mix uploads. Preserve v1 and existing managed/scip-fallback behavior. Publish a rollout order: compatible readers first, richer writes second; once v2 is written, old-binary rollback is not presumed safe.

**Acceptance:** Upgrade from a populated old database; v1/v2 coexist; cancellation, invalid data, concurrent publication, and index-SHA changes leave the previous usable state intact. Query plans use the new indexes on representative sizes. Document retention and recovery behavior.

### S1.04 — Entity-aware traversal and evidence

Branch suffix: `s1-04-query`.

Update `internal/graphquery/`, `internal/graphprotocol/`, and `internal/postgres/graph_query.go`. Replace symbol-only assumptions with entity-aware lookup and neighbors while retaining backward-compatible symbol wrappers. Expose all CodeGraph relationship filters and incoming/outgoing traversal, including file imports/exports/containment.

Define deterministic ordering, pagination tied to a generation, bounded depth/fanout/nodes/edges/time, cycle handling, and cancellation. Preserve original edge direction in results regardless of traversal direction. A caller query remains a call query: do not broaden every default to every relationship. Handle unknown confidence without dropping legitimate edges behind a numeric threshold.

**Acceptance:** Reachability and evidence match the oracle for every relation; files, external entities, cycles, repeated occurrences, and ambiguous symbols are tested. Authorization boundaries never reveal hidden neighbors, metadata, counts, or paths.

### S1.05 — Search, source inspection, and composed exploration

Branch suffix: `s1-05-explore`.

Implement equivalent discovery across symbol names, qualified names, signatures, documentation, file paths, and filters. Inspect upstream ranking/tokenization and generated-code treatment; do not substitute Zoekt text search and assume semantic discovery parity. Reuse existing infrastructure where it produces equivalent behavior, and rebuild derived search indexes from preserved facts where necessary.

Add shared domain operations for `explore`, entity/file inspection, outlines, callers/callees, and source-context assembly. Compose them directly, not through internal REST/MCP round trips. Source must come from the exact indexed commit. Preserve low-confidence/no-entry-point results and return useful handoff information rather than invented explanations.

**Acceptance:** Expected relevant entities and verbatim source appear on the reference tasks; ranking changes cannot remove required answers from the tested result budget. A single exploration request returns source, useful relationships, and completeness metadata. Source-read failures cannot become apparently complete graph answers.

### S1.06 — Flow, impact, type/navigation, and derived analysis

Branch suffix: `s1-06-analysis`; split by workflow as necessary.

Implement source-evidenced flows, impact/blast radius, transitive import-based affected tests, type users/returners, inheritance/overrides, module public surface, routes/navigation, and entry points. The parity inventory also governs browser-backed maps, screen/step views, type hierarchy, dead-code candidates, and other analysis present at the pinned baseline.

Some upstream answers involve query-time rules or source inspection rather than persisted edges. Reproduce the applicable derivation using exact-SHA source and preserved metadata. Distinguish recorded edges, derived links, uncertain candidates, and unresolved stops. Carry conditions, arguments, and framework evidence when upstream exposes them. Never turn “not found statically” into “cannot happen.”

**Acceptance:** Each workflow has an oracle comparison and negative cases; paths explain their hops; affected tests traverse file dependencies, not only call edges; dynamic gaps remain visible. Every parity-inventory analysis row has a corresponding implemented service before this stage passes.

### S1.07 — REST, MCP, and versioned capabilities

Branch suffix: `s1-07-interfaces`.

Update `pkg/api/`, `docs/openapi.yaml`, HTTP handlers, MCP registration/schemas, and existing proxy tests. Expose the shared workflows and a high-level exploration tool. Preserve existing routes/tools or add explicit compatibility aliases instead of silently changing their meaning.

Add capabilities that distinguish server-supported artifact versions/relations/workflows from the selected graph's actual producer coverage. Return revision/generation, provenance, staleness, and truncation consistently. Do not create a public raw database-query execution endpoint.

**Acceptance:** REST/MCP conformance tests produce equivalent facts and scope. Existing clients remain functional. Tool discovery, argument validation, malformed requests, cancellation, response budgets, and unauthorized repository selection are tested.

### S1.08 — Repository-scoped publication and replacement safety

Branch suffix: `s1-08-publish-policy`.

Extend the existing authentication/authorization design with a repository-scoped graph-publication permission, conceptually `graph:write`. Read access does not imply publication access. Preserve administrator behavior where appropriate, enforce token ceilings and current repository grants, and audit publisher, producer, commit, content hash, and replacement/activation.

Expose capability/status/preflight data needed by the future CLI: supported versions, limits, current indexed SHA, active producer/generation, and upload result. Enforce an expected-generation precondition for replacement; require explicit replacement of a different producer. Recheck exact SHA and authority during final publication. Deduplicate retries by scoped content hash/idempotency key.

Do not assume the existing MCP OAuth token is valid for REST: the inspected documentation distinguishes their surfaces. Reuse credentials actually supported by REST; any audience/scope extension is a separately tested security change. [S1, S5]

**Acceptance:** Non-administrator scoped publication succeeds only with the explicit grant; read-only, revoked, expired, and wrong-repository credentials fail. Retry, concurrent replacement, and advancing-SHA tests cannot overwrite the wrong generation. No administrator credential is required on an ordinary developer laptop.

### S1.09 — Browser workflow parity

Branch suffix: `s1-09-browser`; split into source/navigation, analysis views, and trails/export layers.

Extend the existing browser to inspect files and symbols, show source-positioned caller/callee evidence, filter relations, trace paths, and explain impact. Implement the remaining pinned upstream browser inventory, including entry points, module maps, screen/step navigation where supported, and the relevant saved-trail and export behaviors. Visual similarity is not required; task equivalence is.

Persist server-side user-owned trails with repository authorization and revision-aware targets. Reauthorize at reopen/export; do not leak source from a formerly authorized repository through saved data. Preserve moved/renamed/missing-target diagnostics instead of guessing replacements. Distinguish heuristic results and stale/partial state visually. Sanitize all imported content and generated exports.

**Acceptance:** Browser tests perform the same reference tasks, follow exact source locations, restore authorized trails, and exercise all inventory views. UI filtering cannot hide supported relationships by accident. Authentication loss and repository revocation are covered.

### S1.10 — Server conformance, performance, and rollout gate

Branch suffix: `s1-10-server-gate`.

Run fixture/oracle comparisons through serialization, PostgreSQL, service layer, REST, MCP, and browser. Exercise a real, pinned CodeGraph fixture generator in dedicated validation, not just handcrafted artifacts. Record normalized fact diffs, required-answer retrieval, source correctness, latency, memory, response size, and query counts.

Add deployment/compatibility/operations documentation, metrics for import failures and query boundaries, and recovery instructions. Check existing SCIP navigation, managed enrichment, search, authentication, and exact-revision behavior for regressions.

**Stage 1 gate:** All committed-snapshot rows in the parity matrix pass; all known CodeGraph facts survive; required workflows return the right evidence; authorization and legacy tests pass; no required CodeGraph runtime or new persistent graph service is deployed. A merely stored-but-unusable relationship does not pass.

## 5. Stage 2 — GraphNest CLI imports local CodeGraph indexes

**Outcome:** A colleague can validate and publish an existing compatible local CodeGraph index without re-indexing it or acquiring an administrator credential. Unverifiable freshness or incomplete producer output causes an actionable failure, not a misleading success.

### S2.01 — CLI foundation and shared import pipeline

Branch suffix: `s2-01-cli`.

Introduce `cmd/graphnest/` if absent, with focused proposed `internal/cli/`, `internal/client/`, and `internal/graphimport/` packages. Preserve existing `graphnest-mcp` behavior. Implement server/profile selection, supported credential handling, repository-ID resolution, capability negotiation, cancellation, explicit timeouts, structured errors, and stable JSON output on stdout; diagnostics go to stderr. Keep credentials out of command arguments, repository config, logs, and artifacts.

Define the CodeGraph import pipeline, reusable for Stage 3 publication: detect CodeGraph/schema version; establish a consistent read; identify source content; normalize; validate; report; serialize; optionally publish. Accept only explicitly supported CodeGraph schemas; do not auto-detect or execute other analyzers. Distinguish offline artifact creation from online preflight. `--dry-run` never uploads or mutates the source index; online checks must be explicit or clearly documented.

**Acceptance:** CodeGraph reader fixtures and test doubles cover schema detection, corrupt input, bounds, deterministic output, cancellation, zero mutations, and rejection of unsupported source formats. A token cannot be forwarded to an unexpected host during redirects or subprocess execution.

### S2.02 — Complete CodeGraph importer

Branch suffix: `s2-02-codegraph`.

Read supported CodeGraph SQLite schemas through a read-only transaction or a consistent SQLite backup that respects WAL state. Do not copy only an actively used database file. Do not run migrations against the colleague's database. Import the complete Stage 1 model, including unresolved diagnostics and source hashes; reconstruct derived indexes rather than copying implementation-specific FTS tables blindly.

Determine actual schema version from the database, migrations, and required columns, not the initial schema SQL file's header. Verify the complete included-file manifest with the producer's hashing and exclusion rules against immutable Git content for publication. A clean checkout or matching HEAD alone does not prove index freshness. Account for relevant configuration, included untracked files, virtual entities, and extraction failures.

**Acceptance:** All fixture facts/evidence survive normalization; database bytes and active producer state are unchanged; stale indexes, unsupported schemas, hash mismatches, new/deleted files, WAL activity, and Unicode ranges are exercised. No known relationship is dropped.

### S2.03 — Publication, revision integrity, and conflict recovery

Branch suffix: `s2-03-publish`.

Connect the CodeGraph importer to Stage 1 publication. Proposed commands:

```sh
graphnest graph import codegraph --repo . --repository-id 123 --dry-run

graphnest graph import codegraph --repo . --repository-id 123 \
  --output ./graphnest-artifact.pb

graphnest graph upload ./graphnest-artifact.pb --repository-id 123
```

Import with `--output` is artifact creation only. Without `--output` or `--dry-run`, successful import may publish, as documented and tested. Support explicit producer replacement with the observed active-generation precondition; no default last-writer-wins switch of analyzers.

Require the artifact source manifest, immutable commit content, and server indexed SHA to agree. Check again when publishing. Dirty/untracked data must never be labeled as a committed snapshot; offer export-only diagnostics or fresh committed-snapshot generation, not an unsafe force flag. GraphNest verifies artifact integrity and scope, but reports publisher-provided freshness evidence honestly.

**Acceptance:** Exact-SHA success; advancing server SHA, missing source coverage, revoked grants, retries, interrupted uploads, generation conflicts, and replacement rejection. A failed attempt leaves the previous graph active. SCIP remains intact.

### S2.04 — Import UX, distribution, and stage gate

Branch suffix: `s2-04-import-gate`.

Provide human and JSON reports with producer/schema/version, repo/commit, freshness checks, accepted counts by kind, unresolved diagnostics, extensions, coverage gaps, artifact hash, server state, and suggested recovery. Add `graphnest doctor` checks that do not mutate indexes or install software implicitly.

Publish the lightweight CLI for supported platforms, documenting SQLite dependencies and checking compatibility with the existing `CGO_ENABLED=0` test/build boundary. A normal server or import-only install must not pull in a local CodeGraph runtime; direct SQLite import does not require the analyzer to be installed or executed. Include checksums, dependency inventory, and existing release-verification mechanisms.

**Stage 2 gate:** Real, pinned CodeGraph indexes pass end to end; existing indexes are reused without mutation/re-indexing; ordinary scoped credentials suffice; dropped data cannot be hidden by success output; installation and recovery instructions work on supported systems. The CLI command surface, supported-format documentation, fixtures, dependencies, and release criteria cover CodeGraph only. No deferred adapter or approval for another analyzer can block this stage.

## 6. Stage 3 — GraphNest CLI feature parity with local CodeGraph

**Outcome:** An engineer installs GraphNest, initializes a project, edits code, asks questions through CLI/MCP/browser, and works offline without needing a separate CodeGraph installation or any GraphNest server.

### S3.01 — Local engine adapter and runtime lifecycle

Branch suffix: `s3-01-engine`.

Introduce a backend-neutral client contract with explicit local CodeGraph and remote GraphNest implementations. Keep the server's domain/API contracts reusable while permitting CodeGraph-specific local analysis behind a tested adapter; this is a local/remote boundary, not a multi-analyzer plugin system. Avoid turning the Go CLI into a generic shell-command proxy.

Manage the pinned CodeGraph runtime under a versioned GraphNest-owned location. Validate runtime/platform compatibility, licenses/notices, integrity, and protocol version. Support explicit setup, adoption, upgrade, rollback, and diagnosis. Supply a small machine-readable bridge where upstream CLI/library coverage requires it. No shell evaluation, arbitrary executable path from repository content, implicit network installation, or runtime download during an ordinary query.

**Acceptance:** A clean supported machine with no separately installed Node.js/CodeGraph can use the packaged local engine; incompatible runtimes fail clearly; query output contains engine identity. Import-only and server installations remain lightweight.

### S3.02 — Project initialization, incremental indexing, and freshness

Branch suffix: `s3-02-workspace`.

Implement `init`, full `index`, `sync`, `status`, safe stale-lock recovery, and watcher/daemon management, matching the pinned CodeGraph behavior through the engine. Keep state in a GraphNest-owned location and explicitly configure producer database paths without changing colleagues' existing indexes.

Handle create/modify/delete/rename, ignored files, branch switches, multiple worktrees, watcher overflow, missed events, shutdown/restart, crash recovery, and reconnect-time reconciliation. Serialize writers. Associate results with immutable index generations and source-content checks; dirty workspace content is valid locally and is never claimed to equal HEAD.

When source changes before indexing catches up, warn about the affected pending files, wait within a defined bound, or return explicitly partial/stale results with current source. Never silently slice current files with old source ranges.

**Acceptance:** Full rebuild and incremental update converge on equivalent facts; watcher/restart tests do not leave stale hidden state; two clients do not corrupt the database; branch changes cannot return another branch's answer as current.

### S3.03 — Complete local CLI query workflows

Branch suffix: `s3-03-local-queries`; split by workflow as needed.

Expose local equivalents of every relevant pinned CLI/library query: search/query, explore, entity/file inspection, file structure, callers, callees, impact, affected tests, paths, type/public-API/navigation analysis, and all other parity-matrix rows. Keep GraphNest's existing remote advantages available through explicit remote selection.

Human output must be usable without JSON filtering; JSON must be stable and include provenance, freshness, and partial-result information. Preserve disambiguation and code snippets. Reuse shared algorithms when equivalent; use the pinned engine's richer implementation where necessary instead of shipping a weaker imitation.

**Acceptance:** Differential tests run the same tasks through CodeGraph, GraphNest local CLI, and GraphNest server for the same clean snapshot. Fact/source parity is strict; ranking/formatting comparison follows the frozen task-based rubric. Dirty-workspace tests compare local behavior only, never against stale remote main.

### S3.04 — Local MCP and explicit agent integration

Branch suffix: `s3-04-local-mcp`.

Provide `graphnest serve --mcp --backend local` and preserve `graphnest-mcp` remote compatibility. Expose high-level exploration plus the explicitly supported lower-level tools. Keep stdout protocol-clean. Use the same freshness, configuration, workspace isolation, and bounds as the CLI.

Add opt-in agent installation/config printing/removal for the upstream supported integration matrix. Make changes idempotent, backed up or reversible, marker-owned, and previewable. Never delete an existing CodeGraph server entry, broaden tool permissions, or rewrite another agent's instructions without explicit selection. Startup alone must not modify agent configuration.

**Acceptance:** Supported agent fixtures round trip through install/uninstall; existing configuration is preserved; multiple workspace sessions remain isolated; interrupted tool calls do not corrupt indexes; no unexpected networking occurs in local-only mode.

### S3.05 — Local browser and saved workflows

Branch suffix: `s3-05-local-browser`.

Expose `graphnest ui` using the Stage 1 UI components against a local backend. Implement equivalent symbol/file views, paths, maps, screens/steps where supported, saved trails, exports, and update/staleness notifications. Local trails remain local unless explicitly exported/published; keep compatible import/export where feasible.

Bind to loopback, validate Host/Origin, prevent path traversal and unsafe rendering, and protect state-changing routes. Support headless/no-browser and read-only modes. Opening the viewer must not initialize or upload a repository as a side effect.

**Acceptance:** Browser tasks match the parity inventory offline; updates follow moved symbols without stale range slicing; read-only mode refuses writes; no local source is exposed on network interfaces or through cross-origin requests.

### S3.06 — Local/remote routing and explicit sharing

Branch suffix: `s3-06-routing`.

Implement explicit `--backend local|remote` with an optional clearly documented `auto` mode. Within a configured local workspace, auto selects local for working-tree questions; it must not silently send those queries or source to a server when local data is missing. Outside a local workspace, require an explicit/configured remote target and label its revision.

Use Stage 2 publication for clean committed snapshots from GraphNest-managed indexing. Never automatically upload on file changes. Compare source manifests and content hashes, not just matching symbol names, before reusing any remote context. Do not merge local and remote graphs or assign an exact-SHA label to a dirty generation.

**Acceptance:** Dirty/private/local-only source stays local; offline commands make no remote calls; remote failures do not change query meaning; backend selection is inspectable; clean-snapshot publication reproduces Stage 2's validated artifact.

### S3.07 — Distribution, performance, migration, and final gate

Branch suffix: `s3-07-local-gate`.

Package the local runtime without requiring user-managed language runtimes, server databases, Docker, or separate CodeGraph installation. Cover supported macOS, Linux, and Windows architectures actually backed by validated artifacts. Include all transitive notices/dependencies, integrity verification, explicit upgrades/rollback, and enterprise offline bootstrap. Disable helper telemetry and auto-updates in local-only operation; test the network behavior rather than relying on documentation.

Provide adoption/migration without deleting existing CodeGraph data or overwriting its integration. Run full-inventory language/framework smoke coverage and representative deep fixtures against native/portable engines as applicable. Measure startup, warm-query p50/p95, full/incremental indexing, memory, disk, and answer quality on fixed corpora.

**Stage 3 gate:** Every local parity row passes. With network access blocked after explicit bootstrap, initialize and query a previously unindexed fixture, modify/delete/rename files, reconnect MCP, inspect the browser, restore a trail, and recover from a crash. No separate CodeGraph installation or server is used. The supported platform and runtime matrix is demonstrated, not inferred from a successful build on one laptop.

## 7. Cross-cutting validation and completion policy

Run focused tests for each layer and full relevant suites at each stage gate. Existing inspected targets include: [S13]

```sh
make build
make fmt lint staticcheck govulncheck
make test test-race
make integration e2e
make openapi-check tools-check
make ui-smoke
make compose-test helm-lint helm-test
```

Run scanner/image/release checks when those components change. These commands may need Docker, pinned tooling, network bootstrap, and other documented prerequisites. Report unavailable checks and their cause, not a fictional pass. `make fmt` checks formatting; run `gofmt` to fix files before rerunning it.

Add proposed dedicated targets such as `parity-server`, `parity-import`, and `parity-local`, with deterministic fixtures and a documented real-producer refresh path. Do not claim these targets already exist.

Comparison rules:

- **Facts/evidence:** zero unexplained loss for known CodeGraph entities, relationships, occurrences, metadata, and required source. Normalize declared identity/coordinate representations only; never normalize away errors.
- **Queries:** compare required answers, reachable sets, supporting paths, and source context at equivalent budgets. Do not require identical SQLite/PostgreSQL ranking scores or presentation order.
- **Security:** test repository isolation, current authorization, publisher grants, malicious metadata, unsafe paths, token exposure, request limits, local/network boundaries, and revoked access to saved results.
- **Freshness:** test producer writes during export, worktree changes during queries, advancing server commits during upload, and reconnect reconciliation.
- **Performance:** freeze environment, corpus, and budgets in S1.01. As proposed starting gates, preserve existing GraphNest warm-query p95 within 10% and keep GraphNest-local warm-query p95 within 1.25x pinned CodeGraph on the same tasks. These are design budgets, not measured results; any adjustment needs recorded measurements and owner approval, not an agent silently relaxing a failed test.

Every stage ends with an updated parity matrix, actual stack/PR URLs, commands and results, measured regressions, compatibility notes, and remaining gaps. “Implemented,” “validated,” and “released” are separate states. Native stack submission does not imply approval or merge.

## 8. Codex launch instruction

Read this plan and the repository instructions, revalidate the pinned baselines, and execute Stage 1 in dependency order using GitHub's native stacked PRs. Create the living execution record in S1.01. Keep each layer testable and reviewable, submit real draft PRs through `gh stack`, and verify remote stack membership. Do not merge without owner authorization. When Stage 1 has passed its gate and landed, execute Stage 2, then Stage 3, with the same rules. Keep all three stages CodeGraph-only; do not restore superseded integrations as optional tasks or compatibility stubs. Do not replace full CodeGraph parity with a smaller feature list or label unverified behavior complete.

## References

The following are primary sources inspected for this plan. Revalidate their contents at implementation time; source baselines above define the comparison rather than an unpinned moving branch.

[S1] GraphNest README: `https://github.com/balcsida/graphnest/blob/49e77d1bcd7f4be7198d8368e58e062778b68235/README.md`

[S2] GraphNest contributor instructions: `https://github.com/balcsida/graphnest/blob/49e77d1bcd7f4be7198d8368e58e062778b68235/AGENTS.md`

[S3] GraphNest artifact model: `https://github.com/balcsida/graphnest/blob/49e77d1bcd7f4be7198d8368e58e062778b68235/internal/graphartifact/model.go`

[S4] GraphNest query service and storage implementation: `https://github.com/balcsida/graphnest/blob/49e77d1bcd7f4be7198d8368e58e062778b68235/internal/graphquery/service.go`; `https://github.com/balcsida/graphnest/blob/49e77d1bcd7f4be7198d8368e58e062778b68235/internal/postgres/graph_query.go`

[S5] GraphNest external ingestion: `https://github.com/balcsida/graphnest/blob/49e77d1bcd7f4be7198d8368e58e062778b68235/internal/graphingest/service.go`

[S6] CodeGraph graph types: `https://github.com/colbymchenry/codegraph/blob/b9ca4b7981116909900368cc1686a1074cd4d4c1/src/types.ts`

[S7] CodeGraph SQLite schema: `https://github.com/colbymchenry/codegraph/blob/b9ca4b7981116909900368cc1686a1074cd4d4c1/src/db/schema.sql`

[S8] CodeGraph README, installation and CLI reference: `https://github.com/colbymchenry/codegraph/blob/b9ca4b7981116909900368cc1686a1074cd4d4c1/README.md`

[S9] CodeGraph README, auto-sync/browser and framework sections: `https://github.com/colbymchenry/codegraph/blob/b9ca4b7981116909900368cc1686a1074cd4d4c1/README.md#read-your-graph-in-the-browser`

[S10] GitHub native stacked PRs: `https://docs.github.com/en/pull-requests/get-started/about-stacked-prs`

[S11] GitHub stacked-PR CLI reference: `https://docs.github.com/en/pull-requests/reference/stacked-prs-cli-commands`

[S12] GitHub stacked-PR quickstart: `https://docs.github.com/en/pull-requests/get-started/stacked-prs-quickstart`

[S13] GraphNest Makefile: `https://github.com/balcsida/graphnest/blob/49e77d1bcd7f4be7198d8368e58e062778b68235/Makefile`
