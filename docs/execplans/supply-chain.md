# Dependencies & Licenses execution plan

Living plan for the native supply-chain inventory module accepted in
[ADR-0017](../adr/0017-supply-chain-inventory.md). Implementation, validation,
draft publication, and release are separate states. This file records what is
designed, what is implemented, what was verified, and the exact next task.

## Baseline

- Starting commit: `331fa42` (`main`, tag `v0.5.0` merge) on 2026-09-22.
- Working tree: clean except a pre-existing modified `go.work.sum` in the
  original checkout, preserved untouched. Work happens in the isolated
  worktree `../graphnest-supplychain` on `feat/supply-chain/*` branches
  tracked as a local native stack (`gh stack`, trunk `main`).
- Baseline checks on `331fa42`: `go build ./cmd/...` passes;
  `CGO_ENABLED=0 go test ./...` passes (all packages). PostgreSQL is available
  locally through `deploy/compose/compose.yml` for integration tags.
- Prior art: no open or closed PR/issue in `balcsida/graphnest` addresses SBOM
  inventory, licenses, or supply chain (checked 2026-09-22).

## Starting-point assumptions verified against the checkout

| Brief assumption | Checkout state | Adaptation |
| --- | --- | --- |
| `internal/githubapp/dependency.go` keeps limited identifiers and collapses 403/404 to unavailable | Confirmed: `DependencySBOM` decodes only SPDXID/PURL/relationships and returns `available=false` on 403/404 | Add a lossless raw reader (`DependencySBOMDocument`) that returns bytes, status, and rate-limit headers; keep `DependencySBOM` as an adapter over the same endpoint for SCIP callers |
| `scipgraph.RefreshGitHubDependencies` projects SBOM into package mappings | Confirmed: writes `repository_packages` with `source='github'`, relations `provides`/`depends_on` | Keep; add a projection from published snapshots that writes the same rows (`source='github'`), leaving `source='manual'` untouched |
| `internal/httpapi/scip.go` has the refresh HTTP route | Confirmed: `POST /v1/scip/dependencies/github` (administrator-only) | Unchanged; new routes live under `/v1/supply-chain` |
| Durable queue in `internal/postgres` | Confirmed: `index_jobs` and `graph_jobs` use `for update skip locked` leases with `lease_owner`/`lease_expires_at` fencing | Mirror the pattern in `supply_chain_jobs` |
| Embedded same-origin UI with Go/DOM contract tests | Confirmed: `index.html`/`admin.html`, CSP hash of a single inline script/style, Node `vm` DOM tests | Add `supply-chain.html` served at `/supply-chain`, same CSP scheme, same test conventions |
| Authorization conventions | `authz.Postgres` + `repository.Service` scope through `AuthorizedRepositories`/`AllAuthorizedRepositories`, installation boundary, numeric IDs; delegation-only tokens denied everywhere | Every supply-chain read resolves repositories through `authz.Authorizer`; writes require administrator (bootstrap) |
| Config | `internal/config` env-driven, `GRAPHNEST_*`, secret files | Add `GRAPHNEST_SUPPLY_CHAIN*` settings, disabled by default |
| Session CSRF | `RequestAuthenticator` requires `Origin == PublicOrigin` on unsafe methods for cookie sessions | Reused as-is for `POST /v1/supply-chain/...` |

GHES facts confirmed from the 3.20 documentation (2026-09-22): the SBOM
endpoint needs `Contents: read`, returns 200/403/404, has no ref/commit
parameter, and GHES "does not retrieve license information for dependencies".

## Scope and non-goals

In scope: GHES SBOM collection into preserved snapshots, repository inventory
UI, original-document download, exact-version license evidence (npm, NuGet,
Maven), portfolio UI and read APIs, SPDX 2.3 / CycloneDX 1.6 JSON imports,
review workflow with versioned policies, read-only MCP tools, operations.

Out of scope: vulnerability management, a custom source-license scanner,
automatic scanner execution, SaaS/LLM calls, a frontend framework, another
database, Code Insight parity claims, migrating proprietary audit history.

## Logical entities and physical schema

Migration `033_supply_chain.sql` (Milestone 1). Later milestones add
`034_supply_chain_license.sql`, `035_supply_chain_import.sql`,
`036_supply_chain_review.sql`.

```text
supply_chain_documents           byte-preserving original documents
  id, sha256 (unique), format ('spdx-2.3-json' | 'cyclonedx-1.6-json'),
  media_type, byte_size, body bytea, first_seen_at
supply_chain_snapshots           immutable published inventories
  id, repository_id -> repositories(id) cascade, document_id -> documents,
  producer ('github' | 'import'), subject ('source' | 'artifact'),
  stream_key text (e.g. 'github:source', 'import:source:<label>'),
  collected_at (GraphNest), created_at_claimed (producer creationInfo.created),
  producer_tool text, document_namespace text, document_name text,
  spdx_version text, data_license text,
  subject_revision char(40) null, subject_assurance ('unknown' | 'producer_asserted' | 'verified'),
  root_spdx_ids text[], parser_version int, component_count int, edge_count int,
  warning_count int, warnings jsonb, published_at
supply_chain_components          component occurrences (document-scoped identity)
  id, snapshot_id -> snapshots cascade, ordinal int, element_id text (SPDXID/bom-ref),
  name text, version text null, purl text null, ecosystem text null (from purl type),
  purl_namespace/purl_name/purl_version text null, qualifiers jsonb,
  license_declared_raw, license_concluded_raw text null,
  download_location text null, supplier text null, checksums jsonb,
  is_root bool, extra jsonb (unknown nonessential fields kept in document body, not here)
  unique (snapshot_id, element_id)
supply_chain_relationships       preserved edges with direction
  id, snapshot_id cascade, from_element text, relationship text (SPDX type, uppercase),
  to_element text, resolved bool (both ends exist in the snapshot)
supply_chain_collections         one row per attempt (success or failure)
  id, repository_id cascade, job_id null, producer, stream_key, started_at, finished_at,
  outcome ('published' | 'unchanged' | 'unavailable' | 'forbidden' | 'rate_limited' |
           'not_found' | 'malformed' | 'too_large' | 'transient' | 'cancelled' | 'error'),
  http_status int null, retry_after_seconds int null, snapshot_id null, document_id null,
  error_code text, message text (safe diagnostic)
supply_chain_streams             current-stream pointers
  repository_id cascade, stream_key, latest_snapshot_id null, latest_collection_id,
  last_success_at, last_attempt_at, last_outcome, opt_out bool default false
  primary key (repository_id, stream_key)
supply_chain_jobs                durable refresh jobs
  id, repository_id cascade, stream_key, reason ('scheduled' | 'manual' | 'webhook'),
  state ('queued' | 'running' | 'succeeded' | 'failed' | 'cancelled' | 'superseded'),
  attempt, max_attempts, run_after, lease_owner, lease_expires_at, fence bigint,
  requested_by text, created_at, updated_at, error_code
  unique partial: one queued and one running per (repository_id, stream_key)
```

Identity and deduplication rules:

- A document is identified by SHA-256 of the exact bytes stored. Identical
  re-fetches reuse `supply_chain_documents` but still insert a collection row
  with outcome `unchanged`, and update `streams.last_attempt_at`.
- A snapshot is identified by its row ID. Components are identified within a
  snapshot by their document element ID; duplicates within a document are a
  diagnostic and the later occurrence gets a suffixed `element_id` so nothing
  is dropped.
- No cross-repository component table exists in Milestone 1; portfolio
  queries group by `(ecosystem, purl_name, version)` at query time over
  authorized latest snapshots. A later milestone may add a materialized
  coordinate table if measured plans require it.
- `collected_at` is GraphNest's clock; `created_at_claimed` is the producer's
  `creationInfo.created` and is displayed as a claim.
- Subject revision is `null`/`unknown` for GHES snapshots. HEAD is never copied
  into it.

Coverage denominators: "repositories with inventory" counts authorized
repositories whose selected stream has a latest snapshot; "stale" means the
latest successful collection is older than the configured interval × 2;
"failed" means the last attempt outcome is not `published`/`unchanged` while
an older snapshot may still be served.

Retention (Milestone 7): keep the latest N snapshots per stream plus any
snapshot referenced by a review; documents are deleted only when no snapshot
references them.

## Job lifecycle

1. Scheduler (server periodic loop, interval `GRAPHNEST_SUPPLY_CHAIN_INTERVAL`,
   default 24h, jitter up to 10%) enqueues `scheduled` jobs for enabled,
   non-archived, non-opted-out repositories in active installations whose
   stream is due. Enqueue is idempotent per `(repository_id, stream_key)`.
2. Manual refresh (`POST /v1/supply-chain/repositories/{id}/refresh`) enqueues
   a `manual` job with priority; when a job is already queued it returns the
   existing job (202 either way).
3. Worker leases with `for update skip locked`, lease 2 minutes renewed during
   collection, `fence` increments per lease. Publication (`streams` update and
   `snapshots` insert) is a single transaction guarded by
   `lease_owner = $owner and fence = $fence`, so a stale worker cannot
   overwrite a newer publication.
4. Retry with exponential backoff (1m, 4m, 16m, …) up to `max_attempts` (5);
   `rate_limited` honors `Retry-After` when present. Cancellation is a state
   transition checked before publication. Expired leases are reaped to
   `queued` (or `failed` after `max_attempts`) on worker start and each loop.
5. Every attempt inserts a `collections` row; failures never touch
   `latest_snapshot_id`.
6. After publication, the worker projects `repository_packages` rows with
   `source='github'` (relations `provides` from roots, `depends_on` from
   `DEPENDS_ON` edges) in its own transaction; a projection error is recorded
   on the collection row (`error_code='projection_failed'`) and visible in the
   job/status response, and the snapshot remains published.

## API (Milestone 1; designed in `docs/openapi.yaml` before code)

| Method and path | Auth | Purpose |
| --- | --- | --- |
| `GET /v1/supply-chain/repositories/{id}` | any authorized principal for the repository | Stream status: latest snapshot summary, last collection outcome, freshness, job state |
| `GET /v1/supply-chain/repositories/{id}/components?cursor=&limit=` | same | Server-paginated component occurrences of the latest snapshot for the selected stream (`stream` query, default `github:source`) |
| `GET /v1/supply-chain/snapshots/{snapshot_id}/document` | same, re-authorized at retrieval | Original document bytes with `Content-Type`, `Content-Disposition`, `X-Content-SHA256` |
| `GET /v1/supply-chain/repositories/{id}/collections?cursor=` | same | Recent collection attempts |
| `POST /v1/supply-chain/repositories/{id}/refresh` | administrator (bootstrap) | Enqueue a bounded refresh job |
| `GET /v1/supply-chain/jobs/{id}` | authorized for the job's repository | Job status |

Milestone 3 adds portfolio routes (`/v1/supply-chain/overview`,
`/v1/supply-chain/components`, `/v1/supply-chain/components/{key}`,
`/v1/supply-chain/repositories/{id}/export.csv`,
`/v1/supply-chain/snapshots/{id}/compare/{other}`); Milestone 4 adds
`POST /v1/supply-chain/imports`; Milestone 5 adds review routes.

## Authorization matrix

| Principal | Read inventory | Download document | Manual refresh | Import | Review | Policy admin |
| --- | --- | --- | --- | --- | --- | --- |
| Static user token (static mode) | n/a (module requires durable mode) | – | – | – | – | – |
| Durable session / OIDC / GitHub OAuth user | repositories in grants | same | no | no (M4: repository-scoped capability) | no (M5: review capability) | no |
| Scoped API token | repository ceiling | same | no | no | no | no |
| Administrator session | all active repositories | same | yes | yes | yes | yes |
| Administrator API token | repository ceiling | same | yes within ceiling | yes within ceiling | yes | no |
| Delegation-only token | denied (existing confinement) | denied | denied | denied | denied | denied |
| OAuth access token (MCP) | grants | – (MCP has no download) | no | no | no | no |

All reads select repositories through `authz.Authorizer` before any query
touches supply-chain tables; snapshot and job IDs are always joined back to
`repositories` and filtered by the authorized set, so an unauthorized ID
yields `404 not_found` and never a distinguishable error.

## Configuration (defaults)

| Variable | Default | Meaning |
| --- | --- | --- |
| `GRAPHNEST_SUPPLY_CHAIN` | `false` | Enable the module (routes, worker, scheduler, UI page) |
| `GRAPHNEST_SUPPLY_CHAIN_INTERVAL` | `24h` | Scheduled refresh interval per repository stream |
| `GRAPHNEST_SUPPLY_CHAIN_WORKERS` | `1` | Concurrent collection jobs per server process |
| `GRAPHNEST_SUPPLY_CHAIN_MAX_DOCUMENT_BYTES` | `16777216` (16 MiB) | Maximum accepted SBOM document |
| `GRAPHNEST_SUPPLY_CHAIN_MAX_COMPONENTS` | `50000` | Maximum components per snapshot |
| `GRAPHNEST_SUPPLY_CHAIN_RETAIN_SNAPSHOTS` | `10` | Snapshots kept per stream (M7) |

## Supported formats and ecosystems

- Milestone 1: GHES SPDX 2.3 JSON (`spdxVersion: SPDX-2.3`). PURL types are
  recorded as-is; `ecosystem` is the lowercase PURL type.
- Milestone 2: license resolvers for `npm`, `nuget`, `maven` PURL types only.
- Milestone 4: SPDX 2.3 JSON and CycloneDX 1.6 JSON uploads.

## Milestones, layers, and file mapping

Each milestone is one or more stack layers; each layer is independently
buildable and tested.

### M0 — design (this layer, `feat/supply-chain/m0-design`)

- `docs/adr/0017-supply-chain-inventory.md`, `docs/adr/README.md`
- `docs/execplans/supply-chain.md` (this file)

### M1 — GHES inventory vertical slice

Layer `feat/supply-chain/m1-storage`:
- `internal/postgres/migrations/033_supply_chain.sql`
- `internal/supplychain/spdx.go` — SPDX 2.3 JSON normalization, root detection, warnings
- `internal/supplychain/model.go` — snapshot/component/relationship/collection types
- `internal/postgres/supply_chain.go` (+ integration tests) — documents, snapshots, publication, streams, jobs (lease/fence/backoff/reap), collections, reads
- `internal/githubapp/dependency.go` — `DependencySBOMDocument` raw reader with typed outcome; existing `DependencySBOM` adapts over it

Layer `feat/supply-chain/m1-service`:
- `internal/supplychain/service.go` — authorized reads, refresh enqueue, job status
- `internal/supplychain/collector.go` — worker loop, outcome classification, projection into `repository_packages`
- `internal/supplychain/scheduler.go` — due-stream enqueue with jitter
- `internal/config/config.go` — `SupplyChain` settings
- `internal/observability/metrics.go` — bounded collection/queue metrics
- `internal/httpapi/supply_chain.go` — routes above; `docs/openapi.yaml`
- `cmd/graphnest-server/main.go` — gated wiring

Layer `feat/supply-chain/m1-ui`:
- `internal/webui/supply-chain.html` + `handler.go` route `/supply-chain`
- DOM contract test (`supply_chain_dom_test.mjs`), Go contract tests
- `README.md`, `docs/architecture.md`, `docs/operations.md`, `docs/threat-model.md`, Compose/Helm env, `CHANGELOG.md`

### M2 — license enrichment (`feat/supply-chain/m2-*`)

- `internal/supplychain/spdxexpr` — bounded SPDX expression parser, pinned license list
- `internal/supplychain/resolve/{npm,nuget,maven}` — exact-version resolvers behind configured routes
- `034_supply_chain_license.sql` — evidence and assessments
- Evidence detail endpoint and conflict detection

### M3 — portfolio UI/read APIs; M4 — imports; M5 — reviews; M6 — MCP; M7 — operations

Detailed task lists are appended when each milestone starts.

## Decision log

- 2026-09-22 D1: Module gated by `GRAPHNEST_SUPPLY_CHAIN` and available only
  in durable (PostgreSQL) mode; static mode has no GitHub App and no queue.
- 2026-09-22 D2: The collection worker runs inside `graphnest-server` (like
  reconciliation) rather than the indexer, so inventory does not share the
  indexer's Zoekt/archive lifecycle and cannot block lexical indexing. A
  separate binary can be split out later without schema changes.
- 2026-09-22 D3: Store the SPDX member bytes exactly as received (the `sbom`
  JSON value sliced with `json.RawMessage`), plus the HTTP status and selected
  headers on the collection row; the outer envelope is not retained.
- 2026-09-22 D4: Reuse `repository_packages` (`source='github'`) as the
  compatibility projection; the projection is rebuilt from the published
  snapshot, never from the legacy flattened reader, and `source='manual'` rows
  are never touched.
- 2026-09-22 D5: Writes (manual refresh) are administrator-only as bootstrap;
  M4/M5 add repository-scoped capabilities.
- 2026-09-22 D6: A 403 with rate-limit headers is `rate_limited`; a plain 403
  is `forbidden` and a 404 is `not_found`. Neither is reported as "dependency
  graph disabled"; the UI phrases them as "GitHub returned 403/404 for the SBOM
  export (dependency graph disabled, no access, or unsupported)".
- 2026-09-22 D7: Component identity is document-scoped; there is no global
  component table in M1. Portfolio queries in M3 aggregate at query time.
- 2026-09-22 D8: The UI is a new embedded page `/supply-chain` reusing the
  console shell, tokens, and auth flow, so `index.html`/`admin.html` change
  only by adding a navigation link.

## Progress

- 2026-09-22: Baseline recorded; ADR-0017 and this plan created.
- 2026-09-22: **M1 complete** as four local stack layers on top of M0:
  - `feat/supply-chain/m1-storage` (`76dcf7c`): migration 033; SPDX 2.3 JSON
    normalizer with root detection through `documentDescribes` and
    `DESCRIBES`/`DESCRIBED_BY`, bounded warnings, PURL-less/version-less
    occurrences, unresolved edges kept as diagnostics; lossless
    `githubapp.DependencySBOMDocument` (verbatim SPDX member, status,
    rate-limit headers, bounded `Retry-After`); PostgreSQL store with leased
    and fenced jobs, atomic publication, unchanged-document detection, failure
    recording that never moves the latest snapshot, reaping, cancellation,
    opt-out, scoped reads, and the `repository_packages` projection. Legacy
    `DependencySBOM` and SCIP callers keep their contracts.
  - `feat/supply-chain/m1-service` (`6b25c6a`): authorized service (status
    with independent collection/freshness/coverage/enrichment states,
    paginated components with derived scope, snapshots, collections, document
    download re-authorized at retrieval, manual refresh that only enqueues,
    job status), collector with typed outcome classification and projection,
    jittered scheduler, `GRAPHNEST_SUPPLY_CHAIN*` config, bounded metrics,
    `/v1/supply-chain` routes designed in OpenAPI, gated server wiring.
  - `docs(supply-chain)` (`8d59897`): README, operations, architecture, threat
    model, Compose durable env, Helm values/schema/ConfigMap/render tests,
    CHANGELOG.
  - `feat(webui)` (`ca1a930`, implemented by a delegated worker and reviewed):
    embedded `/supply-chain` page with the console shell, hash-based CSP,
    session-then-bearer auth, text-node rendering, Go byte-contract and Node
    DOM tests; navigation links in `index.html`/`admin.html`.
  - `test(supply-chain)` (`e0620d5`): fake GHES → scheduler → collector →
    PostgreSQL → REST vertical slice, including no-indexed-SHA proof,
    rate-limit/403 outcomes, retained inventory behind a failed refresh,
    cross-installation isolation, and projection.
  - `docs(supply-chain)` (`90a1413`): light/dark screenshots of the real page
    rendered in Chromium against a stubbed API (`docs/images/supply-chain-*.png`).
- 2026-09-22: **M2 complete** (`feat/supply-chain/m2-license-core`): bounded
  SPDX expression parser over the embedded SPDX License List 3.27.0 (fuzzed);
  npm/NuGet/Maven resolvers behind explicitly configured routes with pinned
  origin/base path, private-address denial, decompression bounds, credential
  isolation, and no public fallback; immutable evidence rows (duplicates are
  new observations; outages keep earlier resolved evidence); deterministic
  per-occurrence assessments with conflict detection; enrichment worker
  queued from publication; evidence detail route; UI license column and
  evidence panel; Helm route values/secrets; docs.
- 2026-09-22: **M3 complete** (`feat/supply-chain/m3-portfolio`): overview
  with named denominators, keyset-paginated unique coordinates with
  filter-bound cursors, facets, coordinate detail, CSV export with provenance
  and formula-safe cells, snapshot comparison separating component/license/
  edge changes from metadata; Overview/Components/comparison UI views with
  screenshots in both themes.
- 2026-09-22: **M4 complete** (`feat/supply-chain/m4-imports`): SPDX 2.3 JSON
  and CycloneDX 1.6 JSON imports into `import:<subject>:<label>` streams with
  content-based format detection, explicit rejection of other versions,
  byte-preserving storage, uploader recorded apart from the claimed producer,
  producer-asserted subject binding, idempotency, quotas, upload grants;
  derived SPDX export naming GraphNest as creator and linking the original.
- 2026-09-22: **M5 complete** (`feat/supply-chain/m5-review`): tree-walking
  policy evaluator with a truth table (AND/OR/WITH/unknown/or-later), labelled
  example policy, admin-only versioned policies that refuse auto-approval of
  unknowns, review queue, human conclusions as immutable evidence,
  approve/reject/exception decisions with optimistic concurrency on the
  evidence fingerprint, expiry and stale detection, repository-scoped review
  grants, append-only audit trail, background re-evaluation retaining history.
- 2026-09-22: **M6 complete** (`feat/supply-chain/m6-mcp`): read-only MCP tools
  `search_dependency_inventory`, `find_component_repositories`,
  `inspect_component_license` over the same services as REST, with an
  integration test proving scope agreement and no cross-installation leaks.
- 2026-09-22: **M7 partially complete** (`feat/supply-chain/m7-operations`):
  review-preserving snapshot retention and history pruning on the scheduler
  tick, enrichment queue-depth metrics, retention configuration in
  config/Compose/Helm, operations/README/CHANGELOG coverage, pilot comparison
  checklist (`docs/supply-chain-pilot-checklist.md`), and recorded query plans
  on a 50,000-occurrence synthetic dataset (`docs/supply-chain-query-plans.md`).
  Remaining M7 items are listed under gaps.
- Next task: see "Remaining gaps and next task".

## Validation results

Final worktree head `b15bed4` (before this note) (`feat/supply-chain/m7-operations`), 2026-09-22,
macOS arm64, Go 1.27.1, PostgreSQL 18.6 via Compose (reached at the OrbStack
container address because `docker compose port` reports `invalid IP:0` here),
Helm 4.x, Node 26, Playwright 1.62.1 with locally installed Chromium.

| Gate | Result |
| --- | --- |
| `make build` | pass |
| `make fmt lint` | pass |
| `make staticcheck` | pass |
| `make govulncheck` | pass (same as `main`: 0 called vulnerabilities, 1 uncalled module-level finding pre-existing) |
| `CGO_ENABLED=0 go test ./...` | pass (2318 tests, 48 packages) |
| `make test-race` | pass |
| `make postgres-test` (all six integration packages, including every new `TestSupplyChain*`) | pass |
| `make e2e-test` (Go e2e with real Zoekt binaries and PostgreSQL, plus scanner e2e) | pass (run twice) |
| `make openapi-check` | pass with Homebrew Ruby 4.0.7 (`/opt/homebrew/opt/ruby/bin/ruby scripts/check_openapi.rb`); the system Ruby 2.6 lacks `filter_map` and fails identically on `main` |
| `make compose-test` | pass |
| `make helm-lint helm-test` | pass |
| `make brand-check` | pass |
| `make makefile-test abi-test tools-check` | pass |
| `make scanner-build scanner-test` | pass |
| `make parity-reference` | pass |
| `test/smoke/public_ui.sh` (existing Playwright smoke) | pass |
| `test/smoke/supply-chain-screenshots.mjs` (renders repository, overview, components views; 6 PNGs) | pass |
| Fuzz: `FuzzParse` (45s), `FuzzNormalizeSPDX23` (20s), `FuzzNormalizeCycloneDX16` (20s) | pass, no crashers |
| Per-layer `go build`/`go vet`/`go test` at every stack boundary | pass |
| `make image image-test`, `make ui-smoke` via Make (needs `make tools` rebuild) | **not run** (image build not attempted in this pass; the smoke script itself passed) |
| Live GHES / live registries | **not run**; all GitHub and registry behavior is exercised against fixtures and fake servers |

Commit signing: `277730e`, `76dcf7c`, `6b25c6a` are SSH-signed. The 1Password
SSH agent began refusing sign operations mid-session ("agent refused
operation"), so every later commit is unsigned. Before publication, re-sign
with `git rebase --exec 'git commit --amend --no-edit -S' 331fa42` (from the
worktree, once the agent accepts operations) and re-point the stack branches.

## Remaining gaps and next task

Not implemented (honest scope boundaries):

- M7: no bounded fuzz target yet for the registry URL/redirect path beyond
  the unit cases; no hostile-archive test because no archive is ever
  unpacked (NuGet reads the nuspec, never the nupkg); `make image` was not
  built; the Compose durable profile documents but does not template the
  `GRAPHNEST_SUPPLY_CHAIN_REGISTRY_*` variables (they pass through the
  environment); no webhook-driven refresh (`reason='webhook'` reserved;
  periodic reconciliation covers late dependency-graph updates); no
  Prometheus counter for unknown/conflicting assessments (available through
  the overview API instead).
- M3/M5 UI: the review queue, conclusion/decision forms, and policy
  administration have REST routes and tests but no browser view yet; the
  component detail view does not yet show review history inline.
- M6: exact code-usage joins from an occurrence to indexed references are not
  offered; the UI and MCP state that a dependency path is not a call graph and
  GHES snapshots are unbound observations. A later layer may add clearly
  labelled exploratory search links when a matching indexed revision exists.
- M2: Go, PyPI, Cargo, and other ecosystems have no resolver; their
  components stay at producer declarations or imported evidence, as designed.

Exact next task: add the review UI (queue table, conclusion and decision
forms echoing the `basis` fingerprint, history panel, policy list) to
`internal/webui/supply-chain.html` with DOM/contract tests and screenshots,
on a new layer above `feat/supply-chain/m7-operations`; then run
`make image image-test` and record results here.

Nothing is published; no PR exists for this work. Suggested PR titles in
stack order (each layer depends on the one before):

1. `docs(supply-chain): accept ADR-0017 and execution plan` (m0-design)
2. `feat(supply-chain): preserve GHES SBOM observations as immutable snapshots` (m1-storage)
3. `feat(supply-chain): inventory service, collector, and REST routes` (m1-service)
4. `feat(webui): Dependencies & Licenses inventory page` (m1-ui; includes docs, deployment wiring, integration test, screenshots)
5. `feat(supply-chain): exact-version license evidence and assessments` (m2-license-core)
6. `feat(supply-chain): portfolio APIs and views` (m3-portfolio)
7. `feat(supply-chain): SPDX and CycloneDX imports with derived export` (m4-imports)
8. `feat(supply-chain): policies and review workflow` (m5-review)
9. `feat(mcp): read-only dependency inventory tools` (m6-mcp)
10. `feat(supply-chain): retention, operations docs, and query-plan evidence` (m7-operations)
