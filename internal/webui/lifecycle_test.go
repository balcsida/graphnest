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

func TestConsoleKeepsTouchTargetsAtLeast44Pixels(t *testing.T) {
	for _, want := range []string{
		"fieldset label{display:flex;gap:8px;align-items:center;min-height:44px}",
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
