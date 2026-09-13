# Internal entity queries

S1.04 adds `graphquery.Service.Entities` and `Traverse` with generated v2
node/edge facts, through `graphprotocol` and PostgreSQL's `EntityStore` adapter.
The existing v1 Context, Impact and Trace wrappers, REST routes and MCP schemas
retain their current defaults and evidence shapes. Public v2 transport belongs
to S1.07; source composition and derived workflows belong to S1.05/S1.06.

## Scope and identity

Callers supply an already authorized scope of internal repository IDs, public
GitHub IDs and exact indexed commits. This internal API does not authenticate
principals. Future public wrappers must authorize before calling it and recheck
authorization afterward, as existing graphservice wrappers do.

Readiness selects only active v2 generations for that scope and requires every
supplied repository to have the requested indexed SHA and remain enabled,
unarchived and attached to an active installation. A missing/stale generation
returns `ErrGenerationChanged`, without an alternate graph or partial facts.
The active upload and indexed SHA are rechecked before returning either an
entity page or traversal; a concurrent replacement invalidates the result.

Queries pin `(repository, upload, commit)` throughout. V2 edges join endpoints
inside that upload; equal occurrences or names in another repository never
create a cross-repository link. The artifact's external entities remain explicit
local facts. No hidden repository enters generation metadata, counts, lookup,
neighbor expansion or boundary generation.

Returned repository IDs are public GitHub IDs. Entity IDs use the existing
producer/repository/source-ID/occurrence identity contract. Full generated facts
retain optional locations, zero-based UTF-16 positions, absent versus zero
confidence, provenance, source IDs, lists and extension bytes. Incoming traversal
preserves the original source/target endpoints and their public entity IDs.

## Lookup and traversal

Entity selectors intersect exact occurrence, name, qualified name, path and kind
filters. Optional strings distinguish an omitted filter from an empty value.
Original UTF-8 bytea values, including valid NUL identities, are compared along
with their bounded SHA-256 keys. Pages sort by internal repository ID then
original occurrence bytes. Page size defaults to 1,000 and can be reduced;
`NextCursor` binds the ready generations, scope, selection, filters and page
size. A changed generation or query cannot resume that cursor. Offsets are capped
at 10,000,000; cursors are pagination state, not authorization credentials.

Traversal requires an occurrence, name or qualified-name root selector. Zero
roots returns `not_found`; multiple roots returns `ambiguous`, even when the
node budget truncates candidates to one. Neither case expands neighbors.
Default traversal is outgoing calls. Explicit filters use the shared registry's
13 directed relationships, deduplicated in request order. Unknown confidence
remains eligible at any numeric threshold; known confidence is filtered
inclusively. All entity kinds are eligible, including files and external nodes.

Traversal is breadth-first. It visits each entity once but retains separate
edge occurrences, including parallel edges, self-edges and cycle-closing edges.
Each frontier sorts by repository and original occurrence; each relation's SQL
neighbors sort by neighboring occurrence then edge occurrence. Evidence always
has both endpoints in the returned entity set.

Depth defaults to 3 and caps at 32. Other hard ceilings are 1,000 nodes including
the root, 5,000 edges and 100 edge occurrences per parent across all requested
relations. Limits can be reduced. A lookahead identifies actual depth/fanout
truncation. `Partial` and `depth_limit`, `fanout_limit`, `node_limit` or
`edge_limit` boundaries distinguish bounded output. Traversal has no paged or
summary-only mode; legacy Impact paging/summary behavior is unchanged.

The entire operation has a maximum five-second context deadline, reducible via
`Limits.MaxDuration`; PostgreSQL also retains its per-statement timeout. Caller
cancellation and deadlines return an error and no partial response.

The JSON response budget is 4 MiB. SQL caps individual protobuf payload transfer;
stores check cumulative JSON bytes while scanning, and traversal checks bytes
before appending facts. Final encoding checks envelope overhead. An oversized
fact, lookahead row or accumulated response returns `ErrQuerySize`; it is never
silently removed to produce an apparently complete empty result. This bounds
retained result batches, not production latency or peak RSS to exactly 4 MiB.

## Repeat the oracle comparisons

The export reuses graphartifact's existing test-only SQLite converter; its
lossless test checks every original column. The PostgreSQL test compares full
facts and reachability for each recorded relation in both directions. The real
fixture covers 68 nodes, 93 edges, 20 kinds and nine relations (138 traversals).
A separate synthetic fixture covers all 23 CodeGraph kinds and 13 relations
(26 traversals). Synthetic vocabulary is not producer extraction evidence.

```sh
rtk proxy env GOTOOLCHAIN=go1.27.1 GOWORK=off \
  GRAPHNEST_TEST_CODEGRAPH_V2_FIXTURE=/tmp/graphnest-s1-04-oracle.pb \
  go test ./internal/graphartifact \
  -run '^TestV2(ExportQueryFixture|CodeGraphSQLiteLossless)$' -count=1 -v
rtk proxy env GOTOOLCHAIN=go1.27.1 GOWORK=off \
  GRAPHNEST_TEST_CODEGRAPH_V2_FIXTURE=/tmp/graphnest-s1-04-oracle.pb \
  GRAPHNEST_REQUIRE_POSTGRES=1 GRAPHNEST_TEST_POSTGRES_DSN="$GRAPHNEST_TEST_POSTGRES_DSN" \
  go test -tags=integration ./internal/postgres -run '^TestGraphEntit' -count=1 -v
```

Set the DSN to an isolated test database. The export and real-oracle tests are
opt-in; skips do not count as conformance evidence. S1.10 owns the dedicated
cross-layer gate. These internal checks do not complete REST/MCP, browser,
composed exploration, derived-workflow or production performance acceptance.
