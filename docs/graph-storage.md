# PostgreSQL graph generations

Migration 027 retains immutable graph generations in `graph_uploads`. A partial
unique index permits one active generation per internal repository ID. Each
publication locks the repository, checks its indexed SHA, and writes all facts
plus activation in one transaction. Failure, cancellation, or a failed graph-job
completion rolls back retirement and all copied facts. A repository's older
SHA is not selected as current, but its existing generation remains readable
through its pinned upload ID. Existing `graphquery.QuerySnapshot` triples need
no interface change.

## Versions and publication

V1 keeps its existing native node/edge tables and managed > SCIP precedence;
external wins over managed and SCIP. New v1 writes cannot replace a v2 active
generation, even after indexed SHA advances. The legacy manifest and artifact
readers never advertise or decode v2. The old production upload, scanner and
queue APIs remain v1; S1.03 does not enable v2 production uploads.

`postgres.ReplaceGraphV2` is a storage entry point for later importer/publication
layers. It uses the generated v2 messages directly. Its trusted
`GraphPublication` context supplies the publisher, capability strings, expected
active upload ID, and explicit permission to switch producer/source. Expected
ID zero means no active generation. Every publication compares the expected
ID and exact indexed SHA under the repository lock. A different active
producer/source requires `AllowProviderChange`; changing a producer version or
configuration is still subject to the expected-generation check. An unavailable
repository is rejected. Authentication/authorization and capability claims
belong to the later publication service, not artifact-provided metadata.

For server storage, v2 `repository` is the canonical decimal string of the
public GitHub repository ID (`repositories.github_id`). It must match the
storage repository argument. It is never the internal PostgreSQL row ID. The
generic v2 wire/identity contract remains usable with other stable strings
outside server storage. Storage does not rewrite incoming repository identity,
producer evidence, or a supplied valid semantic hash. A missing hash is computed
on a validated clone; the caller's artifact is untouched.

The upload records schema version, publisher, capabilities, public repository,
producer name/version/configuration and semantic hash. V2 producer fields use
byte-preserving `producer_name`, `producer_version` and `producer_configuration`
columns; its unused legacy `analyzer_name`/`analyzer_version` fields are empty.
V1 keeps its existing analyzer text fields. Provider decisions compare the
original producer bytes, including embedded NUL. Existing v1 publisher identity
is unknown (`legacy`); existing capability lists are empty, not invented.

`graph_v2_nodes`, `graph_v2_edges`, `graph_v2_files`,
`graph_v2_unresolved`, and `graph_v2_diagnostics` contain upload-scoped facts.
Occurrences remain distinct even when declarations share names or edges share
endpoints. Composite foreign keys prohibit endpoints in another generation and
facts stored under the wrong artifact version. Kind and hash constraints apply
at the SQL boundary. Query columns cover kind/name/path, traversal endpoints,
confidence, visibility and exported status. Indexes cover both traversal
directions and node kind/name/path/visibility/exported predicates.

Each fact retains its full generated protobuf message; optional false, zero,
empty and missing values, partial or missing source coordinates, timestamps,
original source IDs, lists and extension JSON are preserved. The upload header
retains project metadata, diagnostics-independent import evidence and extensions.
Collection ordinals preserve wire ordering without relying on database row IDs.
`LoadGraphV2(repositoryID, uploadID)` reconstructs and validates the artifact in
a read-only repeatable-read transaction, including retired generations. The
caller must authorize the repository. V1 `LoadGraph` also uses repeatable-read
so concurrent deletion cannot return a header with missing nodes/edges.

## Bounded SQL keys and original string bytes

The wire contract permits 16 KiB identifiers and embedded U+0000 in many strings;
PostgreSQL B-tree entries are smaller and PostgreSQL `text` rejects NUL. V2
occurrences, endpoints, names, qualified names, language, visibility and paths
therefore store the original UTF-8 bytes as `bytea`. Optional absent values are
SQL NULL; present-empty values are zero-length bytea. The generated protobuf
payload remains complete and unchanged. Query code should scan these projections
into Go byte slices/strings, not cast arbitrary bytes back to PostgreSQL text.
Finite registry kinds and validated hexadecimal hashes remain bounded text.

Generated `occurrence_key`, `source_key`, `target_key` and file `path_key` columns
use PostgreSQL's built-in `sha256(bytea)` with no extension. Primary/foreign keys
and lookup indexes contain only these bounded 32-byte keys, upload IDs and
bounded registry/scalar values. Name/path/qualified-name/visibility indexes hash
the corresponding original bytea column. Unique identity digests reject a
collision atomically with the publication; they never merge or upsert facts.
Existing v1 qualified names and paths retain their original storage limits;
migration 027 adds no raw name/path B-tree indexes to v1 tables.

S1.04 queries must compare the original bytes along with hash equality. For
example, bind `$2` as the UTF-8 byte slice and use both conditions:

```sql
SELECT occurrence FROM graph_v2_nodes
WHERE upload_id=$1 AND sha256(name)=sha256($2::bytea) AND name=$2::bytea;
```

Endpoint joins likewise retain the exact-byte condition in addition to bounded
keys and upload scope:

```sql
SELECT target.payload
FROM graph_v2_edges edge
JOIN graph_v2_nodes target ON target.upload_id=edge.upload_id
  AND target.occurrence_key=edge.target_key AND target.occurrence=edge.target
WHERE edge.upload_id=$1 AND edge.source_key=sha256($2::bytea)
  AND edge.source=$2::bytea AND edge.kind=4;
```

Hash equality is only an index/join aid. It does not replace the original
identity/equality contract or change artifact limits. SQL NULL predicates remain
necessary when querying absent optional fields.

## Rollout and recovery

This is a **drained rollout**, not a mixed-binary rolling upgrade. Retained v1
generations alone make old binaries unsafe: their queries do not filter active
rows. Before migration, pause all graph/SCIP/index completion writers and drain
in-flight requests/jobs. Apply migrations, deploy compatible readers and writers
across the fleet, then resume writers. Keep v2 production publication disabled
until the later query and publication layers are deployed and verified.
Migration 019's Go SCIP backfill now runs after all additive DDL within the same
migration transaction, so the current writer can safely handle historical data.

Failed publication leaves the previous active generation intact. A stale or
provider-conflicting publication must re-read state and obtain an explicit new
publication decision; do not blindly retry with a substituted generation ID.
There is no automatic old-generation activation or timed pruning. To recover
facts at the current indexed SHA, publish a validated artifact through the same
expected-generation checks. Rolling back to an old binary requires draining,
removing incompatible generations, and verifying its queries and writes against
the retained schema; it is not guaranteed merely by reverting a binary.

## Offline retention

Retired generations intentionally consume disk until operator cleanup. There is
no bounded history claim and no reader lease/pinning framework in S1.03. Timed
pruning while queries run would break multi-request traversal snapshots.

After stopping/draining **all** graph readers, index/SCIP/graph writers and
background jobs, review the following count and delete only inactive generations
in the target GraphNest database/schema. Invalidated clients must start new
queries after the maintenance window; cached snapshot IDs cannot be resumed.
The transaction locks the generation table and preserves every active upload:

```sql
BEGIN;
LOCK TABLE graph_uploads IN ACCESS EXCLUSIVE MODE;
SELECT count(*) AS retired_generations FROM graph_uploads WHERE NOT active;
DELETE FROM graph_uploads WHERE NOT active;
COMMIT;
```

Foreign keys cascade deletion of the retired generation's facts. Take the usual
database backup before maintenance if old generations are needed for recovery.
This SQL is an operator procedure, not a scheduled cleanup service. Add bounded
online retention only together with a reader pin/expiry contract.

## Reproduce index-plan evidence

The opt-in `TestGraphV2IndexPlanEvidence` diagnostic creates an isolated test
schema, publishes 5,000 function nodes with unique names/paths and 5,000 calls
in a directed ring, runs `ANALYZE`, then checks the exact returned node/edge facts
before recording complete normal-planner `EXPLAIN (ANALYZE, BUFFERS, SETTINGS)`
output for name/path and both traversal directions. Each lookup combines a
bounded hash predicate with original-byte equality. It does not disable
sequential scans or assert optimizer plan names. The schema is dropped by the
existing test cleanup.

```sh
rtk proxy env GOTOOLCHAIN=go1.26.6 GOWORK=off \
  GRAPHNEST_GRAPH_INDEX_EVIDENCE=1 GRAPHNEST_REQUIRE_POSTGRES=1 \
  'GRAPHNEST_TEST_POSTGRES_DSN=postgres://graphnest:graphnest@127.0.0.1:32771/graphnest?sslmode=disable' \
  go test -tags=integration ./internal/postgres \
  -run '^TestGraphV2IndexPlanEvidence$' -count=1 -v
```

Use a test database DSN appropriate to the local environment. Without the
explicit `GRAPHNEST_GRAPH_INDEX_EVIDENCE=1` gate, the diagnostic is **skipped**;
that skip is not evidence of a successful index or performance check. The
retained [diagnostic source](../internal/postgres/graph_index_evidence_test.go)
and [observed PostgreSQL18.6 output](graph-storage-index-evidence.txt) make the
corpus, exact SQL, checked facts and full plans reviewable and repeatable.

This synthetic warm-cache check demonstrates useful generation-scoped indexes.
It does not establish production throughput, real-artifact disk growth,
large-repository or cold-cache latency, metadata selectivity, or online retention
performance. Plan choice and timings can change with PostgreSQL version, data
statistics and hardware; reruns should inspect their own full output.
