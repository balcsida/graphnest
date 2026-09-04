package webui

import (
	"bytes"
	"testing"
)

func TestConsoleKeepsTokenGateDesignSystemBackground(t *testing.T) {
	for _, want := range []string{
		`background-image:radial-gradient(640px 400px at 50% 8%,var(--accent-soft),transparent 70%),linear-gradient(var(--border) 1px,transparent 1px),linear-gradient(90deg,var(--border) 1px,transparent 1px)`,
		`background-size:auto,48px 48px,48px 48px`,
	} {
		if !bytes.Contains(document, []byte(want)) {
			t.Fatalf("console is missing token-gate background %q", want)
		}
	}
}

func TestConsoleInvalidatesCredentialScopedRepositoryState(t *testing.T) {
	for _, want := range []string{
		"repositoryController:null",
		"repositoryGeneration:0",
		"function resetRepositories()",
		"state.repositoryController.abort()",
		"state.repositories=[]",
		"$(\"repository-options\").replaceChildren()",
		`$("repository-status").textContent=""`,
		"$(\"all-repositories\").checked=true",
		"generation!==state.repositoryGeneration",
		"signal:controller.signal",
	} {
		if !bytes.Contains(document, []byte(want)) {
			t.Fatalf("console is missing credential-state protection %q", want)
		}
	}
}

func TestConsoleRendersTruncatedAuthorizedRepositories(t *testing.T) {
	for _, want := range []string{
		`id="repository-status"`,
		`if(!Array.isArray(response.repositories))return`,
		`state.repositories=response.repositories`,
		`$("repository-status").textContent=response.truncated?"Only the first authorized repositories are shown.":""`,
	} {
		if !bytes.Contains(document, []byte(want)) {
			t.Fatalf("console is missing truncated repository lifecycle %q", want)
		}
	}
	if bytes.Contains(document, []byte(`if(response.truncated||!Array.isArray(response.repositories))return`)) {
		t.Fatal("console discards the authorized repository prefix when it is truncated")
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

func TestConsoleClearsPrincipalFileAndNavigationStateOnSignOut(t *testing.T) {
	for _, want := range []string{
		"fileController:null",
		"navigationController:null",
		"fileGeneration:0",
		"navigationGeneration:0",
		"fileRetry:null",
		"navigationRetry:null",
		"selectedIdentifier:null",
		"function resetFileState()",
		"state.fileController.abort()",
		"state.navigationController.abort()",
		"state.fileRetry=null",
		"state.navigationRetry=null",
		"state.selectedIdentifier=null",
		`$("file-lines").replaceChildren()`,
		`$("navigation-locations").replaceChildren()`,
		`$("navigation-status").textContent=""`,
		"resetSearchState();resetRepositories();resetFileState()",
	} {
		if !bytes.Contains(document, []byte(want)) {
			t.Fatalf("console is missing file/navigation lifecycle reset %q", want)
		}
	}
}

func TestConsoleUsesCompactControlScaleWithCoarsePointerFallback(t *testing.T) {
	for _, want := range []string{
		"button,input,select,summary{font:inherit;min-height:36px",
		".nav-button{min-height:32px",
		".icon-button{width:32px",
		"input[type=checkbox]{width:16px;height:16px;min-height:0",
		".file-header a,.repository-link{color:var(--accent);display:inline-flex;min-height:28px",
		".file-line{display:grid;grid-template-columns:56px max-content;min-width:max-content;min-height:22px}",
		".file-code{white-space:pre;padding-right:20px;color:var(--code);font:12.5px/22px var(--mono)}",
		".identifier{min-width:0;min-height:0",
		"@media(pointer:coarse){button,input,select,summary,.nav-button,.example,.location-link,.navigation-tabs button,.file-header a,.repository-link,#syntax-rail,#file-back,#syntax-close,#error button{min-height:44px}input[type=checkbox]{width:22px;height:22px}.file-line,.file-code{min-height:32px;line-height:32px}}",
	} {
		if !bytes.Contains(document, []byte(want)) {
			t.Fatalf("console is missing control scale rule %q", want)
		}
	}
}

func TestConsoleKeepsHiddenApplicationStatesAuthoritative(t *testing.T) {
	for _, want := range []string{
		`[hidden]{display:none!important}`,
		`<div id="application" class="app" hidden>`,
		`<section id="search-view" data-screen="search">`,
		`<section id="repository-view" data-screen="repositories" hidden`,
		`<section id="file-view" data-screen="file" hidden`,
		`<section id="token-gate"`,
		`$("token-gate").hidden=true`,
		`$("application").hidden=false`,
		`$("application").hidden=true`,
		`$("token-gate").hidden=false`,
		`function showScreen(screen)`,
		`$("search-view").hidden=screen!=="search"`,
		`$("repository-view").hidden=screen!=="repositories"`,
		`$("file-view").hidden=screen!=="file"`,
	} {
		if !bytes.Contains(document, []byte(want)) {
			t.Fatalf("console is missing authoritative state transition %q", want)
		}
	}
}

func TestConsoleMarksOnlyTheVisibleScreenAsCurrentPage(t *testing.T) {
	for _, want := range []string{
		`active.setAttribute("aria-current","page")`,
		`inactive.removeAttribute("aria-current")`,
	} {
		if !bytes.Contains(document, []byte(want)) {
			t.Fatalf("console is missing current-page navigation behavior %q", want)
		}
	}
	if bytes.Contains(document, []byte(`toggleAttribute("aria-current"`)) {
		t.Fatal("console can expose aria-current without the page value")
	}
}
