package webui

import (
	"bytes"
	"os/exec"
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
		`setSyntaxOpen(true,$("syntax-rail"))`,
		`event.key==="Escape"`,
	} {
		if !bytes.Contains(document, []byte(want)) {
			t.Fatalf("console is missing query syntax behavior %q", want)
		}
	}
}

func TestConsoleExplainsZoektQueryComposition(t *testing.T) {
	for _, want := range []string{
		`Queries use Zoekt syntax.`,
		`Regular expressions are enabled by default.`,
		`Combine filters with spaces.`,
		`Prefix a term with - to negate it.`,
	} {
		if !bytes.Contains(document, []byte(want)) {
			t.Fatalf("console is missing query guidance %q", want)
		}
	}
}

func TestSyntaxDrawerRestoresItsConnectedOpenerOrQuery(t *testing.T) {
	script, err := elementBody(string(document), "script")
	if err != nil {
		t.Fatal(err)
	}
	setSyntaxOpen, err := functionBody(script, "setSyntaxOpen")
	if err != nil {
		t.Fatal(err)
	}
	harness := `
const focused=[];
const elements={
  "syntax-drawer":{hidden:true},
  "syntax-toggle":{setAttribute(){}},
  "syntax-close":{focus(){focused.push("close")}},
  query:{isConnected:true,focus(){focused.push("query")}}
};
const $=id=>elements[id],state={syntaxTrigger:null};
const document={activeElement:null};
` + setSyntaxOpen + `
const opener={isConnected:true,focus(){focused.push("opener")}};
document.activeElement=opener;setSyntaxOpen(true);
document.activeElement={isConnected:true};setSyntaxOpen(false);
if(focused.at(-1)!=="opener")throw new Error("connected opener was not restored");
document.activeElement=opener;setSyntaxOpen(true);opener.isConnected=false;
document.activeElement={isConnected:true};setSyntaxOpen(false);
if(focused.at(-1)!=="query")throw new Error("query fallback was not restored");
`
	if output, err := exec.Command("node", "-e", harness).CombinedOutput(); err != nil {
		t.Fatalf("drawer behavior failed: %v\n%s", err, output)
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
