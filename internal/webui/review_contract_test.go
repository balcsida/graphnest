package webui

import (
	"bytes"
	"testing"
)

func TestConsoleShowsStructuredAPIErrorWithoutClearingQuery(t *testing.T) {
	for _, want := range []string{
		`typeof detail.error.message==="string"`,
		`detail.error.message.trim()`,
		`new Error(message||(response.status>=500?`,
		`error.requestID=detail.error&&detail.error.request_id`,
	} {
		if !bytes.Contains(document, []byte(want)) {
			t.Fatalf("console is missing safe API error behavior %q", want)
		}
	}
	start := bytes.Index(document, []byte("function showError("))
	end := bytes.Index(document[start:], []byte("function updateRepositorySummary("))
	if start < 0 || end < 0 {
		t.Fatal("console is missing bounded error-rendering function")
	}
	if bytes.Contains(document[start:start+end], []byte(`$("query").value=""`)) {
		t.Fatal("API error handling clears the submitted query")
	}
}

func TestConsoleAnnouncesSearchErrors(t *testing.T) {
	want := `<div id="error" role="alert" aria-live="assertive" aria-atomic="true" hidden></div>`
	if !bytes.Contains(document, []byte(want)) {
		t.Fatalf("console is missing assertive atomic error announcement %q", want)
	}
}

func TestConsoleMatchesSuppliedApplicationVisualSystem(t *testing.T) {
	for _, want := range []string{
		`--bg:#0A0D15`,
		`--accent:#8B93FF`,
		`--disp:ui-rounded,"SF Pro Rounded",system-ui,sans-serif`,
		`--body:system-ui,-apple-system,BlinkMacSystemFont,"Segoe UI",sans-serif`,
		`--mono:ui-monospace,SFMono-Regular,Menlo,monospace`,
		`body.light{--bg:#F4F6FB`,
		`[hidden]{display:none!important}`,
		`class="app-bar"`,
		`data-screen="search"`,
		`data-screen="repositories"`,
		`id="theme-toggle"`,
		`id="repository-view"`,
		`class="token-panel"`,
		`class="search-rail"`,
		`class="results-panel"`,
		`grid-template-columns:252px minmax(0,1fr)`,
		`height:56px`,
		`@media(max-width:800px)`,
		`link.rel="noopener noreferrer"`,
	} {
		if !bytes.Contains(document, []byte(want)) {
			t.Fatalf("console is missing supplied application contract %q", want)
		}
	}
}

func TestConsoleRemovesTheObsoleteSearchWorkspaceShell(t *testing.T) {
	for _, obsolete := range []string{
		`--canvas:#F6F8FA`,
		`class="search-strip"`,
		`class="context-rail"`,
		`grid-template-columns:232px minmax(0,1fr)`,
	} {
		if bytes.Contains(document, []byte(obsolete)) {
			t.Fatalf("console retains obsolete shell rule %q", obsolete)
		}
	}
}

func TestConsoleKeepsResultsStaticUntilTheNextShellTask(t *testing.T) {
	for _, forbidden := range []string{`animation:reveal`, `@keyframes reveal`} {
		if bytes.Contains(document, []byte(forbidden)) {
			t.Fatalf("console retains prohibited result animation %q", forbidden)
		}
	}
}
