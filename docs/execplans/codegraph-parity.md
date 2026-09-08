# CodeGraph parity execution record

This is the living record for the accepted [CodeGraph-only roadmap](codegraph-parity-plan.md).
Implementation, validation, draft publication, and release are separate states.

## Progress

- 2026-09-06: Read the accepted roadmap and repository instructions; inspected the current default branch and existing pull requests.
- Created an isolated checkout at `../graphnest-codegraph`; preserved the original checkout's modified `go.work.sum`.
- Initialized the local native-stack metadata with `github/gh-stack` v0.1.1: `main ← feat/codegraph/s1-01-contract`.
- Published the signed foundation as draft [PR #64](https://github.com/balcsida/graphnest/pull/64),
  commit `c9fbf77c63e478863e7879a8388a6661ee6afab4`; GitHub verifies its signature.
- Started dependent `feat/codegraph/s1-01-workflows` for expanded reference
  analysis workflows and a representative PostgreSQL baseline.
- Captured 41 reproducible upstream answers, including source-evidenced flow,
  exploration, steps/screens/maps, transitive affected tests, hierarchy/member
  views, source refusal, dead-code candidates, and saved-trail operations.
- Recorded the existing PostgreSQL service over 50,000 synthetic symbols and
  200,500 edges, including actual SQL counts and prepared-statement query plans.
- Published reviewed, signed workflow and database-baseline commits as draft
  [PR #65](https://github.com/balcsida/graphnest/pull/65), head
  `2371daa132ebb69936afdd7ec0f611d2582ad440`, based on PR #64.
- Verified native remote stack **#66**, ID `PRS_kwDOTcm09c4ADdBt`, rooted at
  `main`, with PR #64 in position 1 and PR #65 in position 2.
- Started dependent `feat/codegraph/s1-01-timings` for repeated pinned-upstream
  workflow timings before beginning production artifact changes.
- Captured five warm runs each for real callers, exploration and flow queries,
  checking required facts/source on all 1,500 timed answers and 75 warmups.
- Published the reviewed, signed timing layer as draft
  [PR #67](https://github.com/balcsida/graphnest/pull/67), and verified native
  stack #66 now contains PRs #64, #65 and #67 in dependency order.
- S1.01 is complete: mapped inventory, reproducible real-producer answers,
  separately labeled complete vocabulary fixtures and frozen performance
  baselines/budgets. Remaining conformance variants retain their owning stages
  and planned tests; this does not complete production parity or Stage 1.
- S1.02 is complete and published as draft [PR #68](https://github.com/balcsida/graphnest/pull/68),
  based on PR #67, at native stack #66 position 4. GitHub verifies signed
  commits `73ee941` (v1 enum guard) and `a460f73` (v2 contract). Independent
  review, artifact/compatibility checks and clean-tree generation pass.
  CI passes at final record head `03d9b6c`; the optional Buf lint convention
  finding remains tracked.
- S1.03 storage is implemented on dependent `feat/codegraph/s1-03-storage`.
  Independent fix review approves lossless long/NUL strings, bounded SQL keys,
  immutable generations and publication preconditions. Full Go tests, format,
  vet and build pass; all six PostgreSQL integration/race packages pass
  (storage: 38.295s). Opt-in index evidence is retained in
  `docs/graph-storage-index-evidence.txt`. Published as draft
  [PR #69](https://github.com/balcsida/graphnest/pull/69), based on PR #68,
  at stack #66 position 5. GitHub verifies signed head `c9a3a16`; CI run `34059069312` passes.
- S1.04 is implemented and independently approved on dependent
  `feat/codegraph/s1-04-query`, with no review findings. Full Go checks and
  required PostgreSQL integration/race checks pass. Real fixture traversal
  compares 68 nodes/93 edges in 138 queries; separate synthetic vocabulary
  exercises 23 kinds/13 relations in 26 queries. Public v1 compatibility remains
  covered. Published as draft [PR #70](https://github.com/balcsida/graphnest/pull/70),
  native stack #66 position 6; GitHub verifies signed head `7df07c7`. A process-scoped
  proxy invocation restored native GitHub CLI access on September 7; PR #69
  head/base and green CI were reverified without changing system settings.
- Signed S1.04 commit `7df07c7349b4f248571d81d651e0b150e835d990` verifies
  locally. Native `gh stack add` created dependent `feat/codegraph/s1-05-discovery`.
- S1.05a semantic discovery is implemented and independently approved after
  fixing pre-limit corroboration for used variables, constants and properties.
  Required PostgreSQL lifecycle/discovery race checks pass (48.374s); the final
  focused discovery race run passes (39.731s) with real CodeGraph answers and
  permanent actual-edge ranking regressions. Native projection plans retain
  payload joins after candidate selection; production-size costs remain unmeasured.
  Published as draft [PR #71](https://github.com/balcsida/graphnest/pull/71),
  native stack #66 position 7; GitHub verifies signed head `0d2cd96` and parent
  `7df07c7`. Exact PR bodies and 11-file/12-file deltas were read back.
  Exact-head CI runs `34139856642` (PR #70) and `34139863095` (PR #71)
  pass; required verify, integration, e2e and Helm checks all pass.
  Separate GitHub AI scanning jobs failed before review with
  `CAPIError: 400 The requested model is not supported`; those external
  executions are not counted as passing security analysis.
  S1.05b indexed-file/exact-source inspection is independently approved on
  `feat/codegraph/s1-05-explore`; S1.05c will compose exploration, source
  allocation and session behavior. All three increments must pass before
  S1.05 completes. S1.06–S1.10 are pending. Stage 1 has
  not passed its release gate.
- Stages 2 and 3 are pending and cannot start until the preceding stage has passed and landed.
- Inspection testing found that entity-page lookahead needed the same scope
  validation as returned rows. The direct regression failed before the shared
  S1.04 fix and passed afterward; independent review approved the two-file fix.
  Signed query head is now `7d14135`; native upstack rebase produced signed
  discovery head `51172e9`. Native publication and exact remote signatures,
  heads, bases and file deltas were verified. Required verify, integration,
  e2e and Helm checks pass on both new heads (runs `34147508025` and
  `34147508131`). Separate AI scans still fail before review because their
  requested model is unsupported.
- S1.05b inspection is implemented and independently approved. It adds
  bounded indexed-file pages, exact-source entity/file inspection, outlines and
  original caller/callee occurrences. Legacy Context now checks authorization
  and the actual queried generation after source reads. Conditional pre-rebase
  affected race checks passed, including HTTP/MCP and 19 PostgreSQL tests in
  46.687s; after restoring the exact candidate on the repaired parent, focused
  query/service and real PostgreSQL inspection race checks plus format/build
  passed. Composed exploration, allocation and session behavior remain S1.05c.
- Independent inspection review reproduced two false-completeness cases:
  a final outline continuation and a file with stored extraction errors. Both
  were corrected and passed scoped re-review. Permanent regressions observed
  red then green; final graphservice/query race checks and all six required
  PostgreSQL inspection tests passed (2.623s, no skips), as did vet and format
  checks. Original file-error evidence remains visible on contributing source.
- S1.05b is published as draft [PR #72](https://github.com/balcsida/graphnest/pull/72),
  native stack #66 position 8, with verified signed head `078789c` and parent
  `51172e9`. The exact PR body and 17-file delta match the reviewed commit.
  S1.05c is active on dependent `feat/codegraph/s1-05-compose`; inspection
  publication does not complete S1.05 or the Stage 1 gate.
- PR #72's normal integration run exposed missing fixture preparation for the
  mandatory inspection oracle. The existing exporter now prepares a private
  fixture before the six-package PostgreSQL suite and cleans it up afterward.
  Full `make postgres-test` passed; allocation/export failure probes confirm
  preparation failures stop the suite. Independent review approved the fix,
  published as verified signed head `5214a27`; all eight native positions,
  the PR body and the 18-file delta were read back. CI run `34153429805`
  passes on this head, including all required checks. The separate AI scan
  still fails before analysis because its requested model is unsupported.
- The remaining S1.05c work is split into bounded file projections (c1),
  stateless exploration and source allocation (c2), and scoped repeated-call
  deduplication and restoration (c3). C1 is implemented and independently
  approved on the existing compose branch. Every original S1.05 obligation
  remains required.
- C1 provides bounded flat, tree and grouped file inventories with original
  metadata, exact filtered totals and explicit page/depth boundaries. All nine
  pinned views retain the 13 original file facts. Eight hostile wildcard cases
  verify JavaScript UTF-16 matching, including emoji and line terminators.
  Final service/query race checks pass (1.930s/2.360s); ten required PostgreSQL
  tests pass (4.126s, no skips), including final grant/SHA/generation changes.
  Vet, formatting and diff checks pass; independent review has no findings.
  SQL scan costs and the 2,000-node tree ceiling are documented. C2/C3 remain
  pending, and production-size performance remains part of the Stage 1 gate.
- C1 is published as draft [PR #73](https://github.com/balcsida/graphnest/pull/73),
  native stack #66 position 9, with verified signed head `341372d` and parent
  `5214a27`. The exact PR body and ten-file delta were read back. CI run `34156123866`
  passes on this head, including all required checks; the separate AI scan
  still fails before analysis because its requested model is unsupported.
  C2 stateless exploration and allocation is active on dependent
  `feat/codegraph/s1-05-allocation`. C3 state and the Stage 1 gate remain pending.
- C2 independent review identified four source-selection gaps: required
  family/god-file adaptive variants, unequal required-window allocations,
  overlapping required ranges, and affordable whole-file prefixes. All three
  window regressions reproduce before correction. Fix round 1 also covers
  required adaptive variants and an affordable BMP file; 54 service race tests
  and both PostgreSQL Explore tests pass without skips. Independent re-review
  accepts all four fixes without new findings.
- C2 shared stateless exploration is implemented and independently approved.
  It combines discovery, indexed-file facts, bounded relationships and exact-SHA
  source with corpus-scaled UTF-16 allocations and separate byte/work limits.
  Required ranges are funded before optional context; adaptive family/god-file
  selection retains selected bodies and original signatures. Actual pinned
  adaptive-on/off probes and native matching-grant checks retain required source.
  Final domain generation/SHA/grant checks remain mandatory. C3 history,
  source-derived analysis, public adapters, final rendered envelopes and the
  full Stage 1 gate remain pending.
- C2 is published as draft [PR #74](https://github.com/balcsida/graphnest/pull/74),
  native stack #66 position 10, with verified signed head `ed08344` and parent
  `341372d`. The twelve-file delta, prior nine native entries and PR description
  were read back exactly. CI run `34162210818` passes on this signed head,
  including all required checks. The separate AI scan again fails before
  analysis because its requested model is unsupported. C3 is active on dependent
  `feat/codegraph/s1-05-sessions`.
- The S1.05 row audit retains additional work after C3: exact discovery
  selectors and segment/project evidence, generated/ambient file predicates
  and counts, and task-level relevant/build context options. Existing generic
  discovery/source tests do not close every library variant. Separate focused
  layers will reuse the accepted services; ranking comparisons follow required
  answers at equivalent budgets rather than identical SQLite scores. Shared
  language/configuration answer rows remain open through S1.06/S1.10.
- S1.05c3 adds optional, authenticated exploration sessions with bounded
  coverage-only history, repeated-source references and source restoration.
  Independent review accepted the corrected layer: terminator-aware coverage
  preserves unseen LF/CRLF bytes, and any retained source refusal prevents
  history admission even after a successful read of the same file. All 69
  service race tests and both required PostgreSQL Explore tests pass; focused
  vet and formatting checks pass. History remains bounded to 64 entries, four
  per identity, 256 KiB charged per entry, 4 MiB total and a fixed 15-minute
  lifetime. Every call rechecks current authority and the exact generation/SHA.
  The separately reviewed S1.05c2 admission repair now supports the exact
  upstream `core.ts Service normalize` request with `maxFiles=1`. After the
  native upstack rebase, both original-query calls return the full 786-byte
  core.ts source with original occurrences and one read; the repeat restores
  source with zero references or claimed savings. Independent integration
  review passes, with 70 service race tests and both required PostgreSQL Explore
  tests passing. Focused staticcheck and vet pass, including the correction of
  CI's test-only S1038 finding. C3 is draft PR #75 at native position 11;
  full S1.05 and Stage 1 remain open.
- The signed admission/restoration updates are published at PR #74 head
  `4d277ca` and PR #75 head `8a7b024`, with their exact native bases, signatures,
  file deltas and descriptions verified remotely. CI runs `34167657724` and
  `34167658451` pass on these exact heads, including every required check and
  UI smoke. The separate AI scans fail before analysis because their requested
  model is unsupported; those failures are not passing security analysis.
  Work continues on
  S1.05d1 discovery selectors, segment evidence and exact-SHA project tokens.
- S1.05d1 is implemented and independently reviewed with its module-parser
  finding corrected: CRLF declarations retain their token and malformed or
  duplicate declarations supply none. Permanent regressions fail before the
  correction; all project-token race tests pass in 1.460s, with focused vet,
  staticcheck and formatting checks passing. After authorized OrbStack recovery,
  all five required PostgreSQL oracle, replacement, segment, query-plan and
  rebuild checks pass in 1.950s. The affected PostgreSQL race selection passes
  in 45.327s: 27 tests pass and one pre-existing opt-in index-plan diagnostic
  skips; the required D1 plan tests execute and pass. The reviewed code remains
  unchanged. D1 is accepted for signed native draft submission; S1.05 and the
  full Stage 1 gate remain open.
- Read-only preparation for file classification and task context is frozen.
  The task-context capture preserves twelve actual pinned method answers,
  verified against its immediate journal, with original facts/source and
  explicit budget/completeness boundaries. These captures prepare the next
  layers; they do not establish native implementation or Stage 1 acceptance.
- S1.05d1 is signed as `13027d5` and published in draft PR #76, native stack
  #66 position 12. GitHub confirms the valid signature, exact PR #75 parent,
  15-file delta and unchanged preceding eleven layers. The final oracle test
  cleanup also passes independent review and all fifteen substantive PostgreSQL
  cases. CI `34209793322` passes all required verify, integration, end-to-end and
  Helm checks plus UI smoke on this exact head. The separate AI
  security job fails before analysis with an unsupported-model error, so it is
  not a passing security analysis. Work continues on S1.05d2 generated/ambient
  file classification and persisted generated-file counts.

- S1.05d2 implements bounded generated/ambient file classification and stored-flag
  counts. Its frozen oracle covers thirteen original files plus all twenty-six
  case-sensitive filename rules; synthetic tests cover flag presence, structural
  ambient evidence, replacement and authority boundaries. Classification now
  precedes the discovery candidate limit. Projection version 3 requires an
  explicit rebuild for ordinary discovery while accepted name/segment operations
  remain eligible on version 2 or newer; no schema migration is added. The seven
  focused PostgreSQL checks and six query/service race tests pass, as does the
  affected PostgreSQL race selection after correcting an older ambient fixture
  to contain an interface. Focused vet/staticcheck and formatting checks pass.
  Independent review identified that surviving declarations in files with analyzer
  errors could incorrectly receive the ambient penalty. The correction reuses
  retained file protobufs and loaded artifact metadata; direct and one-candidate
  ranking regressions pass, along with seven scoped PostgreSQL race checks in
  4.201s. Independent scoped re-review approves the correction with no remaining
  findings. D2 is accepted for signed native draft publication.

## Baselines

| Component | Verified source identity |
| --- | --- |
| GraphNest default branch | `49e77d1bcd7f4be7198d8368e58e062778b68235` |
| CodeGraph default branch and pinned checkout | `b9ca4b7981116909900368cc1686a1074cd4d4c1` |
| CodeGraph package version at that commit | `1.6.0` (source package metadata; not a release-binary assertion) |
| GraphNest test toolchain | `go version go1.26.6 darwin/arm64`, with `GOWORK=off` |

The source pins matched upstream on 2026-09-06. The original working branch was
`feat/mcp-oauth` at `ca925c946e0c3a08c7cfa99ac6b6e2114a57894e`; it was not reset.
CodeGraph source inspection/builds use a separate task-owned checkout. Existing
local CodeGraph databases and agent configuration are not integration inputs for
these tests.

## Decisions

- Follow accepted ADR-0014 (PostgreSQL graph queries), which supersedes ADR-0012;
  preserve ADR-0008 shared services, ADR-0009 exact-SHA reads, ADR-0013 ephemeral
  archives, and ADR-0015 optional-enrichment isolation.
- Use the pinned CodeGraph source for reference generation. Keep its runtime and
  dependencies confined to dedicated test tooling.
- Keep every inventory row visible until its behavior is implemented and verified;
  generated facts, synthetic contract coverage, and GraphNest conformance are
  distinct evidence.
- Use the available native `github/gh-stack` extension. Tool discovery found no
  native worktree or stack-management MCP operation, so local Git/CLI operations
  manage the isolated checkout and stack.
- Preserve signed commits and normal branch protections. The owner has not
  authorized merging any stack prefix.
- Split S1.05 into discovery, exact-source inspection and composed exploration.
  Inspection needs bounded indexed-file queries and validation after source
  reads before stateful allocation can reuse them. This keeps all original
  requirements mandatory and costs possible interface rework between layers.
- Include legacy Context in the final source-read consistency check. Its
  commit-only result cannot identify same-SHA graph replacement; capture the
  actual queried upload internally and revalidate after source reads while
  preserving v1 wire compatibility. This adds a manifest read to that path.
- Recheck live repository, grant and installation authority in inspection;
  keep credential authentication at the current request boundary. Principal
  has no credential identity or expiry. S1.07/S1.08 must explicitly validate
  credential expiry/revocation and define mid-call behavior before the stage
  gate; inspection does not claim those tests passed. If a stronger final
  credential check is needed, the transport work must supply it.
- Keep indexed-file tree, grouped and maximum-depth variants assigned to
  S1.05c domain projections, S1.07 transports and S1.09 rendering. Flat original
  file-fact equality alone does not complete their inventory rows; this may
  require another focused layer if the composition change becomes too large.
- Use CodeGraph's shared/viewer hierarchy as the descendant contract. A fresh
  paired oracle confirms that the legacy library returns only Base while the
  shared/viewer result includes its Service subtype. Preserve that legacy
  omission as a known comparison difference rather than removing descendants
  from GraphNest. Other hierarchy cases remain unimplemented and unverified.

## Discoveries

- Source-site preparation now selects the existing scanner executable with a
  bounded source-byte subcommand, installed Go statement-list handling, and
  modern Kotlin walker adaptation as the first implementation direction.
  Missing grammars still need exact source/notices and ABI/semantic validation.
  The oversized source-site umbrella will be split into consecutive protocol,
  language and adapter/packaging layers before dispatch; no language is removed.
- Later full-file acquisition will reuse a separately configured
  `repository.Service` through `ContentReader`, with matching exact-SHA identity,
  explicit EOF/truncation checks and a line ceiling that cannot truncate a
  byte-admitted file. Display slices and their current 1,000-line default are
  not accepted parser inputs or parity limits. If this reuse proves inadequate,
  the fallback is a small whole-file method over the same existing primitives.
- The supported server installation must package the versioned helper beside
  the application, with deployment/native-library/notice checks. Missing-helper
  diagnostics remain truthful unavailable states, not a deployment-parity pass.
  Initial malformed-tree policy returns unavailable semantic facets for parser
  recovery. Proposed work/byte/time/process ceilings require adversarial tests
  and real measurements; no preparation result establishes their performance.
  These choices can require walker, protocol, reader or packaging rework if
  later evidence contradicts them. Source-site implementation is not started.

- No existing CodeGraph implementation or parity record was found on the baseline.
- Existing PostgreSQL parity tests already compare normalized context, impact,
  trace, ambiguity, stale/missing snapshots, and authorization boundaries against
  `test/fixtures/graph/query/parity.json`.
- `test/integration/graph_contract_test.go` already connects PostgreSQL, shared
  services, REST, and MCP; future conformance layers can extend that pattern.
- Existing managed-parser fixtures remain managed-producer evidence. Their
  artifacts must not be relabeled as CodeGraph output.
- `docs/benchmarking.md` describes measurement policy but contains no measured
  graph-query p95 baseline. Integration suites can skip without PostgreSQL;
  claimed integration gates must set `GRAPHNEST_REQUIRE_POSTGRES=1`.
- CodeGraph's current SQLite schema is version 9, despite the initial SQL header.
  Actual node identity and `decorates` direction differ from their upstream type
  comments; the inventory cites the implementations.
- The real `polyglot-core` fixture emits 20/23 node kinds and 9/13 relations.
  `protocol`, `parameter`, and `export` nodes plus `exports`, `type_of`, `returns`,
  and `overrides` relations have synthetic vocabulary coverage only. This is not
  a claim that the source fixture extracts those facts.

## Validation results

| Command/check | Actual result |
| --- | --- |
| `git fetch origin` and baseline read-back | Default branch matches the accepted GraphNest pin |
| CodeGraph `git ls-remote` and detached checkout | Both resolve to the accepted CodeGraph pin |
| `gh auth status` | Active `balcsida` GitHub account |
| `gh extension list` | Native `github/gh-stack` v0.1.1 installed |
| `gh stack init --base main feat/codegraph/s1-01-contract` | Local stack created; this does not prove remote-stack availability |
| `GOTOOLCHAIN=go1.26.6 GOWORK=off go test ./...` | Passed on the unchanged GraphNest baseline |
| `GOTOOLCHAIN=go1.26.6 GOWORK=off make build fmt lint` | Passed on the unchanged GraphNest baseline |
| `GOTOOLCHAIN=go1.26.6 GOWORK=off make test-race openapi-check tools-check` | Race tests and OpenAPI check passed; tools generation printed a missing-plugin error despite the target exiting zero |
| `GOTOOLCHAIN=go1.26.6 make tools-check` | Passed with the workspace enabled and no generated-file drift |
| `GOTOOLCHAIN=go1.26.6 GOWORK=off GRAPHNEST_REQUIRE_POSTGRES=1 GRAPHNEST_TEST_POSTGRES_DSN=<isolated-test-dsn> go test -race -count=1 -tags=integration ./internal/postgres ./internal/authz ./internal/webhook ./test/integration ./cmd/graphnest-indexer ./cmd/graphnest-server` | All six packages passed against a dedicated PostgreSQL 18.6 container; no missing-database skips |
| `GOTOOLCHAIN=go1.26.6 GOWORK=off make staticcheck govulncheck` | Passed; no reachable vulnerabilities found (three required-module findings were not called) |
| `make parity-reference` | Passed: offline Python stdlib checks for database integrity, source/config/hash identity, SQL answers, and exact vocabulary fixture coverage; wired into the existing CI verification job |
| `python3 test/parity/generate_reference.py --upstream <pinned-clone> --node <node-24.13.0> --check` | Passed: two independent real producer runs match committed full logical database facts/schema, SQL answers, and 13 library-query answers |
| `GOTOOLCHAIN=go1.26.6 GOWORK=off go test ./internal/graphquery -run . -bench '^BenchmarkGraphQueryWarm$' -benchtime=10000x -count=1 -cpu=1` | Passed: controller read-back of benchmark behavior; timing variation on the shared host is not a release-gate result |
| `GOTOOLCHAIN=go1.26.6 GOWORK=off make test fmt lint` | Passed after adding the reference foundation and benchmark |
| `git diff --check` | Passed; the intentional CRLF source fixture has scoped `cr-at-eol` attributes |
| `sh test/parity/check_stale_dist.sh <pinned-clone> <node-24.13.0>` | Failed with the stale-build sentinel before the fix; passed after rebuilding from pinned tracked source with isolated analyzer settings |
| Foundation code/spec review and scoped re-review | No Critical findings; both Important reproducibility findings fixed and re-reviewed |
| Signed commit and `gh stack submit --auto --remote origin` | Initial signer connection failed; retry signed `c9fbf77`, and submission created draft PR #64 |
| PR #64 and commit read-back | Same-repository head `feat/codegraph/s1-01-contract`, base `main`, exact `c9fbf77`; GitHub signature verified/valid |
| PR #64 CI read-back | All checks passed: verify, integration, e2e, helm, ui-smoke, and CodeQL |
| Default-branch rules read-back | Ruleset requires signed commits, PRs, resolved review threads, and strict verify/integration/e2e/helm checks; disallows deletion and non-fast-forward updates |
| PR #64 native-stack GraphQL read-back | `stack` and `stackEntry` are null; remote membership must be established with the dependent PR |
| Expanded workflow `make parity-reference` and fresh pinned `generate_reference.py --check` | Passed: two independent producer runs match full database/schema/SQL facts and all 41 answers; offline assertions pass |
| Opt-in `TestGraphQueryPostgresBaseline`, Go 1.26.6, `GOWORK=off`, `GOMAXPROCS=1`, required task-owned PostgreSQL | Passed: exact answers, five runs of 200 samples per operation, actual SQL counts and graph-index plan checks; isolated schema cleanup confirmed |
| Workflow and PostgreSQL independent reviews | No remaining Critical/Important findings; exact task-list validation added after a stale-ID negative test and scoped re-review |
| Second-layer formatting, vet and PostgreSQL graph-query race tests | Passed with Go 1.26.6 and PostgreSQL required; manifest/harness hashes and exact 10% budgets read back |
| Second-layer signed submission and native remote read-back | PR #65 head `2371daa`, parent `c9fbf77`, exact 18-file delta; both new signatures verified/valid locally and by GitHub; stack #66 has the two PRs in dependency order |
| PR #65 CI read-back | CI workflow `34052568693` completed successfully on signed head `2371daa`; integration, e2e, helm, ui-smoke and CodeQL checks also passed |
| Pinned `generate_reference.py --check --timings` and ordinary `--check` | Passed: five-run timings captured only after full two-run oracle verification; ordinary checks leave the timing report unchanged; all existing fixture facts/answers remain byte-identical |
| Timing offline and argument checks | Two offline tests pass, including fingerprints and percentile arithmetic; `--timings` without `--check` rejects before building or writing |
| Timing independent review | No Critical/Important findings; actual queries, useful-answer assertions, statistics, fingerprints, memory scope and opt-in no-facts-write behavior reviewed |
| Timing signed submission and native read-back | PR #67 head `e729878`, parent `2371daa`, exact seven-file delta; GitHub verifies the signature; stack #66 has all three layers in order. CI pending at this read-back |
| PR #67 final record and CI read-back | Signed completion-record head `5c05f83` verified/valid by GitHub; CI workflow `34053845274` completed successfully |
| S1.02 artifact checks | Real SQLite column-for-column roundtrip, synthetic 23-kind/13-relation coverage, presence/identity/hash/source tests and three bounded fuzz runs pass |
| S1.02 independent review | Aggregate predecode allocation gap fixed and re-reviewed; the 9,576-byte regression rejects with one allocation rather than 6,400 |
| S1.02 controller compatibility checks | Full `make test`, `make fmt lint`, final artifact race test, and required PostgreSQL integration race tests for `internal/postgres` and `test/integration` pass; v1 generated code, reference fixtures and dependency files remain unchanged |
| S1.02 optional Buf lint | `PACKAGE_DIRECTORY_MATCH` fails for the existing v1 layout and matching v2 layout; tracked as a Minor finding for the final Stage 1 review |
| S1.02 clean generation and publication | `GOTOOLCHAIN=go1.26.6 make tools-check` passes from a clean committed tree; draft PR #68 has exact parent `5c05f83`, initial head `a460f73`, matching 16-file delta, valid GitHub signatures and native stack #66 position 4 |
| PR #68 final record and CI read-back | GitHub verifies signed final head `03d9b6c`; CI workflow `34056236132` completed successfully |

`tools-check` needs the workspace: its nested `go tool protoc-gen-go` executes
from the root module and discovers the plugin through `./tools`. Keep `GOWORK=off`
for ordinary root-module checks, but leave the repository workspace enabled for
this generation check. Do not treat the target's exit code alone as evidence:
the existing recipe can mask a generator error before its final diff check.

The [existing service baseline](../parity/codegraph-server-baseline.json) records
five 10,000-query runs per operation, with a runnable benchmark and corpus/harness
hashes. Median run p95 was 2,334 ns for context, 2,667 ns for impact, and 3,000 ns
for trace. This measures the existing shared service over a three-symbol
in-memory fixture. It is not a PostgreSQL, REST/MCP, authorization, browser,
CodeGraph, or representative-large-repository baseline; those remain explicit
measurement gaps.

The [PostgreSQL baseline](../parity/codegraph-postgres-baseline.json) adds a
deterministic synthetic v1 corpus with 50,501 nodes, 500 files and 200,500 edges.
Five runs of 200 warmed requests give median run p50/p95 of 7.31/8.14 ms for
context, 1.53/1.70 ms for impact and 1.32/1.51 ms for trace. These issue 4/5/5
SQL reads respectively, plus two transaction statements. The report includes
exact requests, corpus/harness hashes, environment, artifact/database sizes and
actual prepared-statement `EXPLAIN ANALYZE BUFFERS` output. This is existing
GraphNest service/storage evidence, not CodeGraph-produced data or a transport,
browser, authorization, cold-cache, concurrent-load or peak-RSS measurement.

Reproduce with `GRAPHNEST_REQUIRE_POSTGRES=1`, `GRAPHNEST_TEST_POSTGRES_DSN` set
to an isolated test database, `GRAPHNEST_POSTGRES_BASELINE` set to a report path,
and `GOTOOLCHAIN=go1.26.6 GOWORK=off GOMAXPROCS=1 go test -tags=integration
./internal/postgres -run '^TestGraphQueryPostgresBaseline$' -count=1 -v`.
Without the report variable, this measurement explicitly skips; that skip is
never counted as baseline or conformance evidence.

The [upstream workflow baseline](../../test/fixtures/codegraph/workflow-baseline.json)
records five consecutive in-process runs of 100 measured samples after five
warmups per run. Median run p50/p95 in milliseconds: callers 0.0274/0.0334,
exploration 2.708/4.166, flow 0.453/0.495. Each sample includes the actual query
and full JSON serialization, then independently checks required source/facts.
The report freezes arguments, numeric result budgets, retained-handler semantics,
raw samples, response bytes, corpus/source/harness fingerprints and environment.
This is a warm portable CodeGraph reference boundary; it is not CLI startup,
HTTP/MCP transport or browser latency, and cannot establish a local parity pass
without an equivalent GraphNest measurement boundary.

Cumulative main-process peak RSS was 3,700,948,992 bytes and includes earlier
indexing/oracle work. Current RSS during the later exploration/flow runs was
about 515–519 MB. Both are recorded with their scope; the peak is not attributed
to an individual query. To refresh only this report, add `--timings` to the
documented pinned generator `--check` command. Failed or missing-answer samples
abort capture rather than disappearing from the reported percentiles.

Regeneration now builds a fresh archive of the verified pinned commit with
locked dependencies rather than trusting checkout `dist/` or `node_modules/`.
The producer runs with a recorded environment and empty temporary HOME so local
feature flags or global configuration cannot change the reference. Explicit
regeneration needs dependency access and Python 3.12+; the committed-reference
CI check remains offline and uses only Python's standard library.

The independently reviewed S1.05c2 admission repair distinguishes hard file pins
and exact occurrence paths from preferred named bodies. The original
`core.ts Service normalize` query at `maxFiles=1` now returns the committed
786-byte core.ts source in one read, with original occurrences and two explicitly
incomplete omitted-source pointers. Conflicting hard obligations still reject;
default preferred-file expansion stops at twenty. The original four-file source
tasks remain intact. All 55 service race tests and both required PostgreSQL
Explore tests pass, as do focused vet, staticcheck and formatting checks.
The rebased sessions layer also passes the exact two-call restoration comparison.

## Remaining gaps

- Representative reference captures are mapped to inventory task IDs;
  remaining conformance variants stay
  planned with their owning stages, comparison contracts and upstream tests.
- New transport/browser/import workflows without a meaningful current
  comparison remain explicitly unmeasured and unratified. Their implementation
  and final measurements belong to later layers; existing reference checks do
  not turn those planned rows into passes.
- Native/portable coordinate conversion assertions belong to S1.02; GraphNest
  query implementations and their parity comparisons belong to subsequent
  layers, and are not circular prerequisites for S1.01.
- V2 artifact/storage foundations are complete. Production query parity, publication policy, browser parity,
  CLI import, and local-engine work remains pending.
- Full Stage 1 validation (including authorization, database, browser, deployment,
  and real-producer conformance) has not run and is not claimed as passing.
- The proposed warm-query p95 budgets remain unchanged: existing GraphNest within
  10%; future GraphNest local within 1.25× pinned CodeGraph on equivalent tasks.
  Neither budget is a measured result.

## Native stack and pull requests

| Stage/layer | Branch | Local state | Remote stack / PR |
| --- | --- | --- | --- |
| S1.01 reference foundation | `feat/codegraph/s1-01-contract` | Implemented; reviewed; signed | Draft [PR #64](https://github.com/balcsida/graphnest/pull/64); native stack #66, position 1 |
| S1.01 reference workflows and PostgreSQL baseline | `feat/codegraph/s1-01-workflows` | Implemented, measured, reviewed and signed; depends on PR #64 | Draft [PR #65](https://github.com/balcsida/graphnest/pull/65); native stack #66, position 2 |
| S1.01 repeated upstream workflow timings | `feat/codegraph/s1-01-timings` | Implemented, measured, reviewed and signed; depends on PR #65 | Draft [PR #67](https://github.com/balcsida/graphnest/pull/67); native stack #66, position 3 |
| S1.02 v2 artifact contract | `feat/codegraph/s1-02-artifact` | Implemented, reviewed and signed; depends on PR #67 | Draft [PR #68](https://github.com/balcsida/graphnest/pull/68); native stack #66, position 4 |
| S1.03 generation storage | `feat/codegraph/s1-03-storage` | Implemented, reviewed and signed; depends on PR #68 | Draft [PR #69](https://github.com/balcsida/graphnest/pull/69); native stack #66, position 5 |
| S1.04 entity traversal | `feat/codegraph/s1-04-query` | Implemented, reviewed and signed; depends on PR #69 | Draft [PR #70](https://github.com/balcsida/graphnest/pull/70); native stack #66, position 6 |
| S1.05a semantic discovery | `feat/codegraph/s1-05-discovery` | Implemented, reviewed and signed; depends on PR #70 | Draft [PR #71](https://github.com/balcsida/graphnest/pull/71); native stack #66, position 7 |
| S1.05b exact-source inspection | `feat/codegraph/s1-05-explore` | Implemented, reviewed and signed; depends on PR #71 | Draft [PR #72](https://github.com/balcsida/graphnest/pull/72); native stack #66, position 8 |
| S1.05c1 file projections | `feat/codegraph/s1-05-compose` | Implemented, reviewed and signed; depends on PR #72 | Draft [PR #73](https://github.com/balcsida/graphnest/pull/73); native stack #66, position 9 |
| S1.05c2 stateless exploration and allocation | `feat/codegraph/s1-05-allocation` | Implemented, reviewed and signed; depends on PR #73 | Draft [PR #74](https://github.com/balcsida/graphnest/pull/74); native stack #66, position 10 |
| S1.05c3 scoped exploration history | `feat/codegraph/s1-05-sessions` | Implemented, independently reviewed and signed; depends on repaired PR #74 | Draft [PR #75](https://github.com/balcsida/graphnest/pull/75); native stack #66, position 11 |
| S1.05d1 discovery variants | `feat/codegraph/s1-05d1-discovery-variants` | Implemented, independently reviewed and signed; depends on PR #75 | Draft [PR #76](https://github.com/balcsida/graphnest/pull/76); native stack #66, position 12 |
| S1.05d2 file classification | `feat/codegraph/s1-05d2-file-classification` | Implemented; independently reviewed; depends on PR #76 | Accepted for signed native draft submission |

The first one-branch submission created a draft PR without a remote stack.
Submitting the second real dependent layer created native stack #66
(`PRS_kwDOTcm09c4ADdBt`); subsequent submissions extended it to ten PRs.
GraphQL independently confirmed the stack size, trunk,
positions and all PR head/base identities; local metadata alone was not used
as proof of remote membership. The signed C3 submission extended the verified
native stack to eleven draft PRs. Its later parent repair and signed upstack
rebase retain the same membership; exact remote heads and CI are read back
after each publication.

Remote membership, exact head/base, each layer's delta, and actual required checks
must be read back after submission. Draft publication alone is not approval or
release.
