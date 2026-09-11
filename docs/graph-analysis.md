# Graph dependency analysis

GraphNest provides internal Go operations for file dependencies, dependency
counts, cycles, and affected tests. `graphquery.Service` works over one selected
v2 graph generation; `graphservice.Service` adds repository authorization and a
final generation and authorization check. HTTP, MCP, CLI, and browser adapters
remain later work.

## File dependency projection

A file dependency is a distinct ordered source/target file pair backed by at
least one cross-file graph relationship. The projection includes every stored
relationship except `contains`; limiting it to `imports` or `calls` would omit
producer-resolved references, inheritance, and other real dependencies. Edges
whose endpoints resolve to the same file are excluded.

PostgreSQL pins repository, upload, schema version, and indexed commit. Each
edge endpoint is joined within that upload by both its bounded hash key and its
original occurrence bytes. Requested paths are likewise checked using the path
hash and original path bytes. Hashes narrow lookup and never replace exact byte
comparison. Unknown confidence remains eligible, matching the reference's
missing-confidence default of one; known confidence is filtered inclusively.

Results sort by original source and target paths. Parallel relationship
occurrences collapse into one pair while `References` retains their count. The
derived operations use that same projection:

- `FileDependencies(path)` returns distinct target files.
- `FileDependents(path)` returns distinct source files.
- `FileDependentCounts(paths)` counts distinct source files per requested
  target. Paths with zero dependents are omitted.
- `FileReachCounts(paths)` reports both distinct targets in `Reaches` and raw
  cross-file occurrence count in `References`. Paths with zero reach are
  omitted.
- `CircularDependencies()` reproduces the pinned depth-first traversal. Each
  back edge emits the active cycle path from the repeated file. Cycles can
  overlap, and their encounter order and path order are preserved.

## Affected tests

`AffectedTests` walks the inverse file-dependency projection from each changed
path. Its default maximum depth is five. A changed test includes itself without
traversal. Otherwise breadth-first traversal records distinct dependents, stops
expanding when it reaches a test, and returns affected tests in sorted order.
Each changed path has its own traversal visited set. The returned affected-test
and traversed-file sets deduplicate results across those per-root traversals.
Missing or unindexed paths return no affected tests and are not errors.

The default test classification recognizes `.spec.`, `.test.`, `__tests__`,
`test`/`tests`, `e2e`, and `spec` path segments. A custom filter uses the pinned
CodeGraph conversion: `**` becomes one-or-more characters, `*` excludes `/`,
`.` and the reference's selected regex punctuation are escaped, and the
resulting expression is not anchored. This preserves the reference behavior,
including its treatment of `?` as a regular-expression operator.

`TotalDependentsTraversed` counts distinct dependent files reached across all
changed roots, including affected tests. It excludes changed roots themselves.
Traversal includes non-call relationships; the managed positive fixture proves
an inheritance/import-only path from `non-call-base.ts` to
`non-call-only.test.ts`.

## Bounds and partial answers

Requests accept at most 64 paths. Confidence must be finite and between zero and
one, limits cannot be negative, and custom test filters are capped at 1 KiB.
Affected-test depth defaults to five and clamps to the configured graph-query
maximum. The configured edge ceiling applies across an affected-test request;
file-dependency operations use the same ceiling. PostgreSQL receives at most one
lookahead row beyond the effective limit and rejects larger store requests.

An `edge_limit` or `depth_limit` boundary makes an affected-test answer partial.
File operations can report `edge_limit`. Boundary depth identifies where the
stop occurred. High-level wrappers reject malformed paths, ordering, counts,
cycles, boundaries, partial flags, and generation metadata rather than exposing
untrusted backend output. Internal graph responses retain the graph query size
ceiling; high-level responses also retain the configured response-size limit and
a five-second operation deadline.

## Authority and generation consistency

Every operation selects and authorizes one repository before graph access. The
query scope contains only that repository and its exact indexed commit. All rows
must come from its active upload, and generation metadata is validated again
after the query. The high-level service then rechecks current repository,
branch, installation, and grant eligibility. Replacement, revocation,
cancellation, or an oversized response returns an error and a zero result.

The permanent parity fixture records pinned CodeGraph commit
`b9ca4b7981116909900368cc1686a1074cd4d4c1`. Its original oracle covers six
cross-file pairs, dependent/reach counts, confidence filtering, and affected
tests. A separately captured managed-source fixture has normalized fact SHA-256
`b8643057b983b9c6a26ef0c46aff983c41b3608aed48a3cabeeb4a37614a9fa0` and
supplies the positive two-file cycle and inheritance-only affected-test cases.

## Graph aggregates and unresolved evidence

The aggregate service exposes graph statistics, raw fan-in and fan-out counts,
node metrics, ambiguous referenced names, languages with exports, unresolved
name matches, top depended-on nodes, top calling files, file nodes, module
aggregation, and unresolved references by source or file. Fan counts include
every edge occurrence. Top depended-on counts distinct source nodes and excludes
`contains` and self edges. Display limits on top results use one-row lookahead
and report `row_limit`; they are never presented as global totals.

Module aggregation accepts explicit file-to-module assignments, relationship
kinds, a confidence floor, pair kinds, and a per-link pair cap. Confident,
declared, and below-floor uncertain counts remain separate, including links with
zero confident edges. PostgreSQL bounds candidate edges before Go decodes their
original v2 protobuf payloads for resolution evidence. Row or byte exhaustion
fails with `ErrQuerySize`, so incomplete scans cannot produce aggregate totals.

Unresolved-name matching checks both the recorded full name and `name_tail`.
Reference responses retain the complete original protobuf row, occurrence
identity, coordinates, path, language, status, candidates, producer, commit,
and upload generation. File results sort by zero-based line and UTF-16 column
before applying the caller's cap. Aggregate queries use the same authorization,
exact-generation recheck, five-second database deadline, and 4 MiB response
ceiling as other graph analysis operations.

The aggregate parity fixture uses the same pinned CodeGraph commit and the full
68-node, 93-edge, 13-file, six-unresolved-row corpus. It compares all stable
direct returns and the separate `0.95` uncertainty control. CodeGraph's SQLite
database/WAL byte counts and call-time `lastUpdated` are recorded as upstream
storage evidence; GraphNest reports persisted semantic counts and its own exact
generation metadata instead of relabeling those values.
