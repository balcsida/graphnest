# Final Review Fix Report

## Scope and RED evidence

- Raw administrator Cypher executed against the complete Ladybug database.
  A real two-repository Ladybug test returned the expected RED because no
  authorization error existed while a stale unauthorized manifest remained.
- Graph lookup scope carried every authorized repository but no selected
  repository anchor. New context, impact, and trace tests failed because the
  protocol had no anchor or candidate fields.
- Impact merged duplicate logical UIDs and trace chose one path. New backend
  and public-service tests failed until `ambiguous` responses carried exact
  candidates.
- `make e2e` failed in `TestMilestone2Vertical` with
  `queued/git_failed`. Bounded diagnostic output identified dyld aborting the
  Ladybug-linked `grepnest-indexer` askpass binary because its build lacked the
  Ladybug runtime rpath. The diagnostic changes were reverted before the fix.
- MCP schema tests failed on `anyOf` and minimum `1`. The Helm render test
  failed until graph/scanner scheduling assertions were scoped to their own
  Deployments.

## Fixes

- Added a selected repository ID to the existing authorized exact-SHA scope.
  Root symbol lookup is anchored there while trace targets and traversal retain
  the authorized cross-repository scope.
- Added impact/trace candidates and stopped traversal when lookup is ambiguous.
- Raw Cypher now rejects any database manifest outside the current authorized
  exact-SHA scope both before and after arbitrary statement execution. No
  statement rewriting or query-shape heuristic was added.
- Built the real E2E indexer/askpass binary with the configured native library
  rpath.
- Aligned MCP selector XOR and zero-default sentinels with REST/OpenAPI.
- Applied all valid review minors: per-call relation allowlist, guide wording,
  shared Compose rendering, scoped Helm scheduling checks, rejected-source
  closure, and Compose-versus-Helm secret wording.

## Verification

- `go test -race ./internal/graphservice ./internal/graphquery ./internal/graphtransport ./internal/graphclient ./internal/mcpserver ./internal/httpapi ./internal/secretstage -count=1`
- `make e2e`
- `make fmt lint test-race build staticcheck govulncheck`
- `make postgres-integration ladybug-test`
- `make compose-test helm-lint helm-test openapi-check`
- `go mod tidy -diff`
- `go mod verify`
- `git diff --check`

All commands passed. `govulncheck` reported no vulnerabilities.

## Signed commits

- `48313a1` `fix(graph): authorize graph query scope`
- `115fc3c` `fix(mcp): align graph input schemas`
- `37d1208` `fix(graph): close rejected secret sources`
- `7087f0d` `fix(test): link native indexer askpass`
- `ce3f7b1` `test(compose): reuse graph render setup`
- `bc8dd52` `test(helm): verify graph scheduling overrides`
- `fc12eb5` `docs: correct graph guidance`
- `cd6868d` `fix(graph): expose anchored ambiguity`

Each commit verifies as a good ED25519 signature for
`SHA256:WjfLjYSGqwAvhKk36hJZdaFyPAyKJcSxfoien1VavOU`.

## Remaining concerns

- No live Kubernetes/OpenShift deployment was run; Helm verification remains
  lint and deterministic render coverage.
- Cypher deliberately fails closed while stale unauthorized manifests remain.
  It becomes available again after synchronization removes them.
