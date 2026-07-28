# Ladybug runtime final-fix report

Base: `133b4e6f714744d3e9d7eb5c3da474cd4ffc50e8`

## 1. Reproducible native normal paths

RED:

- `make build` selected go-ladybug's bundled path and failed at `lbug.h`.
- `make staticcheck` failed at the same bundled native compile.

GREEN:

- One shared Make environment supplies `system_ladybug`, CGO, pinned v0.18.3
  include/link paths, runtime library lookup, writable caches, and build rpath.
- `build`, `test`, `test-race`, `lint`, `staticcheck`, `govulncheck`, `server`,
  and `ladybug-test` use the shared native contract.
- A fresh `/private/tmp/grepnest-ladybug-final.vJlJ5h` provision downloaded the
  v0.18.3 Darwin/arm64 archive, verified SHA-256
  `f626987fe10f6520146793575677d004962b4c6a0dea71cbbca75e73ab673622`,
  and passed the native Ladybug, graph query, and runtime packages.

## 2. Automatic compatibility recovery

RED:

- No persistent database/native compatibility marker existed.
- Runtime startup opened existing databases without compatibility recovery.

GREEN:

- `GraphMetadata` persists schema version `1` and native version `0.18.3`.
- Native tests distinguish absent, current, schema-mismatched, and
  native-mismatched markers without treating a fresh absent database as stale.
- Runtime closes incompatible handles, uses the existing verified
  same-directory rebuild, then opens the atomically replaced live database.
- Rebuild candidates receive current metadata before verification and swap.
- Existing native tests prove verification/load failures preserve the old live
  file. Runtime tests prove failed recovery preservation and successful startup
  rebuild from the authoritative snapshot source.

## 3. Staticcheck

RED:

- `ST1013` reported numeric HTTP status literals in graphclient tests.
- `U1000` reported an unused graphingest test sentinel.
- A deliberately discarded error used a named parameter.

GREEN:

- HTTP status constants replace the literals, the unused sentinel is removed,
  and the deliberately discarded error parameter is `_ error`.
- Repository-pinned staticcheck v0.7.0 passes with the native contract.

## 4. PostgreSQL fallback fixture

RED:

- `make postgres-integration` reproduced `SQLSTATE 57014` in
  `TestGraphManifestsUsesOneFallbackSnapshot`.
- The fixture used `scip go Old#` and `scip go New#`, which are invalid SCIP
  package symbols.

GREEN:

- The fixture now uses distinct valid SCIP package symbols.
- The focused repeatable-read fallback test and the full repository Compose
  PostgreSQL integration target pass without parser or timeout changes.

## 5. Runtime phase verification

GREEN:

- Destructive rebuild tests verify the candidate manifest set and repository
  count before atomic replacement, then compare the rebuilt live manifests.
- Embedded and standalone runtime handlers execute authenticated `context`,
  `impact`, `trace`, and `cypher` operations and compare exact status,
  content type, and response body.
- The full native race target exercises both graph-bearing commands and all
  graph packages through the pinned system library.

## Signed commits

All commits have a good ED25519 signature from
`SHA256:WjfLjYSGqwAvhKk36hJZdaFyPAyKJcSxfoien1VavOU`.

- `94c802281b90d4772c0378da871a5fa3c8f2fea7` —
  `fix(graph): pin native build tooling`
- `18a685cede82df0de7dcd31cea82dae5387693d3` —
  `test(graph): use valid SCIP fallback symbols`
- `2355e5f221c43acebf00bde6836fcad2cf1f8c4b` —
  `fix(graph): rebuild incompatible databases`
- `a01bd8a45be7eeae83652e3d2e2c782492ea5a44` —
  `fix(graph): pin native vulnerability scan`

## Verification

- `make ladybug-test` — pass
- Clean temporary v0.18.3 provisioning plus `make ladybug-test` — pass
- Focused compatibility/recovery native tests — pass
- Authenticated four-operation embedded/standalone parity test — pass
- `make fmt lint test-race build staticcheck` — pass
- Focused uncached native race run — pass, 111 tests in five packages
- `make postgres-integration` — pass
- `go mod tidy -diff` with repository-local `GOCACHE` — pass, no diff
- `go mod verify` — pass, all modules verified
- `make govulncheck` — pass, no vulnerabilities found
- `git diff --check` — pass

## Residual concerns

- The deferred minor duplicate-occurrence permutation test remains ledgered and
  unchanged, as requested.
- Linux/x86_64 provisioning is encoded with its pinned archive checksum and
  shared native contract; this final-fix run executed on Darwin/arm64.
