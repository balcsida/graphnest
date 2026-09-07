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
incidental project-name dominance; the caller obtains these terms from indexed
repository manifests. `Deprioritize` accepts gitignore-style patterns: basename
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
test/peripheral paths 0.5, and unused ambient `.d.ts` declarations 0.5. Generated
and ambient penalties use the stronger value once; the test penalty is separate.
Configured deprioritization dampens exact-name bonuses as well as rank. Explicit
symbol/file pins remain first; automatically inferred name pins do not override
configured deprioritization. Original `File` evidence, generated flags, match
fields, distinct-term counts and usage counts accompany original entities.

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
New v2 publication builds the projection and marks version 1 atomically in the
existing publication transaction. Failed publication cannot leave an active
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
replaces only its search projection, and marks version 1 in one transaction. It
can rebuild active or retired v2 generations. It is explicit and reproducible;
request-time discovery never loads a full artifact or triggers a rebuild. A
failed rebuild preserves the previous committed projection/version. Deployment
operators must rebuild required existing v2 generations before enabling discovery.
Future tokenizer/projection changes must use a new version and explicit rebuild.

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
