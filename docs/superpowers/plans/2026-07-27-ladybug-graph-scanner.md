# LadybugDB Managed Scanner Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add scalable managed scanner workers that produce the approved graph artifact for Go, TypeScript/JavaScript, Java, Kotlin, and Rust without executing repository code.

**Architecture:** Tree-sitter language front ends emit one shared declaration/import/reference IR. A single cross-file resolver produces canonical graph nodes and confidence-bearing edges, then a durable worker checks out the exact indexed commit and publishes a managed artifact through the Phase 1 PostgreSQL boundary.

**Tech Stack:** Go 1.26.5, cgo, `tree-sitter/go-tree-sitter v0.24.0`, ABI-14 grammar matrix, existing GitHub App checkout and PostgreSQL graph queue.

## Global Constraints

- Complete `2026-07-27-ladybug-graph-foundation.md` first.
- Work on `feat/ladybug-graph`; use signed atomic conventional commits.
- The scanner parses source only; never invoke repository compilers, build tools, package managers, hooks, filters, submodules, or generated scripts.
- Never follow symlinks or parse irregular files; skip nested `.git` directories and submodules.
- Default limits: 2 MiB/file, 1 GiB parsed bytes, 100,000 files, 500,000 nodes, 2,000,000 edges, and 30 seconds/file.
- Preserve exact repository ID and 40-character indexed commit in every artifact.
- Prefer exact SCIP symbols when supplied; otherwise use the deterministic fallback identity from Phase 1.
- Keep language-specific behavior in language packages; keep cross-file ranking and edge emission shared.
- Use UTF-8 byte columns and zero-based lines in artifacts.
- Close every Tree-sitter parser, tree, query, cursor, and native handle explicitly.
- Graph scan failures never modify `repositories.indexed_sha` or Zoekt state.
- External current-commit artifacts remain authoritative over managed scans.

## Pinned parser matrix

- `github.com/tree-sitter/go-tree-sitter v0.24.0`
- `github.com/tree-sitter/tree-sitter-go v0.23.4`
- `github.com/tree-sitter/tree-sitter-javascript v0.23.1`
- `github.com/tree-sitter/tree-sitter-typescript v0.23.2`
- `github.com/tree-sitter/tree-sitter-java v0.23.5`
- `github.com/tree-sitter-grammars/tree-sitter-kotlin v1.1.0`
- `github.com/tree-sitter/tree-sitter-rust v0.23.3`

---

## File structure

- `internal/graphscan/ir.go`: common parse records.
- `internal/graphscan/resolve.go`: deterministic cross-file resolution and graph emission.
- `internal/graphscan/scan.go`: secure bounded tree walk and parser dispatch.
- `internal/graphscan/<language>/`: embedded Tree-sitter queries and language adapters.
- `internal/graphscanner/worker.go`: graph queue lease, exact checkout, scan, publish, cleanup.
- `cmd/grepnest-scanner`: standalone scalable worker command.

### Task 1: Add the shared IR and deterministic resolver

**Files:**
- Create: `internal/graphscan/ir.go`
- Create: `internal/graphscan/resolve.go`
- Create: `internal/graphscan/resolve_test.go`

**Interfaces:**
- Consumes `graphartifact.Artifact`, node and edge types from Phase 1.
- Produces `Resolve(repositoryID int64, commit string, files []File) (graphartifact.Artifact, error)`.
- Produces `CanonicalUID(language Language, path, kind, qualifiedName, signature string) string`.

- [ ] **Step 1: Write failing two-file resolution tests**

```go
func TestResolveImportedCall(t *testing.T) {
	files := []File{
		{Path: "lib.go", Language: Go, Declarations: []Declaration{{
			LocalID: "lib.F", Name: "F", QualifiedName: "example/lib.F",
			Kind: "Function", Range: Range{Start: Point{Line: 1}, End: Point{Line: 3}},
		}}},
		{Path: "main.go", Language: Go,
			Imports: []Import{{Path: "main.go", Target: "example/lib", Alias: "lib"}},
			References: []Reference{{
				Path: "main.go", FromLocalID: "main.main", Name: "lib.F",
				Candidates: []string{"example/lib.F"}, Call: true,
				Range: Range{Start: Point{Line: 4, Column: 1}, End: Point{Line: 4, Column: 6}},
			}},
			Declarations: []Declaration{{LocalID: "main.main", Name: "main", QualifiedName: "main.main", Kind: "Function"}},
		},
	}
	got, err := Resolve(101, strings.Repeat("a", 40), files)
	if err != nil || !hasEdge(got, graphartifact.EdgeCalls, "main.main", "example/lib.F") {
		t.Fatalf("Resolve() = %#v, %v", got, err)
	}
}
```

Add ambiguity, unresolved reference, exact SCIP identity, deterministic fallback UID, duplicate declaration, containment, import, and heritage cases.

- [ ] **Step 2: Run the test and observe missing IR**

Run: `go test ./internal/graphscan -run TestResolve -count=1`

Expected: FAIL because `File`, `Resolve`, and related types do not exist.

- [ ] **Step 3: Implement the common IR**

```go
type Language string
const (
	Go Language = "go"; JavaScript Language = "javascript"; TypeScript Language = "typescript"
	Java Language = "java"; Kotlin Language = "kotlin"; Rust Language = "rust"
)
type Point struct{ Line, Column uint32 }
type Range struct{ Start, End Point }
type Declaration struct {
	LocalID, Path, Name, QualifiedName, Signature string
	Kind, ScopeID, Receiver, TypeName, SCIPSymbol string
	Range Range
}
type Import struct {
	Path, Target, Alias string
	Range Range
}
type Reference struct {
	Path, FromLocalID, Name string
	Candidates []string
	Range Range
	Call bool
}
type Heritage struct {
	Path, ChildLocalID string
	Candidates []string
	Kind graphartifact.EdgeKind
	Range Range
}
type File struct {
	Path, Module string
	Language Language
	Declarations []Declaration
	Imports []Import
	References []Reference
	Heritage []Heritage
}
```

- [ ] **Step 4: Implement resolution without a plugin framework**

Index declarations by SCIP identity, qualified name, and ordered candidate keys. Emit repository/file/symbol nodes and `CONTAINS` edges first, then imports, references/calls, and heritage. Resolve only a unique highest-evidence candidate; unresolved or tied candidates do not become false edges. Store a bounded reason and confidence with every non-containment edge.

- [ ] **Step 5: Run focused tests and commit**

Run: `gofmt -w internal/graphscan && go test ./internal/graphscan -count=1`

```bash
git status --short
git add internal/graphscan/ir.go internal/graphscan/resolve.go internal/graphscan/resolve_test.go
git commit -S -m "feat(scanner): resolve shared graph facts"
```

### Task 2: Add the secure bounded tree walk

**Files:**
- Create: `internal/graphscan/scan.go`
- Create: `internal/graphscan/scan_test.go`

**Interfaces:**
- Produces `Scan(ctx context.Context, request Request, parsers map[string]Parser, limits Limits) (graphartifact.Artifact, error)`.
- Produces `Parser func(context.Context, string, []byte) (File, error)`.

- [ ] **Step 1: Write failing filesystem boundary tests**

```go
func TestScanDoesNotFollowSymlinks(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "main.go"), "package main\n")
	if err := os.Symlink("/etc/passwd", filepath.Join(root, "leak.go")); err != nil {
		t.Fatal(err)
	}
	got, err := Scan(t.Context(), Request{RepositoryID: 101, Commit: strings.Repeat("a", 40), Root: root},
		map[string]Parser{".go": fakeParser}, testLimits())
	if err != nil || len(got.Nodes) != expectedRegularNodes {
		t.Fatalf("Scan() = %#v, %v", got, err)
	}
}
```

Cover nested `.git`, irregular files, NUL-byte binaries, per-file/total byte limits, file count, parse timeout, node/edge limits, and context cancellation.

- [ ] **Step 2: Run tests and observe the missing scanner**

Run: `go test ./internal/graphscan -run TestScan -count=1`

Expected: FAIL because secure tree walking does not exist.

- [ ] **Step 3: Implement `WalkDir` with fail-closed accounting**

```go
type Limits struct {
	MaxFileBytes, MaxTotalBytes int64
	MaxFiles, MaxNodes, MaxEdges int
	ParseTimeout time.Duration
	SkipDirectories []string
}
```

Use `DirEntry.Type().IsRegular()`, `os.Lstat`, and `filepath.Rel`; never call `EvalSymlinks`. Read at most `MaxFileBytes+1`, reject count overflow before allocating aggregate slices, and run each parser with `context.WithTimeout`.

- [ ] **Step 4: Run tests and commit**

Run: `gofmt -w internal/graphscan && go test ./internal/graphscan -count=1`

```bash
git status --short
git add internal/graphscan/scan.go internal/graphscan/scan_test.go
git commit -S -m "feat(scanner): bound source traversal"
```

### Task 3: Pin Tree-sitter and add the ABI compatibility gate

**Files:**
- Modify: `go.mod`
- Modify: `go.sum`
- Create: `internal/graphscan/grammars.go`
- Create: `internal/graphscan/grammars_test.go`
- Create: `internal/graphscan/testdata/smoke.go`
- Create: `internal/graphscan/testdata/smoke.js`
- Create: `internal/graphscan/testdata/smoke.ts`
- Create: `internal/graphscan/testdata/smoke.tsx`
- Create: `internal/graphscan/testdata/Smoke.java`
- Create: `internal/graphscan/testdata/smoke.kt`
- Create: `internal/graphscan/testdata/smoke.rs`

**Interfaces:**
- Produces `LanguageForExtension(extension string) (*tree_sitter.Language, bool)`.
- Pins the complete ABI-14 matrix as one atomic dependency change.

- [ ] **Step 1: Write the failing grammar smoke matrix**

```go
func TestGrammarMatrix(t *testing.T) {
	for _, fixture := range []string{"smoke.go", "smoke.js", "smoke.ts", "smoke.tsx", "Smoke.java", "smoke.kt", "smoke.rs"} {
		t.Run(fixture, func(t *testing.T) {
			source := readFixture(t, fixture)
			language, ok := LanguageForExtension(filepath.Ext(fixture))
			if !ok { t.Fatal("missing language") }
			parser := tree_sitter.NewParser()
			defer parser.Close()
			if err := parser.SetLanguage(language); err != nil { t.Fatal(err) }
			tree := parser.Parse(source, nil)
			defer tree.Close()
			if tree.RootNode().HasError() { t.Fatalf("parse error: %s", tree.RootNode().ToSexp()) }
		})
	}
}
```

- [ ] **Step 2: Run the test and observe missing dependencies**

Run: `CGO_ENABLED=1 go test ./internal/graphscan -run TestGrammarMatrix -count=1`

Expected: FAIL because grammar modules and registry do not exist.

- [ ] **Step 3: Pin the exact matrix and implement the registry**

Run one `go get` with all versions listed in this plan. Return grammar pointers directly; do not add runtime grammar loading or a registration interface.

- [ ] **Step 4: Run ABI, module, and vulnerability checks**

Run:

```bash
CGO_ENABLED=1 go test ./internal/graphscan -run TestGrammarMatrix -count=1
go mod tidy -diff
go mod verify
```

Expected: PASS with no module diff.

- [ ] **Step 5: Commit the parser matrix**

```bash
git status --short
git add go.mod go.sum internal/graphscan/grammars.go internal/graphscan/grammars_test.go internal/graphscan/testdata
git commit -S -m "build(scanner): pin parser matrix"
```

### Task 4: Parse Go and TypeScript/JavaScript

**Files:**
- Create: `internal/graphscan/golang/parser.go`
- Create: `internal/graphscan/golang/queries.scm`
- Create: `internal/graphscan/golang/parser_test.go`
- Create: `internal/graphscan/golang/testdata/`
- Create: `internal/graphscan/javascript/parser.go`
- Create: `internal/graphscan/javascript/queries.scm`
- Create: `internal/graphscan/javascript/parser_test.go`
- Create: `internal/graphscan/javascript/testdata/`
- Modify: `internal/graphscan/grammars.go`

**Interfaces:**
- Produces `golang.Parse(context.Context, string, []byte) (graphscan.File, error)`.
- Produces `javascript.Parse(context.Context, string, []byte) (graphscan.File, error)`.

- [ ] **Step 1: Write failing language fixtures**

For Go, assert packages, aliased imports, functions, receivers, method calls, embedded interfaces, and implicit interface implementation evidence. For JS/TS/TSX, assert default/named exports, relative imports, aliases, functions/arrows/classes/methods, calls, `extends`, and `implements`.

```go
func TestParseGoMethodCall(t *testing.T) {
	got, err := Parse(t.Context(), "svc.go", []byte(`package p
type S struct{}
func (S) Run() {}
func main() { S{}.Run() }`))
	if err != nil || !hasDeclaration(got, "S.Run") || !hasCallReference(got, "Run") {
		t.Fatalf("Parse() = %#v, %v", got, err)
	}
}
```

- [ ] **Step 2: Run tests and observe missing parsers**

Run: `CGO_ENABLED=1 go test ./internal/graphscan/golang ./internal/graphscan/javascript -count=1`

Expected: FAIL because parser packages do not exist.

- [ ] **Step 3: Implement embedded queries and adapters**

Compile GrepNest-owned S-expression queries once with `sync.Once`, create and close a parser/tree/query cursor per parse, convert byte/point ranges directly, and emit ordered candidate names rather than resolving imports locally.

- [ ] **Step 4: Run parser and shared resolver tests**

Run:

```bash
gofmt -w internal/graphscan/golang internal/graphscan/javascript
CGO_ENABLED=1 go test ./internal/graphscan/... -count=1
```

Expected: PASS.

- [ ] **Step 5: Commit Go and JavaScript-family parsing**

```bash
git status --short
git add internal/graphscan/golang internal/graphscan/javascript internal/graphscan/grammars.go
git commit -S -m "feat(scanner): parse Go and TypeScript"
```

### Task 5: Parse Java, Kotlin, and Rust

**Files:**
- Create equivalent `parser.go`, `queries.scm`, `parser_test.go`, and `testdata/` under:
  - `internal/graphscan/java/`
  - `internal/graphscan/kotlin/`
  - `internal/graphscan/rust/`
- Modify: `internal/graphscan/grammars.go`

**Interfaces:**
- Each package produces `Parse(context.Context, string, []byte) (graphscan.File, error)`.

- [ ] **Step 1: Write failing language-specific tests**

Java covers packages, explicit/wildcard imports, classes/interfaces, methods/calls, `extends`, and `implements`. Kotlin covers packages, aliases, top-level functions, classes/objects, calls, inheritance, and implementation. Rust covers modules, grouped `use`, functions/calls, traits, inherent `impl`, and trait `impl`.

```go
func TestParseRustTraitImplementation(t *testing.T) {
	got, err := Parse(t.Context(), "lib.rs", []byte(`
trait Run { fn run(&self); }
struct Job;
impl Run for Job { fn run(&self) {} }`))
	if err != nil || !hasImplementation(got, "Job", "Run") {
		t.Fatalf("Parse() = %#v, %v", got, err)
	}
}
```

- [ ] **Step 2: Run tests and observe missing packages**

Run: `CGO_ENABLED=1 go test ./internal/graphscan/java ./internal/graphscan/kotlin ./internal/graphscan/rust -count=1`

Expected: FAIL because the parser packages do not exist.

- [ ] **Step 3: Implement the three adapters**

Keep JVM import candidate construction shared in a small file within `internal/graphscan/java` only if Kotlin imports it without a cycle; otherwise repeat the short candidate construction. Do not create a general language-provider framework.

- [ ] **Step 4: Run every parser and resolver test**

Run: `CGO_ENABLED=1 go test ./internal/graphscan/... -count=1`

Expected: PASS.

- [ ] **Step 5: Commit JVM and Rust parsing**

```bash
git status --short
git add internal/graphscan/java internal/graphscan/kotlin internal/graphscan/rust internal/graphscan/grammars.go
git commit -S -m "feat(scanner): parse JVM and Rust sources"
```

### Task 6: Generalize exact checkout for graph jobs

**Files:**
- Modify: `internal/indexer/git.go`
- Modify: `internal/indexer/git_test.go`
- Modify: `internal/indexer/worker.go`
- Modify: `internal/indexer/worker_test.go`

**Interfaces:**
- Produces `PrepareCommit(ctx, repository, jobID, targetSHA, token) (mirror, worktree string, err error)`.
- Preserves `Prepare(ctx, repository, postgres.IndexJob, token)` as a thin call for existing index workers.

- [ ] **Step 1: Write failing exact-checkout compatibility tests**

```go
func TestPrepareUsesPrepareCommit(t *testing.T) {
	// Assert both index and graph callers use numeric job paths, the exact SHA,
	// hooks disabled, filters disabled, no tags, and no submodules.
}
```

Also preserve every existing invalid-path, symlink, credential-origin, cleanup, and command-environment assertion.

- [ ] **Step 2: Run indexer tests before editing**

Run: `go test ./internal/indexer -count=1`

Expected: PASS, establishing the required baseline.

- [ ] **Step 3: Extract only the job-independent checkout core**

```go
func (git *Git) Prepare(ctx context.Context, repo repository.Repository, job postgres.IndexJob, token string) (string, string, error) {
	return git.PrepareCommit(ctx, repo, job.ID, job.TargetSHA, token)
}
```

Move validation that depends only on job ID/SHA into `PrepareCommit`; preserve the existing command sequence byte-for-byte.

- [ ] **Step 4: Run indexer race tests and commit**

Run: `go test -race ./internal/indexer -count=1`

```bash
git status --short
git add internal/indexer/git.go internal/indexer/git_test.go internal/indexer/worker.go internal/indexer/worker_test.go
git commit -S -m "refactor(indexer): share exact checkout"
```

### Task 7: Add the graph scanner worker

**Files:**
- Create: `internal/graphscanner/worker.go`
- Create: `internal/graphscanner/worker_test.go`

**Interfaces:**
- Consumes Phase 1 `GraphJob`, graph queue, and `ReplaceGraph` boundary.
- Produces `Worker.Run` and `Worker.RunOne`.

- [ ] **Step 1: Write failing worker lifecycle tests**

Cover successful claim/checkout/scan/publish/complete, changed indexed SHA, external precedence, lease loss, retry classification, and cleanup after cancellation.

```go
func TestRunOnePublishesExactCommit(t *testing.T) {
	queue := &fakeQueue{job: postgres.GraphJob{ID: 9, RepositoryID: 101, TargetSHA: strings.Repeat("a", 40)}}
	worker := Worker{ID: "scanner-1", Queue: queue, Store: readyStore(), Git: fakeGit("/work"), Analyzer: fakeAnalyzer()}
	worked, err := worker.RunOne(t.Context())
	if err != nil || !worked || queue.completed != 9 || worker.Store.(*fakeStore).publishedCommit != strings.Repeat("a", 40) {
		t.Fatalf("worked=%v err=%v queue=%#v", worked, err, queue)
	}
}
```

- [ ] **Step 2: Run tests and observe missing worker**

Run: `go test ./internal/graphscanner -count=1`

Expected: FAIL because the worker does not exist.

- [ ] **Step 3: Implement the worker using the indexer lease pattern**

Use narrow interfaces `Queue`, `Store`, `TokenSource`, `GitWorkspace`, and `Analyzer`. Recheck `IndexedSHA` immediately before managed publication. Treat configured scan-limit errors as non-retryable and checkout/token/PostgreSQL failures as retryable.

- [ ] **Step 4: Run scanner and indexer race tests**

Run: `go test -race ./internal/graphscanner ./internal/indexer -count=1`

Expected: PASS.

- [ ] **Step 5: Commit the worker**

```bash
git status --short
git add internal/graphscanner/worker.go internal/graphscanner/worker_test.go
git commit -S -m "feat(scanner): process graph jobs"
```

### Task 8: Add the standalone scanner command and configuration

**Files:**
- Create: `cmd/grepnest-scanner/main.go`
- Create: `cmd/grepnest-scanner/main_test.go`
- Modify: `internal/config/config.go`
- Modify: `internal/config/config_test.go`
- Modify: `internal/observability/metrics.go`
- Modify: `internal/observability/metrics_test.go`
- Modify: `Makefile`

**Interfaces:**
- Produces `config.LoadScanner() (config.Scanner, error)`.
- Produces graph queue and scan phase metrics with fixed labels.

- [ ] **Step 1: Write failing config and command assembly tests**

Assert required PostgreSQL/GitHub/Git paths/worker ID, secure positive limits, default skips, parser map, initialization order, graceful cancellation, and secret-safe logging.

- [ ] **Step 2: Run focused tests and observe missing command**

Run: `go test ./internal/config ./internal/observability ./cmd/grepnest-scanner -count=1`

Expected: FAIL because scanner config and command do not exist.

- [ ] **Step 3: Wire the command without sharing process state with the indexer**

```go
type Scanner struct {
	DatabaseURL, DataDir, GitPath, WorkerID string
	GitHub GitHub
	Limits GraphScanLimits
}
```

Construct PostgreSQL, GitHub signer/client, exact checkout, parser map, analyzer, and worker. Add `scanner-test` to run cgo scanner packages explicitly; do not change server/MCP/migration commands to import scanner packages.

- [ ] **Step 4: Run phase verification**

Run:

```bash
gofmt -w cmd/grepnest-scanner internal/config internal/observability
CGO_ENABLED=1 go test -race ./internal/graphscan/... ./internal/graphscanner ./cmd/grepnest-scanner
make fmt lint test-race build staticcheck govulncheck
go mod tidy -diff
git diff --check
```

Expected: PASS.

- [ ] **Step 5: Commit the scanner command**

```bash
git status --short
git add cmd/grepnest-scanner internal/config internal/observability Makefile
git commit -S -m "feat(scanner): run managed workers"
```

## Phase verification

- [ ] Run all commands from Task 8 Step 4.
- [ ] Run `make postgres-integration` to exercise real graph jobs.
- [ ] Verify all seven grammar objects load in one fresh process.
- [ ] Verify signatures with `git log --show-signature --format='%h %G? %s' origin/main..HEAD`.
- [ ] Confirm `git status --short --branch` is clean before starting the runtime plan.
