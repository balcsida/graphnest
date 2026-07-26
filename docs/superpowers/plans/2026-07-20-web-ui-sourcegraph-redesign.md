# GrepNest Search Workspace Redesign Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace the unstable Web UI with a fast, Sourcegraph-inspired search workspace that remains geometrically correct from 320-pixel mobile widths through desktop.

**Architecture:** Keep the dependency-free embedded document in `internal/webui/index.html`. First make authentication and responsive shell geometry deterministic, then reshape the existing bounded client renderer into repository/file/code groups, and finally verify browser-performance budgets plus real Chrome geometry against the checked-in fixture.

**Tech Stack:** Go 1.26.5, embedded HTML/CSS/vanilla JavaScript, Go `testing`, Chrome with temporary Playwright Core for browser verification.

## Global Constraints

- Preserve the session-scoped bearer token in `sessionStorage`; never use URLs, `localStorage`, logs, or HTML for credentials.
- Keep one embedded HTML response below 40 KiB uncompressed.
- Add no frontend framework, package manager, runtime dependency, external script, stylesheet, font, image, source map, router, or build pipeline.
- Keep JavaScript work linear in the server-bounded maximum of 100 matches.
- Render with detached document fragments and safe DOM APIs; never use `innerHTML`, `outerHTML`, or `insertAdjacentHTML`.
- Run no timer, polling loop, scroll handler, resize handler, or result-opacity animation during steady state.
- Keep the document free of horizontal overflow at 320, 390, 768, and 1440 pixels; code may scroll only inside its code viewport.
- Preserve exact routes, CSP hashing, security headers, same-origin REST calls, request cancellation, repository authorization, and indexed-SHA outbound links.
- Preserve semantic labels, keyboard operation, visible focus, forced-color support, reduced motion, and 44-pixel touch targets.
- Use signed conventional commits, each single-line and no longer than 72 characters.

---

## File Map

- Modify `internal/webui/index.html`: application shell, responsive CSS, compact token panel, grouped code-result DOM, and bounded client behavior.
- Modify `internal/webui/review_contract_test.go`: approved shell, responsive, and palette contracts.
- Modify `internal/webui/lifecycle_test.go`: authoritative hidden-state and touch-target contracts.
- Create `internal/webui/result_contract_test.go`: repository/file/code rendering and safe outbound-link contracts.
- Create `internal/webui/performance_contract_test.go`: resource, payload, steady-state, and bounded-rendering budgets.
- Generate verification screenshots outside the repository under the current Codex visualization directory.

---

### Task 1: Stabilize Authentication and Responsive Shell Geometry

**Files:**
- Modify: `internal/webui/review_contract_test.go`
- Modify: `internal/webui/lifecycle_test.go`
- Modify: `internal/webui/index.html`

**Interfaces:**
- Consumes: existing element IDs `token-gate`, `token-form`, `search-form`, `workspace`, `query`, `repository-picker`, `search-button`, and `sign-out`.
- Produces: structural classes `app-bar`, `search-strip`, `token-panel`, `context-rail`, and `results-panel`; authoritative `hidden` behavior used by existing JavaScript.

- [ ] **Step 1: Replace the old visual byte contract with failing shell invariants**

Replace `TestConsoleMatchesApprovedResponsiveVisualContract` in
`internal/webui/review_contract_test.go` with:

```go
func TestConsoleMatchesApprovedSearchWorkspaceContract(t *testing.T) {
	for _, want := range []string{
		`:root{--ink:#172033;--canvas:#F6F8FA;--surface:#FFFFFF;--border:#D8DEE8;--signal:#2563EB;--match:#FFE08A;`,
		`[hidden]{display:none!important}`,
		`class="app-bar"`,
		`class="search-strip"`,
		`class="token-panel"`,
		`class="context-rail"`,
		`class="results-panel"`,
		`grid-template-columns:232px minmax(0,1fr)`,
		`@media(max-width:760px)`,
		`#search-form{grid-template-columns:minmax(0,1fr) auto`,
		`link.rel="noopener noreferrer"`,
	} {
		if !bytes.Contains(document, []byte(want)) {
			t.Fatalf("console is missing search workspace contract %q", want)
		}
	}
}
```

- [ ] **Step 2: Add failing lifecycle assertions for mutually exclusive states**

Add to `internal/webui/lifecycle_test.go`:

```go
func TestConsoleKeepsHiddenApplicationStatesAuthoritative(t *testing.T) {
	for _, want := range []string{
		`[hidden]{display:none!important}`,
		`<form id="search-form" class="search-strip" hidden>`,
		`<section id="workspace" hidden>`,
		`<section id="token-gate"`,
		`$("token-gate").hidden=true`,
		`$("workspace").hidden=false`,
		`$("search-form").hidden=false`,
		`$("workspace").hidden=true`,
		`$("search-form").hidden=true`,
		`$("token-gate").hidden=false`,
	} {
		if !bytes.Contains(document, []byte(want)) {
			t.Fatalf("console is missing authoritative state transition %q", want)
		}
	}
}
```

- [ ] **Step 3: Run the focused tests and verify RED**

Run:

```bash
GOCACHE=/tmp/grepnest-webui-go-build go test ./internal/webui -run 'TestConsoleMatchesApprovedSearchWorkspaceContract|TestConsoleKeepsHiddenApplicationStatesAuthoritative'
```

Expected: FAIL because the new classes, palette, grid, and authoritative
`[hidden]` rule do not exist.

- [ ] **Step 4: Implement the stable shell and compact token panel**

In `internal/webui/index.html`:

1. replace broad selectors such as `form{display:flex}` with ID/class-scoped layout rules;
2. add `[hidden]{display:none!important}` immediately after the reset;
3. use `.app-bar` for the 52-pixel wordmark/session row;
4. make `#search-form.search-strip` a grid with `minmax(0,1fr) auto auto`;
5. make `#workspace` a `232px minmax(0,1fr)` grid;
6. add `min-width:0` to every result-bearing flex or grid child;
7. at `max-width:760px`, collapse the workspace to one column and make the search form a two-row grid whose query occupies `1/-1`; and
8. keep the token form inside a maximum-400-pixel `.token-panel` directly below the app bar.

Use this structural skeleton while preserving all current IDs and labels:

```html
<header class="app-bar">
  <a class="wordmark" href="/">GrepNest</a>
  <span class="product-label">Code search</span>
  <button id="sign-out" type="button" hidden>Sign out</button>
</header>
<form id="search-form" class="search-strip" hidden>
  <label class="sr-only" for="query">Search code</label>
  <input id="query" name="query" type="search" autocomplete="off" spellcheck="false">
  <details id="repository-picker">
    <summary><span id="repository-summary">All repositories</span></summary>
    <fieldset id="repositories">
      <legend>Search repositories</legend>
      <label><input id="all-repositories" type="checkbox" checked> All authorized repositories</label>
      <div id="repository-options"></div>
    </fieldset>
  </details>
  <button id="search-button" type="submit">Search</button>
</form>
<main>
  <section id="token-gate" aria-labelledby="token-title">
    <form id="token-form" class="token-panel">
      <p class="eyebrow">Private code search</p>
      <h1 id="token-title">Connect to GrepNest</h1>
      <p>Use a bearer token for this browser session.</p>
      <label for="token">Bearer token</label>
      <input id="token" type="password" autocomplete="off" required>
      <button type="submit">Open GrepNest</button>
    </form>
  </section>
  <section id="workspace" hidden>
    <aside class="context-rail" aria-label="Search context">
      <strong id="result-count">No search yet</strong>
      <p id="repository-count">All authorized repositories</p>
      <h2>Query examples</h2>
      <code>file:\.go NewService</code>
      <code>case:yes GrepNest</code>
    </aside>
    <section class="results-panel" aria-labelledby="results-title">
      <header class="results-summary">
        <h1 id="results-title">Code results</h1>
        <p id="status" role="status" aria-live="polite" aria-atomic="true"></p>
      </header>
      <div id="error" hidden></div>
      <div id="results"></div>
    </section>
  </section>
</main>
```

- [ ] **Step 5: Run focused and package tests and verify GREEN**

Run:

```bash
GOCACHE=/tmp/grepnest-webui-go-build go test ./internal/webui
```

Expected: PASS.

- [ ] **Step 6: Commit the shell correction**

```bash
git status --short --branch
git add internal/webui/index.html internal/webui/review_contract_test.go internal/webui/lifecycle_test.go
git commit -S -m "fix(webui): stabilize search workspace layout"
```

---

### Task 2: Render Dense Repository, File, and Code Groups

**Files:**
- Create: `internal/webui/result_contract_test.go`
- Modify: `internal/webui/index.html`

**Interfaces:**
- Consumes: `groupMatches(matches)`, `blobURL(match)`, and the existing bounded `api.SearchResponse.matches` shape.
- Produces: `renderResults(response)` DOM with `repository-group`, `file-result`, `file-header`, `match-block`, `code-row`, `line-gutter`, and `code-viewport` classes.

- [ ] **Step 1: Write the failing result-rendering contract**

Create `internal/webui/result_contract_test.go`:

```go
package webui

import (
	"bytes"
	"testing"
)

func TestConsoleRendersGroupedCodeResults(t *testing.T) {
	for _, want := range []string{
		`group.className="repository-group"`,
		`file.className="file-result"`,
		`header.className="file-header"`,
		`block.className="match-block"`,
		`row.className="code-row"`,
		`gutter.className="line-gutter"`,
		`viewport.className="code-viewport"`,
		`preview.replace(/\n$/,"").split("\n")`,
		`gutter.textContent=String(match.line_number+offset)`,
		`link.textContent="Open indexed source"`,
	} {
		if !bytes.Contains(document, []byte(want)) {
			t.Fatalf("console is missing grouped result rendering %q", want)
		}
	}
	for _, forbidden := range []string{
		`card.className="result"`,
		`animation:reveal`,
	} {
		if bytes.Contains(document, []byte(forbidden)) {
			t.Fatalf("console still contains obsolete result behavior %q", forbidden)
		}
	}
}
```

- [ ] **Step 2: Run the result contract and verify RED**

Run:

```bash
GOCACHE=/tmp/grepnest-webui-go-build go test ./internal/webui -run TestConsoleRendersGroupedCodeResults
```

Expected: FAIL because the current renderer builds `.result` cards.

- [ ] **Step 3: Replace card rendering with bounded grouped rendering**

Keep `groupMatches` as the one-pass map builder. Replace `renderResults`
with the following implementation:

```javascript
function renderResults(response){
  const fragment=document.createDocumentFragment(),groups=groupMatches(response.matches);
  $("result-count").textContent=`${response.matches.length} matches`;
  $("repository-count").textContent=`${groups.size} repositories`;
  if(!response.matches.length){
    const empty=document.createElement("p");
    empty.className="empty-state";
    empty.textContent="No matches. Try file:.go NewService";
    fragment.append(empty);
  }
  for(const [repository,paths] of groups){
    const group=document.createElement("section"),title=document.createElement("h2");
    group.className="repository-group";
    title.textContent=repository;
    group.append(title);
    for(const [path,matches] of paths){
      const file=document.createElement("article"),header=document.createElement("header");
      const pathLabel=document.createElement("h3"),link=document.createElement("a");
      file.className="file-result";
      header.className="file-header";
      pathLabel.textContent=path;
      link.href=blobURL(matches[0]);
      link.textContent="Open indexed source";
      link.target="_blank";
      link.rel="noopener noreferrer";
      header.append(pathLabel,link);
      file.append(header);
      for(const match of matches){
        const block=document.createElement("div"),viewport=document.createElement("div");
        block.className="match-block";
        viewport.className="code-viewport";
        const lines=match.preview.replace(/\n$/,"").split("\n");
        lines.forEach((text,offset)=>{
          const row=document.createElement("div"),gutter=document.createElement("span");
          const code=document.createElement("code");
          row.className="code-row";
          gutter.className="line-gutter";
          gutter.textContent=String(match.line_number+offset);
          code.textContent=text;
          row.append(gutter,code);
          viewport.append(row);
        });
        block.append(viewport);
        file.append(block);
      }
      group.append(file);
    }
    fragment.append(group);
  }
  $("results").replaceChildren(fragment);
}
```

Add CSS that gives file headers a neutral background and thin border, code rows
a fixed line-number column plus `max-content` code, and
`.code-viewport{max-width:100%;overflow-x:auto}`. Do not animate opacity.

- [ ] **Step 4: Run focused and package tests and verify GREEN**

Run:

```bash
GOCACHE=/tmp/grepnest-webui-go-build go test ./internal/webui
```

Expected: PASS.

- [ ] **Step 5: Commit grouped result rendering**

```bash
git status --short --branch
git add internal/webui/index.html internal/webui/result_contract_test.go
git commit -S -m "feat(webui): render grouped code results"
```

---

### Task 3: Enforce Deterministic Browser-Performance Budgets

**Files:**
- Create: `internal/webui/performance_contract_test.go`
- Modify: `internal/webui/index.html` only if the new test exposes a violation.

**Interfaces:**
- Consumes: embedded `document` bytes and the Task 2 renderer.
- Produces: deterministic resource, payload, DOM-construction, and steady-state performance gates.

- [ ] **Step 1: Write the performance contract**

Create `internal/webui/performance_contract_test.go`:

```go
package webui

import (
	"bytes"
	"testing"
)

func TestConsoleKeepsBrowserPerformanceBudget(t *testing.T) {
	if len(document) >= 40<<10 {
		t.Fatalf("document bytes=%d, want less than %d", len(document), 40<<10)
	}
	if got := bytes.Count(document, []byte("<style>")); got != 1 {
		t.Fatalf("style blocks=%d, want 1", got)
	}
	if got := bytes.Count(document, []byte("<script>")); got != 1 {
		t.Fatalf("script blocks=%d, want 1", got)
	}
	for _, forbidden := range []string{
		`<script src=`, `<link rel="stylesheet"`, `@import`, `sourceMappingURL`,
		`setInterval(`, `setTimeout(`, `requestAnimationFrame(`,
		`addEventListener("scroll"`, `addEventListener("resize"`,
		`animation:reveal`,
	} {
		if bytes.Contains(document, []byte(forbidden)) {
			t.Fatalf("console exceeds steady-state budget with %q", forbidden)
		}
	}
	for _, want := range []string{
		`document.createDocumentFragment()`,
		`groupMatches(response.matches)`,
		`$("results").replaceChildren(fragment)`,
		`state.controller.abort()`,
		`code.textContent=text`,
	} {
		if !bytes.Contains(document, []byte(want)) {
			t.Fatalf("console is missing bounded rendering behavior %q", want)
		}
	}
}
```

- [ ] **Step 2: Run the performance test**

Run:

```bash
GOCACHE=/tmp/grepnest-webui-go-build go test ./internal/webui -run TestConsoleKeepsBrowserPerformanceBudget
```

Expected: PASS if Tasks 1 and 2 respected the budget; otherwise FAIL on the
specific forbidden behavior or missing bounded-rendering invariant.

- [ ] **Step 3: Remove only measured violations**

If Step 2 fails, make the smallest correction in `internal/webui/index.html`:
remove external/resource references, timers/listeners, obsolete animation, or
duplicate style/script blocks identified by the failure. Do not minify merely
to hide structural growth.

- [ ] **Step 4: Run all Web UI tests and measure the document**

Run:

```bash
GOCACHE=/tmp/grepnest-webui-go-build go test ./internal/webui
wc -c internal/webui/index.html
```

Expected: tests PASS and `index.html` is below 40,960 bytes.

- [ ] **Step 5: Commit the performance gate**

```bash
git status --short --branch
git add internal/webui/index.html internal/webui/performance_contract_test.go
git commit -S -m "test(webui): enforce browser performance budgets"
```

---

### Task 4: Verify Real Browser Geometry and Capture Settled Screenshots

**Files:**
- Generate outside git: `/Users/hu901131/.codex/visualizations/2026/07/20/019f7f2c-771a-7ec3-9812-3fcfb6c61829/grepnest-redesign/*.png`
- Use temporary browser helper outside git: `/tmp/grepnest-webui-check.mjs`
- No repository files should change.

**Interfaces:**
- Consumes: checked-in fixture, local Zoekt binaries, `grepnest-server`, installed Chrome, and temporary Playwright Core.
- Produces: geometry/performance measurements plus token, desktop-result, and mobile-result screenshots.

- [ ] **Step 1: Start the real checked-in fixture stack**

Follow the fixture commands in `README.md`: index
`test/fixtures/repository` as `fixture/repository` with Zoekt ID 7, start
Zoekt on `127.0.0.1:6070`, and start GrepNest on
`127.0.0.1:58097` with token `grepnest-dev-user-token`.

Expected:

```text
server listening address=127.0.0.1:58097
```

- [ ] **Step 2: Run browser state, geometry, overflow, and resource assertions**

Use Playwright Core only from a temporary directory. The helper must:

1. visit the unauthenticated page and assert `#token-gate` is visible while `#search-form` and `#workspace` are hidden;
2. authenticate, search `GrepNestFixtureNeedle`, and wait for `#result-count` to contain `1 match`;
3. at 320, 390, 768, and 1440 pixels assert `document.documentElement.scrollWidth === document.documentElement.clientWidth`;
4. assert every search control rectangle stays within the viewport and control rectangles do not overlap;
5. assert code overflow is owned by `.code-viewport` while the document remains bounded;
6. assert the initial shell has no cross-origin resource entries;
7. record element count, resource count, and navigation timing for diagnostics; and
8. verify slash focuses the query, Command/Ctrl+Enter submits, and focus rings remain visible; and
9. sign out and assert only the compact token panel returns.

Expected: all assertions pass. Timing values are recorded but are not flaky
thresholds.

- [ ] **Step 3: Exercise a synthetic maximum-size response**

Intercept `POST /v1/search` in the temporary browser helper with 100 bounded
matches spread across repositories and files. Assert the result count is
`100 matches`, the page remains width-bounded at 390 and 1440 pixels, and
the total element count stays below 1,500.

Expected: assertions pass without console errors or page exceptions.

- [ ] **Step 4: Capture settled screenshots**

Use `reducedMotion:"reduce"`, wait for the final status text, and capture:

```text
01-compact-token-panel-desktop.png
02-search-results-desktop.png
03-search-results-mobile.png
```

Expected: no simultaneous authentication/workspace states, no clipped search
controls, no page-level horizontal overflow, and fully opaque results.

- [ ] **Step 5: Run repository verification**

Run:

```bash
GOCACHE=/tmp/grepnest-final-go-build make fmt lint test-race build staticcheck govulncheck helm-lint helm-test
git diff --check
git status --short --branch
git log -3 --show-signature --format='%h %G? %s'
```

Expected: every command passes, all implementation commits have good
signatures, and the worktree is clean.
