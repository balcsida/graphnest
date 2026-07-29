# Ladybug graph branch-final fix report

## Fixes

- `3a878d77b6a016ca56b392a804e1fd03f145b87b` joins one shared Tree-sitter cancellation watcher before parser close and transfers only the completed tree.
- `4f92442eb5c19d91ca461a8c7eff09b4be0ce1e9` carries Go interface/value-method evidence into package-wide resolution, including embedded interfaces and pointer/package exclusions.
- `5e561f225919fb0c2b4e921ede80c9496b1af177` routes query, transaction, and schema statements through one interruptible executor and rolls back every uncommitted panic, cancellation, deadline, or error exit.
- `0c42e889574c9629258b83700072d1351735b4cd` maps all public graph symbols, relationship endpoints, and boundaries through the authorized internal-to-GitHub snapshot map and rejects unknown backend IDs.
- `1112ee6e8f66e5b38306af38d50f5b9f9b7a620f` avoids reading an interrupted query result until its native worker has completed.
- `9fa5aad6df131af76335748b002b025aecafa814` keeps rebuild schema work on the real transaction session and makes the raw-connection compatibility helper join its tracked native worker before returning.
- `3c03f2908311dc8e926d66a5c43e6246addb5965` asserts exact relationship repository IDs and logical UIDs for downstream impact, upstream impact, trace, and reversed service mapping.
- `3618a299affbb79d437772e895849fd0e1c6a182` makes schema statements honor their owning session timeout and removes timer-dependent rebuild coverage.

All eight committed fixes have good SSH signatures from ED25519 key `SHA256:WjfLjYSGqwAvhKk36hJZdaFyPAyKJcSxfoien1VavOU`.

## RED evidence

- `go test ./internal/graphscan/rust -count=20` crashed with `SIGSEGV` in `go-tree-sitter@v0.24.0/parser.go:168`; the every-adapter canceled/deadline regression crashed at the same watcher.
- The real Go parser-to-resolver split-file test omitted `IMPLEMENTS`; an embedded-only interface also omitted the transitive implementation edge.
- Canceled `Update` and canceled schema execution returned nil.
- Reader and writer callback panics left `Connection already has an active transaction`.
- Cross-repository context, impact, and trace exposed internal IDs `1` and `2`, plus an internal boundary name, instead of GitHub IDs `101` and `202`.
- Native race verification found the interrupted result variable read concurrently with the native query worker at `internal/ladybug/query.go:45`.
- Reverting rebuild to the raw `EnsureSchema` call made `TestRebuildOwnsInterruptedSchemaExecution` reach the missing `GraphMetadata` table instead of the real database's interrupt-grace error.
- Disabling upstream relationship reversal made the graphquery impact regression report reversed repository IDs and logical UIDs.

## GREEN evidence

- Parser crash stress passed at count 50; every registered extension passed repeated canceled/deadline cleanup at count 20.
- Scanner language matrix, ABI, scanner race, package-wide Go interface regressions, and all graphscan packages passed.
- Repeated cancellation/deadline/schema/panic tests, `make ladybug-test`, and Ladybug/query/runtime race tests passed.
- Graph service, REST, and MCP race tests passed; impact both directions and trace assert public repository IDs and logical UIDs.
- The deterministic rebuild ownership regression passed ten consecutive runs; focused rebuild/schema/close interruption tests passed under `-race`.
- `GOFLAGS=-count=1 make ladybug-test` passed Ladybug, graph command, graphquery, and graph runtime packages.
- Ladybug, graph command, graphquery, graph runtime, and graph service all passed together under `go test -race -count=1`.
- A fresh proxy-cleared `make govulncheck` completed with `No vulnerabilities found.`
- `make postgres-integration` passed all PostgreSQL, authorization, webhook, public integration, and indexer packages.
- Full E2E passed all language fixtures, REST/MCP graph paths, Milestone 2 vertical flow, cross-repository SCIP, search, and process tests.
- `make fmt lint test-race build staticcheck`, OpenAPI, Compose, Helm lint/render, `go mod tidy -diff`, `go mod verify`, and `git diff --check` passed.

## Remaining concern

Transient 1Password signing-agent failures cleared while the user was present; no unsigned commit was created. No push was performed.
