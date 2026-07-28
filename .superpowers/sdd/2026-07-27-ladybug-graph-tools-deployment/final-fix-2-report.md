# Final Review Fix Wave 2 Report

## Scope and RED evidence

- Missing and empty raw Cypher scope reached the authenticated backend and
  returned HTTP 200. The focused transport test reported three failures, and
  the direct Ladybug query test returned a nil error for an unscoped query.
- Duplicate UIDs in two authorized repositories made selected-repository
  context, impact, and trace requests ambiguous. The three focused Ladybug
  tests returned candidates from repository IDs 101 and 202 instead of the
  selected repository result.
- Public ambiguity candidates serialized without repository identity. The API
  contract test failed for context, impact, and trace; three service tests also
  showed a zero public repository ID or accepted an out-of-scope candidate.
- The OpenAPI checker failed with
  `GraphCandidate must require repository_id`.
- A pressure test of the four installed graph skills found every ambiguity
  path instructed retry by UID alone.
- Scoped round 1 added real cross-repository edges after endpoint anchoring.
  Impact returned `found` with empty depth results, and trace returned
  `no_path`, proving the shared traversal predicate was false-closed.

## Fixes

- Made raw Cypher scope mandatory in the protocol, transport, and Ladybug query
  boundary. Missing, empty, malformed, or stale unauthorized scope now fails
  before statement execution, with a second manifest authorization check after
  execution.
- Restricted context, impact, and both trace endpoint lookups to the selected
  repository. Scoped round 1 changed the shared traversal predicate so the
  frontier identity stays exact while the next endpoint may belong to any
  authorized exact-SHA repository.
- Added only `repository_id` to public candidates. It is the authorized GitHub
  repository ID accepted by `repo`, mapped from the backend internal ID through
  the current authorized snapshot map. Unknown backend repository IDs fail
  closed.
- Updated REST/MCP shared contracts, OpenAPI, and all four graph skills to
  retry with `(repository_id as repo, uid)`, never UID alone.
- Corrected the first report: `validRelations` creates its allowlist per call,
  not statically.

## GREEN evidence

- Focused transport and Ladybug RED tests passed after each minimal fix.
- `go test -race ./internal/graphtransport ./internal/graphclient ./internal/graphservice ./internal/httpapi ./internal/mcpserver ./pkg/api -count=1`
  passed 303 tests.
- `make ladybug-test` passed.
- `make postgres-integration` passed, including the public stale unauthorized
  manifest contract and embedded/standalone internal parity.
- `make e2e` passed after retrying without stale proxy environment variables.
- `make fmt lint test-race build` passed.
- `make staticcheck govulncheck` passed after retrying tool installation
  without stale proxy environment variables; no vulnerabilities were found.
- `make compose-test helm-lint helm-test openapi-check ladybug-test` passed.
- `go mod tidy -diff`, `go mod verify`, and `git diff --check` passed.
- The post-change skill pressure test found no remaining UID-only ambiguity
  retry instruction.
- `go test -race -tags=system_ladybug ./internal/graphquery -count=1`
  passed with selected-repository endpoint anchoring, authorized
  cross-repository impact in both directions, cross-repository trace, and
  non-scope repository exclusion.
- Scoped round 1 reran `make postgres-integration`, `make e2e`,
  `make fmt lint test-race build staticcheck govulncheck ladybug-test`,
  `make openapi-check`, `go mod tidy -diff`, `go mod verify`, and
  `git diff --check`; all passed and no vulnerabilities were found.

## Signed commits

- `95b1a22` `fix(graph): require raw query scope`
- `da74744` `fix(graph): anchor selected repository`
- `2830c42` `fix(graph): qualify ambiguity candidates`
- `9646490` `test(graph): cover stale public query scope`
- `fd8aebd` `fix(graph): allow authorized cross-repo traversal`

Each commit verifies as a good ED25519 signature for
`SHA256:WjfLjYSGqwAvhKk36hJZdaFyPAyKJcSxfoien1VavOU`.

## Remaining concerns

- No live Kubernetes/OpenShift deployment was run; Helm verification remains
  lint and deterministic render coverage.
- Raw Cypher deliberately remains unavailable while any database manifest is
  outside the caller's current authorized exact-SHA scope.
