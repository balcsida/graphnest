package webui

import (
	"bytes"
	"testing"
)

func TestConsoleInvalidatesCredentialScopedRepositoryState(t *testing.T) {
	for _, want := range []string{
		"repositoryController:null",
		"repositoryGeneration:0",
		"function resetRepositories()",
		"state.repositoryController.abort()",
		"state.repositories=[]",
		"$(\"repository-options\").replaceChildren()",
		"$(\"all-repositories\").checked=true",
		"generation!==state.repositoryGeneration",
		"signal:controller.signal",
	} {
		if !bytes.Contains(document, []byte(want)) {
			t.Fatalf("console is missing credential-state protection %q", want)
		}
	}
}

func TestConsoleClearsPrincipalSearchStateOnSignOut(t *testing.T) {
	for _, want := range []string{
		"function resetSearchState()",
		`$("query").value=""`,
		`$("result-count").textContent="No search yet"`,
		`$("repository-count").textContent="All authorized repositories"`,
		`$("status").textContent=""`,
		`$("error").replaceChildren()`,
		`$("error").hidden=true`,
		`$("results").replaceChildren()`,
		`$("repository-picker").open=false`,
		"state.controller=null",
		"state.retry=null",
		"resetSearchState();resetRepositories()",
		`if(response.status===401){signOut()`,
	} {
		if !bytes.Contains(document, []byte(want)) {
			t.Fatalf("console is missing principal search-state reset %q", want)
		}
	}
}

func TestConsoleKeepsTouchTargetsAtLeast44Pixels(t *testing.T) {
	for _, want := range []string{
		"fieldset label{display:flex;gap:8px;align-items:center;min-width:0;min-height:44px;overflow-wrap:anywhere}",
		"fieldset input{width:44px;min-width:44px;min-height:44px}",
		".file-header a{color:var(--signal);display:inline-flex;min-height:44px;align-items:center}",
	} {
		if !bytes.Contains(document, []byte(want)) {
			t.Fatalf("console is missing 44px touch target %q", want)
		}
	}
}

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
