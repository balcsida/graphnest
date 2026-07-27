package webui

import (
	"bytes"
	"os/exec"
	"testing"
)

func TestAdminDocumentContract(t *testing.T) {
	if len(adminDocument) >= 40<<10 {
		t.Fatalf("admin document bytes=%d", len(adminDocument))
	}
	for _, want := range []string{
		`data-grepnest-admin`, `id="admin-shell"`, `id="access-panel"`,
		`data-screen="overview"`, `data-screen="repositories"`, `data-screen="queue"`,
		`data-screen="scip"`, `data-screen="webhooks"`, `data-screen="github"`,
		`id="repo-filter"`, `id="repo-statuses"`, `id="reconcile"`, `id="reindex-selected"`,
		`id="scip-upload"`, `id="dependency-refresh"`, `id="admin-status"`,
		`id="inventory-notices"`,
		`href="/"`, `sessionStorage`, `prefers-reduced-motion: reduce`,
		`/v1/admin/overview`, `/v1/admin/repositories`, `/v1/admin/jobs`,
		`/v1/admin/scip/uploads`, `/v1/admin/scip/dependencies`,
		`/v1/admin/webhook-deliveries`, `/v1/admin/github`,
		`/v1/scip/uploads`, `/v1/scip/dependencies/github`, `/healthz`, `/readyz`,
	} {
		if !bytes.Contains(adminDocument, []byte(want)) {
			t.Errorf("admin document missing %q", want)
		}
	}
	for _, forbidden := range []string{
		"localStorage", "innerHTML", "outerHTML", "insertAdjacentHTML",
		"support.js", "fonts.googleapis.com", "private_key_path", "webhook_secret_path",
	} {
		if bytes.Contains(adminDocument, []byte(forbidden)) {
			t.Errorf("admin document contains forbidden %q", forbidden)
		}
	}
}

func TestAdminDOMContract(t *testing.T) {
	command := exec.Command("node", "admin_dom_test.mjs")
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("admin DOM contract: %v\n%s", err, output)
	}
}

func TestAdminDocumentHidesContentUntilAuthorization(t *testing.T) {
	for _, want := range []string{
		`id="admin-shell" hidden`, `response.status===401`, `response.status===403`,
		`response.status===404`, `shell.hidden=false`, `textContent`,
		`window.confirm`, `setInterval`, `aria-live="polite"`,
	} {
		if !bytes.Contains(adminDocument, []byte(want)) {
			t.Errorf("admin lifecycle missing %q", want)
		}
	}
}
