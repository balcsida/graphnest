# Semantic discovery over v2 generations

`graphquery.Service.Discover` is an internal, trusted-scope operation. It consumes
an already authorized `graphprotocol.Scope`, resolves immutable v2 generations,
and checks those generations again before returning. Repository IDs in results
are public GitHub IDs. Empty, mismatched, hidden, disabled or stale scopes cannot
supply candidates, scores, counts or generation metadata. Callers must perform
final authorization when composing a user-facing operation; this method does
not authorize a principal itself.

The request accepts query text, result and candidate limits, explicit `Symbols`
and `Files`, and request-local `DiscoveryConfig`. `ProjectTerms` suppresses
incidental project-name dominance; the authorized `ProjectNameTokens` operation
below obtains these terms from exact-SHA source. `Deprioritize` accepts gitignore-style patterns: basename
and rooted paths, directories, `*`, `**`, `?`, character classes, escaping,
comments and ordered negation. Ignored parent directories remain ignored until
explicitly re-included. Patterns change rank, not corpus membership. Configuration
is passed on every request; there is no stale process-local configuration cache.

The parser recognizes `kind:`, `lang:`/`language:`, `path:` and `name:`. Values for
kinds and languages follow the pinned CodeGraph vocabularies. Unknown prefixes
and invalid recognized values remain free text. Repeated filters in one field
are ORed; different fields intersect. Name/path filters are case-insensitive
literal substrings; quoting permits spaces. Filters run before candidate limits.
Original case, optional presence, Unicode, NUL bytes and long strings remain in
returned protobuf facts. Search normalization never rewrites those facts.

Search uses PostgreSQL full-text indexes over names, qualified names, signatures,
documentation, paths, kinds and languages. It includes whole identifiers and
camel/Pascal/acronym/snake/dotted/path segments, lowercases and removes combining
marks, removes prose stop words, and adds light suffix/plural variants. Name,
qualified-name, signature and documentation weights have the pinned 20:5:2:1
ratio. Paths also contribute. Distinct original query words corroborate one
another; variants of one word and repeated copies of it do not add independent
corroboration. Concrete multi-term entry candidates survive a larger pool of
one-term distractors because corroboration is applied before candidate LIMIT.

A second native GIN index over one/two/three-character grams supports literal
substring recall, including long identifiers. PostgreSQL lexemes longer than
1,000 bytes use an exact hash term in the text index, while the gram index and
full normalized-field SQL recheck retain long prefix/substring recall. Neither
hash collisions nor gram collisions establish a match by themselves. One-character
free-text queries retain exact names, prefixes and literal substrings, including
names that are prose stop words. Their matches carry low confidence because
selectivity is low; the same candidate, result and deadline bounds apply.

Only when semantic/substring search returns no candidates, a native PostgreSQL
bounded Levenshtein fallback searches normalized names. Its maximum distance is
one for up to four characters and two otherwise; queries shorter than three
characters do not use fuzzy matching. A name-length index restricts the search,
a rolling band bounds each comparison's memory, and the same request deadline
bounds the operation. Returned matches identify fuzzy evidence and edit distance.
No PostgreSQL extension or production Node/CodeGraph process is required.

Ranking uses original-generation usage counts for calls, references, inheritance,
implementation and other usage relationships; containment/import/export edges do
not corroborate isolated weak symbols. Generated files have a 0.3 multiplier,
test/peripheral paths 0.5, and structurally ambient declarations 0.5. Generated
and ambient penalties use the stronger value once; the test penalty is separate.
Configured deprioritization dampens exact-name bonuses as well as rank. Explicit
symbol/file pins remain first; automatically inferred name pins do not override
configured deprioritization. Original `File` evidence, generated flags, match
fields, distinct-term counts and usage counts accompany original entities.

## File classification and generated counts

`graphquery.Service.FileClassifications` accepts at most 64 normalized, unique
paths for one selected repository. Each result keeps file presence, the nullable
producer `generated` flag, the derived generated classification, and ambient
classification separate. A path is generated when its stored producer flag is
true or its filename matches one of the pinned, case-sensitive CodeGraph
conventions. A stored false flag therefore remains visible even when the filename
classifies the path as generated.

A present file is ambient only when its persisted file metadata reports no
extraction errors, it has at least one declaration, every declaration kind is
`interface`, `type_alias`, `enum`, `enum_member`, or `namespace`, it originates
no `calls` or `instantiates` edge, and no node in another file has an incoming
edge to it. File/import/export/parameter nodes are bookkeeping and do not
establish or disqualify ambient declarations. The check is limited to stored
artifact file, node, and edge facts; it does not claim runtime, analyzer, or
language-service coverage.

`graphquery.Service.GeneratedFileCount` returns stored-true generated files and
total stored files with one native PostgreSQL aggregate. Filename fallback is
deliberately excluded from this count because it reports producer metadata. The
authorized graph-service wrappers resolve one repository and recheck both the
generation and repository grant after the query. Cancellation, invalid paths,
scope drift, missing candidate rows, and oversized responses fail without a
partial result. The frozen oracle and every positive/near-miss filename probe are
in `test/fixtures/codegraph/file-classification.json`.

A successful result has status `candidates` and confidence `discovery_only`.
One-character queries and isolated exact-name answers without usage corroboration
retain their matches with confidence `low`. An empty or incidental weak search
has status `no_entry_point` and confidence `low`, with no arbitrary fallback
entities. `Coverage` is always `semantic_discovery_only`; generation provenance
retains unresolved/diagnostic counts and producer capabilities. This operation
never claims complete source, resolved flow analysis, or authorization freshness
for a composed response.

## Bounds and query plans

Defaults are 20 results and `max(100, results*5)` candidates. Hard maxima are 100
results, 1,000 candidates, 16 KiB query text, 128 expanded terms, and 32 explicit
pins/configuration entries per list. Candidate limit must cover the result limit.
One lookahead row reports `CandidateTruncated`; `ResultTruncated` reports the
final result cut. Selection and tie breaking are deterministic within a generation.
The service validates lookahead scope before allowing it to affect metadata.

The candidate CTE reads only the rebuildable projection and materializes at most
candidate-limit-plus-one identities before joining original node/file payloads.
Each payload is checked against the existing 4 MiB limit in SQL before decoding;
store batches and final JSON encoding enforce the same aggregate ceiling. The
whole operation has a five-second maximum deadline. It returns no partial data
on cancellation, oversize, generation change or backend failure.

Indexed retrieval is not a constant-time promise for broad terms, filter-only
requests, fuzzy name sets or low-selectivity grams: PostgreSQL can scan/rank many
projection rows, bounded by the deadline. The normal-planner evidence test uses
4,000 synthetic entities, checks a selective answer, and captures the actual
candidate SQL with `EXPLAIN (ANALYZE, BUFFERS, SETTINGS)`. After ordinary
VACUUM/ANALYZE, the planner uses GIN term/gram and indexed pin probes and joins
original payloads only after the LIMIT. Immediately after bulk COPY, GIN pending
entries can make a projection scan cheaper until normal maintenance runs. These
are access-path observations, not production latency measurements. No planner
settings are forced. Very large/high-distinct-term documents increase projection
construction and storage costs; accepted original facts are not discarded.

## Projection lifecycle

Migration 028 adds `graph_v2_discovery` and `graph_uploads.discovery_version`.
New schema-v2 publication builds the projection and marks version 3 atomically
in the existing publication transaction. Failed publication cannot leave an active
partially indexed generation. Replacement keeps retired generations immutable;
their projection is generation-local and cascades with offline generation cleanup.
Legacy/public v1 publication and default REST/MCP behavior are unchanged.

Existing generations start at discovery version 0. They return
`graphquery.ErrDiscoveryUnavailable`, distinct from a valid empty search. A
trusted offline maintenance caller invokes:

```go
err := store.RebuildGraphDiscovery(ctx, internalRepositoryID, uploadID)
```

The method uses the existing validated `LoadGraphV2`, locks that exact generation,
replaces only its search projection, and marks version 3 in one transaction. It
can rebuild active or retired v2 generations. It is explicit and reproducible;
request-time discovery never loads a full artifact or triggers a rebuild. A
failed rebuild preserves the previous committed projection/version. Deployment
operators must rebuild required existing v2 generations before enabling discovery.
Future tokenizer/projection changes must use a new version and explicit rebuild.

Migration 029 adds original/folded name bytes, length, literal byte grams, and
identifier segments to that same disposable projection. File-classification
parity changes the ranking fields in place and advances the projection to version
3 without a schema migration. Ordinary `Discover` requires version 3; a version-2
projection returns `ErrDiscoveryUnavailable` until explicitly rebuilt. Name
selectors and segment evidence remain valid at version 2 or later. Publication
and rebuild write all fields and the version in the existing transaction. No
request performs a lazy rebuild, loads an artifact, or reads a worker's checkout.

## Literal names and segment evidence

`EntitiesRequest.Selector.NameMatch` accepts a typed `NameSelector` with `Mode`
`prefix` or `substring`, `Value`, optional `Kinds`, and substring-only
`ExcludePrefix`. Prefix membership is case-sensitive and defaults to 20 rows.
Substring membership folds ASCII case only and defaults to 30 rows; `%`, `_`,
NUL and non-ASCII characters remain literal. `Kinds` intersects membership.
Substring pages prefer shorter names; prefix pages order by original name.
Occurrence identity resolves remaining ties deterministically. Existing exact
name/qualified-name/path/kind selectors retain their behavior and compose as
intersections with the new selector.

These use the existing entity maximum rows, 16 KiB selector, 4 MiB response,
five-second deadline and immutable paging. Every cursor binds selector options,
effective page size, repository selection, active upload and indexed SHA.
Changing any bound input fails without partial output. Empty answers are valid;
an empty literal selects all names (except substring `ExcludePrefix`, which
excludes them all). Bounded page unions preserve original protobuf facts and all
overloads. The four older exact selectors are directly compared against the
frozen pinned methods; only test-side path/start-line ordering is projected.

`graphquery.Service.SegmentMatches` accepts up to 32 original `Words` and a
result `Limit` (default 6, maximum 100 and the configured entity row ceiling).
It maps plural variants to their first original word. Tier A requires two
distinct original words in one live name. Only when Tier A is empty, Tier B
accepts words of at least five characters whose segment occurs in 2–25 distinct
live names; candidate names must contain at least two segments. Names are unique,
file/import-only names and orphan proposals cannot supply evidence, and every
match contains a live original `Entity` plus sorted `MatchedWords`. More matched
words precede fewer, then shorter names. `Truncated` reports one bounded
lookahead row. Empty words yield an empty result with validated generation
provenance. Cancellation, byte limits and generation replacement fail closed.

Prefix candidates use an upload-scoped B-tree over the first 128 original bytes,
followed by full literal equality. Substrings use GIN over hex byte grams with a
full ASCII-folded byte containment recheck. Segments use a GIN array lookup and
live original-node joins. Candidate identities are limited before protobuf
payload joins; exact membership may require ranking indexed matching metadata,
bounded by the deadline. The selective 4,000-node plan tests assert the actual
indexes, immutable repository/upload/SHA joins and bounded result plans. Broad
selectors are not constant-time operations. Existing generation cleanup cascades
all derived rows; no new production dependency or PostgreSQL extension is used.

## Authorized project tokens

`graphservice.Service.ProjectNameTokens` resolves one authorized repository,
captures its active generation, and uses `RepositorySnapshot.Name` plus
`ContentReader.ReadFileAt` for `go.mod` and `package.json` at the selected SHA.
The reader's existing 1 MiB transfer ceiling applies; each request asks for at
most 512 lines and only complete manifest content at most 64 KiB contributes.
Missing, malformed, oversized, truncated or unavailable manifests contribute no
token. Module/package/repository basenames normalize to lowercase ASCII
alphanumerics; unique tokens need at least five characters. The module declaration
is parsed narrowly, without general Go/runtime configuration loading.
An answer ending at line 512 does not establish EOF and contributes no token,
even when the reader's `Truncated` flag is false for that explicit selection.

The service validates returned repository/path/SHA coordinates, then revalidates
the graph generation and repository authorization after the reads. Lost grants,
SHA drift, generation replacement, cancellation and invalid scope discard the
entire result. The result exposes `Tokens` and `Generations`; a composition caller
passes `Tokens` as `DiscoveryConfig.ProjectTerms` for that same scope/generation
and keeps the existing final authority checks. Explore passes project terms
explicitly; task-context seed derivation is described below. Artifact metadata has no invented
project-token key: the pinned package manifest is source evidence, absent from
the converted fixture's original file facts.

`test/fixtures/codegraph/discovery-variants.json` preserves all 16 actual pinned
method answers with commit/runtime/source/config/reference hashes. The two
limit-two captures are complete returned arrays, not exhaustive membership;
native pagination recovers their third overload. Exact SQLite ties/FTS scores,
uncapped arrays, public transports, file predicates, context workflows and
broader language/configuration parity are not claimed here.

## Task-level relevant and build context

`graphquery.Service.RelevantContext` composes the existing `Discover`, `Entities`,
`Traverse`, and generation-validation operations. Defaults match the pinned
CodeGraph task operation: search limit 3, traversal depth 1, maximum 20 nodes,
minimum score 0.3, all relationship kinds, and the high-value declaration kinds.
Pointer-valued options preserve omitted versus explicit zero or empty lists.
Expansion is bounded in both directions; explicit edge filters apply to BFS while
bounded hierarchy expansion and evidence recovery may retain other relationships
between retained nodes. Original node and edge protobuf occurrences are returned
unchanged, including parallel evidence with identical triples. The result exposes
effective options, generation, discovery scores, roots, boundaries, and partialness.

`graphservice.Service.FindRelevantContext` derives at most eight exact-name seeds
from segment evidence only when `seed_names` is omitted. An explicit empty list
suppresses derivation. `BuildTaskContext` deliberately calls the builder operation
directly, accepts a query or title/description, and defaults to five 1,500-UTF-16-unit
code blocks. Selection prefers roots, then functions/methods, then classes. Source
is read at the selected indexed SHA and retains original locations and blob evidence;
shortened content remains a verbatim prefix with separate size and truncation fields.
Graph, query, source-read, response-byte, and five-second limits are enforced, and
the final generation, SHA, and repository grant are revalidated after source reads.

This is a structured domain result. Root-connected call-site/registration analysis
and source-site evidence remain S1.06 work. Markdown/JSON presentation and REST/MCP
adapters remain S1.07/S1.09 work and must consume these partialness and provenance
fields without weakening them. The 12 pinned option/build captures and their original
counts are frozen in `test/fixtures/codegraph/task-context.json`; the PostgreSQL oracle
asserts required original facts and source rather than exact SQLite ranking order.

## Composition boundary

S1.05b can call `Discover` with its authorized snapshot, use the returned stable
entity IDs/occurrences and original file/node facts, and compose `Entities`,
`Traverse` and exact-SHA `ReadFileAt`. Source reservations, proportional budgets,
inspection/outline/caller/callee wrappers, session deduplication and final
principal/repository authorization belong to that layer. REST/MCP adapters remain
S1.07. Discovery alone does not complete S1.05.

The committed real CodeGraph fixture supplies required-answer identities. Tests
retain all seven pinned `normalize` search answers at a seven-result budget and
all three `normlize` fallback definitions at a three-result budget. Additional
hostile cases cover parser fallbacks, semantic fields, long/NUL inputs, penalties,
more-than-100 distractor retention, source-fact integrity, generation/scope drift,
rebuild availability, cancellation and payload bounds. The maximum-document case
uses high-distinct Unicode documentation near the 256 KiB fact limit.

Upstream rule provenance and the MIT notice are in
[graph-discovery-NOTICE.txt](graph-discovery-NOTICE.txt).
