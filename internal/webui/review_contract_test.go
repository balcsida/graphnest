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
	if bytes.Contains(document, []byte(`$("query").value=""`)) {
		t.Fatal("API error handling clears the submitted query")
	}
}

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

func TestConsoleUsesOnlyTheScopedSearchWorkspaceShell(t *testing.T) {
	for _, obsolete := range []string{
		`form{display:flex;gap:10px;align-items:center}`,
		`.command-strip{`,
		`--night:#132238`,
		`#token-form{width:min(440px,100%)`,
		`grid-template-columns:minmax(200px,280px) minmax(0,1fr)`,
		`@media(max-width:720px)`,
	} {
		if bytes.Contains(document, []byte(obsolete)) {
			t.Fatalf("console retains obsolete shell rule %q", obsolete)
		}
	}
}
