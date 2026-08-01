# Local UI Repair Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make administrator bearer access, static repository search, the local quick start, and durable two-image deployments behave as documented.

**Architecture:** Keep identity authorization unchanged and make both consoles capability-aware. Split repository inventory registration from durable file reads, make the fixture revision deterministic, and put every durable worker binary in the existing node image.

**Tech Stack:** Go 1.26.5, embedded HTML/JavaScript, Node contract harnesses, POSIX shell, Docker Compose, Docker.

## Global Constraints

- Do not weaken OIDC/local-only identity administration or indexed-SHA validation.
- Do not add an unauthenticated GitHub API client or a third release image.
- Reuse the existing embedded UI, repository service, auth-config endpoint, and node image.
- Each production behavior must have a failing regression check before implementation.
- Keep commits signed, conventional, atomic, and at most 72 characters.

---

### Task 1: Preserve operational administrator-token access

**Files:**
- Modify: `internal/webui/admin.html`
- Test: `internal/webui/admin_dom_test.mjs`

**Interfaces:**
- Consumes: existing `mode`, `request`, `load`, `[data-nav]`, and `[data-screen]` UI state.
- Produces: `setIdentityVisibility(visible)` and overview-only access probing.

- [ ] **Step 1: Write the failing bearer-capability DOM checks**

Make the fetch stub return 403 for `/v1/admin/users`, `/v1/admin/groups`, and `/v1/admin/audit-events` in bearer mode. After bearer login assert that none were requested, `admin-shell` is visible, `access-panel` is hidden, overview cards render, and identity navigation/screens are hidden. Make a reconcile request return 403 and assert the shell stays visible and the stored token remains.

```js
assert.equal(requests.some(({path}) => [
  "/v1/admin/users", "/v1/admin/groups", "/v1/admin/audit-events"
].includes(path)), false);
assert.equal(ids.get("admin-shell").hidden, false);
assert.equal(sessionStorage.has("grepnest_admin_token"), true);
```

- [ ] **Step 2: Verify the new checks fail**

Run: `node internal/webui/admin_dom_test.mjs`

Expected: FAIL because bearer load requests identity endpoints and a 403 calls `lockAccess`.

- [ ] **Step 3: Implement the minimal capability split**

Mark Users, Groups, API tokens, and Audit navigation/screens with `data-identity`. Add `setIdentityVisibility(visible)` to toggle them. Change `request` so only `accessProbe=true` treats 403/404 as console-locking; all 401 responses still lock. In `load`, use `/v1/admin/overview` as the only access probe, omit identity/account requests in bearer mode, and render empty identity state only for bearer mode.

```js
async function request(path,options={},accessProbe=false){
  const response=await api(path,options);
  if(response.status===401||accessProbe&&(response.status===403||response.status===404)){
    // existing lockAccess path
  }
  // existing bounded error path
}
```

- [ ] **Step 4: Verify the DOM contract passes**

Run: `node internal/webui/admin_dom_test.mjs`

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/webui/admin.html internal/webui/admin_dom_test.mjs
git commit -S -m "fix(admin): preserve bearer operations"
```

### Task 2: Expose static repository inventory safely

**Files:**
- Modify: `internal/httpapi/repository.go`
- Modify: `internal/httpapi/auth.go`
- Modify: `internal/httpapi/auth_test.go`
- Modify: `cmd/grepnest-server/main.go`
- Test: `cmd/grepnest-server/main_test.go`

**Interfaces:**
- Produces: `RegisterRepositoryInventory(...)`, `RegisterFileReads(...)`, and auth-config JSON field `file_reads bool`.
- Consumes: `repository.Service{Store: registry}` in static mode; durable mode supplies a non-nil `GitHub` content reader.

- [ ] **Step 1: Make static route and auth-config tests fail**

Update `TestStaticHandlerRegistersSystemRoutes` to authenticate `/v1/repositories` and expect 200 while `/v1/files/read` remains 404. Extend the auth-config test to expect `"file_reads":false` and add a true case.

```go
request := httptest.NewRequest(http.MethodGet, "/v1/repositories", nil)
request.Header.Set("Authorization", "Bearer user")
handler.ServeHTTP(response, request)
if response.Code != http.StatusOK { t.Fatalf("status=%d", response.Code) }
```

- [ ] **Step 2: Verify focused tests fail**

Run: `go test ./cmd/grepnest-server ./internal/httpapi -run 'TestStaticHandlerRegistersSystemRoutes|TestRegisterAuth' -count=1`

Expected: FAIL because static mode registers neither inventory nor `file_reads`.

- [ ] **Step 3: Split route registration and wire static inventory**

Move list/detail/status handlers into `RegisterRepositoryInventory`; move `/v1/files/read` into `RegisterFileReads`; keep `RegisterRepositories` as the durable convenience wrapper calling both. In `newHandler`, pass `&repository.Service{Store: registry}`. In `newAPIHandler`, register inventory whenever the service exists and file reads only when `repositories.GitHub != nil`. Pass that boolean to `RegisterAuth` and serialize it as `file_reads`.

```go
fileReads := repositories != nil && repositories.GitHub != nil
httpapi.RegisterAuth(mux, true, settings.SSO.BreakGlass, fileReads, providers, authenticator, sessions, metrics)
```

- [ ] **Step 4: Verify focused tests pass**

Run: `go test ./cmd/grepnest-server ./internal/httpapi -run 'TestStaticHandlerRegistersSystemRoutes|TestRegisterAuth' -count=1`

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/httpapi/repository.go internal/httpapi/auth.go internal/httpapi/auth_test.go cmd/grepnest-server/main.go cmd/grepnest-server/main_test.go
git commit -S -m "fix(server): expose static repository inventory"
```

### Task 3: Make static search results honest

**Files:**
- Modify: `internal/webui/index.html`
- Test: `internal/webui/result_contract_test.go`
- Test: `internal/webui/application_contract_test.go`

**Interfaces:**
- Consumes: auth-config `file_reads bool` from Task 2.
- Produces: `state.fileReads`; result paths are buttons only when true.

- [ ] **Step 1: Add failing result-rendering checks**

Extend the Node harness around `renderResults` with `state={fileReads:false}` and assert the path is not a button while the exact indexed source link remains. Render again with `fileReads:true` and assert one path button is present and its click calls `openFile`.

```js
state.fileReads=false;
renderResults({matches:[base]});
if(buttons($("results")).length!==0) throw new Error("static result opened in-app");
if(links($("results")).length!==1) throw new Error("indexed source link missing");
```

- [ ] **Step 2: Verify the checks fail**

Run: `go test ./internal/webui -run 'TestConsole.*SearchResult|TestConsole.*Auth' -count=1`

Expected: FAIL because every result path is currently a button.

- [ ] **Step 3: Consume the existing capability response**

Add `fileReads:false` to state. Set it from `c.file_reads===true` in `start`. In `renderResults`, create the path button and listener only when `state.fileReads`; otherwise append a plain text span. Keep `Open indexed source` unchanged.

```js
const pathControl=state.fileReads?document.createElement("button"):document.createElement("span");
pathControl.textContent=path;
if(state.fileReads)pathControl.addEventListener("click",()=>{/* existing openFile */});
```

- [ ] **Step 4: Verify web UI tests pass**

Run: `go test ./internal/webui -count=1`

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/webui/index.html internal/webui/result_contract_test.go internal/webui/application_contract_test.go
git commit -S -m "fix(ui): respect static file-read capability"
```

### Task 4: Make the quick-start fixture revision reproducible

**Files:**
- Modify: `deploy/compose/compose.yml`
- Modify: `deploy/compose/repositories.json`
- Test: `deploy/compose/test.sh`
- Modify: `README.md`

**Interfaces:**
- Produces: fixed fixture commit SHA shared by Compose indexing and the static registry.

- [ ] **Step 1: Add a failing registry-consistency check**

In `deploy/compose/test.sh`, copy `test/fixtures/repository` into a temporary Git repository, use the fixed identity and `2000-01-01T00:00:00Z` author/committer dates, commit, and compare `git rev-parse HEAD` with `.[] | select(.name=="fixture/repository") | .indexed_sha`.

```sh
test "$(git -C "$fixture" rev-parse HEAD)" = \
  "$(jq -r '.[] | select(.name=="fixture/repository") | .indexed_sha' deploy/compose/repositories.json)"
```

- [ ] **Step 2: Verify the consistency check fails**

Run: `sh deploy/compose/test.sh`

Expected: FAIL because the registry SHA is empty.

- [ ] **Step 3: Fix the deterministic fixture and documentation**

Set the same author and committer dates in the Compose index command, calculate the fixture SHA with the test procedure, and store it in `repositories.json`. Remove the README claim that static mode lacks repository metadata; describe repository inventory and exact external source links.

- [ ] **Step 4: Verify Compose and search-boundary checks pass**

Run: `sh deploy/compose/test.sh && go test ./internal/search -count=1`

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add deploy/compose/compose.yml deploy/compose/repositories.json deploy/compose/test.sh README.md
git commit -S -m "fix(compose): pin quick-start fixture revision"
```

### Task 5: Restore the two-image durable runtime

**Files:**
- Modify: `Dockerfile`
- Modify: `deploy/images/test.sh`
- Modify: `deploy/compose/durable.yml`
- Modify: `deploy/compose/test.sh`
- Modify: `README.md`

**Interfaces:**
- Produces: node image commands `grepnest-indexer`, `grepnest-scanner`, and `grepnest-graph`; scanner service uses `GREPNEST_NODE_IMAGE`.

- [ ] **Step 1: Make image and Compose assertions require the two-image contract**

Add `command -v grepnest-scanner`, `command -v grepnest-graph`, and Ladybug linkage checks to the node image smoke test. Change Compose assertions so `grepnest-scanner.image` must equal the node image and remove the separate scanner-image test input.

```sh
command -v grepnest-scanner
command -v grepnest-graph
ldd /usr/local/bin/grepnest-graph | grep -q "liblbug.so.0 => /usr/lib/"
```

- [ ] **Step 2: Verify tests fail before the image change**

Run: `sh deploy/compose/test.sh`

Expected: FAIL because scanner still uses `GREPNEST_SCANNER_IMAGE`.

When Docker is available, run `make image-test`; expected failure is missing scanner/graph commands.

- [ ] **Step 3: Build and package the missing commands**

Build `./cmd/grepnest-scanner` and `./cmd/grepnest-graph` with the same system-Ladybug flags as the indexer. Copy both into the node stage. Change the scanner service image to `${GREPNEST_NODE_IMAGE:?GREPNEST_NODE_IMAGE is required}` and document only application/node images.

- [ ] **Step 4: Verify runtime packaging**

Run: `sh deploy/compose/test.sh && make build`

Expected: PASS.

When Docker is available, run `make image-test`; expected: `image smoke tests passed`.

- [ ] **Step 5: Commit**

```bash
git add Dockerfile deploy/images/test.sh deploy/compose/durable.yml deploy/compose/test.sh README.md
git commit -S -m "fix(images): package durable worker commands"
```

### Task 6: Integrated verification

**Files:**
- Modify only files required by failures attributable to Tasks 1-5.

**Interfaces:**
- Consumes: all prior task outputs.
- Produces: verified repair branch with no uncommitted changes.

- [ ] **Step 1: Run repository checks**

```bash
make fmt
make test
make test-race
make compose-test
make openapi-check
```

- [ ] **Step 2: Run image checks when Docker is available**

```bash
make image-test
```

- [ ] **Step 3: Repeat the public GitHub browser smoke**

Index `octocat/Hello-World` and `octocat/Spoon-Knife` at exact SHAs. Verify repository inventory and filtering render, search returns the expected match, the result path is non-interactive in static mode, and `Open indexed source` targets the exact SHA.

- [ ] **Step 4: Review branch scope**

```bash
git diff v0.2.0...HEAD --check
git status --short --branch
```

Expected: no whitespace errors and no uncommitted files.
