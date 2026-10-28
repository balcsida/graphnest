# Design: advisory exposure checks

See `proposal.md` for the motivation and `specs/` for the required behaviour.
This document records how it is built and why.

## Context

- **Inventory today** (ADR-0017, `docs/execplans/supply-chain.md`): immutable
  snapshots per repository *stream* (`supply_chain_streams`, primary key
  `(repository_id, stream_key)`), today `github:source` (GHES dependency-graph
  SBOM, collected by a server-side worker) and `import:<subject>:<label>`
  (uploaded SPDX/CycloneDX). Publication is one fenced transaction
  (`postgres.Store.PublishSupplyChainSnapshot`). It already works without a
  job lease (`JobID == nil`, as imports do), and so does
  `RecordSupplyChainFailure`. Components carry purl fields but no file
  locations; the CycloneDX normalizer drops `evidence` with the warning
  `evidence_not_carried`.
- **Indexer** (`internal/indexer/worker.go`): claims a job, downloads the
  exact-SHA tree into a job-scoped workspace (`Snapshot.Root`), builds Zoekt
  shards, waits for visibility, calls `PublishIndex`, then runs the optional
  `Enricher` on the same workspace and calls `CompleteIndex`. Enrichment
  failure is recorded as a status and never fails the job. The indexer opens
  the same PostgreSQL store but its config (`config.LoadIndexer`) does not
  read `GRAPHNEST_SUPPLY_CHAIN`.
- **Server loops** (`cmd/graphnest-server/main.go`): when the module is
  enabled, the server runs the SBOM scheduler and collectors, license
  enrichment, and `startPolicyEvaluation`, which re-evaluates pending review
  work every minute. That loop is the pattern for advisory re-checks.
- **Review module** (`internal/supplychain/review`): append-only decisions with
  expiring exceptions, optimistic concurrency through a `basis` fingerprint
  (`409 stale_basis`), and a repository-scoped review grant checked with
  `Store.ReviewAllowed`. Exceptions here reuse those rules.
- **Search** (`internal/search`, `internal/zoekt`): results are capped at 100
  matches, 256 KiB, and 5 s. The Zoekt client turns HTTP 400 into a bare
  `ErrInvalidQuery`, discarding Zoekt's message; MCP maps it to the fixed text
  "search query is invalid" and REST to "request is invalid". Verified against
  the pinned Zoekt build: `FROM node:[0-9 ]` returns `400 {"Error":"error
  parsing regexp: missing closing ]: \`[0-9\`"}`, while `(?i)brotli`,
  `(?i)BROTLI`, `(?i:brotli)` and `"(?i)brotli"` all return 200 with no
  matches against a file containing `Brotli`. Case flags break matching
  without an error; `case:no BROTLI` is the working form. Non-capturing
  groups such as `(?:22|20)` work.
- **GitHub App** permissions today: `Contents: read` (plus the implicit
  `Metadata: read`). Verified in the GitHub.com and GHES 3.20 permission
  tables: `GET /repos/{o}/{r}/collaborators` needs Metadata, `GET
  /repos/{o}/{r}/teams` needs Administration, `GET /repos/{o}/{r}/pulls` and
  pull-request search results need Pull requests, and branches and
  `git/matching-refs` need Contents.
- **OSV** (schema 1.9.2) covers language packages (PyPI, npm, Go, NuGet, Maven
  …) but has no ecosystem for container image tags or for Node.js, Bun, or
  Envoy releases, and no notion of EOL lines or floating tags.

## Goals / Non-Goals

**Goals**

- One advisory, pasted once, yields a classified, evidence-backed list of every
  exposed repository the caller can read, kept current, with a shrinking
  history.
- Runtime and image pins become first-class inventory, bound to the exact
  indexed commit.
- No guessing: anything not resolvable from the commit is reported as
  unresolved, and missing inventory is reported as not checked.

**Non-Goals**

- A vulnerability database. GraphNest fetches no advisory feeds; an
  administrator supplies each advisory.
- Opening fix pull requests. Follow-up: an owner-triggered flow that checks
  push access and consent.
- Scanning running images, registries, or clusters. Reports say "default
  branches only".
- Detecting vendored *source* copies (rare in practice; `search_code` remains
  the tool) and OS packages inside images (needs image SBOM imports and distro
  version ordering).
- Python runtime pins, and CI systems other than GitHub Actions beyond their
  `image:` keys. Follow-ups.
- Pagination for `search_code` (see D11).

**Anti-goals** (failure even if it works): silently reporting a repository as
safe because inventory is missing; letting counts reveal repositories the
caller cannot read; inventing versions; blocking or failing lexical indexing;
storing secrets read from `.env` files.

## Pain points → answers

| Pain point | Answer |
| --- | --- |
| 1. Inventory misses images, runtime pins, indirection, bundled copies | `runtime-inventory` (D2–D4): images, pin files, CI, `engines`, YAML/Helm/env/bake image refs, `ARG` and single-definition variable resolution, unresolved otherwise. Bundled copies are named in the advisory as the bundling packages (pyarrow, duckdb) and matched against the dependency graph and imports. |
| 2. `search_code` 100-match cap | The advisory check never uses code search; it reads PostgreSQL inventory with keyset cursors (D8). `search_code` keeps its cap (D11). |
| 3. Unhelpful invalid-query errors | `search-query-errors` (D11): Zoekt's message passes through with hints; inline case flags such as `(?i)` are refused up front. |
| 4. No version-range logic | `advisory-definitions` and D6: fixed-per-line shorthand, OSV ranges, floating selectors, aliases, EOL, unresolved. |
| 5. No remediation tracking | `advisory-remediation` (D7, D9): exceptions, signals, status history, burn-down. |
| 6. Noisy results | Path classes (D6); test and docs hits are listed but not counted. |

## Decisions

### D1. Extract pins in the indexer (chosen over server-side collection)

Alternatives considered:

- **A, chosen. Indexer, after lexical publication.** The tree is already on
  local disk at the exact commit, so there are no extra GitHub calls. Every
  push refreshes it, and the result is bound to the commit (`verified`). Cost:
  an ADR amending ADR-0017 D2 for this stream, and a one-off reindex to
  backfill.
- **B. Server collector reading files through the GitHub contents API.** Stays
  inside ADR-0017 and backfills automatically, but costs one API call per
  candidate file per refresh (monorepos hit rate limits), and GitHub truncates
  very large trees.
- **C. Server collector reading files from Zoekt.** No GitHub calls, but needs
  new Zoekt modes (more than 100 file results, whole-file reads), inherits the
  256 KiB response cap and Zoekt's file-size skips, and couples inventory to
  search internals.
- **Rejected: classify at query time with code search.** No history baseline,
  cannot resolve `ARG` or env indirection without full files, and brings back
  the cap.

Placement: `Worker.RunOne` calls a new optional `Runtime` hook after
`PublishIndex` and before `Enricher`. The hook takes the workspace, repository,
and SHA and returns nothing: it records its own outcome, so the worker cannot
fail on it. The index lease keeps renewing during extraction. ADR-0020 records
the amendment: supply-chain *collection* stays in the server, and this one
stream is produced by the indexer because it describes the indexed tree
itself.

### D2. The `graphnest:runtime` stream and its document

- Stream `graphnest:runtime`, producer `graphnest` (added to the producer
  `check` constraints), subject `source`, `subject_revision` = indexed SHA,
  `subject_assurance` = `verified` (first use of that value).
- The extractor writes a **CycloneDX 1.6 JSON** document: one component per
  distinct purl (type `container` for images, `platform` for runtimes), with
  `evidence.occurrences[]` (`location`, `line`, `additionalContext` = line
  text plus any resolution note), `metadata.component` naming the repository
  and commit, and `metadata.tools` = `graphnest-runtime/<ParserVersion>`.
  Components are sorted for stable output. Because the commit is in the
  document, every indexed commit publishes a new snapshot, and history per
  commit comes for free. Retention prunes it like any stream.
- The document goes through the **existing** `NormalizeCycloneDX16`, so the
  snapshot is derived from the preserved bytes (ADR-0017's invariant). The
  normalizer gains one capability: `evidence.occurrences` becomes
  `Component.Locations` (`path`, `line`, `context`; at most 100 per component,
  context at most 300 bytes), stored in the new column
  `supply_chain_components.locations jsonb not null default '[]'`. Imports
  benefit too. `evidence_not_carried` now fires only for evidence other than
  occurrences.
- Publication uses `PublishSupplyChainSnapshot` with `JobID = nil`. Failures
  use `RecordSupplyChainFailure` (outcomes `too_large`, `malformed`, `error`)
  with a safe message. No projection into `repository_packages`, and no
  license-enrichment enqueue for this stream.
- `ValidStream`/`StreamProducer` accept `graphnest:runtime`. The SBOM scheduler
  never schedules it, and `Refresh` rejects it with `invalid_request`
  ("runtime inventory refreshes when the repository is indexed").

### D3. Extractor structure and budgets

New package `internal/supplychain/runtime`: a pure function
`Extract(root string, budget Budget) (Result, error)` over a directory, plus a
`Publisher` that builds the document, normalizes, publishes, and stores
CODEOWNERS. File-level parsers are small functions with table tests:

| File | Records |
| --- | --- |
| `Dockerfile`, `Dockerfile.*`, `*.Dockerfile`, `*.dockerfile`, `Containerfile*` | `FROM` images: continuations, comments, flags, `AS` stages, `scratch`, stage references, digests |
| `.nvmrc`, `.node-version`, `.bun-version` | first non-comment token |
| `.tool-versions` | `nodejs`/`node`, `bun`, `dotnet`/`dotnet-core` |
| `global.json` | `sdk.version` → `pkg:nuget/Microsoft.NET.Sdk` |
| `*.csproj`, `*.fsproj`, `*.vbproj`, `Directory.Build.props` | `TargetFramework(s)` `netX.Y`/`netcoreappX.Y` → `pkg:nuget/Microsoft.NETCore.App@X.Y` |
| `.github/workflows/*.y{a,}ml` | `node-version`, `bun-version`, `dotnet-version` (scalars, lists, `${{ matrix.k }}` from the job's matrix); `container:` and `services.*.image` |
| `package.json` (not under `node_modules`) | `engines.node`, `engines.bun` |
| other `*.y{a,}ml` | `image:` scalars; maps with `repository` + `tag` |
| `*.env`, `build.env` | keys `NODE_VERSION`, `BUN_VERSION`, `DOTNET_VERSION`, `DOTNET_SDK_VERSION` (pins) and `*IMAGE` (image refs); all others ignored |
| `docker-bake.hcl` | `variable "X" { default = "…" }` and `args = { … }` (line-oriented parse, no HCL dependency) |
| `CODEOWNERS` in `.github/`, root, `docs/` | rules (first file found) |

- YAML uses the existing `gopkg.in/yaml.v3` with node line numbers. Values
  containing `{{` or `${{` that are not matrix references are skipped.
- Variable resolution runs in a second pass, after all files are read: first
  the `ARG` default in the same Dockerfile (global `ARG` before the first
  `FROM`), then a single repository-wide definition from env or bake files.
  Two or more distinct candidates, or none, means unresolved, with candidates
  in the context.
- Image references follow the Docker reference grammar. The first segment is a
  registry when it contains `.` or `:` or is `localhost`. Docker Hub
  (`docker.io`, `index.docker.io`, or omitted) gets no `repository_url`, and
  single-segment Docker Hub names get namespace `library`. When both a tag and
  a digest are present, the version is the tag and the digest goes in the
  `digest` qualifier. A digest-only reference uses the digest as its version.
- Budgets (constants, with a `shortcut:` comment): at most 50,000 candidate
  files, 256 KiB per file (larger files are skipped with a warning), 60 s, and
  50,000 components (the supply-chain default). Large infrastructure
  monorepos, with thousands of YAML files, are the main candidates for the
  file cap. Exceeding the file, time, or component budget publishes nothing
  (`too_large`): a partial inventory would hide exposure.
- Walk with `filepath.WalkDir`. Skip `.git` and `node_modules` directories,
  skip anything that is not a regular file (symlinks included), and never
  execute or fetch anything.

### D4. Owners

New table `repository_owners (repository_id, source, owners text[], rules
jsonb, commit_sha, fetched_at, error_code)` with primary key `(repository_id,
source)`, where `source` is `codeowners`, `admin_team`, or `admin_user`.

- The indexer's runtime publisher upserts `codeowners` (parsed rules, at most
  1,000 rules from a file of at most 256 KiB) with the commit.
- The server's owner refresher (D9) upserts `admin_team` (teams whose
  `permission` is `admin`; needs `Administration: read`, so a 403 stores
  `error_code=permission_missing`) and `admin_user` (`collaborators?
  affiliation=direct&permission=admin`, needs only Metadata).
- Resolution order at evaluation time: CODEOWNERS for the most urgent counted
  evidence path (gitignore-style subset: anchored `/`, directory `/` suffix,
  `*`, `**`, basename patterns; last match wins), then admin teams, then
  direct admins, then the organization. The result stores the owner text and
  its `owner_source`.

### D5. Advisory definitions

Package `internal/supplychain/advisory`.

- **Parsing:** walk a `yaml.v3` node tree (JSON is YAML), reject alias and
  anchor nodes, reject unknown keys, and collect errors with field paths
  (`products[1].fixed[2]`). Input is capped at 256 KiB in the HTTP layer, plus
  count limits: 200 products, 20 patterns, 100 versions, 20 aliases, 100 tag
  aliases, 50 OSV records.
- **OSV:** detected as a top-level object with `affected` and `modified` or
  `schema_version`, or as entries of `osv`. Packages map to a purl via
  `package.purl` (version stripped), or via ecosystem → purl type: npm→npm,
  PyPI→pypi, Go→golang, NuGet→nuget, Maven→maven (`group:artifact` →
  namespace/name), crates.io→cargo, RubyGems→gem, Packagist→composer, Hex→hex,
  Pub→pub, GitHub Actions→githubactions. Other ecosystems are reported as
  skipped. `SEMVER`/`ECOSYSTEM` events and `versions` become explicit ranges;
  `GIT` ranges are skipped and reported.
- **Supported pattern types:** `docker`, `generic`, `nuget`, `npm`, `pypi`,
  `golang`, `maven`, `cargo`, `gem`, `composer`, `hex`, `pub`,
  `githubactions`. Distribution types (`deb`, `rpm`, `apk`, `alpm`) are
  rejected, because their versions do not order like the others (D6).
- **Lifecycle:** create and edit accept `application/yaml` or
  `application/json` (the YAML parser reads both). Other media types get
  `415`. An edit must keep the same `id`.
- **Normalized form**, stored as JSONB per version: products with patterns and
  either `fixed` plus line precision or explicit ranges, `introduced`, EOL
  selectors, and alias map. The original bytes are stored alongside it.
- **Line precision:** explicit `line`, else the minimum `k` such that all
  fixed versions' first `k` components are distinct, else one line (`k=0`)
  when there is a single fixed version.

### D6. Classification rules

- **Versions:** an optional `v` prefix, 1–4 numeric dot segments, then an
  optional pre-release or PEP 440 suffix (`-rc.1`, `rc1`, `a1`, `b2`, `.dev0`,
  `.post1`). Segments compare numerically, with missing segments read as 0.
  Known pre-release markers (`alpha`, `a`, `beta`, `b`, `rc`, `c`, `pre`,
  `preview`, `dev`, `snapshot`, `m`/`milestone`) sort before the release, and
  `.post` sorts after it. Other alphabetic suffixes (`.Final`, `.RELEASE`,
  `-jre`, Go's `+incompatible`) compare equal to the numeric release. A value
  without a leading numeric segment is unparsable, which means unresolved.
- **Unaffected set U** per product: with `fixed`, `U = [0, introduced) ∪ ⋃ [F,
  lineEnd_k(F))`. With explicit ranges, U is the complement of their union.
  `lineEnd_k(22.21.1)` is `23` for `k=1`, `22.22` for `k=2`, and `∞` for
  `k=0`.
- **Selectors** come from the component version text:
  - **exact:** three or more numeric segments.
  - **floating span `[lo, hi)`:** fewer segments (`22` → `[22, 23)`,
    `22.18` → `[22.18, 22.19)`), `X.x` and `X.*`, npm ranges (`^`, `~`, `>=`,
    `>= <`, `||` evaluated as the highest alternative), `latest` and
    variant-only tags (all versions).
  - **alias:** a word looked up in `tag_aliases`, with `lts/` stripped.
  - **unresolved:** `${…}`, digest-only, unknown words, unparsable.
- **Docker tags:** the version is the first `v?\d+(\.\d+){0,3}` token. The
  remaining words must be variant words (`slim`, `alpine`, `alpine3.20`,
  `bookworm`, `bullseye`, `jammy`, `noble`, `distroless`, `latest`,
  `windowsservercore`, `nanoserver`, …) or an alias; anything else is
  unresolved.
- **Status of one occurrence:**
  - **exact `v`:** in U → `fixed`, or `not_affected` if `v < introduced`;
    otherwise EOL → `eol`; otherwise `vulnerable`.
  - **span:** `[lo, hi) ⊆ U` → `fixed`; some fixed `F ∈ [lo, hi)` with
    `[F, hi) ⊆ U` → `needs_rebuild`; EOL(lo) → `eol`; otherwise `vulnerable`.

  The reason text names the product, the version or selector, and the
  relevant fixed version.
- **Path classes**, first match wins:
  - `ci`: `.github/workflows/`, `.github/actions/`, `.gitlab-ci.yml`,
    `.gitlab/`, `.circleci/`, `.buildkite/`, `Jenkinsfile*`,
    `azure-pipelines*.y{a,}ml`, `bitbucket-pipelines.yml`, `.drone.yml`
  - `test`: a segment in {test, tests, `__tests__`, testdata, fixtures,
    `__fixtures__`, e2e, spec, specs, mocks, `__mocks__`}, or a basename
    `*_test.*`, `*.test.*`, `*.spec.*`, or `test_*.py`
  - `docs`: a segment in {docs, doc, documentation, examples, example, samples,
    sample, demo, demos, plans, rfcs, experiments, experimental, sandbox,
    playground}, or the extensions `.md`, `.mdx`, `.rst`, `.adoc`
  - otherwise `prod`. Dependency-graph and import occurrences without a path
    count as `prod`.
- **Repository status:** the most urgent counted (`prod`, `ci`) status,
  ranked vulnerable, eol, unresolved, needs_rebuild, fixed, not_affected.
  With no counted occurrence it is `non_production`. With a current exception
  on an open status it is `excepted`. Repositories with only `not_affected`
  occurrences get no row unless they had one before.

### D7. Advisory storage, evaluation, and history

Migration `040_supply_chain_advisories.sql` (additive):

```text
supply_chain_advisories           id, key unique, state open|closed, current_version,
                                  evaluated_through timestamptz, created_by/at, closed_by/at, updated_at
supply_chain_advisory_versions    (advisory_id, version) pk, title, aliases text[], published_at,
                                  definition jsonb, source bytea, source_media_type, created_by/at
supply_chain_advisory_results     (advisory_id, repository_id → repositories cascade) pk,
                                  advisory_version, status, rank, counted, owner, owner_source,
                                  evidence jsonb (≤ 200 occurrences, plus total), fingerprint bytea,
                                  evidence_commit char(40), exception_id, signals jsonb, signals_checked_at,
                                  signals_error, status_since, evaluated_at
                                  index (advisory_id, rank, repository_id)
supply_chain_advisory_events      id, advisory_id, repository_id null, at, kind, from_status, to_status,
                                  counted, commit_sha, advisory_version, actor, detail jsonb
                                  index (advisory_id, repository_id, at)
supply_chain_advisory_exceptions  id, advisory_id, repository_id, kind not_affected|risk_accepted|rebuilt|withdrawn,
                                  reason, expires_at, basis bytea, recorded_by, recorded_at, superseded_by, lapsed_at
repository_owners                 (see D4)
supply_chain_components.locations jsonb not null default '[]'
producer check constraints        add 'graphnest'
```

- **Evaluation** (`advisory.Service.EvaluatePending(ctx, limit)`) runs on the
  server's one-minute loop next to policy evaluation. For each open advisory:
  1. If `current_version` changed since the last full pass, reset
     `evaluated_through` and append an `advisory_versioned` event.
  2. Select repositories whose streams changed after `evaluated_through`
     (`supply_chain_streams.updated_at`), in order, up to `limit`.
  3. Load their latest-snapshot components whose ecosystem matches a product
     (the existing `(ecosystem, purl_name, version)` index serves exact names;
     wildcards filter in Go), classify, and upsert results. A status change
     appends a `status_changed` event with the evidence commit.
  4. Advance `evaluated_through`.

  Exceptions that expired, owners that were refreshed, and newly recorded
  exceptions re-evaluate their repository directly. A full pass over 1,000
  repositories is a few thousand indexed component reads.
- **Several server replicas:** each tick processes an advisory inside a
  transaction that first takes `select … from supply_chain_advisories where
  id=$1 for update skip locked`, so only one replica evaluates a given
  advisory at a time. The result upsert and its event append share that
  transaction, and an event is appended only when the stored status actually
  changes.
- **Fingerprint:** SHA-256 of the sorted counted evidence `(stream, path or
  element ID, line, purl, version)`. Exceptions store it as `basis`; when it
  changes, the exception lapses (event `exception_lapsed`, reason
  `evidence_changed` or `expired`).
- **Exceptions in the history:** recording, lapsing, or withdrawing an
  exception also appends a `status_changed` event to or from `excepted`, so
  the history alone determines the open count.
- **Burn-down:** computed at read time from `status_changed` events of
  authorized repositories. For each UTC day from the first check to today,
  count repositories whose last event on or before that day is open
  (`vulnerable|eol|unresolved|needs_rebuild`) and counted. The publication
  date is marked when it is earlier, and edits are marked from
  `advisory_versioned` events.
- **Coverage** (read time, authorized set):
  - runtime not checked: the indexed SHA is not equal to the latest
    `graphnest:runtime` snapshot's `subject_revision`
  - dependencies not checked: no `github:source` snapshot
- **Dry runs** evaluate the same code over the caller's authorized repositories
  without writing. They are recomputed per page (stateless), with the cursor
  bound to a hash of the definition, like the portfolio cursor's filter key.
  They have a time budget of 10 s and carry no signals, which need a saved
  advisory.

### D8. Interfaces

**REST** (registered only when the module is enabled; errors use the existing
envelope):

| Route | Who |
| --- | --- |
| `GET /v1/supply-chain/advisories?state=` | readers; counts over the authorized set |
| `POST /v1/supply-chain/advisories` (YAML or JSON body) | administrators; `409` duplicate key |
| `POST /v1/supply-chain/advisory-checks?cursor=&limit=` (definition body) | readers; dry run |
| `GET /v1/supply-chain/advisories/{id}` | readers; current version, inferred lines, counts, coverage, burn-down, versions |
| `GET /v1/supply-chain/advisories/{id}/versions/{n}` | readers; normalized and original input |
| `PUT /v1/supply-chain/advisories/{id}` | administrators; new version |
| `POST /v1/supply-chain/advisories/{id}/close`, `/reopen` | administrators |
| `GET /v1/supply-chain/advisories/{id}/repositories?status=&class=&owner=&org=&q=&cursor=&limit=` | readers |
| `GET /v1/supply-chain/advisories/{id}/repositories/{github_id}` | readers; all evidence, history, signals, exceptions, `basis`, `can_record_exception` |
| `POST /v1/supply-chain/advisories/{id}/repositories/{github_id}/exceptions` | administrators or review-grant holders; `basis` required |
| `GET /v1/supply-chain/advisories/{id}/export.csv`, `export.md` (same filters) | readers; full authorized set, streamed |

`{id}` is the numeric advisory ID; list and MCP also accept the key. Page size
is bounded by `GRAPHNEST_MAX_RESULTS`, and the cursor is keyset `(rank, name)`
bound to the filter key. Delegation-only tokens are refused as on every
supply-chain route. Contracts go in `docs/openapi.yaml` before code.

**MCP** (read-only, registered beside the inventory tools):

- `list_advisories {state?}`
- `check_advisory {advisory? | definition?, status[]?, path_class[]?, owner?,
  org?, cursor?, limit?, max_output_bytes?}` returns `{advisory, summary,
  coverage, repositories[{id, name, status, status_since, owner,
  owner_source, evidence[≤20], signals, exception}], next_cursor, scope,
  provenance}`. The output budget trims rows and sets `next_cursor` instead of
  failing. The untrusted-content note is reused.

**Web** (`web/src/pages/supply-chain`, lazy chunk; Recharts is already a
dependency):

- `/supply-chain/advisories` (list)
- `/supply-chain/advisories/:id` (layout A: header, scope banner, counters,
  burn-down, filters, repository table, side sheet with evidence, history,
  signals, and the exception form shown when `can_record_exception`)
- `/supply-chain/advisories/new` and `/:id/edit` (administrators: paste,
  Check, Publish)

These follow the mockups the owner approved, saved as standalone pages in
`mockups/advisory-detail.html` (option A, the repository table with the most
urgent first, was chosen) and `mockups/advisory-rest.html`.

**Exports:**
- CSV: one row per evidence line, with columns repository, owner,
  owner_source, status, status_since, path, line, class, product, version
  text, reason, commit, signals, exception. Cells go through the existing
  `csvSafe`.
- Markdown: a header (advisory, version, generated at, scope statement,
  coverage), a status count table, open-count change since publication, then
  a repository table. `|`, backticks, and newlines in untrusted values are
  escaped.

### D9. Signals and owner refresh (server)

A refresher runs in the server beside the evaluation loop:

- **Signals:** every hour for open rows of open advisories. Ticks are bounded,
  and a rate-limit response stops the tick and is retried next time.
  - **Pull requests:** per installation account, issue search `"<id>" OR
    "<alias>"… is:pr org:<account>` (or `user:` for user accounts) with
    `updated:>=<published>`, paged up to GitHub's 1,000-result limit, kept
    only for listed repositories. Requires `pull_requests` in the
    installation's permissions, read from `GET /app/installations` (add
    `Permissions` to `githubapp.Installation`). Without it, store
    `signals_error=pull_requests_permission_missing`.
  - **Branches:** `GET /repos/{o}/{r}/git/matching-refs/heads/` per open row,
    keeping names that contain the id or an alias (case-insensitive).
  - **Evidence changed:** derived from events; no API call.
  - New signals append `signal_seen` events.
- **Owners:** every 24 h for repositories listed in open advisories, and
  immediately for newly listed ones (D4).
- **Several server replicas:** each refresher tick runs only while holding
  `pg_try_advisory_lock` on a fixed key, so one replica makes the GitHub
  calls.
- **GitHub client additions** in `internal/githubapp`, reusing
  `doInstallationJSON` and `nextPage`: `RepositoryTeams`,
  `RepositoryAdmins`, `MatchingHeads`, `SearchPullRequests`. All are
  read-only. Titles and branch names are stored as untrusted text and
  length-bounded.

### D10. Configuration and authorization

- No new environment variables. `config.Indexer` gains `SupplyChain bool` from
  `GRAPHNEST_SUPPLY_CHAIN`; the Helm chart and Compose pass the same value to
  the indexer. Cadences and budgets are constants.
- **Authorization:**
  - reads: live principal's authorized repositories via `Authorizer` before
    any query; counts after authorization; unauthorized repository IDs →
    `404`
  - advisory writes: `principal.Administrator`
  - exceptions: administrator or `Store.ReviewAllowed`
  - dry runs: any reader
  - MCP: read-only, OAuth tokens per grants
  - delegation-only tokens: refused
- **Metrics** (bounded labels): `graphnest_supply_chain_runtime_extractions_total{outcome}`,
  `graphnest_supply_chain_advisory_evaluations_total{outcome}`,
  `graphnest_supply_chain_advisory_signal_refreshes_total{outcome}`.

### D11. Search errors, and why no search pagination

- **Errors:**
  - `zoekt.Client.call` reads at most 4 KiB of a 400 body and returns
    `search.QueryError{Reason}`, which wraps `ErrInvalidQuery`, from
    `{"Error": …}`.
  - `search.Service.Search` first refuses flag groups that set or clear `i`:
    `(?…i…)` or `(?…i…:` not preceded by a backslash, with the group named
    and `case:no`/`case:yes` suggested. Non-capturing `(?:` and other flags
    pass through, because they were not observed to misbehave.
  - A small hint table maps `missing closing ]` to the quoting/`\s` hint and
    paren messages to the escaping hint.
  - `writeSearchError` and MCP `runSearch` render `search query is invalid:
    <reason>[; hint: …]`, with control characters stripped and the length
    bounded.
  - The GitHub backend maps `422` to `QueryError` with GitHub's `message`.
- **No pagination.** The advisory workflow, which hit the cap, now reads
  PostgreSQL inventory with keyset cursors and never calls code search.
  Zoekt's JSON API has no offset or cursor, and it orders by score with
  unstable ties, so offset paging could skip or repeat matches. Honest
  pagination needs repository-sliced re-querying, a separate change if
  general search needs it. `truncated` remains the signal to narrow a query.

## Risks / Trade-offs

- **[Release-line inference wrong for a product]** → The check and detail views
  show the inferred lines per product, `line:` overrides it, and the "lines
  without a fix count as affected" default errs toward exposure.
- **[Heuristic extraction noise]** (YAML `image:` keys in unrelated files) →
  Every occurrence carries file:line evidence and a path class, test and docs
  hits are not counted, and exceptions cover the rest.
- **[Backfill gap]** (repositories not reindexed since upgrade have no runtime
  inventory) → They show as "runtime not checked", never as unaffected. The
  how-to gives a reindex loop over `POST
  /v1/admin/repositories/{id}/reindex`.
- **[Extraction lengthens index jobs]** → It runs after the commit is
  searchable, with a 60 s budget; overruns publish nothing and are visible as
  `too_large`.
- **[Very large monorepos exceed the budget]** → They show as "runtime not
  checked" with outcome `too_large` instead of a partial list. The caps are
  constants that can be raised once real repositories are measured.
- **[ADR-0017 boundary]** → ADR-0020 narrows the amendment to this one stream,
  and supply-chain collection stays in the server.
- **[Optional GitHub permissions not granted]** → Signals and owners degrade
  with a named reason, and branches and direct admins still work.
- **[Untrusted text in evidence, PR titles, and branch names]** → It is
  escaped in the web view and exports and carries the MCP untrusted-content
  note.
- **[Advisory input abuse]** → Writes are administrator-only, input is capped
  at 256 KiB, YAML aliases are rejected, references are never fetched, and the
  original input is stored for audit.
- **[Exception misuse]** → Exceptions require the review grant, a reason, and
  an expiry of at most one year, bind to the evidence fingerprint, and are
  append-only in the history.
- **[Behaviour change]** → Queries with inline flags now fail with `400`
  instead of returning wrong results. Noted in the CHANGELOG under Changed.

## Migration Plan

1. Deploy migration `040` with `graphnest-migrate` (additive), then the server
   and the indexer from the same release. The indexer writes producer
   `graphnest` only after `040` exists, and migrations run first in the
   chart's existing order.
2. Set `GRAPHNEST_SUPPLY_CHAIN=true` for the indexer as well as the server.
3. Backfill: reindex repositories (documented loop), or let pushes refresh
   them. Coverage shows progress.
4. Optionally grant the App `Administration: read` and `Pull requests: read`.
   Org owners approve the updated permissions.
5. Rollback: unset the flag on the indexer to stop runtime snapshots. Older
   server binaries ignore the new tables, and runtime streams are invisible to
   them. The migration is additive and stays.

## Open Questions

- Exact hint wording and the variant-word list can be tuned during
  implementation without changing the specs.
- The owner plans to test the new GitHub calls against a real installation;
  results may adjust error codes, not behaviour.
