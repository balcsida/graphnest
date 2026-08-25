# Task 10 Report: End-to-end mode parity and CI gates

## Status

Complete on `feat/ladybug-graph`.

## Delivered

- Added one real PostgreSQL/LadybugDB public graph contract for embedded
  runtime ownership and the standalone `graphnest-graph` command.
- Compared REST and MCP context, impact, trace, and administrator Cypher
  results for both modes.
- Covered exact commits, category/depth boundaries, partial results, Cypher
  row limits, ordinary-token Cypher rejection, unauthorized-row suppression,
  and post-query indexed-SHA reauthorization.
- Added exact-checkout managed scanner fixtures for Go, JavaScript,
  TypeScript and TSX, Java, Kotlin, and Rust. Each fixture resolves a
  cross-file call and a language-specific relationship before flowing through
  the real graph worker, PostgreSQL, LadybugDB, REST, MCP, and graph status.
- Added minimal Make gates for the existing seven-grammar ABI smoke matrix and
  cgo scanner/indexer/graph builds plus native Ladybug dynamic-link inspection.
- Wired the native, ABI, Ladybug, and graph E2E gates into CI without adding a
  second harness or workflow framework. Existing race, static, security,
  PostgreSQL, E2E, Compose, OpenAPI, and Helm jobs remain.
- Added workflow-level cancellation for superseded runs.

## RED evidence

The fixture contract was added before fixture files:

```text
go test -tags=e2e ./test/e2e \
  -run '^TestGraphLanguageFixturesResolveCrossFileCalls$' -count=1

0 passed, 7 failed
graph_test.go:47: invalid scanner request
```

After adding the first fixture drafts, the real resolver rejected five
cross-file calls. The fixtures were corrected to use the resolver's actual
language import/module rules; no production resolver change was made.

The first real PostgreSQL/Ladybug public contract run failed correctly:

```text
make postgres-integration

TestGraphPublicContractMatchesRuntimeModes/embedded:
cypher status=409 want=200
{"error":{"code":"ambiguous", ...}}

TestGraphPublicContractMatchesRuntimeModes/separate:
cypher status=409 want=200
{"error":{"code":"ambiguous", ...}}
```

The Cypher request was corrected to select repository `101`, matching the
public contract when multiple authorized repositories exist. A later RED
showed the hand-derived depth-boundary setup used the same public and backend
depth cap. The backend cap was set to one while the public cap remained two,
which exercises an actual `depth_limit` boundary.

The first native-link gate built all three binaries but failed because Darwin
records `@rpath/liblbug.0.dylib`, not the installer symlink name
`liblbug.dylib`. The inspection now verifies the actual `liblbug` load entry.

## Focused GREEN evidence

```text
go test -tags=e2e ./test/e2e \
  -run '^TestGraphLanguageFixturesResolveCrossFileCalls$' -count=1
Go test: 7 passed in 1 packages

go test -tags='integration system_ladybug' ./test/integration \
  -run '^TestGraphPublicContractMatchesRuntimeModes$' -count=1
Go test: 3 passed in 1 packages

go test -tags='e2e system_ladybug' ./test/e2e \
  -run '^TestGraphLanguageFixturesReachRESTAndMCP$' -count=1
Go test: 1 passed in 1 packages

make native-link-test abi-test ladybug-test
@rpath/liblbug.0.dylib (current version 0.18.3)
@rpath/liblbug.0.dylib (current version 0.18.3)
TestGrammarMatrix: PASS
Ladybug/command/query/runtime packages: PASS
```

## Complete local gate

Host: Darwin/arm64, Go 1.26.5, LadybugDB 0.18.3, Helm 4.2.3.

```text
make fmt lint test-race build staticcheck govulncheck
```

- `fmt`, `lint`, `test-race`, and `build`: PASS.
- The first `staticcheck` install attempt failed because stale proxy variables
  resolved `ce2.proxy.tesco.org` unsuccessfully.
- Retried the remaining pinned tools with upper- and lower-case
  HTTP(S)/ALL proxy variables cleared:

```text
make staticcheck govulncheck
No vulnerabilities found.
```

```text
make postgres-integration
internal/postgres: PASS
internal/authz: PASS
internal/webhook: PASS
test/integration: PASS
cmd/graphnest-indexer: PASS
```

This includes the new embedded/standalone public contract and the pre-existing
actual indexer/standalone internal four-route parity test.

```text
make ladybug-test
PASS

make e2e
exit 0; PostgreSQL healthy; full E2E suite PASS

make compose-test helm-lint helm-test openapi-check
1 chart(s) linted, 0 chart(s) failed
helm render tests passed
OpenAPI validation passed

GOCACHE=$PWD/.cache/go-build XDG_CACHE_HOME=$PWD/.cache \
  go mod tidy -diff
(no diff)

GOCACHE=$PWD/.cache/go-build XDG_CACHE_HOME=$PWD/.cache \
  go mod verify
PASS

git diff --check
PASS
```

The first unqualified `go mod tidy -diff` attempt used the sandbox-inaccessible
user Go cache. Re-running with the repository-local cache passed without
changing module files.

## Native and CI evidence

- Darwin/arm64 was executed locally. The pinned archive checksum had already
  been enforced by the shared Make installer. Scanner, indexer, and graph
  binaries built with cgo. `otool -L` resolved LadybugDB for indexer and graph
  as `@rpath/liblbug.0.dylib`, current version 0.18.3.
- Linux/x86_64 is encoded but was not executed locally. CI uses the existing
  pinned `liblbug-linux-x86_64.tar.gz` SHA-256 and requires `ldd` to resolve
  `liblbug` from the pinned Make directory.
- The existing Compose and Helm tests render embedded and separate modes,
  require one writable graph owner, and reject public graph ingress. No
  duplicate render harness was added.
- `actions/setup-go` remains the pinned Go/module-cache boundary. No uploaded
  build artifact or cross-job dependency was added because each independent
  job verifies its own pinned native input and no later job consumes a build
  output from `verify`.
- Workflow concurrency cancels superseded runs for the same workflow/ref.

## Commits

```text
4a4d7164d872dfe4c2f49eb1ca0935ffd1e67c02 G test(graph): verify graph analysis
16b4cc90d08f358d80423239a753409ee8324194 G docs: record graph verification
```

Both commits were signed with the configured 1Password-backed ED25519 key.
Signing succeeded while the user was present; no unsigned fallback was used.

## Self-review

- Real backend: PostgreSQL is the snapshot authority and LadybugDB is queried
  through the real runtime/client path; no graph backend mock is used.
- Public parity: REST and MCP compare normalized structured responses for all
  four query operations in both runtime modes.
- Languages: Go, JavaScript, TypeScript, TSX, Java, Kotlin, and Rust all parse;
  JavaScript is independent from TypeScript, and both `.ts` and `.tsx` are
  asserted.
- Exact expectations: fixture call pairs, commits, statuses, limits, boundary
  reasons/depths, trace nodes, Cypher rows, and authorization outcomes are
  literal or hand-derived.
- Exact checkout: each language fixture is committed to a temporary Git
  repository and the production graph worker scans a detached worktree at the
  exact queued SHA.
- Security: ordinary REST and MCP callers cannot run Cypher; ordinary graph
  queries cannot observe the unauthorized repository's symbol or commit.
- Freshness: changing `indexed_sha` after the backend result but before public
  reauthorization returns `graph_not_ready` instead of stale data.
- Status and bounds: managed graph status is `ready`; context category limits,
  impact depth/partial behavior, and Cypher row truncation are asserted.
- Cleanup/cancellation: runtime contexts are canceled and awaited, graph
  databases are closed, scanner worktrees are removed, and PostgreSQL schemas
  use existing bounded cleanup.
- Skill installation: existing race coverage for `internal/agentskills` and
  `cmd/graphnest-mcp` remained green; Task 7's idempotent install and no-write
  proxy behavior was not reimplemented.
- No production graph code, new testing framework, dependency, speculative
  platform matrix, public graph endpoint, or live deployment claim was added.

## Concerns

- Linux/x86_64 native provisioning and `ldd` inspection are CI-encoded but
  unexecuted on this Darwin/arm64 host.
- Compose and Helm verification is render/configuration-only. No production
  image, live cluster, OpenShift deployment, storage-class recovery, or public
  registry artifact was exercised or claimed.

## Review round 1: CI and native-link hardening

### Reproducible Ladybug commands

All Ladybug-focused commands in this report run with this repository-local
environment (the PostgreSQL DSN is required by graph integration/E2E tests):

```sh
export CGO_ENABLED=1 LBUG_VERSION=0.18.3
export GOCACHE="$PWD/.cache/go-build" XDG_CACHE_HOME="$PWD/.cache"
export DYLD_LIBRARY_PATH="$PWD/.cache/ladybug/v0.18.3"
export CGO_CFLAGS="-I$PWD/.cache/ladybug/v0.18.3"
export CGO_LDFLAGS="-L$PWD/.cache/ladybug/v0.18.3"
export GRAPHNEST_TEST_POSTGRES_DSN='postgres://graphnest:graphnest@192.168.107.2:5432/graphnest?sslmode=disable'

go test -v -tags='e2e system_ladybug' ./test/e2e \
  -run '^TestGraphLanguageFixturesReachRESTAndMCP$' -count=1
make native-link-test abi-test ladybug-test
```

`DYLD_LIBRARY_PATH` is the Darwin runtime setting used for this host; the
Makefile selects `LD_LIBRARY_PATH` for Linux. The DSN host is compose-derived,
so rerun `make e2e` or `make postgres-integration` when it changes.

### RED/GREEN evidence

```text
# RED: the old inspection loop returned the last successful pipeline status.
for binary in missing-binary graphnest-graph; do \
  otool -L .cache/native/$binary | rg 'liblbug'; done
error: ... can't open file: .cache/native/missing-binary
@rpath/liblbug.0.dylib ...
reproduction-exit=0

# GREEN: set -e now stops either platform loop; Darwin also asserts rpath.
make native-link-test
@rpath/liblbug.0.dylib (current version 0.18.3)
path .../.cache/ladybug/v0.18.3 (offset 12)
@rpath/liblbug.0.dylib (current version 0.18.3)
path .../.cache/ladybug/v0.18.3 (offset 12)

make makefile-test
PASS

GOFLAGS=-definitely-invalid make test
go: parsing $GOFLAGS: unknown flag -definitely-invalid
make: *** [test] Error 1
expected-red: invalid GOFLAGS propagated
```

### Covering checks

```text
go test -v -tags='e2e system_ladybug' ./test/e2e \
  -run '^TestGraphLanguageFixturesReachRESTAndMCP$' -count=1
PASS (real PostgreSQL -> LadybugDB -> REST/MCP; persisted Go, JavaScript,
TypeScript/TSX, Java, Kotlin, and Rust fixture facts)

make makefile-test native-link-test abi-test ladybug-test
PASS

make fmt lint test-race build
PASS

env -u HTTPS_PROXY -u HTTP_PROXY -u ALL_PROXY -u https_proxy -u http_proxy \
  -u all_proxy make e2e
TestGraphLanguageFixturesResolveCrossFileCalls: PASS
TestGraphLanguageFixturesReachRESTAndMCP: PASS
TestMilestone2Vertical: FAIL, job=queued/git_failed
```

`make e2e` was rerun cleanly after clearing stale proxy variables and failed
the same unrelated `TestMilestone2Vertical` fixture twice. The graph REST/MCP
test passed in both full-suite attempts and in the focused real-backend run;
no change in this round touches the vertical fixture path.
