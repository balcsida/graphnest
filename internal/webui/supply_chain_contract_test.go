package webui

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"strings"
	"testing"
)

func TestSupplyChainDocumentContract(t *testing.T) {
	if len(supplyChainDocument) >= 40<<10 {
		t.Fatalf("supply chain document bytes=%d", len(supplyChainDocument))
	}
	for _, want := range []string{
		`data-graphnest-supply-chain`, `id="sc-shell"`, `id="access-panel"`,
		`id="sc-repository"`, `id="sc-stream"`, `id="sc-search"`, `id="sc-rows"`,
		`id="sc-more"`, `id="sc-collections"`, `id="sc-warnings"`, `id="sc-notice"`,
		`github:source`, `GitHub dependency graph (source observation)`,
		`/v1/repositories`, `/v1/supply-chain/repositories/`, `/components`, `/refresh`,
		`/collections`, `license_declared_raw`, `created_at_claimed`,
		`unknown — not bound to a commit`, `not configured`,
		`No components in this snapshot`, `No inventory has been collected yet`,
		`no_inventory`, `administrator access required`, `Load more`,
		`Content-Disposition`, `URL.createObjectURL`, `scope="col"`,
		`aria-live="polite"`, `prefers-reduced-motion`, `:focus-visible`,
		`credentials:"same-origin"`, `sessionStorage`, `/v1/auth/config`,
		`/v1/auth/session`, `/auth/logout`, `response.status!==204`,
		`textContent`, `@media(max-width:900px)`, `@media(max-width:520px)`,
	} {
		if !bytes.Contains(supplyChainDocument, []byte(want)) {
			t.Errorf("supply chain document missing %q", want)
		}
	}
	for _, forbidden := range []string{
		"localStorage", "innerHTML", "outerHTML", "insertAdjacentHTML", "eval(",
		"fonts.googleapis.com", "cdn.", "<script src", "http://", "https://cdn",
	} {
		if bytes.Contains(supplyChainDocument, []byte(forbidden)) {
			t.Errorf("supply chain document contains forbidden %q", forbidden)
		}
	}
}

func TestRegisterServesSupplyChainWithConsoleSecurityHeaders(t *testing.T) {
	mux := http.NewServeMux()
	Register(mux)
	response := httptest.NewRecorder()
	mux.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/supply-chain", nil))

	if response.Code != http.StatusOK {
		t.Fatalf("status=%d", response.Code)
	}
	if got := response.Header().Get("Content-Type"); got != "text/html; charset=utf-8" {
		t.Fatalf("content type=%q", got)
	}
	policy := response.Header().Get("Content-Security-Policy")
	if !strings.Contains(policy, "script-src 'sha256-") || !strings.Contains(policy, "style-src 'sha256-") || strings.Contains(policy, "unsafe-inline") {
		t.Fatalf("CSP=%q", policy)
	}
}

func TestSupplyChainDOMContract(t *testing.T) {
	command := exec.Command(requireNode(t), "supply_chain_dom_test.mjs")
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("supply chain DOM contract: %v\n%s", err, output)
	}
}
