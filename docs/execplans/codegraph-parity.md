# CodeGraph parity execution record

This is the living record for the accepted [CodeGraph-only roadmap](codegraph-parity-plan.md).
Implementation, validation, draft publication, and release are separate states.

## Progress

- 2026-09-06: Read the accepted roadmap and repository instructions; inspected the current default branch and existing pull requests.
- Created an isolated checkout at `../graphnest-codegraph`; preserved the original checkout's modified `go.work.sum`.
- Initialized the local native-stack metadata with `github/gh-stack` v0.1.1: `main ← feat/codegraph/s1-01-contract`.
- S1.01 reference foundation is implemented and reviewed: inventory, pinned
  real-producer reference harness, fixtures, and explicitly scoped performance
  measurements. Full S1.01 acceptance remains pending the gaps below.
- S1.02–S1.10 are pending. Stage 1 has not passed its release gate.
- Stages 2 and 3 are pending and cannot start until the preceding stage has passed and landed.

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

## Discoveries

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

Regeneration now builds a fresh archive of the verified pinned commit with
locked dependencies rather than trusting checkout `dist/` or `node_modules/`.
The producer runs with a recorded environment and empty temporary HOME so local
feature flags or global configuration cannot change the reference. Explicit
regeneration needs dependency access and Python 3.12+; the committed-reference
CI check remains offline and uses only Python's standard library.

## Remaining gaps

- S1.01 acceptance is pending: inventory rows without real workflow fixtures,
  and representative PostgreSQL/transport/browser latency baselines remain
  outstanding. Current reference
  checks cover the committed fixture; they do not turn planned rows into passes.
- Native/portable coordinate conversion assertions belong to S1.02; GraphNest
  query implementations and their parity comparisons belong to subsequent
  layers, and are not circular prerequisites for S1.01.
- All production parity, v2 artifact/storage, publication policy, browser parity,
  CLI import, and local-engine work remains pending.
- Full Stage 1 validation (including authorization, database, browser, deployment,
  and real-producer conformance) has not run and is not claimed as passing.
- The proposed warm-query p95 budgets remain unchanged: existing GraphNest within
  10%; future GraphNest local within 1.25× pinned CodeGraph on equivalent tasks.
  Neither budget is a measured result.

## Native stack and pull requests

| Stage/layer | Branch | Local state | Remote stack / PR |
| --- | --- | --- | --- |
| S1.01 reference foundation | `feat/codegraph/s1-01-contract` | Implemented; reviewed | Not submitted |

Remote membership, exact head/base, each layer's delta, and actual required checks
must be read back after submission. Draft publication alone is not approval or
release.
