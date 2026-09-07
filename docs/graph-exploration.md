# Stateless graph exploration

`graphservice.Service.Explore(ctx, currentPrincipal, ExploreRequest)` composes
semantic discovery, indexed file facts, occurrence-preserving one-hop graph
queries and exact indexed-commit source in a single domain operation. HTTP/MCP
adapters remain S1.07. Repeated-call history and deduplication remain S1.05c3;
this operation retains no principal, session or source between calls.

The request selects one repository and optional branch. Query, Symbols, Files,
Limit and CandidateLimit use accepted discovery semantics. RequiredOccurrences
selects exact producer occurrences independently of relevance ranking. Explicit
file paths, including literal paths in query text, remain selectable when they
have no callable entities. Missing required occurrences/files remain boundaries.
Overloads retain separate original occurrences and source selections.

ExploreConfig embeds DiscoveryConfig (ProjectTerms, Deprioritize, NoMultiterm).
NoMultiterm disables corroboration before the SQL candidate limit as well as
final ranking. LineNumbers defaults true and is a presentation flag: segments
always contain original source bytes; renderers can prefix lines using StartLine.
Adaptive defaults true. It emits original signature-line segments for off-answer
implementation siblings when at least three distinct implementing entities are
present in returned extends/implements evidence. A file defining that family
supertype can mix named method bodies with signatures for redundant members,
even when the file is required. A required god-file also uses this focused view
when its named bodies exceed its grant and there are named bodies outside the
explicit RequiredOccurrences. Exact required occurrences take priority, followed
by named bodies that fit the per-file cap. Pinned files retain ordinary body
selection. A required entry must exist before adaptive reduction applies. No
source field contains inserted elisions, rewritten signatures or line numbers.

## Allocation and source evidence

An unfiltered IndexedFiles request with IncludeCount and a one-file page supplies
the immutable corpus size. Default source budgets mirror the pinned allocator:
13,000 UTF-16 units/four files below 150 files; 18,000/five below 500; 24,000/eight
thereafter. SourceUnits overrides the logical budget up to 100,000. SourceBytes
is an independent UTF-8 ceiling, default/maximum 256 KiB. This covers the UTF-8
cost of default logical tiers, including BMP-heavy source. The final JSON limit
still applies independently and includes all original graph/provenance facts.

Per-file allocation uses relevance scores with a second source-worth penalty,
700-unit floors, a relative 15% cliff capped at score ten, a twofold required-file
weight, and a 70% maximum initial share. Pinned files weigh at least as much as
the strongest file. The pinned allocator's 200-unit per-file presentation reserve
is retained for numerical parity; it is not a substitute for measuring JSON.
Graph-only neighbors inherit one quarter of the contributing root's score,
with a floor of one. Stored generated metadata lowers their worth too. Files
under the cliff remain original fact/entity pointers and consume no source slot.
Unused reservations are distributed proportionally to remaining source demand.

RequiredOccurrences and explicitly named callable discovery matches supply the
required-file weight. This is bounded source allocation over the accepted graph,
not a claim of S1.06 dynamic flow-spine extraction. File pins and files resolved
from exact RequiredOccurrences are admitted first. An explicit MaxFiles which
cannot hold those distinct hard obligations is refused. Named matches retain
body priority and weighting, but do not force every matching file into the source
budget. With MaxFiles omitted, the default may expand for preferred named files
up to twenty. Omitted named candidates retain original fact/occurrence pointers,
source boundaries and a focus handoff; Complete is false. Explicit MaxFiles is
never exceeded. Unavoidable source-budget truncation remains visible even for
required entities.

A bounded prefix read preserves affordable small files, including BMP-heavy
UTF-8 files; reader EOF evidence decides whether the whole file was captured.
Larger files receive whole-line windows. Required bodies across all buffered
ranges are funded before optional bodies and surrounding context. A read is
redundant only when an earlier buffer covers the entire required range; overlapping
buffers emit each original line once. Disjoint required
regions can require separate exact-SHA ReadFileAt calls, sharing the operation's
read/byte ceilings. Each segment carries its original line bounds, indexed SHA
and blob SHA. Selections reference a segment and retain the original zero-based
UTF-16 Location plus an end-exclusive byte selection when the whole entity range
is present. Missing, partial, invalid and windowed ranges never become exact
selections. CR characters, Unicode and internal LF bytes remain verbatim.

## Bounds and authority

- Five seconds cover discovery, graph work, source I/O and final validation.
- Existing discovery bounds: 16-KiB query, 100 matches, 1,000 candidates. Symbols,
  Files and RequiredOccurrences each accept at most twenty entries. Original
  per-value bounds and discovery configuration bounds also apply.
- Query file hints perform at most twenty distinct exact path probes. At most
  ten entry roots perform two one-hop traversals each. Returned neighborhoods
  total at most 1,000 entities, 5,000 edges and four MiB of encoded query data;
  a neighborhood that exceeds row limits is withheld with graph_work_limit.
  The underlying engine bounds still govern each query, including a query whose
  result is withheld. At most twenty traversals can execute.
- At most 100 contributing file candidates have metadata queried. Aggregate file
  facts have a separate four-MiB bound. A pinned file-only outline has at most
  100 original entities and reports a continuation boundary. These projections
  use existing stores; no full artifact is loaded by Explore.
- At most twenty source read attempts execute, including disjoint windows.
  Repository ReadFileAt enforces its one-MiB decoded-file ceiling and bounded
  GitHub envelope, so repeated reads can process at most twenty MiB of decoded
  files. The aggregate retained source buffers are at most four MiB. Each read
  contributes at most 1,000 lines; lower repository limits remain visible.
  Files beyond the repository ceiling report oversized rather than bypassing it.
- Window construction works only on those bounded lines/entities. Reservations,
  source UTF-8 bytes and final serialized response bytes are separate limits.
  Final JSON defaults/maxes at 256 KiB and honors lower service limits. Overflow
  refuses the complete result with ErrQuerySize.

Every graph step must match the captured repository, upload generation and SHA.
Every source call uses that exact indexed SHA. A final generation validation and
repository/grant/branch/installation check runs after all source and composition,
followed by a cancellation check. Failure returns the zero result, including no
paths, scores, counts, source or provenance. An unrelated authorized v1 repository
is excluded from the selected v2 scope.

Complete describes the selected bounded neighborhood and source only. Weak or
missing entry points, omitted graph/file/source evidence, extraction errors,
generation diagnostics and unresolved analysis prevent completeness. Successful
source remains useful alongside these boundaries. Handoffs identify how to focus
another request without inventing an entry point or suggesting that missing source
has been supplied.

Principal has no credential identity or expiration. The domain rechecks current
repository eligibility; current-request credential authentication and mid-call
credential revocation remain adapter-owned. No old Principal is reused as authority.
