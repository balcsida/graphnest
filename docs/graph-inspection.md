# Indexed graph inspection

`graphservice.Service` provides shared Go operations for a selected, authorized
v2 repository. REST/MCP adapters remain S1.07. These operations use PostgreSQL
file/entity projections, the existing entity traversal, and
`repository.Service.ReadFileAt`; they never load a whole graph or call an internal
HTTP/MCP endpoint.

## Operations

- `ListFiles(ListFilesRequest)` lists original indexed file facts, including
  files without entities, generated files, language, size, hash and producer
  metadata. Results are ordered by original path bytes. `Directory` includes
  descendants; `Glob` is a case-sensitive, repository-rooted pattern supporting
  `*`, `?`, character classes and whole-component `**`. Both filters intersect.
  `./` and redundant separators normalize; absolute paths, parent components,
  backslashes and NUL are refused. Paths in returned facts remain unchanged.
- `InspectFile(InspectFileRequest)` returns original file metadata, a paged
  entity outline, and original `contains` relationships rooted at the file
  entity. The structure has the existing traversal limits, including a maximum
  depth of 32. Missing or ambiguous file containers are explicit when entities
  exist. Files without entities remain useful file/source answers.
- `InspectEntity(InspectEntityRequest)` accepts occurrence, name or qualified-name
  selectors, with existing path/kind narrowing. It preserves every overload and
  returns `ambiguous` until the caller selects one occurrence. Subsequent pages
  remain ambiguous even when their last page contains one entity. A unique
  result includes incoming/outgoing `calls` neighborhoods and source context for
  the root and bounded related entities. Repeated edge occurrences, original
  edge direction, optional confidence and producer provenance remain intact.

File, outline and ambiguous-entity pages expose generation-bound `NextCursor`
values. Reuse the same selector, limits and repository scope on the next call.
Changing the active upload or indexed commit invalidates the cursor. Directory
structure is represented by original relative paths; file declaration structure
is represented by the `contains` graph, rather than inferred from names.
Every outline continuation remains incomplete with an `outline_page` boundary,
including the final page whose `NextCursor` is empty. Valid facts and source are
still returned on that page.

## Exact source and uncertainty

`InspectionSource.Content` is verbatim whole-line source at `IndexedSHA`, without
inserted line numbers or normalized line endings. `StartLine`/`EndLine` are
one-based inclusive display bounds. Unicode and CRLF survive unchanged.

`Range` retains the producer's zero-based, end-exclusive UTF-16 coordinates.
When both endpoints are present and valid, `Selection` supplies end-exclusive
UTF-8 byte offsets **inside Content**, computed with `graphartifact.SourceOffset`.
This retains useful declarations such as the `export` prefix omitted by the
producer's narrower selection, while preserving precise entity coordinates.
No duplicate source string is needed. Consumers can render line numbers using
the supplied bounds. Absent coordinates remain absent; line-only evidence uses
`partial_range`, never invented character positions.

Source statuses distinguish `ok`, `partial_range`, `missing_range`,
`virtual_entity`, `file_not_indexed`, `source_unavailable`, `unreadable`,
`oversized`, `binary`, `invalid_range`, `truncated` and `budget_exhausted`.
Backend error messages are not exposed. A failed source read cannot produce a
complete answer. Indexed-SHA mismatch and operation cancellation refuse the
entire buffered response. There is no current-working-tree fallback.

`Coverage` is `indexed_file` or `indexed_neighborhood`. `Complete` never asserts
whole-program semantic completeness: it is false for ambiguous/missing results,
omitted/failed source, paging, traversal/source limits, partial coordinates, or
known unresolved analysis/diagnostics. Generation-wide unresolved/diagnostic
counts are retained as provenance and explicitly labeled generation-wide
boundaries, rather than attributed to a particular entity without evidence.
Generated, ambient, virtual and external entities are retained on explicit
inspection; discovery downranking does not hide an explicitly selected entity.

Original file extraction errors are independent of generation diagnostics.
File inspection retains them in `File.Fact.Errors`; entity source records retain
their contributing file's original namespace and JSON in `FileErrors`, even
when source is unreadable, lacks a range or exceeds the source budget. A nonempty
error list adds one `file_extraction_errors` boundary and prevents completeness
without discarding valid source. An absent, null or empty error list adds no
failure boundary; unknown producer error shapes are retained conservatively as
analysis uncertainty. The existing final JSON limit also bounds this evidence;
oversized evidence refuses the result rather than silently dropping the errors.

## Authority and consistency

Each operation resolves authorization before graph capture. Only the selected
repository enters the v2 scope, so an unrelated authorized v1 repository cannot
invalidate it. Original graph responses must belong to that repository; hidden
lookahead rows cannot influence paging metadata. Every graph step must report
the same immutable upload and indexed commit.

After all graph and source work, the shared completion path checks serialization
size, calls `graphquery.Service.ValidateGenerations` against the active uploads
and indexed SHA, and reauthorizes repository/branch/installation eligibility.
Failure returns a zero result, including no paths, source, counts or provenance.
S1.05c can reuse these same package-local composition helpers.

Legacy symbol `Context` also captures the actual manifest upload IDs before
querying and revalidates them after source reads, followed by final
reauthorization. This closes same-SHA replacement and grant/SHA drift during
source I/O. Snapshot state is internal (`json:"-"`); v1 and existing public JSON
remain supported. Production wiring uses `graphquery.Service`, which implements
the validator. Older injected query engines lacking the optional validator keep
their prior generation guarantees, with the new final repository recheck.

Credentials are authenticated by the request adapter. `authn.Principal` contains
no credential ID or expiry, so this domain cannot reauthenticate or detect
mid-operation credential expiry/revocation. It does recheck live repository and
installation eligibility. Credential revalidation is an explicit S1.07/S1.08
integration acceptance question; it is not claimed as inspection coverage.

## Bounds

The whole operation has a five-second context deadline. File/outline/entity
pages default to and are capped at 100 rows (lower engine limits still apply),
with one lookahead row and 512-byte cursors. Paths/selectors are bounded by the
existing 16-KiB query contract; file globs are capped at 1 KiB. Cursor offsets
cannot exceed 10 million. Broad directory/glob queries may scan projected paths
within the deadline; only bounded page payloads cross the store boundary.

Entity inspection reads at most 20 source slices, root first, with related entity
IDs deduplicated within the call. Source defaults to 64 KiB total and is capped
at 256 KiB. A whole-line slice that does not fit is explicitly refused; adaptive
allocation belongs to S1.05c. The repository reader separately enforces its
one-MiB file ceiling and default 1,000 returned lines per read. Query batches and
intermediate graph responses retain the four-MiB entity-query bound; final JSON
is capped by `graphservice.Limits.MaxResponseBytes` (default/max 256 KiB).
Serialization over budget refuses the whole answer rather than silently losing
required identities or source. No session content or cross-call cache is stored.

## Executed evidence

`TestGraphInspectionRealOracle` requires the real pinned producer export and
PostgreSQL. It compares all 13 original file facts, three verbatim file bodies
and outlines/structures, `lib-getCode`/`lib-getContext` identity and source,
`ui-source-verbatim`, every immediate caller/callee occurrence, and exact
UTF-16 selections in related source. The source gateway serves committed fixture
bytes and verifies every requested immutable commit; it does not claim a live
GitHub network fetch. Real generation replacement, SHA changes and repository
revocation exercise the final boundary. Focused hostile tests cover file-only
and generated retention, overload paging, virtual/ambient/partial locations,
Unicode, oversized/unreadable/truncated source, budgets, cancellation, hidden
lookahead and the legacy same-SHA source-return race.

Run the normal PostgreSQL gate with only the test database DSN:

```sh
GOTOOLCHAIN=go1.26.6 GOWORK=off GRAPHNEST_REQUIRE_POSTGRES=1 \
  GRAPHNEST_TEST_POSTGRES_DSN="$GRAPHNEST_TEST_POSTGRES_DSN" make postgres-test
```

The target exports a fresh private fixture from the committed SQLite oracle
before running the PostgreSQL packages, then removes it on success, failure or
signals. It ignores and never modifies a caller's
`GRAPHNEST_TEST_CODEGRAPH_V2_FIXTURE`.
