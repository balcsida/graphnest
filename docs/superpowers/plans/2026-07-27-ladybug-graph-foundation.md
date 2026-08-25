# LadybugDB Graph Foundation Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add the versioned graph artifact, authoritative PostgreSQL storage, scanner queue, external upload, and graph status without introducing LadybugDB or source scanners yet.

**Architecture:** A bounded protobuf artifact is validated into domain rows and transactionally stored in PostgreSQL at the repository's exact indexed SHA. Completing a Zoekt job atomically enqueues an independent graph job; external uploads take precedence over managed results. REST exposes administrator upload and authorized status while current SCIP metadata remains an explicit fallback source.

**Tech Stack:** Go 1.26.5, protobuf v1 wire format, `google.golang.org/protobuf v1.36.11`, PostgreSQL/pgx, existing bearer authorization and HTTP adapters.

## Global Constraints

- Work on `feat/ladybug-graph`; never commit to `main` or `master`.
- Use signed conventional commits, one logical change per commit, with subjects under 72 characters.
- PostgreSQL remains authoritative; this plan adds no LadybugDB dependency.
- Accept artifacts only for the repository's current 40-character lowercase `indexed_sha`.
- Artifact positions are zero-based lines and zero-based UTF-8 byte offsets from line start.
- Content hashes are exactly 32 SHA-256 bytes.
- Default graph upload limit is 64 MiB; the hard configuration cap is 256 MiB.
- Default limits are 500,000 nodes and 2,000,000 edges; hard caps are 2,000,000 nodes and 10,000,000 edges.
- Paths are clean slash-separated relative paths of at most 4,096 bytes; UIDs and canonical identities are at most 16,384 bytes.
- Graph jobs use the existing two-minute lease, five-attempt cap, and bounded error codes.
- External artifacts win over managed artifacts for the same repository and commit.
- Validate authorization and current SHA before reading a large upload and repeat both checks before persistence.
- Bound request bodies, decoded counts, database copies, status results, and encoded responses.
- Preserve existing Zoekt, SCIP, REST, MCP, and static-mode behavior.
- Pin code generation tools as `github.com/bufbuild/buf/cmd/buf v1.57.0` and `google.golang.org/protobuf/cmd/protoc-gen-go v1.36.11`.

---

## File structure

- `internal/graphartifact/v1/artifact.proto`: stable external protobuf contract.
- `internal/graphartifact/v1/artifact.pb.go`: committed generated Go binding.
- `internal/graphartifact/model.go`: storage-independent domain rows and enums.
- `internal/graphartifact/parse.go`: protobuf decoding, canonical identity, and validation.
- `internal/postgres/migrations/006_graph_artifacts.sql`: authoritative uploads, facts, and graph jobs.
- `internal/postgres/graph.go`: artifact replacement, loading, status, and graph queue operations.
- `internal/graphingest/service.go`: authorization-aware external ingestion and status.
- `internal/httpapi/graph.go`: bounded upload and status routes.
- `pkg/api/graph.go`: public graph status response types.

### Task 1: Define and validate the versioned graph artifact

**Files:**
- Modify: `go.mod`
- Modify: `go.sum`
- Create: `buf.yaml`
- Create: `buf.gen.yaml`
- Create: `internal/graphartifact/v1/artifact.proto`
- Create: `internal/graphartifact/v1/artifact.pb.go`
- Create: `internal/graphartifact/model.go`
- Create: `internal/graphartifact/parse.go`
- Create: `internal/graphartifact/parse_test.go`

**Interfaces:**
- Produces: `Parse(data []byte, limits Limits) (Artifact, error)`.
- Produces: `Validate(artifact Artifact, limits Limits) error`.
- Produces: `Identity(node Node) (string, error)`.
- Produces the `Artifact`, `Analyzer`, `Node`, `Edge`, `Range`, `NodeKind`, `EdgeKind`, and `Limits` types shown below.

- [ ] **Step 1: Write failing parser and identity tests**

Cover a valid v1 round trip, exact SCIP identity preference, deterministic fallback identity, unsupported schema, malformed commit/hash/path/range, duplicate UID/edge, missing endpoints, illegal kind/confidence, and count overflow.

```go
func TestParseArtifactV1(t *testing.T) {
	data := marshalArtifact(t, &graphv1.Artifact{
		SchemaVersion: 1,
		RepositoryId:  101,
		Commit:        strings.Repeat("a", 40),
		ContentHash:   bytes.Repeat([]byte{1}, sha256.Size),
		Analyzer:      &graphv1.Analyzer{Name: "graphnest-scanner", Version: "1"},
		Nodes: []*graphv1.Node{
			{Uid: "repository:101", Kind: graphv1.NodeKind_NODE_KIND_REPOSITORY},
			{Uid: "file:a.go", Kind: graphv1.NodeKind_NODE_KIND_FILE, Path: "a.go"},
			{Uid: "symbol:a", Kind: graphv1.NodeKind_NODE_KIND_SYMBOL, Path: "a.go", QualifiedName: "a.A"},
		},
		Edges: []*graphv1.Edge{
			{SourceUid: "repository:101", TargetUid: "file:a.go", Kind: graphv1.EdgeKind_EDGE_KIND_CONTAINS, Confidence: 1},
			{SourceUid: "file:a.go", TargetUid: "symbol:a", Kind: graphv1.EdgeKind_EDGE_KIND_CONTAINS, Confidence: 1},
		},
	})
	got, err := Parse(data, Limits{MaxNodes: 10, MaxEdges: 10, MaxPathBytes: 4096, MaxIdentifierBytes: 16384})
	if err != nil || got.RepositoryID != 101 || len(got.Nodes) != 3 || len(got.Edges) != 2 {
		t.Fatalf("Parse() = %#v, %v", got, err)
	}
}

func TestIdentityPrefersSCIP(t *testing.T) {
	got, err := Identity(Node{SCIPSymbol: "scip-go gomod example.com/a v1 A#"})
	if err != nil || got != "scip-go gomod example.com/a v1 A#" {
		t.Fatalf("Identity() = %q, %v", got, err)
	}
}
```

- [ ] **Step 2: Run the focused test and observe the missing package**

Run: `go test ./internal/graphartifact/... -count=1`

Expected: FAIL because the graph artifact package and generated binding do not exist.

- [ ] **Step 3: Add the protobuf contract and generated binding**

Use this wire shape; enum zero values remain invalid so missing values fail validation.

```protobuf
syntax = "proto3";
package graphnest.graph.v1;
option go_package = "github.com/balcsida/graphnest/internal/graphartifact/v1;graphv1";

message Artifact {
  uint32 schema_version = 1;
  int64 repository_id = 2;
  string commit = 3;
  bytes content_hash = 4;
  Analyzer analyzer = 5;
  repeated Node nodes = 6;
  repeated Edge edges = 7;
}

message Analyzer { string name = 1; string version = 2; }
message Range {
  int32 start_line = 1;
  int32 start_character = 2;
  int32 end_line = 3;
  int32 end_character = 4;
}
enum NodeKind {
  NODE_KIND_UNSPECIFIED = 0;
  NODE_KIND_REPOSITORY = 1;
  NODE_KIND_FILE = 2;
  NODE_KIND_SYMBOL = 3;
}
enum EdgeKind {
  EDGE_KIND_UNSPECIFIED = 0;
  EDGE_KIND_CONTAINS = 1;
  EDGE_KIND_IMPORTS = 2;
  EDGE_KIND_REFERENCES = 3;
  EDGE_KIND_CALLS = 4;
  EDGE_KIND_EXTENDS = 5;
  EDGE_KIND_IMPLEMENTS = 6;
}
message Node {
  string uid = 1;
  NodeKind kind = 2;
  string path = 3;
  string language = 4;
  string symbol_kind = 5;
  string qualified_name = 6;
  string signature = 7;
  string scip_symbol = 8;
  Range range = 9;
}
message Edge {
  string source_uid = 1;
  string target_uid = 2;
  EdgeKind kind = 3;
  string path = 4;
  Range range = 5;
  float confidence = 6;
  string resolution_reason = 7;
}
```

Pin Buf and `protoc-gen-go` as Go tools and configure `buf generate` to invoke the local `go tool protoc-gen-go`; commit the generated file.

- [ ] **Step 4: Implement domain conversion and fail-closed validation**

```go
type Artifact struct {
	SchemaVersion uint32
	Analyzer      Analyzer
	RepositoryID  int64
	Commit        string
	ContentHash   []byte
	Nodes         []Node
	Edges         []Edge
}

type Limits struct {
	MaxNodes, MaxEdges                 int
	MaxPathBytes, MaxIdentifierBytes   int
}

func Parse(data []byte, limits Limits) (Artifact, error) {
	var wire graphv1.Artifact
	if err := proto.Unmarshal(data, &wire); err != nil {
		return Artifact{}, ErrInvalidArtifact
	}
	artifact := fromWire(&wire)
	return artifact, Validate(artifact, limits)
}
```

Use `path.Clean`, validate lowercase SHA, require SHA-256 length, require unique UIDs and edge tuples, verify endpoints after all nodes are read, and compute fallback identity as the SHA-256 hex digest of length-prefixed language/path/kind/qualified-name/signature fields. Do not concatenate ambiguous separators.

- [ ] **Step 5: Generate, format, and run focused tests**

Run:

```bash
go tool buf generate
gofmt -w internal/graphartifact
go test ./internal/graphartifact/... -count=1
go mod tidy -diff
```

Expected: PASS; `go mod tidy -diff` prints no diff.

- [ ] **Step 6: Commit the artifact contract**

```bash
git status --short
git add go.mod go.sum buf.yaml buf.gen.yaml internal/graphartifact
git commit -S -m "feat(graph): define versioned artifact"
```

### Task 2: Add the authoritative PostgreSQL schema

**Files:**
- Create: `internal/postgres/migrations/006_graph_artifacts.sql`
- Modify: `internal/postgres/migrate_test.go`
- Create: `internal/postgres/graph_test.go`

**Interfaces:**
- Produces PostgreSQL tables `graph_uploads`, `graph_nodes`, `graph_edges`, and `graph_jobs`.
- Preserves the migration lock and idempotence behavior in `internal/postgres/migrate.go`.

- [ ] **Step 1: Write failing migration integration tests**

```go
func TestMigrateCreatesGraphTables(t *testing.T) {
	pool := testPool(t)
	if err := Migrate(t.Context(), pool); err != nil {
		t.Fatal(err)
	}
	for _, table := range []string{"graph_uploads", "graph_nodes", "graph_edges", "graph_jobs"} {
		var found bool
		if err := pool.QueryRow(t.Context(),
			`select to_regclass('public.' || $1) is not null`, table).Scan(&found); err != nil || !found {
			t.Fatalf("%s found=%v err=%v", table, found, err)
		}
	}
}
```

Also assert migration count six, repository cascades, legal source/kind/confidence constraints, endpoint foreign keys, and one queued/one running job partial indexes.

- [ ] **Step 2: Run PostgreSQL tests and observe the missing migration**

Run: `make postgres-test`

Expected: FAIL because migration `006_graph_artifacts.sql` does not exist.

- [ ] **Step 3: Add normalized graph and queue tables**

The upload is unique per repository. Make node identity `(upload_id, uid)` the referenced key so edges cannot point outside their upload.

```sql
create table graph_uploads (
    id bigint generated always as identity primary key,
    repository_id bigint not null unique references repositories(id) on delete cascade,
    commit char(40) not null check (commit ~ '^[0-9a-f]{40}$'),
    schema_version integer not null check (schema_version = 1),
    source varchar(16) not null check (source in ('managed', 'external')),
    analyzer_name text not null,
    analyzer_version text not null,
    content_hash bytea not null check (octet_length(content_hash) = 32),
    node_count integer not null check (node_count >= 0),
    edge_count integer not null check (edge_count >= 0),
    uploaded_at timestamptz not null default now()
);
```

Add range checks identical to the domain validator, fixed node/edge kind checks, confidence `between 0 and 1`, queue state checks, lease fields, run time, attempts, and indexes mirroring `index_jobs`.

- [ ] **Step 4: Run migration and repository tests**

Run: `make postgres-test`

Expected: PASS.

- [ ] **Step 5: Commit the schema**

```bash
git status --short
git add internal/postgres/migrations/006_graph_artifacts.sql internal/postgres/migrate_test.go internal/postgres/graph_test.go
git commit -S -m "feat(graph): add authoritative schema"
```

### Task 3: Persist artifacts with external precedence

**Files:**
- Create: `internal/postgres/graph.go`
- Modify: `internal/postgres/graph_test.go`

**Interfaces:**
- Consumes: `graphartifact.Artifact`.
- Produces: `ReplaceGraph(ctx context.Context, repositoryID int64, source GraphSource, artifact graphartifact.Artifact) (GraphReplacement, error)`.
- Produces: `LoadGraph(ctx context.Context, uploadID int64) (graphartifact.Artifact, error)`.

- [ ] **Step 1: Write failing replacement and round-trip tests**

```go
func TestReplaceGraphExternalWins(t *testing.T) {
	store, repositoryID := readyGraphStore(t, strings.Repeat("a", 40))
	managed := artifactFor(repositoryID, strings.Repeat("a", 40), "managed")
	if got, err := store.ReplaceGraph(t.Context(), repositoryID, GraphSourceManaged, managed); err != nil || !got.Applied {
		t.Fatalf("managed = %#v, %v", got, err)
	}
	external := artifactFor(repositoryID, strings.Repeat("a", 40), "external")
	if got, err := store.ReplaceGraph(t.Context(), repositoryID, GraphSourceExternal, external); err != nil || !got.Applied {
		t.Fatalf("external = %#v, %v", got, err)
	}
	if got, err := store.ReplaceGraph(t.Context(), repositoryID, GraphSourceManaged, managed); err != nil || got.Applied {
		t.Fatalf("late managed = %#v, %v", got, err)
	}
}
```

Also cover stale commit no-op, invalid edge rollback preserving the old upload, load round trip, and repository cascade.

- [ ] **Step 2: Run focused integration tests and observe missing methods**

Run: `make postgres-test`

Expected: FAIL because graph store methods do not exist.

- [ ] **Step 3: Implement one-transaction replacement**

```go
type GraphSource string
const (
	GraphSourceManaged  GraphSource = "managed"
	GraphSourceExternal GraphSource = "external"
)

type GraphReplacement struct {
	Upload  GraphUpload
	Applied bool
}
```

Lock `repositories` and the current upload, require both public repository ID and artifact repository ID to match the selected internal repository, enforce current `indexed_sha`, preserve an external current-commit upload against managed replacement, then use `pgx.CopyFrom` for nodes and edges before commit.

- [ ] **Step 4: Run focused and full PostgreSQL tests**

Run: `make postgres-test`

Expected: PASS.

- [ ] **Step 5: Commit graph persistence**

```bash
git status --short
git add internal/postgres/graph.go internal/postgres/graph_test.go
git commit -S -m "feat(graph): persist graph artifacts"
```

### Task 4: Add the scanner queue and atomic index publication

**Files:**
- Modify: `internal/postgres/graph.go`
- Modify: `internal/postgres/graph_test.go`
- Modify: `internal/postgres/queue.go`
- Modify: `internal/postgres/queue_test.go`

**Interfaces:**
- Produces `GraphJob`, `ClaimGraph`, `RenewGraphLease`, `CompleteGraph`, `FailGraph`, `ReapExpiredGraph`, `GraphQueueDepths`, and `ActiveGraphJobIDs`.
- Changes `CompleteIndex` so graph enqueue and `indexed_sha` publication share one transaction.

- [ ] **Step 1: Write failing atomic enqueue and queue lifecycle tests**

```go
func TestCompleteIndexEnqueuesGraphAtomically(t *testing.T) {
	store, job := runningIndexJob(t)
	if err := store.CompleteIndex(t.Context(), job.ID, job.LeaseOwner); err != nil {
		t.Fatal(err)
	}
	graph, err := store.ClaimGraph(t.Context(), "scanner-1")
	if err != nil || graph.RepositoryID != job.RepositoryID || graph.TargetSHA != job.TargetSHA {
		t.Fatalf("ClaimGraph() = %#v, %v", graph, err)
	}
}
```

Add a trigger-induced enqueue failure and assert `indexed_sha` remains unchanged and the index job remains running. Cover coalescing, superseding, lease loss, retry cap, reaping, and external precedence during completion.

- [ ] **Step 2: Run queue integration tests and observe missing behavior**

Run: `make postgres-test`

Expected: FAIL because graph queue methods and atomic enqueue do not exist.

- [ ] **Step 3: Implement the graph queue without a generic queue framework**

```go
func enqueueGraph(ctx context.Context, tx pgx.Tx, repositoryID int64, targetSHA string) error {
	_, err := tx.Exec(ctx, `update graph_jobs set state='superseded', updated_at=now()
		where repository_id=$1 and state='queued' and target_sha<>$2`, repositoryID, targetSHA)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `insert into graph_jobs(repository_id,target_sha,state,max_attempts)
		values($1,$2,'queued',5)
		on conflict do nothing`, repositoryID, targetSHA)
	return err
}
```

Call `enqueueGraph` after updating `repositories.indexed_sha` and before terminal index-job update. `CompleteGraph` must replace the managed artifact and complete the lease in the same transaction; external precedence completes the job as superseded without changing the upload.

- [ ] **Step 4: Run queue, race, and PostgreSQL tests**

Run:

```bash
make postgres-test
go test -race ./internal/indexer ./internal/postgres -count=1
```

Expected: PASS.

- [ ] **Step 5: Commit graph queueing**

```bash
git status --short
git add internal/postgres/graph.go internal/postgres/graph_test.go internal/postgres/queue.go internal/postgres/queue_test.go
git commit -S -m "feat(graph): enqueue scanner jobs"
```

### Task 5: Add authorized ingestion and graph status

**Files:**
- Create: `pkg/api/graph.go`
- Create: `internal/graphingest/service.go`
- Create: `internal/graphingest/service_test.go`
- Modify: `internal/postgres/graph.go`
- Modify: `internal/postgres/graph_test.go`

**Interfaces:**
- Produces: `ValidateExternalUpload`, `UploadExternal`, and `Status` on `graphingest.Service`.
- Produces: public `api.GraphStatus` and `api.SCIPFallbackStatus`.

- [ ] **Step 1: Write failing service and status tests**

```go
func TestUploadExternalRevalidatesAfterParse(t *testing.T) {
	store := &fakeStore{repository: readyRepository(101, strings.Repeat("a", 40))}
	service := Service{Store: store, Limits: testArtifactLimits()}
	store.afterAuthorize = func() { store.repository.IndexedSHA = strings.Repeat("b", 40) }
	_, err := service.UploadExternal(t.Context(), adminPrincipal(101), 101, strings.Repeat("a", 40), validArtifactBytes(t, 101))
	if !errors.Is(err, ErrNotIndexed) || store.replaced {
		t.Fatalf("err=%v replaced=%v", err, store.replaced)
	}
}
```

Cover admin and repository scope, ordinary-user status, ready/pending/fallback/degraded/not-indexed states, and stale SCIP not being advertised as fallback.

- [ ] **Step 2: Run focused tests and observe missing service**

Run: `go test ./internal/graphingest -count=1`

Expected: FAIL because ingestion service and public status types do not exist.

- [ ] **Step 3: Implement the narrow service and status query**

```go
type Service struct {
	Store  Store
	Limits graphartifact.Limits
}

func (s *Service) UploadExternal(ctx context.Context, principal authn.Principal, repositoryID int64, commit string, data []byte) (api.GraphStatus, error) {
	if !principal.Administrator {
		return api.GraphStatus{}, ErrForbidden
	}
	if _, err := s.validate(ctx, principal, repositoryID, commit); err != nil {
		return api.GraphStatus{}, err
	}
	artifact, err := graphartifact.Parse(data, s.Limits)
	if err != nil || artifact.RepositoryID != repositoryID || artifact.Commit != commit {
		return api.GraphStatus{}, ErrInvalidArtifact
	}
	if _, err := s.validate(ctx, principal, repositoryID, commit); err != nil {
		return api.GraphStatus{}, err
	}
	if _, err := s.Store.ReplaceGraph(ctx, repositoryID, postgres.GraphSourceExternal, artifact); err != nil {
		return api.GraphStatus{}, err
	}
	return s.Status(ctx, principal, repositoryID)
}
```

Keep storage errors behind domain sentinels. The status SQL left-joins the current upload, latest current-SHA graph job, and only a current-commit SCIP upload.

- [ ] **Step 4: Run service and PostgreSQL tests**

Run:

```bash
go test ./internal/graphingest -count=1
make postgres-test
```

Expected: PASS.

- [ ] **Step 5: Commit ingestion service**

```bash
git status --short
git add pkg/api/graph.go internal/graphingest internal/postgres/graph.go internal/postgres/graph_test.go
git commit -S -m "feat(graph): authorize graph ingestion"
```

### Task 6: Expose external upload and status REST contracts

**Files:**
- Create: `internal/httpapi/graph.go`
- Create: `internal/httpapi/graph_test.go`
- Modify: `internal/config/config.go`
- Modify: `internal/config/config_test.go`
- Modify: `cmd/graphnest-server/main.go`
- Modify: `cmd/graphnest-server/main_test.go`
- Modify: `docs/openapi.yaml`
- Modify: `scripts/check_openapi.rb`

**Interfaces:**
- Produces: `RegisterGraphIngestion(mux, authenticator, service, maxUploadBytes, maxResponseBytes)`.
- Adds `POST /v1/graph/uploads` and `GET /v1/graph/repositories/{id}/status`.

- [ ] **Step 1: Write failing HTTP and configuration tests**

```go
func TestGraphUploadRejectsUnauthorizedBodyBeforeRead(t *testing.T) {
	body := &countingReader{Reader: bytes.NewReader(validArtifactBytes(t, 101))}
	request := httptest.NewRequest(http.MethodPost,
		"/v1/graph/uploads?repository_id=101&commit="+strings.Repeat("a", 40), body)
	request.Header.Set("Content-Type", "application/vnd.graphnest.graph.v1+protobuf")
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusUnauthorized || body.reads != 0 {
		t.Fatalf("status=%d reads=%d", recorder.Code, body.reads)
	}
}
```

Cover exact method/content type/query parsing, 64 MiB default and 256 MiB hard cap, `204` replacement, bounded status JSON, stale `409`, routes absent in static mode, and OpenAPI coverage.

- [ ] **Step 2: Run focused tests and observe missing routes/config**

Run:

```bash
go test ./internal/httpapi ./internal/config ./cmd/graphnest-server -count=1
make openapi-check
```

Expected: FAIL because graph routes and graph upload limits do not exist.

- [ ] **Step 3: Implement deadline-safe upload and durable-only wiring**

```go
func RegisterGraphIngestion(mux *http.ServeMux, authenticator authn.Authenticator, service *graphingest.Service, maxUploadBytes, maxResponseBytes int64) {
	mux.Handle("/v1/graph/uploads", exactMethod(http.MethodPost,
		AuthenticateBearer(authenticator, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// Parse selectors, authorize before clearing read deadline, read max+1,
			// restore a write deadline, revalidate, and replace.
		}))))
}
```

Follow the proven SCIP upload deadline sequence rather than adding middleware abstraction. Add `GRAPHNEST_GRAPH_MAX_UPLOAD_BYTES`, default `67108864`, hard cap `268435456`.

- [ ] **Step 4: Run focused and full verification**

Run:

```bash
gofmt -w internal/httpapi internal/config cmd/graphnest-server pkg/api
go test ./internal/httpapi ./internal/config ./cmd/graphnest-server -count=1
make openapi-check
make test-race
git diff --check
```

Expected: PASS.

- [ ] **Step 5: Commit the foundation API**

```bash
git status --short
git add internal/httpapi/graph.go internal/httpapi/graph_test.go internal/config/config.go internal/config/config_test.go cmd/graphnest-server/main.go cmd/graphnest-server/main_test.go docs/openapi.yaml scripts/check_openapi.rb
git commit -S -m "feat(graph): expose ingestion API"
```

## Phase verification

- [ ] Run `make fmt lint test-race build staticcheck govulncheck openapi-check`.
- [ ] Run `make postgres-integration`.
- [ ] Run `git diff --check`.
- [ ] Verify every new commit with `git log --show-signature --format='%h %G? %s' origin/main..HEAD`.
- [ ] Confirm `git status --short --branch` is clean before starting the scanner plan.
