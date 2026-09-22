//go:build integration && unix

package integration

import (
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
