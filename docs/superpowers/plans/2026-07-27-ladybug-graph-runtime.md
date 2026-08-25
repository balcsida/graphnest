# LadybugDB Graph Runtime Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Build the derived LadybugDB graph, bounded query engine, authenticated internal transport, and identical embedded-indexer and standalone runtime modes.

**Architecture:** One runtime process owns the writable LadybugDB handle, serialized updates, and a bounded reader pool. It synchronizes authoritative enriched artifacts or current SCIP fallback facts from PostgreSQL, rebuilds incompatible databases beside the live file, and exposes only authenticated internal graph operations. The same runtime is started inside `graphnest-indexer` or by `graphnest-graph`.

**Tech Stack:** Go 1.26.5, cgo, `github.com/LadybugDB/go-ladybug v0.17.0`, `liblbug v0.18.3`, PostgreSQL/pgx, `net/http`, existing metrics and lifecycle patterns.

## Global Constraints

- Complete the foundation and scanner plans first.
- Work on `feat/ladybug-graph`; use signed atomic conventional commits.
- Pin Go binding `v0.17.0` and native `liblbug v0.18.3`; never download an unpinned latest library during build.
- PostgreSQL remains authoritative; deleting the LadybugDB files must be recoverable by rebuilding.
- One process owns the writable database; serialize writers and use separate reader connections.
- Open no LadybugDB handle in `graphnest-server`, `graphnest-mcp`, or `graphnest-migrate`.
- Use prepared parameters for values; never interpolate artifact or user values into Cypher.
- Close every LadybugDB query result before returning a connection to its pool.
- Curated queries require a nonempty repository allowlist and exact current commits.
- Raw Cypher requires administrator authorization at the public layer and always executes in a read-only transaction.
- Default query timeout is 5 seconds; connection interrupt grace is 2 seconds.
- Default reader pool is 8; hard cap is 32.
- Default result cap is 1,000 rows and 256 KiB encoded output.
- A stale repository graph is excluded and reported; it is never served.
- Schema/library mismatch rebuilds a sibling file and atomically swaps it on the same filesystem.
- Embedded and standalone modes must pass the same internal contract suite.
- `graph.mode` defaults to `embedded`; `separate` leaves the indexer free of LadybugDB handles.

---

## File structure

- `internal/ladybug/schema.go`: physical schema and schema version.
- `internal/ladybug/database.go`: handle ownership, read pool, writer lock, health, and close.
- `internal/ladybug/query.go`: read-only/write transaction and timeout/interrupt execution.
- `internal/ladybug/store.go`: repository subgraph replacement and manifests.
- `internal/ladybug/sync.go`: PostgreSQL reconciliation loop.
- `internal/ladybug/rebuild.go`: verified sibling rebuild and atomic swap.
- `internal/graphprotocol/protocol.go`: internal request/response and snapshot contract.
- `internal/graphquery/`: context, impact, trace, and raw Cypher engine.
- `internal/graphtransport/`: authenticated internal HTTP handler.
- `internal/graphclient/`: bounded internal HTTP client.
- `internal/graphruntime/runtime.go`: common lifecycle used by both modes.
- `cmd/graphnest-graph`: standalone runtime command.

### Task 1: Pin LadybugDB and create the physical schema

**Files:**
- Modify: `go.mod`
- Modify: `go.sum`
- Create: `internal/ladybug/schema.go`
- Create: `internal/ladybug/schema_test.go`
- Modify: `Makefile`

**Interfaces:**
- Produces `const SchemaVersion = 1`.
- Produces `EnsureSchema(ctx context.Context, connection *lbug.Connection) error`.

- [ ] **Step 1: Write the failing on-disk schema smoke test**

```go
func TestEnsureSchemaIsIdempotent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "graph")
	database, err := lbug.OpenDatabase(path, lbug.DatabaseConfig{})
	if err != nil { t.Fatal(err) }
	defer database.Close()
	connection, err := lbug.OpenConnection(database)
	if err != nil { t.Fatal(err) }
	defer connection.Close()
	if err := EnsureSchema(t.Context(), connection); err != nil { t.Fatal(err) }
	if err := EnsureSchema(t.Context(), connection); err != nil { t.Fatal(err) }
	for _, table := range []string{"Repository", "File", "Symbol", "CONTAINS", "IMPORTS", "REFERENCES", "CALLS", "EXTENDS", "IMPLEMENTS"} {
		if !tableExists(t, connection, table) { t.Fatalf("missing %s", table) }
	}
}
```

- [ ] **Step 2: Run the native test and observe missing dependency/schema**

Run: `CGO_ENABLED=1 LBUG_VERSION=0.18.3 go test -tags=system_ladybug ./internal/ladybug -run TestEnsureSchema -count=1`

Expected: FAIL because the binding and schema do not exist.

- [ ] **Step 3: Pin the Go/native versions and implement schema creation**

Use node tables `Repository`, `File`, and `Symbol`; relationship table groups cover only legal endpoint pairs. Store repository ID, commit, upload ID, artifact schema version, source, and content hash in `Repository`.

```go
const SchemaVersion = 1

var schemaStatements = []string{
	`CREATE NODE TABLE IF NOT EXISTS Repository(id INT64, name STRING, commit STRING, upload_id INT64, schema_version INT32, source STRING, content_hash BLOB, PRIMARY KEY(id))`,
	`CREATE NODE TABLE IF NOT EXISTS File(uid STRING, repository_id INT64, path STRING, PRIMARY KEY(uid))`,
	`CREATE NODE TABLE IF NOT EXISTS Symbol(uid STRING, repository_id INT64, path STRING, language STRING, kind STRING, qualified_name STRING, signature STRING, start_line INT32, start_character INT32, end_line INT32, end_character INT32, PRIMARY KEY(uid))`,
}
```

Add prepared indexes only where LadybugDB supports them and a measured query needs them; do not add speculative FTS/vector extensions.

- [ ] **Step 4: Add the native smoke target and run tests**

Add `ladybug-test` using `CGO_ENABLED=1`, `LBUG_VERSION=0.18.3`, and the binding's `system_ladybug` build tag.

Run:

```bash
make ladybug-test
go mod tidy -diff
go mod verify
```

Expected: PASS.

- [ ] **Step 5: Commit the native schema**

```bash
git status --short
git add go.mod go.sum internal/ladybug/schema.go internal/ladybug/schema_test.go Makefile
git commit -S -m "feat(graph): create LadybugDB schema"
```

### Task 2: Own connections, transactions, and cancellation

**Files:**
- Create: `internal/ladybug/database.go`
- Create: `internal/ladybug/database_test.go`
- Create: `internal/ladybug/query.go`
- Create: `internal/ladybug/query_test.go`

**Interfaces:**
- Produces `Open`, `Close`, `Health`, `View`, `Update`, and `Session.Execute`.
- Produces `Options`, `QueryLimits`, and `Result`.

- [ ] **Step 1: Write failing lifecycle and cancellation tests**

Cover concurrent readers, serialized writers, rollback on callback failure, write rejection in `View`, timeout, context cancellation, row/byte truncation, and unhealthy state after interrupt grace.

```go
func TestViewRejectsWrites(t *testing.T) {
	db := testDatabase(t)
	err := db.View(t.Context(), func(session *Session) error {
		_, err := session.Execute(t.Context(), `CREATE (:Symbol {uid: "bad"})`, nil, QueryLimits{MaxRows: 1, MaxBytes: 1024})
		return err
	})
	if err == nil { t.Fatal("write unexpectedly succeeded") }
}
```

- [ ] **Step 2: Run focused tests and observe missing database owner**

Run: `make ladybug-test`

Expected: FAIL because database lifecycle types do not exist.

- [ ] **Step 3: Implement one writer and a bounded reader channel**

```go
type Options struct {
	Path string
	ReadConnections int
	QueryTimeout, InterruptGrace time.Duration
}
type Database struct {
	handle *lbug.Database
	writer *lbug.Connection
	readers chan *lbug.Connection
	writeMu sync.Mutex
	unhealthy atomic.Bool
}
```

Create all handles in `Open`, close idle readers before writer/database, and make `Close` idempotent. Never close a connection while native execution may still be running.

- [ ] **Step 4: Implement read-only execution and cooperative interruption**

```go
func (db *Database) View(ctx context.Context, fn func(*Session) error) error {
	connection := <-db.readers
	reusable := true
	defer func() { if reusable { db.readers <- connection } }()
	if err := executeAndClose(connection, `BEGIN TRANSACTION READ ONLY`); err != nil { return err }
	err := fn(&Session{connection: connection, timeout: db.options.QueryTimeout})
	if rollbackErr := executeAndClose(connection, `ROLLBACK`); err == nil { err = rollbackErr }
	return err
}
```

`Session.Execute` calls `SetTimeout`, runs the query in one goroutine, calls `Interrupt` when the context ends, and waits only for `InterruptGrace`. If native execution does not return, mark the database unhealthy and never return that connection to the pool.

- [ ] **Step 5: Run native race tests and commit**

Run: `CGO_ENABLED=1 LBUG_VERSION=0.18.3 go test -race -tags=system_ladybug ./internal/ladybug -count=1`

```bash
git status --short
git add internal/ladybug/database.go internal/ladybug/database_test.go internal/ladybug/query.go internal/ladybug/query_test.go
git commit -S -m "feat(graph): manage LadybugDB sessions"
```

### Task 3: Expose authoritative graph snapshots and SCIP fallback

**Files:**
- Modify: `internal/postgres/graph.go`
- Modify: `internal/postgres/graph_test.go`
- Create: `internal/graphartifact/fallback.go`
- Create: `internal/graphartifact/fallback_test.go`

**Interfaces:**
- Produces `GraphManifests(ctx) ([]graphartifact.Manifest, error)`.
- Produces `GraphArtifact(ctx, repositoryID, uploadID int64) (graphartifact.Artifact, error)`.
- Produces `FromSCIP(repository, occurrences, relationships) (Artifact, error)`.

- [ ] **Step 1: Write failing snapshot/fallback tests**

```go
func TestGraphArtifactFallsBackToCurrentSCIP(t *testing.T) {
	store, repositoryID := readySCIPStore(t, strings.Repeat("a", 40))
	manifests, err := store.GraphManifests(t.Context())
	if err != nil || len(manifests) != 1 || manifests[0].Source != "scip" {
		t.Fatalf("manifests=%#v err=%v", manifests, err)
	}
	artifact, err := store.GraphArtifact(t.Context(), repositoryID, manifests[0].UploadID)
	if err != nil || !containsSCIPSymbol(artifact) {
		t.Fatalf("artifact=%#v err=%v", artifact, err)
	}
}
```

Cover enriched preference, current-only SCIP fallback, stale SCIP exclusion, disabled/deleted repository omission, deterministic file/symbol nodes, and explicit relationship conversion.

- [ ] **Step 2: Run PostgreSQL tests and observe missing source**

Run: `make postgres-test`

Expected: FAIL because graph snapshot source and fallback conversion do not exist.

- [ ] **Step 3: Implement manifest selection and reduced fallback**

```go
type Manifest struct {
	RepositoryID, UploadID int64
	Commit, Source string
	SchemaVersion uint32
	ContentHash []byte
}
```

Use enriched upload when it matches `indexed_sha`; otherwise use a synthetic negative-free SCIP upload identity derived from the real SCIP upload ID and source `"scip"`. Convert documents/files, symbols, `REFERENCES`, `EXTENDS`, and `IMPLEMENTS`; do not invent `CALLS`.

- [ ] **Step 4: Run artifact and PostgreSQL tests, then commit**

Run:

```bash
go test ./internal/graphartifact -count=1
make postgres-test
```

```bash
git status --short
git add internal/graphartifact/fallback.go internal/graphartifact/fallback_test.go internal/postgres/graph.go internal/postgres/graph_test.go
git commit -S -m "feat(graph): expose graph snapshots"
```

### Task 4: Replace repository subgraphs and track manifests

**Files:**
- Create: `internal/ladybug/store.go`
- Create: `internal/ladybug/store_test.go`

**Interfaces:**
- Produces `Manifests`, `ReplaceRepository`, and `DeleteRepository`.

- [ ] **Step 1: Write failing transactional replacement tests**

```go
func TestReplaceRepositoryRollsBackInvalidEdge(t *testing.T) {
	db := seededDatabase(t, artifactA())
	broken := artifactB()
	broken.Edges[0].TargetUID = "missing"
	if err := db.ReplaceRepository(t.Context(), manifestB(), broken); err == nil {
		t.Fatal("replacement unexpectedly succeeded")
	}
	got, _ := db.Manifests(t.Context())
	if got[101].Commit != manifestA().Commit { t.Fatalf("manifest=%#v", got[101]) }
}
```

Cover nodes-before-edges, complete replacement, repository isolation, delete, duplicate rollback, and manifest update only at commit.

- [ ] **Step 2: Run native tests and observe missing store**

Run: `make ladybug-test`

Expected: FAIL because repository store methods do not exist.

- [ ] **Step 3: Implement prepared transactional replacement**

Delete relationships before nodes for one repository, insert `Repository`, `File`, and `Symbol` nodes, then relationships, all inside one `Database.Update`. Use parameterized `UNWIND` batches small enough to remain under configured memory limits.

- [ ] **Step 4: Run native tests and commit**

Run: `make ladybug-test`

```bash
git status --short
git add internal/ladybug/store.go internal/ladybug/store_test.go
git commit -S -m "feat(graph): replace repository subgraphs"
```

### Task 5: Synchronize and rebuild from PostgreSQL

**Files:**
- Create: `internal/ladybug/sync.go`
- Create: `internal/ladybug/sync_test.go`
- Create: `internal/ladybug/rebuild.go`
- Create: `internal/ladybug/rebuild_test.go`

**Interfaces:**
- Produces `Syncer.SyncOnce`, `Syncer.Run`, and `Rebuild`.

- [ ] **Step 1: Write failing reconciliation and swap tests**

Cover new/changed upload, changed commit, repository deletion, failed replacement, complete rebuild, verification failure preserving live file, and successful same-filesystem swap.

```go
func TestSyncOnceDeletesAbsentRepository(t *testing.T) {
	db := seededDatabase(t, artifactA())
	syncer := Syncer{Source: &fakeSource{manifests: nil}, Database: db}
	if err := syncer.SyncOnce(t.Context()); err != nil { t.Fatal(err) }
	if got, _ := db.Manifests(t.Context()); len(got) != 0 { t.Fatalf("got=%#v", got) }
}
```

- [ ] **Step 2: Run native tests and observe missing synchronizer**

Run: `make ladybug-test`

Expected: FAIL because sync/rebuild do not exist.

- [ ] **Step 3: Implement manifest diff and periodic polling**

```go
type SnapshotSource interface {
	GraphManifests(context.Context) ([]graphartifact.Manifest, error)
	GraphArtifact(context.Context, int64, int64) (graphartifact.Artifact, error)
}
type Syncer struct {
	Source SnapshotSource
	Database *Database
	Interval time.Duration
}
```

Sort repository IDs for deterministic work. Load and replace only changed manifests; delete absent repositories last. `Run` executes one initial sync, then a ticker, and exits on context cancellation.

- [ ] **Step 4: Implement verified sibling rebuild**

Create `<path>.new-<random>` in the live parent, load every snapshot, compare manifests byte-for-byte, run one repository count query, close all handles, then use one same-filesystem `os.Rename` to atomically replace the live database. Verification failure removes only the sibling candidate and leaves the live database untouched.

- [ ] **Step 5: Run native tests and commit**

Run: `make ladybug-test`

```bash
git status --short
git add internal/ladybug/sync.go internal/ladybug/sync_test.go internal/ladybug/rebuild.go internal/ladybug/rebuild_test.go
git commit -S -m "feat(graph): synchronize derived graph"
```

### Task 6: Add bounded context, impact, trace, and Cypher queries

**Files:**
- Create: `internal/graphprotocol/protocol.go`
- Create: `internal/graphquery/service.go`
- Create: `internal/graphquery/context.go`
- Create: `internal/graphquery/context_test.go`
- Create: `internal/graphquery/impact.go`
- Create: `internal/graphquery/impact_test.go`
- Create: `internal/graphquery/trace.go`
- Create: `internal/graphquery/trace_test.go`
- Create: `internal/graphquery/cypher.go`
- Create: `internal/graphquery/cypher_test.go`

**Interfaces:**
- Produces internal protocol requests/responses and `graphquery.Service` methods.
- Requires explicit repository snapshots, not user-provided IDs alone.

- [ ] **Step 1: Write failing query-engine tests**

```go
func TestImpactGroupsByDepth(t *testing.T) {
	service := seededQueryService(t, callChain("A", "B", "C"))
	got, err := service.Impact(t.Context(), graphprotocol.ImpactRequest{
		Scope: scope(101), TargetUID: "A", Direction: "downstream", MaxDepth: 3,
	})
	if err != nil || len(got.ByDepth[1]) != 1 || got.ByDepth[1][0].UID != "B" || got.ByDepth[2][0].UID != "C" {
		t.Fatalf("Impact()=%#v,%v", got, err)
	}
}
```

Cover mandatory allowlist, exact manifest commit, context ambiguity, edge categories, shortest trace, depth/node/edge limits, partial boundaries, non-admin Cypher denial, write rejection, timeout, rows, and output bytes.

- [ ] **Step 2: Run query tests and observe missing engine**

Run: `make ladybug-test`

Expected: FAIL because graph protocol and query services do not exist.

- [ ] **Step 3: Implement the internal protocol and curated queries**

```go
type RepositorySnapshot struct {
	ID, GitHubID int64
	Name, Branch, Commit string
}
type Scope struct { Repositories []RepositorySnapshot `json:"repositories"` }
```

Build Cypher statements from fixed relation allowlists only. Pass selectors, repository IDs, commits, depth, confidence, limit, and offset as parameters. Context limits each edge category; impact uses bounded breadth-first traversal; trace records visited/fanout caps and reconstructs one shortest directed path.

- [ ] **Step 4: Implement read-only raw Cypher**

Accept one nonblank statement and scalar parameters only. Run through `Database.View`; stop collecting at row/output caps. Do not depend on keyword blocking for security—the read-only transaction is authoritative.

- [ ] **Step 5: Run native query tests and commit**

Run: `make ladybug-test`

```bash
git status --short
git add internal/graphprotocol internal/graphquery
git commit -S -m "feat(graph): query code relationships"
```

### Task 7: Add authenticated internal handler and client

**Files:**
- Create: `internal/graphtransport/handler.go`
- Create: `internal/graphtransport/handler_test.go`
- Create: `internal/graphclient/client.go`
- Create: `internal/graphclient/client_test.go`

**Interfaces:**
- Produces `graphtransport.NewHandler`.
- Produces `graphclient.New` and four client methods matching `graphprotocol.QueryEngine`.

- [ ] **Step 1: Write failing transport contract tests**

Cover constant-time bearer verification, strict method/content type/JSON, nonempty scope, request/response caps, exact commit format, timeout propagation, error mapping, and absence of raw parameters in errors.

```go
func TestHandlerRejectsWrongSecret(t *testing.T) {
	request := httptest.NewRequest(http.MethodPost, "/internal/v1/graph/context", strings.NewReader(`{}`))
	request.Header.Set("Authorization", "Bearer wrong")
	recorder := httptest.NewRecorder()
	NewHandler([]byte("right"), fakeEngine{}, testLimits()).ServeHTTP(recorder, request)
	if recorder.Code != http.StatusUnauthorized { t.Fatalf("status=%d", recorder.Code) }
}
```

- [ ] **Step 2: Run focused tests and observe missing transport**

Run: `go test ./internal/graphtransport ./internal/graphclient -count=1`

Expected: FAIL because handler/client do not exist.

- [ ] **Step 3: Implement four fixed internal routes and health**

Use `/internal/v1/graph/context`, `/impact`, `/trace`, `/cypher`, plus `/healthz` and `/readyz`. Compare `sha256.Sum256` values with `subtle.ConstantTimeCompare`; never compare variable-length secret strings directly.

- [ ] **Step 4: Implement the bounded client and round-trip tests**

```go
func New(baseURL string, secret []byte, httpClient *http.Client, maxResponseBytes int64) (*Client, error)
```

Require HTTP(S) base URL without user info/query/fragment, clone secret bytes, use `io.LimitReader(max+1)`, and map only stable error codes.

- [ ] **Step 5: Run transport tests and commit**

Run: `go test -race ./internal/graphtransport ./internal/graphclient -count=1`

```bash
git status --short
git add internal/graphtransport internal/graphclient
git commit -S -m "feat(graph): secure internal queries"
```

### Task 8: Run the shared runtime embedded or standalone

**Files:**
- Create: `internal/graphruntime/runtime.go`
- Create: `internal/graphruntime/runtime_test.go`
- Create: `cmd/graphnest-graph/main.go`
- Create: `cmd/graphnest-graph/main_test.go`
- Modify: `cmd/graphnest-indexer/main.go`
- Modify: `cmd/graphnest-indexer/main_test.go`
- Modify: `internal/config/config.go`
- Modify: `internal/config/config_test.go`
- Modify: `internal/observability/metrics.go`
- Modify: `internal/observability/metrics_test.go`

**Interfaces:**
- Produces one `graphruntime.Runtime` used by both commands.
- Adds `graph.mode=embedded|separate` runtime configuration.

- [ ] **Step 1: Write failing mode-parity and lifecycle tests**

Assert embedded mode starts runtime beside a healthy index worker, separate mode does not open Ladybug in the indexer, standalone initialization order, initial sync before readiness, graceful cancellation, unhealthy query affecting graph readiness only, and identical handler bodies for both modes.

- [ ] **Step 2: Run focused tests and observe missing runtime**

Run: `go test ./internal/graphruntime ./internal/config ./cmd/graphnest-indexer ./cmd/graphnest-graph -count=1`

Expected: FAIL because shared runtime/config/standalone command do not exist.

- [ ] **Step 3: Implement shared config and runtime**

```go
type Config struct {
	DatabasePath, ListenAddress string
	InternalSecret []byte
	ReadConnections int
	SyncInterval, QueryTimeout, InterruptGrace time.Duration
	QueryLimits graphquery.Limits
}
type Runtime struct { /* database, syncer, HTTP server, health */ }
func New(ctx context.Context, config Config, source ladybug.SnapshotSource, logger *slog.Logger) (*Runtime, error)
```

Use one private config loader so embedded and separate defaults/caps cannot drift. Read the internal secret from a bounded file, never an environment value.

- [ ] **Step 4: Wire both commands and low-cardinality metrics**

Indexer starts graph runtime only in embedded mode. `graphnest-graph` always starts it. Add fixed-label query/sync/readiness metrics; repository IDs and commits belong only in structured logs.

- [ ] **Step 5: Run phase verification**

Run:

```bash
gofmt -w internal/graphruntime cmd/graphnest-graph cmd/graphnest-indexer internal/config internal/observability
make ladybug-test
go test -race ./internal/graphtransport ./internal/graphclient ./internal/graphruntime ./cmd/graphnest-indexer ./cmd/graphnest-graph
make fmt lint test-race build staticcheck govulncheck
git diff --check
```

Expected: PASS.

- [ ] **Step 6: Commit runtime modes**

```bash
git status --short
git add internal/graphruntime cmd/graphnest-graph cmd/graphnest-indexer internal/config internal/observability
git commit -S -m "feat(graph): run embedded or standalone"
```

## Phase verification

- [ ] Run all commands from Task 8 Step 5.
- [ ] Run `make postgres-integration` with a real snapshot source.
- [ ] Delete the temporary Ladybug database and prove rebuild restores identical manifests.
- [ ] Run the internal contract once through embedded mode and once through `graphnest-graph`.
- [ ] Verify signatures with `git log --show-signature --format='%h %G? %s' origin/main..HEAD`.
- [ ] Confirm `git status --short --branch` is clean before starting the tools/deployment plan.
