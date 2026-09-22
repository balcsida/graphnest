//go:build integration && unix

package integration

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/balcsida/graphnest/internal/authn"
	"github.com/balcsida/graphnest/internal/authz"
	"github.com/balcsida/graphnest/internal/githubapp"
	"github.com/balcsida/graphnest/internal/httpapi"
	"github.com/balcsida/graphnest/internal/postgres"
	"github.com/balcsida/graphnest/internal/supplychain"
	"github.com/balcsida/graphnest/internal/supplychain/license"
	"github.com/balcsida/graphnest/internal/supplychain/review"
	"github.com/balcsida/graphnest/pkg/api"
)

// fakeSBOMGitHub is a GHES stand-in that mints installation tokens and serves
// the SBOM export with a configurable response per repository name.
type fakeSBOMGitHub struct {
	server    *httptest.Server
	responses map[string]func(http.ResponseWriter)
	calls     atomic.Int32
}

func newFakeSBOMGitHub(t *testing.T) (*fakeSBOMGitHub, *githubapp.Client) {
	t.Helper()
	github := &fakeSBOMGitHub{responses: map[string]func(http.ResponseWriter){}}
	github.server = httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		path := request.URL.EscapedPath()
		switch {
		case strings.HasSuffix(path, "/access_tokens"):
			fmt.Fprint(writer, `{"token":"installation-token","expires_at":"2099-01-01T00:00:00Z"}`)
		case strings.HasSuffix(path, "/dependency-graph/sbom"):
			github.calls.Add(1)
			if request.Header.Get("Authorization") != "Bearer installation-token" {
				writer.WriteHeader(http.StatusUnauthorized)
				return
			}
			parts := strings.Split(strings.TrimPrefix(path, "/api/v3/repos/"), "/")
			respond, ok := github.responses[parts[0]+"/"+parts[1]]
			if !ok {
				writer.WriteHeader(http.StatusNotFound)
				return
			}
			respond(writer)
		default:
			writer.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(github.server.Close)
	base, err := url.Parse(github.server.URL)
	if err != nil {
		t.Fatal(err)
	}
	apiURL := *base
	apiURL.Path = "/api/v3"
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := githubapp.NewSigner(7, pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)}), nil)
	if err != nil {
		t.Fatal(err)
	}
	client := githubapp.NewClient(githubapp.Endpoints{Web: base, API: &apiURL, Upload: base, Git: base}, github.server.Client(), signer, "2022-11-28", 2<<20, nil)
	return github, client
}

func sbomEnvelope(t *testing.T) string {
	t.Helper()
	member, err := os.ReadFile("../fixtures/supplychain/ghes-spdx-2.3.json")
	if err != nil {
		t.Fatal(err)
	}
	return `{"sbom":` + string(member) + `}`
}

// TestSupplyChainVerticalSlice proves GHES collection -> preserved snapshot ->
// authorized inventory -> original-document download against real PostgreSQL,
// for repositories that have no Zoekt index, no SCIP upload, no indexed SHA,
// and no graph enrichment. It also proves that a later failed refresh keeps
// the previous inventory and that another installation's principal sees
// nothing.
func TestSupplyChainVerticalSlice(t *testing.T) {
	h := newPostgresHarness(t)
	widgets := h.seedRepository(t, 10, 101)
	gadgets := h.seedRepository(t, 10, 102)
	if err := h.store.UpsertInstallation(t.Context(), postgres.InstallationUpdate{GitHubID: 20, AccountLogin: "other", AccountType: "Organization", Status: "active"}); err != nil {
		t.Fatal(err)
	}
	otherRepository, err := h.store.UpsertRepository(t.Context(), postgres.RepositoryUpdate{GitHubID: 201, InstallationID: 20, Owner: "acme", Name: "repo-201", CloneURL: "https://example.invalid/repo.git", WebURL: "https://example.invalid/repo", DefaultBranch: "main", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	other := otherRepository.ID
	github, client := newFakeSBOMGitHub(t)
	envelope := sbomEnvelope(t)
	github.responses["acme/repo-101"] = func(writer http.ResponseWriter) {
		writer.Header().Set("Content-Type", "application/json; charset=utf-8")
		fmt.Fprint(writer, envelope)
	}
	github.responses["acme/repo-102"] = func(writer http.ResponseWriter) {
		writer.Header().Set("X-RateLimit-Remaining", "0")
		writer.Header().Set("Retry-After", "600")
		writer.WriteHeader(http.StatusForbidden)
	}
	github.responses["acme/repo-201"] = func(writer http.ResponseWriter) {
		fmt.Fprint(writer, `{"sbom":{"spdxVersion":"SPDX-2.3","SPDXID":"SPDXRef-DOCUMENT","creationInfo":{"created":"2026-01-01T00:00:00Z","creators":["Tool: GitHub.com-Dependency-Graph"]},"packages":[],"relationships":[]}}`)
	}

	scheduler := &supplychain.Scheduler{Store: h.store, Interval: time.Hour}
	created, err := scheduler.Tick(t.Context())
	if err != nil || created != 3 {
		t.Fatalf("scheduled %d jobs, err=%v", created, err)
	}
	// Scheduled jobs carry jitter; make them runnable now.
	if _, err := h.pool.Exec(t.Context(), `update supply_chain_jobs set run_after=now()`); err != nil {
		t.Fatal(err)
	}
	collector := &supplychain.Collector{Store: h.store, GitHub: client, Owner: "integration-worker", MaxDocumentBytes: 1 << 20}
	for range 3 {
		if processed, err := collector.RunOnce(t.Context()); err != nil || !processed {
			t.Fatalf("collection processed=%v err=%v", processed, err)
		}
	}
	if processed, err := collector.RunOnce(t.Context()); err != nil || processed {
		t.Fatalf("queue should be drained: processed=%v err=%v", processed, err)
	}
	if github.calls.Load() != 3 {
		t.Fatalf("GitHub SBOM calls = %d", github.calls.Load())
	}

	service := &supplychain.Service{Store: h.store, Authorizer: authz.NewPostgres(h.store), Interval: time.Hour, MaxResults: 100}
	authenticator := authn.RequestAuthenticator{Bearer: authn.NewStatic(map[string]authn.Principal{
		"acme":  {Subject: "acme", Method: "api_token", InstallationID: 10, RepositoryIDs: []int64{101, 102}},
		"other": {Subject: "other", Method: "api_token", InstallationID: 20, RepositoryIDs: []int64{201}},
		"admin": {Subject: "admin", Method: "session", Administrator: true},
	})}
	mux := http.NewServeMux()
	httpapi.RegisterSupplyChain(mux, authenticator, service, 100, 256<<10)
	get := func(token, path string) (*httptest.ResponseRecorder, map[string]any) {
		request := httptest.NewRequest(http.MethodGet, path, nil)
		request.Header.Set("Authorization", "Bearer "+token)
		recorder := httptest.NewRecorder()
		mux.ServeHTTP(recorder, request)
		var body map[string]any
		if strings.HasPrefix(recorder.Header().Get("Content-Type"), "application/json") && recorder.Code != http.StatusOK || recorder.Code == http.StatusOK && !strings.Contains(path, "/document") {
			_ = json.Unmarshal(recorder.Body.Bytes(), &body)
		}
		return recorder, body
	}

	// Published inventory for widgets: no indexed SHA, no Zoekt, no SCIP.
	response, _ := get("acme", "/v1/supply-chain/repositories/101")
	var status api.SupplyChainRepositoryStatus
	if err := json.Unmarshal(response.Body.Bytes(), &status); err != nil || response.Code != http.StatusOK {
		t.Fatalf("status = %d %s", response.Code, response.Body.String())
	}
	if status.Collection != "current" || status.LatestSnapshot == nil || status.LatestSnapshot.ComponentCount != 7 || status.LatestSnapshot.SubjectAssurance != "unknown" || status.LatestSnapshot.SubjectRevision != "" || len(status.Documents) != 1 {
		t.Fatalf("status = %+v", status)
	}
	var indexedSHA *string
	if err := h.pool.QueryRow(t.Context(), `select indexed_sha from repositories where id=$1`, widgets).Scan(&indexedSHA); err != nil || indexedSHA != nil {
		t.Fatalf("repository must have no indexed SHA for this proof: %v %v", indexedSHA, err)
	}
	response, _ = get("acme", "/v1/supply-chain/repositories/101/components?limit=100")
	var page api.SupplyChainComponentList
	if err := json.Unmarshal(response.Body.Bytes(), &page); err != nil || response.Code != http.StatusOK || len(page.Components) != 7 || page.Truncated {
		t.Fatalf("components = %d %s", response.Code, response.Body.String())
	}
	scopes := map[string]int{}
	for _, component := range page.Components {
		scopes[component.Scope]++
		if component.LicenseDeclaredRaw != nil && *component.LicenseDeclaredRaw != "NOASSERTION" {
			t.Fatalf("license was rewritten: %+v", component)
		}
	}
	if scopes["root"] != 1 || scopes["direct"] != 6 {
		t.Fatalf("scopes = %v", scopes)
	}
	// Original document: exact bytes, attachment headers, SHA-256 header matches the status.
	response, _ = get("acme", status.Documents[0].Path)
	// The stored document is the exact SPDX member inside GitHub's envelope; the
	// fixture file's trailing newline is envelope whitespace, not member bytes.
	member, _ := os.ReadFile("../fixtures/supplychain/ghes-spdx-2.3.json")
	if response.Code != http.StatusOK || response.Body.String() != strings.TrimSpace(string(member)) || response.Header().Get("X-Content-SHA256") != status.Documents[0].SHA256 || !strings.HasPrefix(response.Header().Get("Content-Disposition"), "attachment;") {
		t.Fatalf("document = %d headers=%v", response.Code, response.Header())
	}

	// Rate-limited gadgets: no snapshot, failed collection with Retry-After honored, job requeued.
	response, body := get("acme", "/v1/supply-chain/repositories/102")
	if response.Code != http.StatusOK || body["collection"] != "failed" || body["latest_snapshot"] != nil {
		t.Fatalf("gadgets status = %d %v", response.Code, body)
	}
	last := body["last_collection"].(map[string]any)
	if last["outcome"] != "rate_limited" || last["http_status"].(float64) != 403 || last["retry_after_seconds"].(float64) != 600 {
		t.Fatalf("gadgets last collection = %v", last)
	}
	if body["active_job"] == nil || body["active_job"].(map[string]any)["state"] != "queued" {
		t.Fatalf("gadgets should have a requeued job: %v", body["active_job"])
	}
	response, _ = get("acme", "/v1/supply-chain/repositories/102/components")
	if response.Code != http.StatusNotFound || !strings.Contains(response.Body.String(), "no_inventory") {
		t.Fatalf("gadgets components = %d %s", response.Code, response.Body.String())
	}

	// Cross-installation isolation: other's empty inventory is valid, and acme cannot see it.
	response, body = get("other", "/v1/supply-chain/repositories/201")
	if response.Code != http.StatusOK || body["collection"] != "current" || body["latest_snapshot"].(map[string]any)["component_count"].(float64) != 0 {
		t.Fatalf("other status = %d %v", response.Code, body)
	}
	otherSnapshot := int64(body["latest_snapshot"].(map[string]any)["id"].(float64))
	for _, path := range []string{"/v1/supply-chain/repositories/201", fmt.Sprintf("/v1/supply-chain/snapshots/%d/document", otherSnapshot), "/v1/supply-chain/repositories/201/components"} {
		if response, _ := get("acme", path); response.Code != http.StatusNotFound {
			t.Fatalf("acme reached %s: %d", path, response.Code)
		}
	}
	for _, path := range []string{"/v1/supply-chain/repositories/101", status.Documents[0].Path} {
		if response, _ := get("other", path); response.Code != http.StatusNotFound {
			t.Fatalf("other reached %s: %d", path, response.Code)
		}
	}
	if response, _ := get("admin", fmt.Sprintf("/v1/supply-chain/snapshots/%d/document", otherSnapshot)); response.Code != http.StatusOK {
		t.Fatalf("admin document = %d", response.Code)
	}

	// A later failed refresh keeps widgets' inventory and is visible as failed.
	github.responses["acme/repo-101"] = func(writer http.ResponseWriter) { writer.WriteHeader(http.StatusForbidden) }
	request := httptest.NewRequest(http.MethodPost, "/v1/supply-chain/repositories/101/refresh", nil)
	request.Header.Set("Authorization", "Bearer admin")
	recorder := httptest.NewRecorder()
	mux.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusAccepted {
		t.Fatalf("refresh = %d %s", recorder.Code, recorder.Body.String())
	}
	if processed, err := collector.RunOnce(t.Context()); err != nil || !processed {
		t.Fatalf("refresh collection processed=%v err=%v", processed, err)
	}
	response, _ = get("acme", "/v1/supply-chain/repositories/101")
	var after api.SupplyChainRepositoryStatus
	if err := json.Unmarshal(response.Body.Bytes(), &after); err != nil || after.Collection != "failed" || after.LatestSnapshot == nil || after.LatestSnapshot.ID != status.LatestSnapshot.ID || after.LastCollection.Outcome != "forbidden" {
		t.Fatalf("status after failed refresh = %+v", after)
	}
	if response, _ := get("acme", status.Documents[0].Path); response.Code != http.StatusOK {
		t.Fatalf("document after failed refresh = %d", response.Code)
	}
	// The compatibility projection populated GitHub-sourced package mappings for the published snapshot.
	var projected int
	if err := h.pool.QueryRow(t.Context(), `select count(*) from repository_packages where repository_id=$1 and source='github'`, widgets).Scan(&projected); err != nil || projected != 3 {
		t.Fatalf("projected mappings = %d err=%v", projected, err)
	}
	_ = gadgets
	_ = other
}

// TestSupplyChainLicenseEnrichment proves registry evidence flows from a
// configured route through the real worker and store into assessments and
// the REST detail view, that unconfigured ecosystems produce no traffic, and
// that evidence stays reachable only through authorized occurrences.
func TestSupplyChainLicenseEnrichment(t *testing.T) {
	h := newPostgresHarness(t)
	widgets := h.seedRepository(t, 10, 101)
	github, client := newFakeSBOMGitHub(t)
	envelope := sbomEnvelope(t)
	github.responses["acme/repo-101"] = func(writer http.ResponseWriter) { fmt.Fprint(writer, envelope) }

	var registryCalls atomic.Int32
	registry := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		registryCalls.Add(1)
		switch request.URL.EscapedPath() {
		case "/npm/@scope%2Fleft-pad/1.3.0":
			fmt.Fprint(writer, `{"name":"@scope/left-pad","version":"1.3.0","license":"MIT"}`)
		default:
			writer.WriteHeader(http.StatusNotFound)
		}
	}))
	defer registry.Close()
	base, _ := url.Parse(registry.URL + "/npm/")
	certificate := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: registry.Certificate().Raw})
	routes, err := license.NewRegistry([]license.Route{{Name: "npm:test", Ecosystem: "npm", BaseURL: base, CAPEM: certificate, AllowPrivateHosts: true, Timeout: 5 * time.Second, MaxResponseBytes: 1 << 20}})
	if err != nil {
		t.Fatal(err)
	}
	enricher := &license.Worker{Store: h.store, Registry: routes, Owner: "enrich"}
	collector := &supplychain.Collector{Store: h.store, GitHub: client, Owner: "collect", MaxDocumentBytes: 1 << 20, Enricher: enricher}
	if _, _, err := h.store.EnqueueSupplyChainJob(t.Context(), widgets, supplychain.StreamGitHubSource, "manual", "t", 10, time.Now()); err != nil {
		t.Fatal(err)
	}
	if processed, err := collector.RunOnce(t.Context()); err != nil || !processed {
		t.Fatalf("collection processed=%v err=%v", processed, err)
	}
	// Only the npm coordinate has a route; maven, nuget, golang, and githubactions stay unqueued.
	depths, err := h.store.EnrichmentQueueDepths(t.Context())
	if err != nil || depths["queued"] != 1 {
		t.Fatalf("enrichment depths = %v %v", depths, err)
	}
	if processed, err := enricher.RunOnce(t.Context()); err != nil || !processed {
		t.Fatalf("enrichment processed=%v err=%v", processed, err)
	}
	if processed, err := enricher.RunOnce(t.Context()); err != nil || processed {
		t.Fatalf("enrichment queue not drained: processed=%v err=%v", processed, err)
	}
	if registryCalls.Load() != 1 {
		t.Fatalf("registry calls = %d, want exactly one exact-version lookup", registryCalls.Load())
	}

	service := &supplychain.Service{Store: h.store, Authorizer: authz.NewPostgres(h.store), Interval: time.Hour, MaxResults: 100, License: h.store, EnrichmentEcosystems: routes.Ecosystems()}
	authenticator := authn.RequestAuthenticator{Bearer: authn.NewStatic(map[string]authn.Principal{
		"acme":  {Subject: "acme", Method: "api_token", InstallationID: 10, RepositoryIDs: []int64{101}},
		"other": {Subject: "other", Method: "api_token", InstallationID: 20, RepositoryIDs: []int64{999}},
	})}
	mux := http.NewServeMux()
	httpapi.RegisterSupplyChain(mux, authenticator, service, 100, 256<<10)
	get := func(token, path string) *httptest.ResponseRecorder {
		request := httptest.NewRequest(http.MethodGet, path, nil)
		request.Header.Set("Authorization", "Bearer "+token)
		recorder := httptest.NewRecorder()
		mux.ServeHTTP(recorder, request)
		return recorder
	}
	response := get("acme", "/v1/supply-chain/repositories/101")
	var status api.SupplyChainRepositoryStatus
	if err := json.Unmarshal(response.Body.Bytes(), &status); err != nil || response.Code != http.StatusOK {
		t.Fatalf("status = %d %s", response.Code, response.Body.String())
	}
	if status.Enrichment != "configured" || len(status.EnrichmentEcosystems) != 1 || status.EnrichmentEcosystems[0] != "npm" || status.LicenseSummary["resolved"] != 1 {
		t.Fatalf("status = %+v", status)
	}
	response = get("acme", "/v1/supply-chain/repositories/101/components?q=left-pad")
	var page api.SupplyChainComponentList
	if err := json.Unmarshal(response.Body.Bytes(), &page); err != nil || response.Code != http.StatusOK || len(page.Components) != 1 {
		t.Fatalf("components = %d %s", response.Code, response.Body.String())
	}
	leftPad := page.Components[0]
	if leftPad.License == nil || leftPad.License.Status != "resolved" || leftPad.License.Expression != "MIT" || leftPad.License.EvidenceCount != 1 || len(leftPad.License.EvidenceFingerprint) != 64 {
		t.Fatalf("left-pad assessment = %+v", leftPad.License)
	}
	if leftPad.LicenseDeclaredRaw == nil || *leftPad.LicenseDeclaredRaw != "NOASSERTION" {
		t.Fatalf("declared raw must remain the producer's NOASSERTION: %+v", leftPad)
	}
	response = get("acme", "/v1/supply-chain/repositories/101/components?q=core")
	if err := json.Unmarshal(response.Body.Bytes(), &page); err != nil || len(page.Components) != 1 || page.Components[0].License != nil {
		t.Fatalf("maven component without a route must have no assessment: %s", response.Body.String())
	}
	response = get("acme", "/v1/supply-chain/repositories/101/component?element=SPDXRef-npm-scope-left-pad-1.3.0")
	var detail api.SupplyChainComponentDetail
	if err := json.Unmarshal(response.Body.Bytes(), &detail); err != nil || response.Code != http.StatusOK {
		t.Fatalf("detail = %d %s", response.Code, response.Body.String())
	}
	if len(detail.Declarations) != 2 || detail.Declarations[0].ParseStatus != "no_assertion" || detail.Declarations[0].RawKind != "sentinel" {
		t.Fatalf("declarations = %+v", detail.Declarations)
	}
	if len(detail.Evidence) != 1 || detail.Evidence[0].Source != "registry_npm" || detail.Evidence[0].Route != "npm:test" || detail.Evidence[0].Expression != "MIT" || detail.Evidence[0].RawValue != "MIT" ||
		detail.Evidence[0].LicenseListVersion != "3.27.0" || detail.Evidence[0].ResolverVersion != 1 || len(detail.Evidence[0].ContentSHA256) != 64 || detail.Evidence[0].Outcome != "resolved" {
		t.Fatalf("evidence = %+v", detail.Evidence)
	}
	if len(detail.Relationships) != 1 || detail.Relationships[0].Type != "DEPENDS_ON" || detail.Relationships[0].To != "SPDXRef-npm-scope-left-pad-1.3.0" {
		t.Fatalf("relationships = %+v", detail.Relationships)
	}
	// Evidence is reachable only through an authorized occurrence.
	if response := get("other", "/v1/supply-chain/repositories/101/component?element=SPDXRef-npm-scope-left-pad-1.3.0"); response.Code != http.StatusNotFound {
		t.Fatalf("other principal reached evidence: %d", response.Code)
	}
	if response := get("acme", "/v1/supply-chain/repositories/101/component?element=SPDXRef-nope"); response.Code != http.StatusNotFound {
		t.Fatalf("unknown element = %d", response.Code)
	}
	if response := get("acme", "/v1/supply-chain/repositories/101/component"); response.Code != http.StatusBadRequest {
		t.Fatalf("missing element = %d", response.Code)
	}
	// A second publication of the same document does not re-enqueue resolved coordinates.
	if _, _, err := h.store.EnqueueSupplyChainJob(t.Context(), widgets, supplychain.StreamGitHubSource, "manual", "t", 10, time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, err := collector.RunOnce(t.Context()); err != nil {
		t.Fatal(err)
	}
	if depths, _ := h.store.EnrichmentQueueDepths(t.Context()); depths["queued"] != 0 {
		t.Fatalf("resolved coordinates were re-queued: %v", depths)
	}
	if registryCalls.Load() != 1 {
		t.Fatalf("registry calls after unchanged republish = %d", registryCalls.Load())
	}
}

// TestSupplyChainPortfolioAuthorization proves that overview counts, facets,
// component pages, coordinate lookups, CSV exports, and snapshot comparisons
// are computed strictly inside the caller's authorized repository scope, that
// pagination is stable, and that CSV cells cannot inject formulas.
func TestSupplyChainPortfolioAuthorization(t *testing.T) {
	h := newPostgresHarness(t)
	widgets := h.seedRepository(t, 10, 101)
	gadgets := h.seedRepository(t, 10, 102)
	if err := h.store.UpsertInstallation(t.Context(), postgres.InstallationUpdate{GitHubID: 20, AccountLogin: "other", AccountType: "Organization", Status: "active"}); err != nil {
		t.Fatal(err)
	}
	secretRepository, err := h.store.UpsertRepository(t.Context(), postgres.RepositoryUpdate{GitHubID: 201, InstallationID: 20, Owner: "other", Name: "secret", CloneURL: "https://example.invalid/s.git", WebURL: "https://example.invalid/s", DefaultBranch: "main", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	secret := secretRepository.ID
	github, client := newFakeSBOMGitHub(t)
	envelope := sbomEnvelope(t)
	// gadgets shares left-pad with widgets and adds a formula-shaped package name; secret has a private package.
	gadgetsDocument := `{"sbom":{"spdxVersion":"SPDX-2.3","SPDXID":"SPDXRef-DOCUMENT","name":"acme/gadgets","creationInfo":{"created":"2026-09-20T09:00:00Z","creators":["Tool: GitHub.com-Dependency-Graph"]},
		"documentDescribes":["SPDXRef-root"],
		"packages":[{"SPDXID":"SPDXRef-root","name":"com.github.acme/gadgets","externalRefs":[{"referenceType":"purl","referenceLocator":"pkg:github/acme/gadgets"}]},
			{"SPDXID":"SPDXRef-lp","name":"npm:@scope/left-pad","versionInfo":"1.3.0","licenseDeclared":"NOASSERTION","externalRefs":[{"referenceType":"purl","referenceLocator":"pkg:npm/%40scope/left-pad@1.3.0"}]},
			{"SPDXID":"SPDXRef-evil","name":"=HYPERLINK(\"http://evil\")","versionInfo":"-1","licenseDeclared":"+MIT","externalRefs":[{"referenceType":"purl","referenceLocator":"pkg:npm/evil@1.0.0"}]}],
		"relationships":[{"spdxElementId":"SPDXRef-root","relationshipType":"DEPENDS_ON","relatedSpdxElement":"SPDXRef-lp"},{"spdxElementId":"SPDXRef-root","relationshipType":"DEPENDS_ON","relatedSpdxElement":"SPDXRef-evil"}]}}`
	secretDocument := `{"sbom":{"spdxVersion":"SPDX-2.3","SPDXID":"SPDXRef-DOCUMENT","name":"other/secret","creationInfo":{"created":"2026-09-20T09:00:00Z","creators":["Tool: GitHub.com-Dependency-Graph"]},
		"documentDescribes":["SPDXRef-root"],
		"packages":[{"SPDXID":"SPDXRef-root","name":"other/secret"},{"SPDXID":"SPDXRef-p","name":"npm:@other/private-thing","versionInfo":"9.9.9","externalRefs":[{"referenceType":"purl","referenceLocator":"pkg:npm/%40other/private-thing@9.9.9"}]},
			{"SPDXID":"SPDXRef-lp","name":"npm:@scope/left-pad","versionInfo":"1.3.0","externalRefs":[{"referenceType":"purl","referenceLocator":"pkg:npm/%40scope/left-pad@1.3.0"}]}],
		"relationships":[{"spdxElementId":"SPDXRef-root","relationshipType":"DEPENDS_ON","relatedSpdxElement":"SPDXRef-p"}]}}`
	github.responses["acme/repo-101"] = func(writer http.ResponseWriter) { fmt.Fprint(writer, envelope) }
	github.responses["acme/repo-102"] = func(writer http.ResponseWriter) { fmt.Fprint(writer, gadgetsDocument) }
	github.responses["other/secret"] = func(writer http.ResponseWriter) { fmt.Fprint(writer, secretDocument) }
	collector := &supplychain.Collector{Store: h.store, GitHub: client, Owner: "collect", MaxDocumentBytes: 1 << 20}
	for _, id := range []int64{widgets, gadgets, secret} {
		if _, _, err := h.store.EnqueueSupplyChainJob(t.Context(), id, supplychain.StreamGitHubSource, "manual", "t", 10, time.Now()); err != nil {
			t.Fatal(err)
		}
		if processed, err := collector.RunOnce(t.Context()); err != nil || !processed {
			t.Fatalf("collection processed=%v err=%v", processed, err)
		}
		collections, err := h.store.SupplyChainCollections(t.Context(), id, supplychain.StreamGitHubSource, 0, 1)
		if err != nil || len(collections) != 1 || collections[0].Outcome != supplychain.OutcomePublished {
			t.Fatalf("repository %d collection = %+v %v", id, collections, err)
		}
	}
	// Assess left-pad in the widgets snapshot only, to make the "mixed" aggregation observable.
	leftPad := license.Coordinates{Ecosystem: "npm", Namespace: "@scope", Name: "left-pad", Version: "1.3.0"}
	if _, _, err := h.store.InsertLicenseEvidence(t.Context(), license.Evidence{Source: license.SourceRegistryNPM, Route: "npm:test", Coordinates: leftPad, Outcome: license.OutcomeResolved, FetchedAt: time.Now(), RawValue: "MIT", RawKind: license.RawExpression,
		ParseStatus: "parsed", NormalizedExpression: "MIT", ResolverVersion: 1, LicenseListVersion: "3.27.0", ContentSHA256: make([]byte, 32)}); err != nil {
		t.Fatal(err)
	}
	occurrences, err := h.store.ComponentsForCoordinates(t.Context(), leftPad, 10)
	if err != nil || len(occurrences) != 3 {
		t.Fatalf("left-pad occurrences = %v %v", occurrences, err)
	}
	evidence, _ := h.store.LatestLicenseEvidence(t.Context(), leftPad)
	for _, occurrence := range occurrences {
		declared, concluded, _ := h.store.ComponentDeclarations(t.Context(), occurrence[0])
		if err := h.store.UpsertAssessment(t.Context(), license.Assess(occurrence[0], occurrence[1], declared, concluded, evidence, time.Now())); err != nil {
			t.Fatal(err)
		}
	}

	portfolio := &supplychain.Portfolio{Store: h.store, Snapshots: h.store, Authorizer: authz.NewPostgres(h.store), Interval: time.Hour, MaxResults: 100}
	authenticator := authn.RequestAuthenticator{Bearer: authn.NewStatic(map[string]authn.Principal{
		"acme":  {Subject: "acme", Method: "api_token", InstallationID: 10, RepositoryIDs: []int64{101, 102}},
		"one":   {Subject: "one", Method: "api_token", InstallationID: 10, RepositoryIDs: []int64{101}},
		"other": {Subject: "other", Method: "api_token", InstallationID: 20, RepositoryIDs: []int64{201}},
		"admin": {Subject: "admin", Method: "session", Administrator: true},
	})}
	mux := http.NewServeMux()
	httpapi.RegisterSupplyChainPortfolio(mux, authenticator, portfolio, 100, 256<<10)
	get := func(token, path string) *httptest.ResponseRecorder {
		request := httptest.NewRequest(http.MethodGet, path, nil)
		request.Header.Set("Authorization", "Bearer "+token)
		recorder := httptest.NewRecorder()
		mux.ServeHTTP(recorder, request)
		return recorder
	}
	decode := func(t *testing.T, recorder *httptest.ResponseRecorder, target any) {
		t.Helper()
		if recorder.Code != http.StatusOK {
			t.Fatalf("status %d: %s", recorder.Code, recorder.Body.String())
		}
		if err := json.Unmarshal(recorder.Body.Bytes(), target); err != nil {
			t.Fatal(err)
		}
	}

	// Overview denominators follow the principal's scope.
	var overview api.SupplyChainOverview
	decode(t, get("acme", "/v1/supply-chain/overview"), &overview)
	if overview.Repositories.Authorized != 2 || overview.Repositories.WithInventory != 2 || overview.Components.Occurrences != 10 || overview.Components.UniqueCoordinates != 9 || overview.Components.Assessments["resolved"] != 2 || overview.Components.Unassessed != 8 {
		t.Fatalf("acme overview = %+v", overview)
	}
	decode(t, get("one", "/v1/supply-chain/overview"), &overview)
	if overview.Repositories.Authorized != 1 || overview.Components.Occurrences != 7 {
		t.Fatalf("one overview = %+v", overview)
	}
	decode(t, get("admin", "/v1/supply-chain/overview"), &overview)
	if overview.Repositories.Authorized != 3 || overview.Components.Occurrences != 13 || overview.Components.Assessments["resolved"] != 3 {
		t.Fatalf("admin overview = %+v", overview)
	}
	// Requesting another installation's repository is silently dropped, not revealed.
	decode(t, get("acme", "/v1/supply-chain/overview?repository_id=201&repository_id=101"), &overview)
	if overview.Repositories.Authorized != 1 || overview.Components.Occurrences != 7 {
		t.Fatalf("acme scoped overview = %+v", overview)
	}
	if len(overview.Denominators) < 4 {
		t.Fatalf("denominators = %v", overview.Denominators)
	}

	// Facets and component pages: the private package never appears for acme.
	var facets api.SupplyChainFacets
	decode(t, get("acme", "/v1/supply-chain/facets"), &facets)
	for _, facet := range facets.Ecosystems {
		// widgets' left-pad, gadgets' left-pad, gadgets' evil; never the private package.
		if facet.Value == "npm" && facet.Count != 3 {
			t.Fatalf("npm facet = %+v", facet)
		}
	}
	var page api.SupplyChainPortfolioComponentList
	decode(t, get("acme", "/v1/supply-chain/components?limit=4"), &page)
	if len(page.Components) != 4 || !page.Truncated || page.NextCursor == "" || page.RepositoriesInScope != 2 {
		t.Fatalf("page 1 = %+v", page)
	}
	seen := map[string]bool{}
	for _, component := range page.Components {
		seen[component.Key] = true
	}
	var second api.SupplyChainPortfolioComponentList
	decode(t, get("acme", "/v1/supply-chain/components?limit=4&cursor="+page.NextCursor), &second)
	var third api.SupplyChainPortfolioComponentList
	decode(t, get("acme", "/v1/supply-chain/components?limit=4&cursor="+second.NextCursor), &third)
	all := append(append(page.Components, second.Components...), third.Components...)
	if len(all) != 9 || third.Truncated {
		t.Fatalf("paged total = %d truncated=%v", len(all), third.Truncated)
	}
	for _, component := range all {
		if strings.Contains(component.Name, "private-thing") {
			t.Fatalf("private package leaked into acme's portfolio: %+v", component)
		}
		if component.Name == "left-pad" && (component.RepositoryCount != 2 || component.OccurrenceCount != 2 || component.Assessment != "resolved" || component.Expression != "MIT" || len(component.Repositories) != 2) {
			t.Fatalf("left-pad = %+v", component)
		}
	}
	// A cursor is bound to its filter set.
	if response := get("acme", "/v1/supply-chain/components?limit=4&ecosystem=npm&cursor="+page.NextCursor); response.Code != http.StatusBadRequest {
		t.Fatalf("cursor reuse across filters = %d", response.Code)
	}
	// Filters: ecosystem, license expression, assessment status, search.
	decode(t, get("acme", "/v1/supply-chain/components?ecosystem=npm"), &page)
	if len(page.Components) != 2 {
		t.Fatalf("npm unique coordinates for acme = %+v", page.Components)
	}
	decode(t, get("acme", "/v1/supply-chain/components?license=mit"), &page)
	if len(page.Components) != 1 || page.Components[0].Name != "left-pad" {
		t.Fatalf("license filter = %+v", page.Components)
	}
	decode(t, get("acme", "/v1/supply-chain/components?assessment=unassessed"), &page)
	if len(page.Components) != 8 {
		t.Fatalf("unassessed filter = %d", len(page.Components))
	}
	decode(t, get("acme", "/v1/supply-chain/components?q=%25"), &page)
	if len(page.Components) != 1 || page.Components[0].Name != "left-pad" {
		t.Fatalf("literal percent search = %+v", page.Components)
	}
	// Coordinate detail: authorized occurrences only; the private coordinate is 404 for acme and 200 for other.
	var leftPadKey, privateKey string
	var adminPage api.SupplyChainPortfolioComponentList
	decode(t, get("admin", "/v1/supply-chain/components"), &adminPage)
	for _, component := range adminPage.Components {
		switch component.Name {
		case "left-pad":
			leftPadKey = component.Key
			if component.RepositoryCount != 3 || component.Assessment != "resolved" {
				t.Fatalf("admin left-pad = %+v", component)
			}
		case "private-thing":
			privateKey = component.Key
		}
	}
	var detail api.SupplyChainPortfolioComponentDetail
	decode(t, get("acme", "/v1/supply-chain/components/"+leftPadKey), &detail)
	if len(detail.Occurrences) != 2 || detail.Occurrences[0].Repository != "acme/repo-101" || detail.Occurrences[1].Repository != "acme/repo-102" || !strings.HasPrefix(detail.Occurrences[0].DetailPath, "/v1/supply-chain/repositories/101/component?element=") {
		t.Fatalf("acme left-pad detail = %+v", detail)
	}
	decode(t, get("admin", "/v1/supply-chain/components/"+leftPadKey), &detail)
	if len(detail.Occurrences) != 3 {
		t.Fatalf("admin left-pad detail = %+v", detail)
	}
	if response := get("acme", "/v1/supply-chain/components/"+privateKey); response.Code != http.StatusNotFound {
		t.Fatalf("acme reached the private coordinate: %d", response.Code)
	}
	if response := get("other", "/v1/supply-chain/components/"+privateKey); response.Code != http.StatusOK {
		t.Fatalf("other cannot see its own coordinate: %d", response.Code)
	}
	if response := get("acme", "/v1/supply-chain/components/zz"); response.Code != http.StatusBadRequest {
		t.Fatalf("bad key = %d", response.Code)
	}

	// CSV export: authorized only, formula cells neutralized, provenance columns present.
	response := get("acme", "/v1/supply-chain/exports/102/components.csv")
	if response.Code != http.StatusOK || response.Header().Get("Content-Type") != "text/csv; charset=utf-8" || !strings.HasPrefix(response.Header().Get("Content-Disposition"), "attachment;") {
		t.Fatalf("export = %d %v", response.Code, response.Header())
	}
	body := response.Body.String()
	if !strings.Contains(body, `"'=HYPERLINK(""http://evil"")"`) || !strings.Contains(body, ",'-1,") || !strings.Contains(body, ",'+MIT,") || strings.Contains(body, "\n=HYPERLINK") {
		t.Fatalf("csv did not neutralize formulas:\n%s", body)
	}
	if !strings.HasPrefix(body, "repository,snapshot_id,stream,producer,collected_at,document_sha256,element_id,name,version,purl,ecosystem,is_root,license_declared_raw,license_concluded_raw,assessment_status,assessed_expression\n") || !strings.Contains(body, "acme/repo-102,") {
		t.Fatalf("csv header/provenance:\n%s", body)
	}
	if response := get("one", "/v1/supply-chain/exports/102/components.csv"); response.Code != http.StatusNotFound {
		t.Fatalf("unauthorized export = %d", response.Code)
	}
	if response := get("acme", "/v1/supply-chain/exports/201/components.csv"); response.Code != http.StatusNotFound {
		t.Fatalf("cross-installation export = %d", response.Code)
	}

	// Comparison: a second widgets snapshot with one added package and a changed declared license.
	changed := strings.Replace(strings.Replace(envelope, `"name": "vendored:legacy-widget-lib",`, `"name": "vendored:legacy-widget-lib","versionInfo":"2.0",`, 1),
		`"SPDXID": "SPDXRef-nuget-Newtonsoft.Json-13.0.3",
      "versionInfo": "13.0.3",
      "downloadLocation": "NOASSERTION",
      "filesAnalyzed": false,
      "licenseConcluded": "NOASSERTION",
      "licenseDeclared": "NOASSERTION",`, `"SPDXID": "SPDXRef-nuget-Newtonsoft.Json-13.0.3",
      "versionInfo": "13.0.3",
      "downloadLocation": "NOASSERTION",
      "filesAnalyzed": false,
      "licenseConcluded": "NOASSERTION",
      "licenseDeclared": "MIT",`, 1)
	if changed == envelope {
		t.Fatal("fixture edit did not apply")
	}
	github.responses["acme/repo-101"] = func(writer http.ResponseWriter) { fmt.Fprint(writer, changed) }
	if _, _, err := h.store.EnqueueSupplyChainJob(t.Context(), widgets, supplychain.StreamGitHubSource, "manual", "t", 10, time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, err := collector.RunOnce(t.Context()); err != nil {
		t.Fatal(err)
	}
	snapshots, err := h.store.SupplyChainSnapshots(t.Context(), widgets, supplychain.StreamGitHubSource, 0, 10)
	if err != nil || len(snapshots) != 2 {
		t.Fatalf("widgets snapshots = %d %v", len(snapshots), err)
	}
	var comparison api.SupplyChainSnapshotComparison
	decode(t, get("acme", fmt.Sprintf("/v1/supply-chain/compare?repository_id=101&base=%d&head=%d", snapshots[1].ID, snapshots[0].ID)), &comparison)
	if len(comparison.AddedComponents) != 1 || comparison.AddedComponents[0] != "vendored:legacy-widget-lib@2.0" || len(comparison.RemovedComponents) != 1 || comparison.RemovedComponents[0] != "vendored:legacy-widget-lib" ||
		len(comparison.LicenseChanges) != 1 || comparison.LicenseChanges[0].From != "NOASSERTION" || comparison.LicenseChanges[0].To != "MIT" || comparison.EdgesAdded != 0 || comparison.EdgesRemoved != 0 {
		t.Fatalf("comparison = %+v", comparison)
	}
	if response := get("one", fmt.Sprintf("/v1/supply-chain/compare?repository_id=102&base=%d&head=%d", snapshots[1].ID, snapshots[0].ID)); response.Code != http.StatusNotFound {
		t.Fatalf("compare across unauthorized repository = %d", response.Code)
	}
	// Snapshot IDs from another repository cannot be compared through an authorized one.
	otherSnapshots, _ := h.store.SupplyChainSnapshots(t.Context(), secret, supplychain.StreamGitHubSource, 0, 1)
	if response := get("acme", fmt.Sprintf("/v1/supply-chain/compare?repository_id=101&base=%d&head=%d", snapshots[0].ID, otherSnapshots[0].ID)); response.Code != http.StatusNotFound {
		t.Fatalf("compare with foreign snapshot = %d", response.Code)
	}
}

// TestSupplyChainImports proves standards-based uploads: real-format round
// trips for SPDX 2.3 and CycloneDX 1.6, stream separation from the GitHub
// observation, uploader identity separate from the claimed producer,
// producer-asserted subject binding, idempotency, quota, repository-scoped
// permission, and explicit rejection of unsupported versions.
func TestSupplyChainImports(t *testing.T) {
	h := newPostgresHarness(t)
	widgets := h.seedRepository(t, 10, 101)
	h.seedRepository(t, 10, 102)
	github, client := newFakeSBOMGitHub(t)
	envelope := sbomEnvelope(t)
	github.responses["acme/repo-101"] = func(writer http.ResponseWriter) { fmt.Fprint(writer, envelope) }
	collector := &supplychain.Collector{Store: h.store, GitHub: client, Owner: "collect", MaxDocumentBytes: 1 << 20}
	if _, _, err := h.store.EnqueueSupplyChainJob(t.Context(), widgets, supplychain.StreamGitHubSource, "manual", "t", 10, time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, err := collector.RunOnce(t.Context()); err != nil {
		t.Fatal(err)
	}
	importer := &supplychain.Importer{Store: h.store, Authorizer: authz.NewPostgres(h.store), MaxDocumentBytes: 1 << 20, QuotaPerRepository: 12, QuotaWindow: time.Hour}
	service := &supplychain.Service{Store: h.store, Authorizer: authz.NewPostgres(h.store), Interval: time.Hour, MaxResults: 100, License: h.store}
	authorizer := authz.NewPostgres(h.store)
	grants := &httpapi.UploadGrants{Set: h.store.SetSupplyChainUploadGrant, Resolve: func(ctx context.Context, principal authn.Principal, githubID int64) (int64, error) {
		repo, err := authorizer.AuthorizedRepository(ctx, principal, githubID)
		return repo.ID, err
	}}
	authenticator := authn.RequestAuthenticator{Bearer: authn.NewStatic(map[string]authn.Principal{
		"admin":  {Subject: "admin@acme", Method: "session", Administrator: true},
		"dev":    {Subject: "dev@acme", Method: "api_token", InstallationID: 10, RepositoryIDs: []int64{101, 102}},
		"reader": {Subject: "reader@acme", Method: "api_token", InstallationID: 10, RepositoryIDs: []int64{101}},
	})}
	mux := http.NewServeMux()
	httpapi.RegisterSupplyChain(mux, authenticator, service, 100, 256<<10)
	httpapi.RegisterSupplyChainImports(mux, authenticator, importer, grants, 1<<20, 256<<10)
	post := func(token, path, contentType string, body []byte) *httptest.ResponseRecorder {
		request := httptest.NewRequest(http.MethodPost, path, strings.NewReader(string(body)))
		request.Header.Set("Authorization", "Bearer "+token)
		request.Header.Set("Content-Type", contentType)
		recorder := httptest.NewRecorder()
		mux.ServeHTTP(recorder, request)
		return recorder
	}
	get := func(token, path string) *httptest.ResponseRecorder {
		request := httptest.NewRequest(http.MethodGet, path, nil)
		request.Header.Set("Authorization", "Bearer "+token)
		recorder := httptest.NewRecorder()
		mux.ServeHTTP(recorder, request)
		return recorder
	}
	cyclonedx, err := os.ReadFile("../fixtures/supplychain/syft-cyclonedx-1.6.json")
	if err != nil {
		t.Fatal(err)
	}
	spdxMember, _ := os.ReadFile("../fixtures/supplychain/ghes-spdx-2.3.json")
	// An ORT-style SPDX document claiming a different tool and a known commit.
	ortDocument := strings.Replace(string(spdxMember), `"Tool: GitHub.com-Dependency-Graph"`, `"Tool: ORT-45.0.0"`, 1)
	sha := strings.Repeat("c", 40)

	// Without a grant, a non-administrator cannot import; administrators can.
	if response := post("dev", "/v1/supply-chain/imports?repository_id=101&subject=artifact&label=syft-image", "application/vnd.cyclonedx+json", cyclonedx); response.Code != http.StatusForbidden {
		t.Fatalf("ungranted import = %d %s", response.Code, response.Body.String())
	}
	response := post("admin", "/v1/supply-chain/imports?repository_id=101&subject=artifact&label=syft-image", "application/vnd.cyclonedx+json", cyclonedx)
	var imported api.SupplyChainImportResponse
	if err := json.Unmarshal(response.Body.Bytes(), &imported); err != nil || response.Code != http.StatusCreated {
		t.Fatalf("cyclonedx import = %d %s", response.Code, response.Body.String())
	}
	if imported.Outcome != "published" || imported.Format != "cyclonedx-1.6-json" || imported.ComponentCount != 8 || imported.Stream != "import:artifact:syft-image" || imported.SubjectAssurance != "unknown" || imported.SnapshotID == nil {
		t.Fatalf("cyclonedx import = %+v", imported)
	}
	// Idempotent: the same bytes return the earlier result without a new snapshot.
	response = post("admin", "/v1/supply-chain/imports?repository_id=101&subject=artifact&label=syft-image", "application/vnd.cyclonedx+json", cyclonedx)
	var repeated api.SupplyChainImportResponse
	if err := json.Unmarshal(response.Body.Bytes(), &repeated); err != nil || response.Code != http.StatusCreated || !repeated.Repeated || repeated.Outcome != "unchanged" || *repeated.SnapshotID != *imported.SnapshotID {
		t.Fatalf("repeated import = %d %+v", response.Code, repeated)
	}
	// The GitHub stream is untouched by the artifact import.
	response = get("dev", "/v1/supply-chain/repositories/101")
	var status api.SupplyChainRepositoryStatus
	if err := json.Unmarshal(response.Body.Bytes(), &status); err != nil || status.Producer != "github" || status.LatestSnapshot == nil || status.LatestSnapshot.ComponentCount != 7 || len(status.Streams) != 2 {
		t.Fatalf("github stream after import = %d %+v", response.Code, status)
	}
	response = get("dev", "/v1/supply-chain/repositories/101?stream=import:artifact:syft-image")
	if err := json.Unmarshal(response.Body.Bytes(), &status); err != nil || status.Producer != "import" || status.Subject != "artifact" || status.LatestSnapshot == nil || status.LatestSnapshot.ProducerTool != "syft 1.20.0" || status.LatestSnapshot.UploadedBy != "admin@acme" || status.LatestSnapshot.UploadLabel != "syft-image" {
		t.Fatalf("import stream status = %d %+v", response.Code, status)
	}
	found := false
	for _, note := range status.Notes {
		found = found || strings.Contains(note, "uploaded by admin@acme")
	}
	if !found {
		t.Fatalf("notes must name the uploader separately from the claimed producer: %v", status.Notes)
	}
	response = get("dev", "/v1/supply-chain/repositories/101/components?stream=import:artifact:syft-image&q=libssl")
	var page api.SupplyChainComponentList
	if err := json.Unmarshal(response.Body.Bytes(), &page); err != nil || len(page.Components) != 1 || *page.Components[0].LicenseDeclaredRaw != "OpenSSL; Apache-2.0" || page.Components[0].Scope != "direct" || page.Components[0].Qualifiers["distro"] != "debian-12" {
		t.Fatalf("libssl occurrence = %d %s", response.Code, response.Body.String())
	}
	if response := get("dev", *documentPath(&status)); response.Code != http.StatusOK || response.Body.String() != string(cyclonedx) {
		t.Fatalf("original CycloneDX bytes were not preserved: %d", response.Code)
	}

	// Grant dev upload rights to 101 only; an SPDX source import with a subject revision is producer_asserted.
	grantRequest := httptest.NewRequest(http.MethodPut, "/v1/supply-chain/upload-grants", strings.NewReader(`{"repository_id":101,"subject":"dev@acme","allow":true}`))
	grantRequest.Header.Set("Authorization", "Bearer admin")
	grantRequest.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	mux.ServeHTTP(recorder, grantRequest)
	if recorder.Code != http.StatusNoContent {
		t.Fatalf("grant = %d %s", recorder.Code, recorder.Body.String())
	}
	response = post("dev", "/v1/supply-chain/imports?repository_id=101&subject=source&label=ort&subject_revision="+sha, "application/spdx+json", []byte(ortDocument))
	if err := json.Unmarshal(response.Body.Bytes(), &imported); err != nil || response.Code != http.StatusCreated || imported.Format != "spdx-2.3-json" || imported.SubjectAssurance != "producer_asserted" || imported.ComponentCount != 7 {
		t.Fatalf("spdx import = %d %s", response.Code, response.Body.String())
	}
	response = get("reader", "/v1/supply-chain/repositories/101?stream=import:source:ort")
	if err := json.Unmarshal(response.Body.Bytes(), &status); err != nil || status.LatestSnapshot.SubjectRevision != sha || status.LatestSnapshot.SubjectAssurance != "producer_asserted" || status.LatestSnapshot.ProducerTool != "ORT-45.0.0" || status.LatestSnapshot.UploadedBy != "dev@acme" {
		t.Fatalf("ort stream = %d %+v", response.Code, status.LatestSnapshot)
	}
	// A spoofed "Tool: GitHub.com-Dependency-Graph" upload never lands in the GitHub stream and is still labelled as an import.
	response = post("dev", "/v1/supply-chain/imports?repository_id=101&subject=source&label=spoof", "application/json", spdxMember)
	if err := json.Unmarshal(response.Body.Bytes(), &imported); err != nil || response.Code != http.StatusCreated || imported.Stream != "import:source:spoof" {
		t.Fatalf("spoof import = %d %s", response.Code, response.Body.String())
	}
	response = get("dev", "/v1/supply-chain/repositories/101?stream=import:source:spoof")
	if err := json.Unmarshal(response.Body.Bytes(), &status); err != nil || status.Producer != "import" || status.LatestSnapshot.ProducerTool != "GitHub.com-Dependency-Graph" || status.LatestSnapshot.UploadedBy != "dev@acme" {
		t.Fatalf("spoof stream = %+v", status)
	}
	// dev has no grant on 102; the reader has no grant at all; an unknown repository is not found.
	if response := post("dev", "/v1/supply-chain/imports?repository_id=102&subject=source&label=ort", "application/spdx+json", []byte(ortDocument)); response.Code != http.StatusForbidden {
		t.Fatalf("import to ungranted repository = %d", response.Code)
	}
	if response := post("reader", "/v1/supply-chain/imports?repository_id=101&subject=source&label=ort", "application/spdx+json", []byte(ortDocument)); response.Code != http.StatusForbidden {
		t.Fatalf("reader import = %d", response.Code)
	}
	if response := post("dev", "/v1/supply-chain/imports?repository_id=999&subject=source&label=ort", "application/spdx+json", []byte(ortDocument)); response.Code != http.StatusNotFound {
		t.Fatalf("unknown repository import = %d", response.Code)
	}
	// Explicit rejections: unsupported version, wrong format claim, bad label, bad revision, wrong media type, malformed.
	for name, target := range map[string]struct {
		path, contentType, body string
		status                  int
	}{
		"old spdx":         {"/v1/supply-chain/imports?repository_id=101&subject=source&label=x", "application/spdx+json", strings.Replace(string(spdxMember), "SPDX-2.3", "SPDX-2.2", 1), http.StatusUnsupportedMediaType},
		"old cyclonedx":    {"/v1/supply-chain/imports?repository_id=101&subject=source&label=x", "application/json", strings.Replace(string(cyclonedx), `"specVersion": "1.6"`, `"specVersion": "1.5"`, 1), http.StatusUnsupportedMediaType},
		"mismatched claim": {"/v1/supply-chain/imports?repository_id=101&subject=source&label=x", "application/spdx+json", string(cyclonedx), http.StatusUnsupportedMediaType},
		"unknown format":   {"/v1/supply-chain/imports?repository_id=101&subject=source&label=x", "application/json", `{"hello":"world"}`, http.StatusUnsupportedMediaType},
		"malformed":        {"/v1/supply-chain/imports?repository_id=101&subject=source&label=x", "application/json", `{"spdxVersion":"SPDX-2.3","packages":"x"}`, http.StatusBadRequest},
		"bad label":        {"/v1/supply-chain/imports?repository_id=101&subject=source&label=Bad%20Label", "application/json", string(spdxMember), http.StatusBadRequest},
		"bad subject":      {"/v1/supply-chain/imports?repository_id=101&subject=release&label=x", "application/json", string(spdxMember), http.StatusBadRequest},
		"bad revision":     {"/v1/supply-chain/imports?repository_id=101&subject=source&label=x&subject_revision=HEAD", "application/json", string(spdxMember), http.StatusBadRequest},
		"wrong media type": {"/v1/supply-chain/imports?repository_id=101&subject=source&label=x", "text/plain", string(spdxMember), http.StatusUnsupportedMediaType},
	} {
		response := post("admin", target.path, target.contentType, []byte(target.body))
		if response.Code != target.status {
			t.Fatalf("%s = %d, want %d: %s", name, response.Code, target.status, response.Body.String())
		}
	}
	// Quota counts accepted and rejected attempts per repository per window.
	count, err := h.store.SupplyChainImportCount(t.Context(), widgets, time.Now().Add(-time.Hour))
	if err != nil || count < 8 {
		t.Fatalf("import count = %d %v", count, err)
	}
	importer.QuotaPerRepository = count
	if response := post("admin", "/v1/supply-chain/imports?repository_id=101&subject=source&label=quota", "application/json", []byte(strings.Replace(string(spdxMember), `"name": "com.github.acme/widgets"`, `"name": "quota"`, 1))); response.Code != http.StatusTooManyRequests {
		t.Fatalf("over quota = %d %s", response.Code, response.Body.String())
	}
	// Another repository has its own quota.
	if response := post("admin", "/v1/supply-chain/imports?repository_id=102&subject=source&label=ort", "application/spdx+json", []byte(ortDocument)); response.Code != http.StatusCreated {
		t.Fatalf("other repository import = %d %s", response.Code, response.Body.String())
	}
	// Idempotent replay of an accepted document is still answered when over quota (it does no new work).
	if response := post("admin", "/v1/supply-chain/imports?repository_id=101&subject=artifact&label=syft-image", "application/vnd.cyclonedx+json", cyclonedx); response.Code != http.StatusCreated {
		t.Fatalf("replay over quota = %d", response.Code)
	}
}

func documentPath(status *api.SupplyChainRepositoryStatus) *string {
	if len(status.Documents) == 0 {
		empty := ""
		return &empty
	}
	return &status.Documents[0].Path
}

// TestSupplyChainDerivedSPDXExport proves the derived document names
// GraphNest as creator, links the preserved original by URL and hash, carries
// assessments only as comments, validates with the SPDX reader, and never
// alters the original download.
func TestSupplyChainDerivedSPDXExport(t *testing.T) {
	h := newPostgresHarness(t)
	widgets := h.seedRepository(t, 10, 101)
	github, client := newFakeSBOMGitHub(t)
	envelope := sbomEnvelope(t)
	github.responses["acme/repo-101"] = func(writer http.ResponseWriter) { fmt.Fprint(writer, envelope) }
	collector := &supplychain.Collector{Store: h.store, GitHub: client, Owner: "collect", MaxDocumentBytes: 1 << 20}
	if _, _, err := h.store.EnqueueSupplyChainJob(t.Context(), widgets, supplychain.StreamGitHubSource, "manual", "t", 10, time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, err := collector.RunOnce(t.Context()); err != nil {
		t.Fatal(err)
	}
	leftPad := license.Coordinates{Ecosystem: "npm", Namespace: "@scope", Name: "left-pad", Version: "1.3.0"}
	if _, _, err := h.store.InsertLicenseEvidence(t.Context(), license.Evidence{Source: license.SourceRegistryNPM, Route: "npm:test", Coordinates: leftPad, Outcome: license.OutcomeResolved, FetchedAt: time.Now(), RawValue: "MIT", RawKind: license.RawExpression,
		ParseStatus: "parsed", NormalizedExpression: "MIT", ResolverVersion: 1, LicenseListVersion: "3.27.0", ContentSHA256: make([]byte, 32)}); err != nil {
		t.Fatal(err)
	}
	occurrences, _ := h.store.ComponentsForCoordinates(t.Context(), leftPad, 10)
	evidence, _ := h.store.LatestLicenseEvidence(t.Context(), leftPad)
	declared, concluded, _ := h.store.ComponentDeclarations(t.Context(), occurrences[0][0])
	if err := h.store.UpsertAssessment(t.Context(), license.Assess(occurrences[0][0], occurrences[0][1], declared, concluded, evidence, time.Now())); err != nil {
		t.Fatal(err)
	}
	service := &supplychain.Service{Store: h.store, Authorizer: authz.NewPostgres(h.store), Interval: time.Hour, MaxResults: 100, License: h.store}
	portfolio := &supplychain.Portfolio{Store: h.store, Snapshots: h.store, Authorizer: authz.NewPostgres(h.store), Interval: time.Hour, MaxResults: 100}
	authenticator := authn.RequestAuthenticator{Bearer: authn.NewStatic(map[string]authn.Principal{
		"acme":  {Subject: "acme", Method: "api_token", InstallationID: 10, RepositoryIDs: []int64{101}},
		"other": {Subject: "other", Method: "api_token", InstallationID: 20, RepositoryIDs: []int64{999}},
	})}
	mux := http.NewServeMux()
	httpapi.RegisterSupplyChain(mux, authenticator, service, 100, 256<<10)
	httpapi.RegisterSupplyChainPortfolio(mux, authenticator, portfolio, 100, 256<<10, &httpapi.DerivedExport{Service: service, PublicOrigin: "https://graphnest.example"})
	get := func(token, path string) *httptest.ResponseRecorder {
		request := httptest.NewRequest(http.MethodGet, path, nil)
		request.Header.Set("Authorization", "Bearer "+token)
		recorder := httptest.NewRecorder()
		mux.ServeHTTP(recorder, request)
		return recorder
	}
	response := get("acme", "/v1/supply-chain/exports/101/derived.spdx.json")
	if response.Code != http.StatusOK || response.Header().Get("Content-Type") != "application/spdx+json" || response.Header().Get("X-GraphNest-Derived") != "true" {
		t.Fatalf("derived export = %d %v %s", response.Code, response.Header(), response.Body.String())
	}
	var derived struct {
		SPDXVersion  string `json:"spdxVersion"`
		Name         string `json:"name"`
		CreationInfo struct {
			Creators []string `json:"creators"`
			Comment  string   `json:"comment"`
		} `json:"creationInfo"`
		ExternalDocumentRefs []struct {
			ExternalDocumentID string `json:"externalDocumentId"`
			SPDXDocument       string `json:"spdxDocument"`
			Checksum           struct {
				Algorithm string `json:"algorithm"`
				Value     string `json:"checksumValue"`
			} `json:"checksum"`
		} `json:"externalDocumentRefs"`
		DocumentDescribes []string `json:"documentDescribes"`
		Packages          []struct {
			SPDXID           string `json:"SPDXID"`
			Name             string `json:"name"`
			LicenseDeclared  string `json:"licenseDeclared"`
			LicenseConcluded string `json:"licenseConcluded"`
			LicenseComments  string `json:"licenseComments"`
		} `json:"packages"`
		Relationships []struct {
			Type string `json:"relationshipType"`
		} `json:"relationships"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &derived); err != nil {
		t.Fatal(err)
	}
	if derived.SPDXVersion != "SPDX-2.3" || len(derived.CreationInfo.Creators) != 2 || !strings.HasPrefix(derived.CreationInfo.Creators[0], "Tool: GraphNest-supply-chain-") || !strings.Contains(derived.CreationInfo.Comment, "preserved unchanged") {
		t.Fatalf("creation info = %+v", derived.CreationInfo)
	}
	original := get("acme", "/v1/supply-chain/snapshots/1/document")
	if len(derived.ExternalDocumentRefs) != 1 || derived.ExternalDocumentRefs[0].SPDXDocument != "https://graphnest.example/v1/supply-chain/snapshots/1/document" || derived.ExternalDocumentRefs[0].Checksum.Value != original.Header().Get("X-Content-SHA256") {
		t.Fatalf("external refs = %+v (original sha %s)", derived.ExternalDocumentRefs, original.Header().Get("X-Content-SHA256"))
	}
	if len(derived.Packages) != 7 || len(derived.DocumentDescribes) != 1 || derived.DocumentDescribes[0] != "SPDXRef-com.github.acme-widgets" {
		t.Fatalf("packages = %d describes = %v", len(derived.Packages), derived.DocumentDescribes)
	}
	for _, pkg := range derived.Packages {
		if pkg.LicenseConcluded != "NOASSERTION" {
			t.Fatalf("derived export must not conclude licenses: %+v", pkg)
		}
		if pkg.LicenseDeclared != "NOASSERTION" {
			t.Fatalf("GHES NOASSERTION must stay NOASSERTION: %+v", pkg)
		}
		if pkg.Name == "npm:@scope/left-pad" && !strings.Contains(pkg.LicenseComments, "GraphNest assessment: resolved (MIT)") {
			t.Fatalf("assessment must appear as a comment only: %+v", pkg)
		}
	}
	describes, dependsOn := 0, 0
	for _, relationship := range derived.Relationships {
		switch relationship.Type {
		case "DESCRIBES":
			describes++
		case "DEPENDS_ON":
			dependsOn++
		}
	}
	if describes != 1 || dependsOn != 6 {
		t.Fatalf("relationships = %d DESCRIBES, %d DEPENDS_ON (the unresolved edge must be dropped)", describes, dependsOn)
	}
	// The original is still served byte-for-byte and is a different document.
	if original.Code != http.StatusOK || original.Body.String() != strings.TrimSpace(envelope[len(`{"sbom":`):len(envelope)-1]) {
		t.Fatalf("original changed: %d", original.Code)
	}
	if response := get("other", "/v1/supply-chain/exports/101/derived.spdx.json"); response.Code != http.StatusNotFound {
		t.Fatalf("unauthorized derived export = %d", response.Code)
	}
}

// TestSupplyChainReviewWorkflow proves the review queue, policy evaluation
// against the example policy, human conclusions with optimistic concurrency,
// scoped decisions with expiry, the permission matrix, and immutable history.
func TestSupplyChainReviewWorkflow(t *testing.T) {
	h := newPostgresHarness(t)
	widgets := h.seedRepository(t, 10, 101)
	gadgets := h.seedRepository(t, 10, 102)
	github, client := newFakeSBOMGitHub(t)
	envelope := sbomEnvelope(t)
	github.responses["acme/repo-101"] = func(writer http.ResponseWriter) { fmt.Fprint(writer, envelope) }
	github.responses["acme/repo-102"] = func(writer http.ResponseWriter) { fmt.Fprint(writer, envelope) }
	collector := &supplychain.Collector{Store: h.store, GitHub: client, Owner: "collect", MaxDocumentBytes: 1 << 20}
	for _, id := range []int64{widgets, gadgets} {
		if _, _, err := h.store.EnqueueSupplyChainJob(t.Context(), id, supplychain.StreamGitHubSource, "manual", "t", 10, time.Now()); err != nil {
			t.Fatal(err)
		}
		if _, err := collector.RunOnce(t.Context()); err != nil {
			t.Fatal(err)
		}
	}
	// Registry evidence: left-pad resolves to GPL-3.0-only (prohibited by the example), core to MIT (approved).
	assess := func(coordinates license.Coordinates, raw string) {
		if _, _, err := h.store.InsertLicenseEvidence(t.Context(), license.Evidence{Source: license.SourceRegistryNPM, Route: "npm:test", Coordinates: coordinates, Outcome: license.OutcomeResolved, FetchedAt: time.Now(), RawValue: raw, RawKind: license.RawExpression,
			ParseStatus: "parsed", NormalizedExpression: raw, ResolverVersion: 1, LicenseListVersion: "3.27.0", ContentSHA256: make([]byte, 32)}); err != nil {
			t.Fatal(err)
		}
		occurrences, _ := h.store.ComponentsForCoordinates(t.Context(), coordinates, 10)
		evidence, _ := h.store.LatestLicenseEvidence(t.Context(), coordinates)
		for _, occurrence := range occurrences {
			declared, concluded, _ := h.store.ComponentDeclarations(t.Context(), occurrence[0])
			if err := h.store.UpsertAssessment(t.Context(), license.AssessWithHuman(occurrence[0], occurrence[1], declared, concluded, evidence, time.Now())); err != nil {
				t.Fatal(err)
			}
		}
	}
	leftPad := license.Coordinates{Ecosystem: "npm", Namespace: "@scope", Name: "left-pad", Version: "1.3.0"}
	core := license.Coordinates{Ecosystem: "maven", Namespace: "org.example", Name: "core", Version: "2.1.0"}
	assess(leftPad, "GPL-3.0-only")
	assess(core, "MIT")

	authorizer := authz.NewPostgres(h.store)
	reviews := &review.Service{Store: h.store, Authorizer: authorizer, MaxResults: 100}
	authenticator := authn.RequestAuthenticator{Bearer: authn.NewStatic(map[string]authn.Principal{
		"admin":    {Subject: "admin@acme", Method: "session", Administrator: true},
		"reviewer": {Subject: "reviewer@acme", Method: "api_token", InstallationID: 10, RepositoryIDs: []int64{101, 102}},
		"reader":   {Subject: "reader@acme", Method: "api_token", InstallationID: 10, RepositoryIDs: []int64{101}},
		"outsider": {Subject: "outsider", Method: "api_token", InstallationID: 20, RepositoryIDs: []int64{999}},
	})}
	mux := http.NewServeMux()
	httpapi.RegisterSupplyChainReview(mux, authenticator, reviews, 64<<10, 256<<10)
	call := func(token, method, path, body string) *httptest.ResponseRecorder {
		request := httptest.NewRequest(method, path, strings.NewReader(body))
		request.Header.Set("Authorization", "Bearer "+token)
		if body != "" {
			request.Header.Set("Content-Type", "application/json")
		}
		recorder := httptest.NewRecorder()
		mux.ServeHTTP(recorder, request)
		return recorder
	}
	decode := func(t *testing.T, recorder *httptest.ResponseRecorder, want int, target any) {
		t.Helper()
		if recorder.Code != want {
			t.Fatalf("status %d (want %d): %s", recorder.Code, want, recorder.Body.String())
		}
		if target != nil {
			if err := json.Unmarshal(recorder.Body.Bytes(), target); err != nil {
				t.Fatal(err)
			}
		}
	}

	// Policy administration is administrator-only; the example is labelled and can be installed, then activated.
	if response := call("reviewer", http.MethodPost, "/v1/supply-chain/policies", `{"install_example":true,"activate":true}`); response.Code != http.StatusForbidden {
		t.Fatalf("reviewer installed a policy: %d", response.Code)
	}
	var installed api.SupplyChainPolicy
	decode(t, call("admin", http.MethodPost, "/v1/supply-chain/policies", `{"install_example":true,"activate":true}`), http.StatusCreated, &installed)
	if installed.Kind != "example" || !installed.Active || installed.UnknownHandling != "review_required" || !strings.Contains(installed.Description, "EXAMPLE FIXTURE") {
		t.Fatalf("installed = %+v", installed)
	}
	if response := call("admin", http.MethodPost, "/v1/supply-chain/policies", `{"name":"acme","rules":{"approved":["MIT","Not-Real"]},"unknown_handling":"prohibited"}`); response.Code != http.StatusBadRequest {
		t.Fatalf("invalid rules accepted: %d %s", response.Code, response.Body.String())
	}
	// No pretend company policy: default unknown handling must not auto-approve.
	if response := call("admin", http.MethodPost, "/v1/supply-chain/policies", `{"name":"acme","rules":{"approved":["MIT"]},"unknown_handling":"approved"}`); response.Code != http.StatusBadRequest {
		t.Fatalf("unknown_handling=approved accepted: %d", response.Code)
	}
	evaluated, err := reviews.EvaluatePending(t.Context(), 500)
	if err != nil || evaluated != 14 {
		t.Fatalf("evaluated %d err=%v", evaluated, err)
	}
	if evaluated, err := reviews.EvaluatePending(t.Context(), 500); err != nil || evaluated != 0 {
		t.Fatalf("re-evaluation without change = %d %v", evaluated, err)
	}

	// Queue: scoped to the caller; the approved MIT component is not in it; prohibited and unknown ones are.
	var queue api.SupplyChainReviewQueue
	decode(t, call("reviewer", http.MethodGet, "/v1/supply-chain/review/queue", ""), http.StatusOK, &queue)
	verdicts := map[string]int{}
	var leftPadItem *api.SupplyChainReviewItem
	for index := range queue.Items {
		verdicts[queue.Items[index].Verdict]++
		if queue.Items[index].Coordinates.Name == "left-pad" && queue.Items[index].RepositoryID == 101 {
			leftPadItem = &queue.Items[index]
		}
		if queue.Items[index].Coordinates.Name == "core" {
			t.Fatalf("approved component in queue: %+v", queue.Items[index])
		}
	}
	if len(queue.Items) != 12 || verdicts["prohibited"] != 2 || verdicts["review_required"] != 10 || leftPadItem == nil || leftPadItem.Basis == "" || leftPadItem.Reason != "no decision recorded" {
		t.Fatalf("queue = %d items, verdicts %v, left-pad %+v", len(queue.Items), verdicts, leftPadItem)
	}
	decode(t, call("reader", http.MethodGet, "/v1/supply-chain/review/queue", ""), http.StatusOK, &queue)
	if len(queue.Items) != 6 {
		t.Fatalf("reader queue = %d items (must cover only repository 101)", len(queue.Items))
	}
	decode(t, call("outsider", http.MethodGet, "/v1/supply-chain/review/queue", ""), http.StatusOK, &queue)
	if len(queue.Items) != 0 {
		t.Fatalf("outsider queue = %d items", len(queue.Items))
	}

	// Permission matrix for decisions: reader lacks a grant; reviewer needs one; outsider cannot even see the repository.
	decisionBody := func(kind, basis, expires string) string {
		body := fmt.Sprintf(`{"repository_id":101,"coordinates":{"ecosystem":"npm","namespace":"@scope","name":"left-pad","version":"1.3.0"},"kind":%q,"reason":"pilot needs it","basis":%q`, kind, basis)
		if expires != "" {
			body += `,"expires_at":"` + expires + `"`
		}
		return body + "}"
	}
	if response := call("reader", http.MethodPost, "/v1/supply-chain/review/decisions", decisionBody("approve", leftPadItem.Basis, "")); response.Code != http.StatusForbidden {
		t.Fatalf("reader decided: %d", response.Code)
	}
	if response := call("reviewer", http.MethodPost, "/v1/supply-chain/review/decisions", decisionBody("approve", leftPadItem.Basis, "")); response.Code != http.StatusForbidden {
		t.Fatalf("ungranted reviewer decided: %d", response.Code)
	}
	if response := call("outsider", http.MethodPost, "/v1/supply-chain/review/decisions", decisionBody("approve", leftPadItem.Basis, "")); response.Code != http.StatusNotFound {
		t.Fatalf("outsider decided: %d", response.Code)
	}
	if response := call("reviewer", http.MethodPut, "/v1/supply-chain/review/grants", `{"repository_id":101,"subject":"reviewer@acme","allow":true}`); response.Code != http.StatusForbidden {
		t.Fatalf("reviewer granted themselves: %d", response.Code)
	}
	decode(t, call("admin", http.MethodPut, "/v1/supply-chain/review/grants", `{"repository_id":101,"subject":"reviewer@acme","allow":true}`), http.StatusNoContent, nil)
	// Stale basis is refused; an exception needs an expiry; a wrong kind is invalid.
	if response := call("reviewer", http.MethodPost, "/v1/supply-chain/review/decisions", decisionBody("exception", strings.Repeat("0", 64), "2027-01-01T00:00:00Z")); response.Code != http.StatusConflict {
		t.Fatalf("stale basis accepted: %d %s", response.Code, response.Body.String())
	}
	if response := call("reviewer", http.MethodPost, "/v1/supply-chain/review/decisions", decisionBody("exception", leftPadItem.Basis, "")); response.Code != http.StatusBadRequest {
		t.Fatalf("exception without expiry accepted: %d", response.Code)
	}
	if response := call("reviewer", http.MethodPost, "/v1/supply-chain/review/decisions", decisionBody("allow", leftPadItem.Basis, "")); response.Code != http.StatusBadRequest {
		t.Fatalf("unknown kind accepted: %d", response.Code)
	}
	var decision api.SupplyChainDecision
	decode(t, call("reviewer", http.MethodPost, "/v1/supply-chain/review/decisions", decisionBody("exception", leftPadItem.Basis, time.Now().Add(48*time.Hour).UTC().Format(time.RFC3339))), http.StatusCreated, &decision)
	if decision.Kind != "exception" || decision.PolicyVerdict != "prohibited" || decision.Reviewer != "reviewer@acme" || decision.ExpiresAt == nil || decision.PolicyID == nil {
		t.Fatalf("decision = %+v", decision)
	}
	// The exception removes the occurrence from the queue for repository 101 only; the policy verdict stays prohibited.
	decode(t, call("reviewer", http.MethodGet, "/v1/supply-chain/review/queue", ""), http.StatusOK, &queue)
	for _, item := range queue.Items {
		if item.Coordinates.Name == "left-pad" && item.RepositoryID == 101 {
			t.Fatalf("excepted occurrence still queued: %+v", item)
		}
	}
	if len(queue.Items) != 11 {
		t.Fatalf("queue after exception = %d", len(queue.Items))
	}
	var history api.SupplyChainReviewHistory
	decode(t, call("reader", http.MethodGet, "/v1/supply-chain/review/history?repository_id=101&ecosystem=npm&namespace=%40scope&name=left-pad&version=1.3.0", ""), http.StatusOK, &history)
	if history.Current == nil || history.Current.ID != decision.ID || history.CurrentStale || len(history.PolicyResults) != 1 || history.PolicyResults[0].Verdict != "prohibited" || history.Basis != leftPadItem.Basis {
		t.Fatalf("history = %+v", history)
	}

	// A human conclusion on left-pad (MIT) supersedes the registry's GPL evidence, changes the basis, marks the exception stale, and re-evaluates to approved.
	conclusionBody := fmt.Sprintf(`{"repository_id":101,"coordinates":{"ecosystem":"npm","namespace":"@scope","name":"left-pad","version":"1.3.0"},"expression":"MIT","reason":"registry metadata is wrong; LICENSE file says MIT","basis":%q}`, leftPadItem.Basis)
	if response := call("reviewer", http.MethodPost, "/v1/supply-chain/review/conclusions", strings.Replace(conclusionBody, `"MIT"`, `"see LICENSE"`, 1)); response.Code != http.StatusBadRequest {
		t.Fatalf("free-text conclusion accepted: %d", response.Code)
	}
	var conclusion api.SupplyChainConclusion
	decode(t, call("reviewer", http.MethodPost, "/v1/supply-chain/review/conclusions", conclusionBody), http.StatusCreated, &conclusion)
	if conclusion.Reviewer != "reviewer@acme" || conclusion.Basis != leftPadItem.Basis {
		t.Fatalf("conclusion = %+v", conclusion)
	}
	if response := call("reviewer", http.MethodPost, "/v1/supply-chain/review/conclusions", conclusionBody); response.Code != http.StatusConflict {
		t.Fatalf("replaying a conclusion on the old basis must be stale: %d", response.Code)
	}
	if evaluated, err := reviews.EvaluatePending(t.Context(), 500); err != nil || evaluated != 2 {
		t.Fatalf("re-evaluation after conclusion = %d %v (both left-pad occurrences)", evaluated, err)
	}
	decode(t, call("reader", http.MethodGet, "/v1/supply-chain/review/history?repository_id=101&ecosystem=npm&namespace=%40scope&name=left-pad&version=1.3.0", ""), http.StatusOK, &history)
	if !history.CurrentStale || history.StaleReason != "evidence changed since the decision" || history.Basis == leftPadItem.Basis || len(history.PolicyResults) != 2 || history.PolicyResults[0].Verdict != "approved" || history.PolicyResults[1].Verdict != "prohibited" || len(history.Conclusions) != 1 {
		t.Fatalf("history after conclusion = %+v", history)
	}
	evidence, _ := h.store.LatestLicenseEvidence(t.Context(), leftPad)
	sources := map[string]int{}
	for _, item := range evidence {
		sources[string(item.Source)]++
	}
	if sources["human"] != 1 || sources["registry_npm"] != 1 {
		t.Fatalf("human conclusion must not erase registry evidence: %v", sources)
	}
	// The assessment records the override without hiding the automated evidence.
	occurrences, _ := h.store.ComponentsForCoordinates(t.Context(), leftPad, 10)
	assessments, _ := h.store.SupplyChainAssessments(t.Context(), occurrences[0][1], []int64{occurrences[0][0]})
	if assessment := assessments[occurrences[0][0]]; assessment.Status != license.AssessmentResolved || assessment.NormalizedExpression != "MIT" || !strings.Contains(assessment.ConflictDetail, "GPL-3.0-only") {
		t.Fatalf("assessment after conclusion = %+v", assessment)
	}

	// Expiry: an expired exception surfaces in the queue with its reason and history marks it stale.
	if _, err := h.pool.Exec(t.Context(), `update supply_chain_decisions set expires_at=now()-interval '1 minute' where id=$1`, decision.ID); err != nil {
		t.Fatal(err)
	}
	// Re-decide on the new basis to have a current, then expire it.
	var fresh api.SupplyChainDecision
	decode(t, call("reviewer", http.MethodPost, "/v1/supply-chain/review/decisions", strings.Replace(decisionBody("exception", history.Basis, time.Now().Add(time.Hour).UTC().Format(time.RFC3339)), `"repository_id":101`, `"repository_id":101`, 1)), http.StatusCreated, &fresh)
	if fresh.PolicyVerdict != "approved" {
		t.Fatalf("fresh decision verdict = %q, want the re-evaluated approved", fresh.PolicyVerdict)
	}
	if _, err := h.pool.Exec(t.Context(), `update supply_chain_decisions set expires_at=now()-interval '1 minute' where id=$1`, fresh.ID); err != nil {
		t.Fatal(err)
	}
	decode(t, call("reader", http.MethodGet, "/v1/supply-chain/review/history?repository_id=101&ecosystem=npm&namespace=%40scope&name=left-pad&version=1.3.0", ""), http.StatusOK, &history)
	if !history.CurrentStale || history.StaleReason != "exception expired" || len(history.Decisions) != 2 || history.Decisions[1].SupersededBy == nil || *history.Decisions[1].SupersededBy != fresh.ID {
		t.Fatalf("history after expiry = %+v", history)
	}
	// Audit history is append-only and scoped.
	var events struct {
		Events []api.SupplyChainReviewEvent `json:"events"`
	}
	decode(t, call("reader", http.MethodGet, "/v1/supply-chain/review/events", ""), http.StatusOK, &events)
	kinds := map[string]int{}
	for _, event := range events.Events {
		kinds[event.Kind]++
	}
	if kinds["policy_created"] != 1 || kinds["review_grant_changed"] != 1 || kinds["decision_recorded"] != 2 || kinds["conclusion_recorded"] != 1 {
		t.Fatalf("events = %v", kinds)
	}
	if _, err := h.pool.Exec(t.Context(), `delete from supply_chain_review_events`); err == nil || !strings.Contains(err.Error(), "append-only") {
		t.Fatalf("review events were deletable: %v", err)
	}
	if _, err := h.pool.Exec(t.Context(), `update supply_chain_decisions set reason='edited' where id=$1`, decision.ID); err != nil {
		t.Fatal(err)
	}
	_ = gadgets
}
