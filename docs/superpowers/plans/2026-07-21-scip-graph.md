# SCIP Graph Support Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add authorized, SHA-pinned SCIP upload and cross-repository navigation with manual package metadata and optional GitHub dependency-graph SBOM ingestion.

**Architecture:** Keep Zoekt unchanged and ingest standard SCIP protobuf artifacts into PostgreSQL query tables. One `scipgraph.Service` owns validation, authorization, navigation, and dependency metadata; REST and MCP are thin adapters. GitHub SBOM data only enriches package ownership and dependency selection.

**Tech Stack:** Go 1.26.5, `github.com/scip-code/scip/bindings/go/scip v0.9.0`, protobuf, PostgreSQL/pgx, GitHub REST, existing MCP Go SDK.

## Global Constraints

- Pre-generated SCIP artifacts only; do not run language-specific indexers.
- Accept only the repository's current `indexed_sha` and query only current uploads.
- Scope local SCIP symbols to one upload; match only global symbols across repositories.
- Apply existing installation and repository authorization to origin and result repositories.
- Manual package metadata wins over GitHub metadata.
- GitHub `403` or `404` is a non-destructive unavailable result.
- Bound all request bodies, GitHub responses, result counts, and JSON/MCP output.
- Keep Zoekt search behavior and existing REST/MCP contracts unchanged.

---

## File structure

- `pkg/api/scip.go`: public navigation and dependency request/response types.
- `internal/scipgraph/parse.go`: bounded protobuf validation and normalized rows.
- `internal/scipgraph/package.go`: purl normalization and SCIP package identity.
- `internal/scipgraph/service.go`: authorization-aware upload, navigation, and metadata orchestration.
- `internal/postgres/migrations/003_scip_graph.sql`: graph and package tables/indexes.
- `internal/postgres/scip.go`: transactional replacement and authorized graph queries.
- `internal/githubapp/dependency.go`: bounded GitHub SPDX SBOM fetch and reduction.
- `internal/httpapi/scip.go`: administrator writes and authenticated navigation routes.
- Existing server, MCP, configuration, chart, Compose, README, and tests receive narrow wiring changes.

### Task 1: Pin SCIP and validate artifacts

**Files:**
- Modify: `go.mod`
- Modify: `go.sum`
- Create: `internal/scipgraph/parse.go`
- Create: `internal/scipgraph/parse_test.go`

**Interfaces:**
- Produces: `Parse(data []byte) (Upload, error)`.
- Produces: `Upload{ProjectRoot, IndexerName, IndexerVersion string; Occurrences []Occurrence; Relationships []Relationship}`.
- Produces: `Occurrence{Path, Symbol string; StartLine, StartCharacter, EndLine, EndCharacter, Roles int32; Local bool}`.
- Produces: `Relationship{Source, Target string; Definition, Reference, Implementation, TypeDefinition bool}` where flags preserve SCIP's `is_definition`, `is_reference`, `is_implementation`, and `is_type_definition` meanings.

- [ ] **Step 1: Add the failing parser tests**

Create a protobuf fixture with one definition, one reference, and one implementation relationship; assert normalization to zero-based stored lines, local-symbol detection, four-element and three-element SCIP range handling, duplicate-document rejection, invalid canonical path rejection, invalid symbol rejection, missing metadata rejection, and unspecified position-encoding rejection.

```go
func TestParseNormalizesSCIP(t *testing.T) {
	data := marshalIndex(t, &scip.Index{
		Metadata: &scip.Metadata{ProjectRoot: "file:///workspace", ToolInfo: &scip.ToolInfo{Name: "scip-go", Version: "1"}},
		Documents: []*scip.Document{{RelativePath: "pkg/a.go", PositionEncoding: scip.PositionEncoding_UTF8CodeUnitOffsetFromLineStart,
			Occurrences: []*scip.Occurrence{{Range: []int32{2, 4, 9}, Symbol: "scip-go gomod example.com/a v1.0.0 A#", SymbolRoles: int32(scip.SymbolRole_Definition)}}}},
	})
	upload, err := Parse(data)
	if err != nil || len(upload.Occurrences) != 1 || upload.Occurrences[0].EndLine != 2 || upload.Occurrences[0].EndCharacter != 9 {
		t.Fatalf("Parse() = %#v, %v", upload, err)
	}
}
```

- [ ] **Step 2: Run the parser tests and observe the missing implementation**

Run: `go test ./internal/scipgraph -run TestParse -count=1`

Expected: FAIL because `Parse` and its row types do not exist.

- [ ] **Step 3: Pin and implement the minimal parser**

Run: `go get github.com/scip-code/scip/bindings/go/scip@v0.9.0`

Use `proto.Unmarshal`, `scip.ParseSymbol`, `scip.IsLocalSymbol`, and `path.Clean`. Accept UTF-8/UTF-16/UTF-32 position encodings but preserve offsets exactly; reject only unspecified encoding because callers submit offsets in the document's declared unit. Expand a three-element range `[line,start,end]` to one stored line and a four-element range `[startLine,start,endLine,end]` directly. Flatten `Document.Symbols[].Relationships` into rows.

```go
func Parse(data []byte) (Upload, error) {
	var index scip.Index
	if err := proto.Unmarshal(data, &index); err != nil || index.Metadata == nil || index.Metadata.ToolInfo == nil {
		return Upload{}, ErrInvalidIndex
	}
	// Validate each document once and append normalized occurrences and relationships.
	return upload, nil
}
```

- [ ] **Step 4: Run focused and repository tests**

Run: `go test ./internal/scipgraph ./...`

Expected: PASS.

- [ ] **Step 5: Commit the parser**

```bash
git status --short
git add go.mod go.sum internal/scipgraph/parse.go internal/scipgraph/parse_test.go
git commit -S -m "feat(scip): parse graph uploads"
```

### Task 2: Persist and replace SCIP graphs atomically

**Files:**
- Create: `internal/postgres/migrations/003_scip_graph.sql`
- Create: `internal/postgres/scip.go`
- Create: `internal/postgres/scip_test.go`
- Modify: `internal/postgres/migrate_test.go`

**Interfaces:**
- Consumes: `scipgraph.Upload`, `scipgraph.Occurrence`, and `scipgraph.Relationship` from Task 1.
- Produces: `ReplaceSCIP(ctx context.Context, repositoryID int64, commit string, upload scipgraph.Upload) error`.
- Produces: `OccurrenceAt(ctx context.Context, repositoryID int64, commit, path string, line, character int) (scipgraph.StoredOccurrence, error)`.
- Produces: `Locations(ctx context.Context, principal authn.Principal, origin scipgraph.StoredOccurrence, operation string, max int) ([]scipgraph.Location, bool, error)`.

- [ ] **Step 1: Write failing migration and store integration tests**

Assert all three tables exist, repository deletion cascades, replacement removes the previous upload, stale commits return no current occurrence, local symbols match only within one upload, global definitions/references match across two authorized repositories, implementation relationships resolve their target, unauthorized repositories are excluded in SQL, and `max+1` reports truncation.

```go
func TestReplaceSCIPIsAtomicAndCurrent(t *testing.T) {
	store := migratedStore(t)
	repositoryID := seedReadyRepository(t, store, 101, testSHA('a'))
	if err := store.ReplaceSCIP(t.Context(), repositoryID, testSHA('a'), uploadWith("a.go", globalSymbol)); err != nil { t.Fatal(err) }
	if err := store.ReplaceSCIP(t.Context(), repositoryID, testSHA('a'), uploadWith("b.go", globalSymbol)); err != nil { t.Fatal(err) }
	if _, err := store.OccurrenceAt(t.Context(), repositoryID, testSHA('a'), "a.go", 0, 1); !errors.Is(err, pgx.ErrNoRows) { t.Fatalf("err = %v", err) }
}
```

- [ ] **Step 2: Run the PostgreSQL test and observe missing tables/methods**

Run: `GRAPHNEST_TEST_POSTGRES_DSN="$GRAPHNEST_TEST_POSTGRES_DSN" go test ./internal/postgres -run 'TestMigrate|TestReplaceSCIP|TestSCIPLocations' -count=1`

Expected: FAIL because migration `003` and store methods do not exist.

- [ ] **Step 3: Add normalized tables and indexes**

Create `scip_uploads`, `scip_occurrences`, `scip_relationships`, and
`repository_packages`. Use `on delete cascade`, one upload per repository, a
commit SHA check, unique occurrence identity,
`(upload_id,path,start_line,start_character,end_line,end_character)` and
`(symbol,local)` indexes, source/target symbol indexes for relationships, a
unique package key over repository/source/relation/purl, and a package lookup
index on `(manager,name,version,relation)`.

```sql
create table scip_uploads (
    id bigint generated always as identity primary key,
    repository_id bigint not null unique references repositories(id) on delete cascade,
    commit char(40) not null check (commit ~ '^[0-9a-f]{40}$'),
    project_root text not null,
    indexer_name text not null,
    indexer_version text not null,
    uploaded_at timestamptz not null default now()
);
```

- [ ] **Step 4: Implement one-transaction replacement and authorized queries**

Lock the repository row, require `indexed_sha=commit`, delete the old upload, insert the new upload, and bulk insert rows with `pgx.CopyFrom`. In location queries join `repositories.indexed_sha=scip_uploads.commit` and reuse the durable authorization predicate: installation match plus repository-ID scope. Prefix local symbols with the upload ID in comparisons instead of changing their stored value.

- [ ] **Step 5: Run integration and full tests**

Run: `GRAPHNEST_TEST_POSTGRES_DSN="$GRAPHNEST_TEST_POSTGRES_DSN" go test ./internal/postgres ./...`

Expected: PASS.

- [ ] **Step 6: Commit persistence**

```bash
git status --short
git add internal/postgres/migrations/003_scip_graph.sql internal/postgres/scip.go internal/postgres/scip_test.go internal/postgres/migrate_test.go
git commit -S -m "feat(scip): persist navigation graphs"
```

### Task 3: Add package metadata and dependency-aware resolution

**Files:**
- Create: `internal/scipgraph/package.go`
- Create: `internal/scipgraph/package_test.go`
- Modify: `internal/postgres/scip.go`
- Modify: `internal/postgres/scip_test.go`

**Interfaces:**
- Produces: `ParsePackageURL(value string) (Package, error)`.
- Produces: `Package{PURL, Manager, Name, Version string}`.
- Produces: `PackageMapping{Package Package; Relation, Source string}`.
- Produces: `ReplacePackages(ctx context.Context, repositoryID int64, source string, mappings []scipgraph.PackageMapping) error`.

- [ ] **Step 1: Write failing purl and resolution tests**

Cover canonical `pkg:golang/example.com/acme/lib@v1.2.3`, npm scoped packages, percent decoding, rejected qualifiers/subpaths, invalid schemes, manual-over-GitHub provider choice, preservation of manual rows during GitHub replacement, and version-relaxed resolution only when the origin has a matching `depends_on` row and the destination has `provides`.

```go
func TestParsePackageURL(t *testing.T) {
	got, err := ParsePackageURL("pkg:golang/example.com/acme/lib@v1.2.3")
	if err != nil || got.Manager != "golang" || got.Name != "example.com/acme/lib" || got.Version != "v1.2.3" {
		t.Fatalf("ParsePackageURL() = %#v, %v", got, err)
	}
}
```

- [ ] **Step 2: Run focused tests and observe failure**

Run: `go test ./internal/scipgraph ./internal/postgres -run 'TestParsePackage|TestPackage|TestDependencyAssisted' -count=1`

Expected: FAIL because package parsing and storage do not exist.

- [ ] **Step 3: Implement the bounded purl subset with the standard library**

Use `net/url.PathUnescape` and `strings.Cut`; accept `pkg:type/name@version` only. Normalize type and percent escapes, preserve case in package name/version, reject empty components, query qualifiers, and subpaths. Derive SCIP managers with the explicit minimal map `golang→gomod`, `npm→npm`, `maven→maven`, `pypi→pip`, `cargo→cargo`, `nuget→nuget`, and `gem→gem`; reject unsupported purl types instead of guessing.

- [ ] **Step 4: Add package storage and dependency-assisted SQL**

Use the `repository_packages` table and lookup index created in Task 2.
`ReplacePackages` deletes only the selected source. Exact SCIP symbol matches
remain first; if none exist, parse the origin symbol using `scip.ParseSymbol`,
require a declared dependency, and match the same
scheme/manager/name/descriptors to a visible provider while ignoring version.
Return `Approximate: true` on that path.

- [ ] **Step 5: Run focused and full tests**

Run: `go test ./internal/scipgraph ./internal/postgres ./...`

Expected: PASS.

- [ ] **Step 6: Commit dependency metadata**

```bash
git status --short
git add internal/scipgraph/package.go internal/scipgraph/package_test.go internal/postgres/scip.go internal/postgres/scip_test.go
git commit -S -m "feat(scip): map repository packages"
```

### Task 4: Build the authorization-aware SCIP service

**Files:**
- Create: `pkg/api/scip.go`
- Create: `internal/scipgraph/service.go`
- Create: `internal/scipgraph/service_test.go`

**Interfaces:**
- Consumes store methods from Tasks 2-3.
- Produces: `Upload(ctx context.Context, principal authn.Principal, repositoryID int64, commit string, data []byte) error`.
- Produces: `Navigate(ctx context.Context, principal authn.Principal, request api.SCIPNavigationRequest) (api.SCIPNavigationResponse, error)`.
- Produces: `SetDependencies(ctx context.Context, principal authn.Principal, repositoryID int64, purls api.RepositoryPackages) error`.
- Public requests use one-based lines and zero-based characters; storage remains zero-based.

- [ ] **Step 1: Write failing service tests**

Assert administrator-only uploads and metadata writes, repository-scope enforcement even for administrators, ordinary-user navigation, exact SHA checks, line conversion, the smallest containing occurrence, result truncation, approximate flags, and stable errors for invalid operation/path/position/index.

```go
func TestNavigateAuthorizesEveryLocation(t *testing.T) {
	service := Service{Store: &fakeStore{origin: occurrence, locations: []Location{allowed, forbidden}}, MaxResults: 100}
	got, err := service.Navigate(t.Context(), userPrincipal, api.SCIPNavigationRequest{RepositoryID: 101, Path: "a.go", Line: 3, Character: 4, Operation: "definitions"})
	if err != nil || len(got.Locations) != 1 || got.Locations[0].RepositoryID != 101 { t.Fatalf("Navigate() = %#v, %v", got, err) }
}
```

- [ ] **Step 2: Run the service tests and observe missing API/service**

Run: `go test ./internal/scipgraph -run 'TestUpload|TestNavigate|TestSetDependencies' -count=1`

Expected: FAIL because `Service` and public API types do not exist.

- [ ] **Step 3: Implement the minimal service**

Use the existing `AuthorizedRepository` store contract before every operation. Require `principal.Administrator` for writes, but never treat it as authorization bypass. Convert API lines to storage lines once. Return `ErrForbidden`, `ErrInvalidRequest`, `ErrInvalidIndex`, and `ErrNotIndexed` for adapters to classify without exposing internal errors.

- [ ] **Step 4: Run focused and full tests**

Run: `go test ./internal/scipgraph ./...`

Expected: PASS.

- [ ] **Step 5: Commit the service**

```bash
git status --short
git add pkg/api/scip.go internal/scipgraph/service.go internal/scipgraph/service_test.go
git commit -S -m "feat(scip): authorize graph navigation"
```

### Task 5: Consume GitHub dependency-graph SBOMs

**Files:**
- Create: `internal/githubapp/dependency.go`
- Create: `internal/githubapp/dependency_test.go`
- Modify: `internal/githubapp/client.go`
- Modify: `internal/githubapp/client_test.go`
- Modify: `internal/scipgraph/service.go`
- Modify: `internal/scipgraph/service_test.go`

**Interfaces:**
- Produces: `DependencySBOM(ctx context.Context, installationID int64, owner, name string) (SBOM, bool, error)` where the bool is availability.
- Produces: `SBOM{Packages []SBOMPackage; Relationships []SBOMRelationship}` with only bounded fields needed for purl reduction.
- Produces: `RefreshGitHubDependencies(ctx context.Context, principal authn.Principal, repositoryID int64) (api.DependencyRefreshResponse, error)`.

- [ ] **Step 1: Write failing GitHub client and service tests**

Use `httptest.Server` to assert the exact `/repos/acme/repo/dependency-graph/sbom` path, installation authentication, one 401 token refresh, purl extraction from SPDX external references, root `documentDescribes` as `provides`, `DEPENDS_ON` as dependencies, response-size enforcement, `403`/`404` as `available=false`, and preservation of old GitHub rows when unavailable.

```go
func TestDependencySBOMUnavailable(t *testing.T) {
	client := dependencyClient(t, http.StatusNotFound, `{}`)
	_, available, err := client.DependencySBOM(t.Context(), 10, "acme", "repo")
	if err != nil || available { t.Fatalf("available=%v err=%v", available, err) }
}
```

- [ ] **Step 2: Run focused tests and observe failure**

Run: `go test ./internal/githubapp ./internal/scipgraph -run 'TestDependencySBOM|TestRefreshGitHub' -count=1`

Expected: FAIL because the SBOM client and refresh service do not exist.

- [ ] **Step 3: Preserve HTTP status as a typed error**

Add `HTTPStatusError{StatusCode int}` whose `Error()` remains `GitHub API status N`; keep the special 401 retry behavior unchanged. This makes only 403/404 detectable without changing existing error text.

- [ ] **Step 4: Implement bounded SPDX reduction and service refresh**

Decode only `sbom.SPDXID`, `documentDescribes`, packages' `SPDXID` and purl external refs, and `relationships`. Ignore packages without a valid purl. Deduplicate mappings. If unavailable, return `{available:false, packages:0}` without calling `ReplacePackages`; otherwise atomically replace only `source="github"`.

- [ ] **Step 5: Run focused and full tests**

Run: `go test ./internal/githubapp ./internal/scipgraph ./...`

Expected: PASS.

- [ ] **Step 6: Commit GitHub enrichment**

```bash
git status --short
git add internal/githubapp/client.go internal/githubapp/client_test.go internal/githubapp/dependency.go internal/githubapp/dependency_test.go internal/scipgraph/service.go internal/scipgraph/service_test.go
git commit -S -m "feat(scip): import GitHub dependencies"
```

### Task 6: Expose REST and MCP contracts

**Files:**
- Create: `internal/httpapi/scip.go`
- Create: `internal/httpapi/scip_test.go`
- Modify: `internal/mcpserver/server.go`
- Modify: `internal/mcpserver/server_test.go`
- Modify: `cmd/graphnest-server/main.go`
- Modify: `cmd/graphnest-server/main_test.go`
- Modify: `internal/config/config.go`
- Modify: `internal/config/config_test.go`

**Interfaces:**
- `POST /v1/scip/uploads?repository_id=N&commit=SHA`, content type `application/vnd.scip+protobuf`.
- `POST /v1/scip/navigation`, JSON `api.SCIPNavigationRequest`.
- `PUT /v1/scip/dependencies`, JSON `{repository_id, provides, depends_on}`.
- `POST /v1/scip/dependencies/github`, JSON `{repository_id}`.
- MCP tool `navigate_symbol` mirrors the navigation request.

- [ ] **Step 1: Write failing route, limit, and MCP tests**

Cover method/content-type enforcement, strict query/JSON decoding, upload byte limit, admin-only writes, ordinary navigation, error classification, bounded responses, service wiring only in durable mode, and MCP output equality with the service response.

```go
func TestSCIPUploadRequiresAdministrator(t *testing.T) {
	response := scipRequest(t, handler, http.MethodPost, "/v1/scip/uploads?repository_id=101&commit="+sha, protobuf, "user", "application/vnd.scip+protobuf")
	if response.Code != http.StatusForbidden { t.Fatalf("status = %d", response.Code) }
}
```

- [ ] **Step 2: Run adapter tests and observe missing registration**

Run: `go test ./internal/httpapi ./internal/mcpserver ./cmd/graphnest-server -run SCIP -count=1`

Expected: FAIL because routes, MCP tool, and service wiring do not exist.

- [ ] **Step 3: Add the dedicated upload limit**

Add `Limits.SCIPMaxUploadBytes`, default `64<<20`, environment variable `GRAPHNEST_SCIP_MAX_UPLOAD_BYTES`, and safety cap `256<<20`. Do not raise the existing 64 KiB JSON/search cap.

- [ ] **Step 4: Implement thin REST adapters**

Reuse `AuthenticateBearer`, `exactMethod`, `writeBoundedJSON`, and strict JSON conventions. Read protobuf through `http.MaxBytesReader`; require exactly one positive repository ID and one lowercase 40-hex commit. Classify forbidden as 403, invalid request/index as 400, stale/not-indexed as 409, missing repository as 404, and backend failures as 503.

- [ ] **Step 5: Add one MCP navigation tool and durable wiring**

Pass the service optionally to `NewWithLimits`, as repository service is today. Register only `navigate_symbol`; keep upload and metadata writes REST-only. Construct `scipgraph.Service{Store: store, GitHub: githubClient, MaxResults: settings.Limits.MaxResults}` in durable runtime and register routes there.

- [ ] **Step 6: Run adapter and full tests**

Run: `go test ./internal/httpapi ./internal/mcpserver ./cmd/graphnest-server ./...`

Expected: PASS.

- [ ] **Step 7: Commit the public surface**

```bash
git status --short
git add internal/httpapi/scip.go internal/httpapi/scip_test.go internal/mcpserver/server.go internal/mcpserver/server_test.go cmd/graphnest-server/main.go cmd/graphnest-server/main_test.go internal/config/config.go internal/config/config_test.go
git commit -S -m "feat(scip): expose navigation APIs"
```

### Task 7: Document deployment and verify end to end

**Files:**
- Modify: `README.md`
- Modify: `deploy/compose/compose.yml`
- Modify: `deploy/helm/graphnest/values.yaml`
- Modify: `deploy/helm/graphnest/templates/configmaps.yaml`
- Modify: `deploy/helm/graphnest/values.schema.json`
- Modify: `deploy/helm/graphnest/tests/render.sh`
- Create: `test/e2e/scip_test.go`
- Modify: `docs/implementation-report.md`

**Interfaces:**
- Consumes all public contracts from Task 6.
- Produces deployment configuration for `GRAPHNEST_SCIP_MAX_UPLOAD_BYTES` and a runnable upload/navigation example.

- [ ] **Step 1: Write the failing E2E and chart assertions**

Build two tiny SCIP indexes in Go: repository A defines a symbol and repository B references it. Upload both at their indexed SHAs, navigate from B as a principal authorized for both, and assert the definition in A. Repeat with a principal authorized only for B and assert no A location. Extend the render harness to require the new environment setting and schema bounds.

- [ ] **Step 2: Run the focused E2E/render checks and observe missing documentation/configuration**

Run: `go test -tags=e2e ./test/e2e -run TestSCIPCrossRepository -count=1`

Run: `deploy/helm/graphnest/tests/render.sh`

Expected: the E2E fails until its server fixture registers SCIP; the chart check fails until the value is rendered.

- [ ] **Step 3: Complete the fixture, deployment values, and operator documentation**

Document generating `.scip` in CI, the exact upload command, navigation request, manual purl metadata request, GitHub refresh request, required GitHub dependency-graph read permission, graceful 403/404 behavior, upload size limit, SHA consistency, and supported position units. Add no managed indexer containers.

- [ ] **Step 4: Run all verification gates**

Run: `gofmt -w internal/scipgraph internal/postgres/scip.go internal/githubapp/dependency.go internal/httpapi/scip.go pkg/api/scip.go test/e2e/scip_test.go`

Run: `go mod tidy`

Run: `git diff --check`

Run: `make test-race`

Run: `GRAPHNEST_TEST_POSTGRES_DSN="$GRAPHNEST_TEST_POSTGRES_DSN" go test -race ./internal/postgres ./test/e2e -tags=e2e -count=1`

Run: `deploy/helm/graphnest/tests/render.sh`

Expected: every command exits 0; no race, PostgreSQL, real SCIP/Zoekt, or Helm failures.

- [ ] **Step 5: Commit docs and E2E coverage**

```bash
git status --short
git add README.md deploy/compose/compose.yml deploy/helm/graphnest/values.yaml deploy/helm/graphnest/templates/configmaps.yaml deploy/helm/graphnest/values.schema.json deploy/helm/graphnest/tests/render.sh test/e2e/scip_test.go docs/implementation-report.md
git commit -S -m "docs: explain SCIP graph setup"
```

- [ ] **Step 6: Audit final history and worktree**

Run: `git status --short --branch`

Run: `git log --show-signature --oneline main..HEAD`

Run: `git diff --stat main...HEAD`

Expected: clean feature branch, signed atomic commits, and only planned files changed.
