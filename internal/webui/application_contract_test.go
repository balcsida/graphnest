package webui

import (
	"bytes"
	"testing"
)

func TestConsoleExplainsUnavailableSignInChoices(t *testing.T) {
	for _, want := range []string{
		`Continue with GitHub`,
		`Continue with SSO (SAML)`,
		`disabled aria-describedby="provider-help"`,
		`id="provider-help"`,
		`not configured on this server`,
		`Tokens are kept for this browser session only and are never written to disk.`,
	} {
		if !bytes.Contains(document, []byte(want)) {
			t.Fatalf("console is missing truthful sign-in choice %q", want)
		}
	}
}

func TestConsoleProvidesFunctionalSearchFiltersAndExamples(t *testing.T) {
	for _, want := range []string{
		`id="language-filter"`,
		`<option value="">All languages</option>`,
		`function queryWithLanguage(query)`,
		`query:queryWithLanguage($("query").value)`,
		`data-query="lang:go -test NewService"`,
		`data-query="case:yes repo:payments Token"`,
		`function useExample(button)`,
	} {
		if !bytes.Contains(document, []byte(want)) {
			t.Fatalf("console is missing functional search aid %q", want)
		}
	}
}

func TestConsoleProvidesAccessibleQuerySyntaxDrawer(t *testing.T) {
	for _, want := range []string{
		`id="syntax-toggle"`,
		`id="syntax-drawer"`,
		`aria-labelledby="syntax-title"`,
		`file:\.go$`,
		`repo:payments`,
		`"exact phrase"`,
		`function setSyntaxOpen(open,trigger)`,
		`syntaxTrigger.focus()`,
		`setSyntaxOpen(true,$("syntax-rail"))`,
		`event.key==="Escape"`,
	} {
		if !bytes.Contains(document, []byte(want)) {
			t.Fatalf("console is missing query syntax behavior %q", want)
		}
	}
}

func TestConsoleGuidesRepositoryLoadingErrorAndEmptyStates(t *testing.T) {
	for _, want := range []string{
		`$("repository-status").textContent="Loading authorized repositories…";`,
		`"Repository list is unavailable. Search still covers every authorized repository."`,
		`"No authorized repositories are available for this token."`,
		`state.repositories.length===0`,
	} {
		if !bytes.Contains(document, []byte(want)) {
			t.Fatalf("console is missing repository state %q", want)
		}
	}
}
