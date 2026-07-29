# LadybugDB Graph Tools and Deployment Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Use superpowers:writing-skills for Task 6. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Publish authorization-aware `context`, `impact`, `trace`, and administrator-only `cypher` through REST and MCP, install the initial agent skills, and package both embedded and standalone graph modes.

**Architecture:** The API server resolves user-facing repository selectors into authorized exact-SHA snapshots and calls the authenticated internal graph client from Phase 3. REST and MCP share one public graph service. Helm and Compose choose the runtime owner while preserving one internal contract and one stateless server path.

**Tech Stack:** Go 1.26.5, existing MCP Go SDK, `net/http`, OpenAPI 3, Prometheus, Docker Compose, Helm 4.2.3, embedded Markdown skills.

## Global Constraints

- Complete the foundation, scanner, and runtime plans first.
- Work on `feat/ladybug-graph`; use signed atomic conventional commits.
- Public graph calls reuse existing bearer authentication and repository authorization.
- Resolve numeric `repo` as public GitHub repository ID; resolve names exactly and case-sensitively.
- Omitted `repo` succeeds only when exactly one repository is authorized.
- Accept `branch` but support only the current indexed default branch.
- Reauthorize and recheck `indexed_sha` after every internal graph call.
- Curated backend calls always carry an explicit allowlist and exact commits.
- Raw Cypher is discoverable but returns a safe forbidden error to non-administrators.
- Default impact depth is 3; maximum 32. Default trace depth is 10; maximum 30.
- Use existing 100-item and 256 KiB public response limits unless a smaller request limit applies.
- Internal graph endpoints remain secret-authenticated, ClusterIP-only, and absent from ingress.
- Normal `grepnest-mcp` proxy startup never writes local files.
- Skill installation writes only GrepNest-marked directories and rejects symlink destinations.
- Preserve existing MCP tool names and behavior.

---

## File structure

- `pkg/api/graph.go`: public selectors and result discriminators.
- `internal/graphservice/`: public authorization, version, limits, and source-content boundary.
- `internal/httpapi/graph_query.go`: public graph REST adapter.
- `internal/mcpserver/graph.go`: graph tool registration and explicit schemas.
- `internal/agentskills/`: embedded initial skills and safe installer.
- `deploy/compose/graph-*.yml`: explicit runtime-mode overlays.
- `deploy/helm/grepnest/templates/graph.yaml`: standalone graph resources.
- `docs/adr/0012-derived-ladybug-graph.md`: accepted storage/runtime boundary.

### Task 1: Add public graph contracts and strict repository resolution

**Files:**
- Modify: `pkg/api/graph.go`
- Create: `pkg/api/graph_test.go`
- Create: `internal/graphservice/resolver.go`
- Create: `internal/graphservice/resolver_test.go`
- Modify: `internal/postgres/repository.go`
- Modify: `internal/postgres/repository_test.go`

**Interfaces:**
- Produces public request/response types for all four tools.
- Produces `ResolveRepository` and `Snapshot`.

- [ ] **Step 1: Write failing selector and resolver tests**

```go
func TestGraphRepositorySelectorJSON(t *testing.T) {
	for _, test := range []struct{ input string; id int64; name string; valid bool }{
		{`101`, 101, "", true},
		{`"owner/repo"`, 0, "owner/repo", true},
		{`0`, 0, "", false},
		{`""`, 0, "", false},
		{`{"id":101}`, 0, "", false},
	} {
		var got GraphRepositorySelector
		err := json.Unmarshal([]byte(test.input), &got)
		if (err == nil) != test.valid || got.ID != test.id || got.Name != test.name {
			t.Fatalf("%s => %#v, %v", test.input, got, err)
		}
	}
}
```

Resolver tests cover omitted single/multiple repository behavior, public GitHub ID, exact name, no fuzzy/case-folded match, missing SHA, valid default branch, and `branch_not_indexed`.

- [ ] **Step 2: Run tests and observe missing contracts**

Run: `go test ./pkg/api ./internal/graphservice ./internal/postgres -run 'TestGraph|TestResolve' -count=1`

Expected: FAIL because public graph types and resolver do not exist.

- [ ] **Step 3: Implement strict selector decoding and snapshot resolution**

```go
type GraphRepositorySelector struct {
	ID int64
	Name string
}
type GraphSymbolSelector struct {
	UID string `json:"uid,omitempty"`
	Name string `json:"name,omitempty"`
	FilePath string `json:"file_path,omitempty"`
	Kind string `json:"kind,omitempty"`
}
type Snapshot struct {
	ID, GitHubID int64
	Name, Branch, Commit string
}
```

Add one PostgreSQL method that lists authorized repositories for selector resolution; do not perform fuzzy matching in Go or SQL.

- [ ] **Step 4: Define stable result discriminators**

Use explicit status strings and typed fields rather than `map[string]any`:

```go
type GraphCandidate struct {
	UID, Name, Kind, FilePath string
	Line int
	Score float64
}
type GraphContextResponse struct {
	Status string `json:"status"`
	Symbol *GraphSymbol `json:"symbol,omitempty"`
	Candidates []GraphCandidate `json:"candidates,omitempty"`
	Incoming map[string][]GraphReference `json:"incoming,omitempty"`
	Outgoing map[string][]GraphReference `json:"outgoing,omitempty"`
	Boundaries []GraphBoundary `json:"boundaries,omitempty"`
	Commits map[string]string `json:"commits"`
}
```

Define equally concrete impact and trace outputs and a tabular Cypher result.

- [ ] **Step 5: Run focused tests and commit**

Run: `gofmt -w pkg/api internal/graphservice internal/postgres && go test ./pkg/api ./internal/graphservice ./internal/postgres -count=1`

```bash
git status --short
git add pkg/api/graph.go pkg/api/graph_test.go internal/graphservice/resolver.go internal/graphservice/resolver_test.go internal/postgres/repository.go internal/postgres/repository_test.go
git commit -S -m "feat(graph): resolve repository snapshots"
```

### Task 2: Add the shared authorization-aware graph service

**Files:**
- Create: `internal/graphservice/service.go`
- Create: `internal/graphservice/context.go`
- Create: `internal/graphservice/context_test.go`
- Create: `internal/graphservice/impact.go`
- Create: `internal/graphservice/impact_test.go`
- Create: `internal/graphservice/trace.go`
- Create: `internal/graphservice/trace_test.go`
- Create: `internal/graphservice/cypher.go`
- Create: `internal/graphservice/cypher_test.go`
- Modify: `internal/repository/service.go`
- Modify: `internal/repository/service_test.go`

**Interfaces:**
- Produces `Service.Context`, `Impact`, `Trace`, and `Cypher`.
- Consumes `graphprotocol.QueryEngine` implemented by `graphclient.Client`.

- [ ] **Step 1: Write failing authorization and exact-SHA tests**

```go
func TestContextReauthorizesAfterBackend(t *testing.T) {
	store := &fakeRepositoryStore{repositories: []repository.Repository{readyRepository("a")}}
	backend := &fakeBackend{after: func() { store.repositories[0].IndexedSHA = strings.Repeat("b", 40) }}
	service := Service{Store: store, Backend: backend, Limits: testLimits()}
	_, err := service.Context(t.Context(), principalFor(101), api.GraphContextRequest{
		Repo: api.GraphRepositorySelector{ID: 101}, GraphSymbolSelector: api.GraphSymbolSelector{UID: "symbol:a"},
	})
	if !errors.Is(err, ErrGraphNotReady) { t.Fatalf("err=%v", err) }
}
```

Cover unauthorized repositories never reaching the backend, backend commit mismatch, content read SHA mismatch, ambiguity, partial boundaries, alias conflicts, depth defaults/caps, pagination, relation allowlist, confidence, test filtering, trace endpoint readiness, and non-admin Cypher rejection before backend access.

- [ ] **Step 2: Run focused tests and observe missing service**

Run: `go test ./internal/graphservice -count=1`

Expected: FAIL because the service does not exist.

- [ ] **Step 3: Implement one service with shared pre/post checks**

```go
type Service struct {
	Store RepositoryStore
	Backend graphprotocol.QueryEngine
	Files ContentReader
	Limits Limits
}
```

Resolve a snapshot before each call, build a scope from authorized current repositories, call the backend, then resolve/recheck every returned repository commit. Keep operation-specific validation in its operation file.

- [ ] **Step 4: Add exact-SHA source content reads**

```go
func (s *Service) ReadFileAt(ctx context.Context, principal authn.Principal, request api.ReadFileRequest, expectedSHA string) (api.ReadFileResponse, error) {
	file, err := s.ReadFile(ctx, principal, request)
	if err != nil { return api.ReadFileResponse{}, err }
	if file.IndexedSHA != expectedSHA { return api.ReadFileResponse{}, ErrNotIndexed }
	return file, nil
}
```

Retain the existing pre/post authorization behavior inside `ReadFile`.

- [ ] **Step 5: Run race tests and commit**

Run: `go test -race ./internal/graphservice ./internal/repository -count=1`

```bash
git status --short
git add internal/graphservice internal/repository/service.go internal/repository/service_test.go
git commit -S -m "feat(graph): authorize graph analysis"
```

### Task 3: Expose the four REST graph queries

**Files:**
- Create: `internal/httpapi/graph_query.go`
- Create: `internal/httpapi/graph_query_test.go`
- Modify: `cmd/grepnest-server/main.go`
- Modify: `cmd/grepnest-server/main_test.go`
- Modify: `docs/openapi.yaml`
- Modify: `scripts/check_openapi.rb`

**Interfaces:**
- Produces `RegisterGraphQueries`.
- Adds four `POST /v1/graph/*` endpoints.

- [ ] **Step 1: Write failing REST adapter tests**

Mirror the strict SCIP handler tests for bearer authentication, methods, content type, unknown JSON fields, multiple values, body caps, error codes, ambiguity, branch rejection, output caps, and administrator-only Cypher.

```go
func TestCypherRequiresAdministrator(t *testing.T) {
	request := jsonRequest(t, "/v1/graph/cypher", api.GraphCypherRequest{Statement: "MATCH (n) RETURN n LIMIT 1"}, "user")
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusForbidden { t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String()) }
}
```

- [ ] **Step 2: Run HTTP tests and observe missing registration**

Run: `go test ./internal/httpapi ./cmd/grepnest-server -run Graph -count=1`

Expected: FAIL because query routes are absent.

- [ ] **Step 3: Implement strict JSON adapters**

```go
func RegisterGraphQueries(mux *http.ServeMux, authenticator authn.Authenticator, service *graphservice.Service, maxRequestBytes, maxResponseBytes int64)
```

Routes are `/v1/graph/context`, `/impact`, `/trace`, and `/cypher`. Map only stable graph-domain errors; never return raw LadybugDB errors or Cypher parameters.

- [ ] **Step 4: Wire durable server runtime**

Construct `graphclient.Client` from bounded secret-file bytes and `GREPNEST_GRAPH_URL`, then inject one `graphservice.Service`. Static mode omits all graph ingestion/query routes.

- [ ] **Step 5: Document the routes, run focused tests, and commit**

Add strict OpenAPI request objects, discriminated responses, branch behavior, `graph_not_ready`, timeout/output limits, and administrator-only Cypher before committing the routes.

Run:

```bash
gofmt -w internal/httpapi cmd/grepnest-server
go test ./internal/httpapi ./cmd/grepnest-server -count=1
make openapi-check
```

```bash
git status --short
git add internal/httpapi/graph_query.go internal/httpapi/graph_query_test.go cmd/grepnest-server/main.go cmd/grepnest-server/main_test.go docs/openapi.yaml scripts/check_openapi.rb
git commit -S -m "feat(graph): expose graph REST queries"
```

### Task 4: Register MCP graph tools through the same service

**Files:**
- Create: `internal/mcpserver/graph.go`
- Modify: `internal/mcpserver/server.go`
- Modify: `internal/mcpserver/server_test.go`
- Modify: `cmd/grepnest-server/main.go`

**Interfaces:**
- Replaces variadic service construction with one explicit `Services` struct.
- Registers `context`, `impact`, `trace`, and `cypher`.

- [ ] **Step 1: Write failing MCP schema and parity tests**

Assert exact tool names, descriptions, explicit `additionalProperties:false` schemas, numeric-or-string repo selector, aliases, branch parameter, Cypher discoverability, direct-service parity, principal propagation, and output budgets.

```go
func TestGraphMCPMatchesService(t *testing.T) {
	server := NewWithLimits(Services{Search: searchService, Repositories: repositories, SCIP: scip, Graph: graphService}, testLimits())
	result := callTool(t, server, "impact", map[string]any{"repo": 101, "target_uid": "symbol:a", "direction": "downstream"})
	if diff := cmp.Diff(wantImpact, result.StructuredContent); diff != "" { t.Fatal(diff) }
}
```

- [ ] **Step 2: Run MCP tests and observe missing tools**

Run: `go test ./internal/mcpserver -count=1`

Expected: FAIL because graph tools and explicit service grouping do not exist.

- [ ] **Step 3: Refactor construction without changing existing tools**

```go
type Services struct {
	Search *search.Service
	Repositories *repository.Service
	SCIP *scipgraph.Service
	Graph *graphservice.Service
}
func NewWithLimits(services Services, limits Limits) *mcp.Server
```

Update all callers/tests atomically. Keep `New` as a compatibility wrapper if external packages use it.

- [ ] **Step 4: Register explicit graph schemas and handlers**

Put graph-specific schemas/handlers in `graph.go`. Each handler calls the shared service with `httpapi.PrincipalFromContext`, uses structured output, and applies the same encoded-output budget as REST.

- [ ] **Step 5: Run MCP/server tests and commit**

Run: `go test -race ./internal/mcpserver ./cmd/grepnest-server -count=1`

```bash
git status --short
git add internal/mcpserver/graph.go internal/mcpserver/server.go internal/mcpserver/server_test.go cmd/grepnest-server/main.go
git commit -S -m "feat(graph): add MCP analysis tools"
```

### Task 5: Complete server graph-client configuration

**Files:**
- Modify: `internal/config/config.go`
- Modify: `internal/config/config_test.go`
- Modify: `cmd/grepnest-server/main.go`
- Modify: `cmd/grepnest-server/main_test.go`

**Interfaces:**
- Extends the runtime plan's shared graph loader with server URL and public request/response limits.
- Uses the fixed-label query metrics already created by the runtime plan.

- [ ] **Step 1: Write failing configuration and metrics tests**

Cover URL and secret-file validation, public depth/count/request/response caps, absent static-mode graph config, and query metric calls without repository/commit labels.

- [ ] **Step 2: Run focused tests and observe incomplete wiring**

Run: `go test ./internal/config ./internal/observability ./cmd/grepnest-server -count=1`

Expected: FAIL until server graph client configuration is complete.

- [ ] **Step 3: Extend the existing private graph loader**

```go
type Graph struct {
	Mode, URL, ListenAddress, DataDir, SecretFile string
	SyncInterval, QueryTimeout, InterruptGrace time.Duration
	ReadConnections, DefaultImpactDepth, MaxImpactDepth int
	DefaultTraceDepth, MaxTraceDepth, MaxRows, MaxNodes, MaxEdges int
	MaxRequestBytes, MaxResponseBytes int64
}
```

Preserve the runtime plan's defaults and mode validation. Add only the server URL and public request/response limits to that loader so command-specific defaults cannot drift.

- [ ] **Step 4: Instrument public graph calls**

Call the runtime plan's existing `ObserveGraphQuery` method after each public graph operation. Keep its operation/result labels fixed; repository IDs and commits remain structured-log fields only.

- [ ] **Step 5: Run tests and commit**

Run: `go test -race ./internal/config ./internal/observability ./cmd/grepnest-server -count=1`

```bash
git status --short
git add internal/config cmd/grepnest-server
git commit -S -m "feat(graph): configure graph clients"
```

### Task 6: Ship and safely install the initial agent skills

**Required sub-skill:** Invoke `superpowers:writing-skills` before editing skill content.

**Files:**
- Create: `internal/agentskills/install.go`
- Create: `internal/agentskills/install_test.go`
- Create: `internal/agentskills/assets/grepnest-guide/SKILL.md`
- Create: `internal/agentskills/assets/grepnest-exploring/SKILL.md`
- Create: `internal/agentskills/assets/grepnest-debugging/SKILL.md`
- Create: `internal/agentskills/assets/grepnest-impact-analysis/SKILL.md`
- Create: `internal/agentskills/assets/*/.grepnest-generated`
- Modify: `cmd/grepnest-mcp/main.go`
- Modify: `cmd/grepnest-mcp/main_test.go`

**Interfaces:**
- Produces `agentskills.Install(root string) error`.
- Adds `grepnest-mcp install-skills [--root PATH]`.

- [ ] **Step 1: Write failing installer safety tests**

Cover `.claude/skills` installation, `.agents/skills` only when `.agents` exists, idempotent generated updates, refusal to overwrite unmarked directories, destination symlinks, path traversal, atomic replacement, and normal proxy startup making no writes.

```go
func TestInstallRefusesUnownedSkill(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, ".claude", "skills", "grepnest-guide")
	mkdir(t, target)
	writeFile(t, filepath.Join(target, "SKILL.md"), "user content")
	if err := Install(root); !errors.Is(err, ErrUnownedDestination) { t.Fatalf("err=%v", err) }
}
```

- [ ] **Step 2: Run tests and observe missing installer**

Run: `go test ./internal/agentskills ./cmd/grepnest-mcp -count=1`

Expected: FAIL because installer/assets/subcommand do not exist.

- [ ] **Step 3: Write the four concise skills**

- Guide documents schema, selectors, statuses, confidence, boundaries, and administrator-only Cypher.
- Exploring uses `list_repositories`, `context`, and bounded `trace`.
- Debugging starts from the observed symbol, follows upstream/downstream impact, then confirms one trace.
- Impact Analysis records target ambiguity, depth, confidence, tests, boundaries, and graph commits.

Do not mention unavailable query, PDG, taint, mutation, group, or generated-area tools.

- [ ] **Step 4: Implement atomic marked-directory installation**

Use `os.Lstat` for every destination component, create a sibling temporary directory, write embedded files with `0600`/directories `0700`, sync/close, then rename. Update only when `.grepnest-generated` matches the embedded marker.

- [ ] **Step 5: Add the explicit subcommand and commit**

Run:

```bash
gofmt -w internal/agentskills cmd/grepnest-mcp
go test -race ./internal/agentskills ./cmd/grepnest-mcp -count=1
```

```bash
git status --short
git add internal/agentskills cmd/grepnest-mcp
git commit -S -m "feat(mcp): install graph agent skills"
```

### Task 7: Package embedded and standalone modes in Compose

**Files:**
- Modify: `deploy/compose/durable.yml`
- Create: `deploy/compose/graph-embedded.yml`
- Create: `deploy/compose/graph-separate.yml`
- Modify: `deploy/compose/test.sh`
- Modify: `README.md`
- Modify: `docs/operations.md`

**Interfaces:**
- Produces two explicit Compose overlays with the same server graph URL contract.

- [ ] **Step 1: Add failing rendered-Compose assertions**

Assert one graph runtime owner, one writable graph volume, no graph host port, same internal secret mount, server URL, scanner scaling, and no change to base/static Compose.

- [ ] **Step 2: Run Compose tests and observe missing overlays**

Run: `make compose-test`

Expected: FAIL because graph overlays do not exist.

- [ ] **Step 3: Add the two minimal overlays**

Add common `grepnest-indexer` and `grepnest-scanner` services to `durable.yml`, using required `GREPNEST_NODE_IMAGE` and `GREPNEST_SCANNER_IMAGE` values. Embedded mode adds graph path/listener to that indexer service. Separate mode disables embedded ownership and adds one `grepnest-graph` service and volume. Both give the server only the internal URL and read-only secret, and both mount the durable Zoekt index writable only in the indexer.

- [ ] **Step 4: Render both combinations and document commands**

Run:

```bash
docker compose -f deploy/compose/compose.yml -f deploy/compose/durable.yml -f deploy/compose/graph-embedded.yml config
docker compose -f deploy/compose/compose.yml -f deploy/compose/durable.yml -f deploy/compose/graph-separate.yml config
make compose-test
```

Expected: PASS.

- [ ] **Step 5: Commit Compose modes**

```bash
git status --short
git add deploy/compose README.md docs/operations.md
git commit -S -m "feat(deploy): compose graph modes"
```

### Task 8: Package both modes in Helm

**Files:**
- Modify: `deploy/helm/grepnest/values.yaml`
- Modify: `deploy/helm/grepnest/values.schema.json`
- Modify: `deploy/helm/grepnest/templates/configmaps.yaml`
- Modify: `deploy/helm/grepnest/templates/node.yaml`
- Create: `deploy/helm/grepnest/templates/graph.yaml`
- Modify: `deploy/helm/grepnest/templates/networkpolicies.yaml`
- Modify: `deploy/helm/grepnest/templates/serviceaccounts.yaml`
- Modify: `deploy/helm/grepnest/templates/servicemonitor.yaml`
- Modify: `deploy/helm/grepnest/tests/render.sh`
- Modify: `deploy/helm/grepnest/ci/minimal-values.yaml`
- Modify: `deploy/helm/grepnest/ci/optional-values.yaml`
- Modify: `deploy/helm/grepnest/README.md`

**Interfaces:**
- Adds `graph.mode: embedded|separate`, default `embedded`.
- Renders one runtime owner and internal service in either mode.

- [ ] **Step 1: Add failing schema/render assertions**

Cover valid modes, invalid mode rejection, embedded node volume/port, separate Deployment/Service/PVC, scanner replicas/resources, probes, non-root/read-only-root/seccomp, no ingress, network policy, secret mounts, and identical server URL.

- [ ] **Step 2: Run Helm tests and observe missing values/templates**

Run: `make helm-lint helm-test`

Expected: FAIL because graph values and templates do not exist.

- [ ] **Step 3: Add values and runtime ownership**

Keep `graph.yaml` absent in embedded mode. In separate mode render one graph Deployment, ClusterIP Service, ServiceAccount, PVC, probes, and metrics. In embedded mode mount `/data/graph` writable only in the indexer container and expose its graph port through an internal Service.

- [ ] **Step 4: Add scanner deployment and network restrictions**

Render scalable scanner workers only when enabled. Disable service-account token automount, use ephemeral worktree storage, and allow egress only to configured GitHub, PostgreSQL, and DNS according to existing chart policy capabilities.

- [ ] **Step 5: Run Helm verification and commit**

Run: `make helm-lint helm-test`

```bash
git status --short
git add deploy/helm/grepnest
git commit -S -m "feat(helm): deploy graph runtime modes"
```

### Task 9: Complete architecture and operator contracts

**Files:**
- Modify: `docs/architecture.md`
- Modify: `docs/operations.md`
- Modify: `docs/compatibility.md`
- Create: `docs/adr/0012-derived-ladybug-graph.md`
- Modify: `README.md`

**Interfaces:**
- Documents graph architecture, runtime modes, recovery, compatibility, and operation.

- [ ] **Step 1: Audit the already-enforced API contract**

Confirm upload/status/context/impact/trace/Cypher paths, exact request schemas, response discriminators, repo one-of integer/string, administrator-only documentation, branch behavior, and `graph_not_ready` already pass `make openapi-check`.

- [ ] **Step 2: Run documentation checks before editing**

Run: `make openapi-check`

Expected: PASS, establishing the public contract baseline.

- [ ] **Step 3: Complete the ADR**

ADR records PostgreSQL authority, LadybugDB as derived storage, single-writer topology, embedded/separate modes, cgo/glibc consequences, SCIP fallback, and rejected per-server copies.

- [ ] **Step 4: Document operations and compatibility**

Include scanner enablement, external upload, graph status, rebuild, backup implications, readiness separation, query bounds, skill installation, supported languages/platforms, and native dependency pinning.

- [ ] **Step 5: Run docs checks and commit**

Run: `make openapi-check && git diff --check`

```bash
git status --short
git add docs README.md
git commit -S -m "docs: document graph analysis"
```

### Task 10: Add end-to-end mode parity and CI gates

**Files:**
- Create: `test/integration/graph_contract_test.go`
- Create: `test/e2e/graph_test.go`
- Create: `test/fixtures/graph/go/`
- Create: `test/fixtures/graph/typescript/`
- Create: `test/fixtures/graph/javascript/`
- Create: `test/fixtures/graph/java/`
- Create: `test/fixtures/graph/kotlin/`
- Create: `test/fixtures/graph/rust/`
- Modify: `Makefile`
- Modify: `.github/workflows/ci.yml`
- Modify: `docs/implementation-report.md`

**Interfaces:**
- Runs one graph contract against embedded and standalone runtime ownership.
- Adds explicit native build/link, parser ABI, graph integration, and both-mode render gates.

- [ ] **Step 1: Write the failing cross-mode contract**

Seed identical PostgreSQL facts, start each runtime mode, then compare normalized context/impact/trace/Cypher responses. Advance `indexed_sha` between resolution and query and prove no stale result is returned.

- [ ] **Step 2: Add language E2E fixtures**

Each fixture contains at least one cross-file call and one language-specific relationship. Exercise exact checkout → managed scan → PostgreSQL → LadybugDB → REST → MCP for all six parser variants (Go, JS, TS/TSX, Java, Kotlin, Rust).

- [ ] **Step 3: Add CI-native and packaging gates**

Build scanner/indexer/graph with cgo and pinned `liblbug`, run dynamic-link inspection, run the ABI smoke matrix, execute PostgreSQL/Ladybug integration, render both deployment modes, and retain all existing security/static/race/E2E jobs.

- [ ] **Step 4: Run the complete local gate**

Run:

```bash
make fmt lint test-race build staticcheck govulncheck
make postgres-integration
make ladybug-test
make e2e
make compose-test helm-lint helm-test openapi-check
go mod tidy -diff
go mod verify
git diff --check
```

Expected: PASS.

- [ ] **Step 5: Record evidence and commit**

Update the implementation report with exact commands, versions, modes, and results.

```bash
git status --short
git add test Makefile .github/workflows/ci.yml docs/implementation-report.md
git commit -S -m "test(graph): verify graph analysis"
```

## Final verification

- [ ] Run every command from Task 10 Step 4 in a clean process.
- [ ] Verify ordinary tokens cannot execute Cypher or observe unauthorized graph rows.
- [ ] Verify every graph response reports current commits or explicit boundaries.
- [ ] Verify `grepnest-mcp install-skills` is idempotent and normal startup leaves the checkout untouched.
- [ ] Verify embedded and separate Helm/Compose modes have one writer and no public graph endpoint.
- [ ] Verify every commit signature with `git log --show-signature --format='%h %G? %s' origin/main..HEAD`.
- [ ] Confirm `git status --short --branch` is clean.
