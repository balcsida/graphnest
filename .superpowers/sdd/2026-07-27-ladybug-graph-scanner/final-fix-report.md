# Ladybug scanner final-fix report

## Scope

Base: `21c1f47e07d90187b4e80a5664815f0ee7df03b9`

This final wave addressed only the five load-bearing review findings:

1. enforce graph IR budgets while language adapters traverse and allocate;
2. scope resolution by language, file, module, and imports;
3. emit stable Java and Kotlin overload signatures;
4. serialize mutation of each shared bare mirror across processes;
5. fetch an exact SHA directly when the default-branch fetch does not contain it.

The deferred graph-job retention/indexing, malformed-artifact classification,
and duplicate AskPass validation findings remain untouched. Cross-file Go
implicit-interface inference was not adjacent to these root-cause changes and
was not added.

## Tests-first evidence

- Initial focused parser/resolver run: 1 passed and 5 failed. The expected RED
  cases were parsed Go and Java two-file import resolution, package-qualified
  Go call ordering, and missing Java/Kotlin overload signatures.
- The mirror/fallback focused run failed to compile because `lockMirror` did
  not exist, establishing the per-mirror serialization boundary before its
  implementation. After the lock was added, the rewritten-branch exact-SHA
  fixture exercised the fallback and passed.
- The scanner allocation regression uses the real `Scan` budget context and a
  parser that attempts 100 declarations. It proves allocation stops on the
  second attempt when only one declaration remains, rather than returning a
  fully allocated IR for a post-parse rejection.
- Green focused result: 110 tests passed across `internal/graphscan/...` and
  `internal/indexer`.

## Files and behavior

- `internal/graphscan/budget.go`, `scan.go`, and every language adapter now
  reserve declaration node/containment-edge and import/reference/heritage edge
  capacity before appending IR. Adapters stop descending after exhaustion and
  return `ErrLimitExceeded`.
- `internal/graphscan/resolve.go` now scopes source local IDs to language and
  file, restricts bare matching to the current file/module, translates explicit
  and wildcard imports, resolves imported files, preserves deterministic
  candidate order, and avoids cross-language or global bare-name binding.
- Go selector candidates now place the package/member form before the bare
  member. Real Go and Java parser-to-resolver two-file tests cover imports,
  calls, and import edges.
- Java and Kotlin methods/functions now carry normalized parameter/return
  signatures. Their local IDs include the signature, so overloads remain
  distinct while canonical qualified names stay stable.
- `internal/indexer/git.go` uses an advisory per-mirror file lock around bare
  mirror configuration, fetch, worktree addition, and mirror pruning. The lock
  is a no-follow regular file and waiting honors context cancellation.
- When the branch fetch lacks the requested commit, checkout performs one
  targeted `fetch origin <sha>` and verifies the object before adding the
  detached worktree.

## Commits and signatures

- `86e57ef1d143ec81ecf45a98d55cbf9feb26446e`
  `fix(scanner): enforce analysis boundaries`
- `805d40ae0ff834da973bdf5c1d2b8527c1145b4b`
  `fix(indexer): serialize exact checkouts`

Both commits have good SSH signatures from ED25519 key
`SHA256:WjfLjYSGqwAvhKk36hJZdaFyPAyKJcSxfoien1VavOU`.

## Verification

- Focused: `go test ./internal/graphscan/... ./internal/indexer -count=1`
  passed with 110 tests.
- Full requested suite:
  `make fmt lint test-race build openapi-check scanner-test` passed.
- `go mod tidy -diff` passed with no output.
- `git diff --check` passed.

The Go cache was placed under `/private/tmp` because the managed sandbox cannot
write the default macOS Go build cache. Git HTTP tests were run with permission
to bind loopback ports.

## Concerns

No known blocker remains in the requested final-fix scope. Import resolution is
deliberately heuristic where source files do not expose a full build-system
module graph; ambiguous candidates remain unresolved instead of being guessed.
