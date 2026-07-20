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

func TestConsoleMatchesApprovedResponsiveVisualContract(t *testing.T) {
	for _, want := range []string{
		`:root{--night:#132238;--paper:#F4F7FB;--panel:#FFFFFF;--steel:#66758A;--signal:#2F6FEB;--match:#FFE08A;`,
		`width:min(300px,calc(100vw - 40px));max-width:100%`,
		`link.rel="noopener noreferrer"`,
	} {
		if !bytes.Contains(document, []byte(want)) {
			t.Fatalf("console is missing responsive visual contract %q", want)
		}
	}
}
