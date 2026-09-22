package httpapi

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/balcsida/graphnest/internal/authn"
	"github.com/balcsida/graphnest/internal/repository"
	"github.com/balcsida/graphnest/internal/supplychain"
	"github.com/balcsida/graphnest/pkg/api"
	"github.com/jackc/pgx/v5"
)

// supplyChainFakeStore is an in-memory supplychain.Store holding one published
// snapshot for repository row 1 (GitHub 101) and one for row 2 (GitHub 202).
type supplyChainFakeStore struct {
	snapshots   map[int64]supplychain.Snapshot
	documents   map[int64][]byte
	components  map[int64][]supplychain.Component
	edges       map[int64][]supplychain.Relationship
	streams     map[int64]supplychain.Stream
	collections map[int64][]supplychain.Collection
	jobs        map[int64]supplychain.Job
	enqueued    int
}

func newSupplyChainFakeStore(t *testing.T) *supplyChainFakeStore {
	t.Helper()
	document, err := os.ReadFile("../../test/fixtures/supplychain/ghes-spdx-2.3.json")
	if err != nil {
		t.Fatal(err)
	}
	normalized, err := supplychain.NormalizeSPDX23(document, supplychain.Limits{})
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(document)
	store := &supplyChainFakeStore{snapshots: map[int64]supplychain.Snapshot{}, documents: map[int64][]byte{}, components: map[int64][]supplychain.Component{}, edges: map[int64][]supplychain.Relationship{},
		streams: map[int64]supplychain.Stream{}, collections: map[int64][]supplychain.Collection{}, jobs: map[int64]supplychain.Job{}}
	collected := time.Date(2026, 9, 21, 10, 0, 0, 0, time.UTC)
	for _, repo := range []struct{ row, snapshot int64 }{{1, 11}, {2, 22}} {
		store.snapshots[repo.snapshot] = supplychain.Snapshot{ID: repo.snapshot, RepositoryID: repo.row, DocumentID: repo.snapshot, Producer: supplychain.ProducerGitHub, Subject: supplychain.SubjectSource,
			StreamKey: supplychain.StreamGitHubSource, CollectedAt: collected, CreatedAtClaimed: normalized.CreatedAtClaimed, ProducerTool: normalized.ProducerTool, DocumentName: normalized.DocumentName,
			SPDXVersion: "SPDX-2.3", DataLicense: "CC0-1.0", SubjectAssurance: supplychain.AssuranceUnknown, RootElementIDs: normalized.RootElementIDs, ParserVersion: 1,
			ComponentCount: len(normalized.Components), EdgeCount: len(normalized.Relationships), WarningCount: normalized.WarningCount, Warnings: normalized.Warnings, PublishedAt: collected,
			DocumentSHA256: digest[:], DocumentFormat: supplychain.FormatSPDX23JSON, DocumentBytes: int64(len(document))}
		store.documents[repo.snapshot] = document
		store.components[repo.snapshot] = normalized.Components
		store.edges[repo.snapshot] = normalized.Relationships
		snapshotID := repo.snapshot
		collectionID := repo.snapshot * 10
		store.streams[repo.row] = supplychain.Stream{RepositoryID: repo.row, StreamKey: supplychain.StreamGitHubSource, Producer: supplychain.ProducerGitHub, Subject: supplychain.SubjectSource,
			LatestSnapshotID: &snapshotID, LatestCollectionID: &collectionID, LastSuccessAt: &collected, LastAttemptAt: &collected, LastOutcome: supplychain.OutcomePublished}
		store.collections[repo.row] = []supplychain.Collection{{ID: collectionID, RepositoryID: repo.row, Producer: supplychain.ProducerGitHub, StreamKey: supplychain.StreamGitHubSource,
			StartedAt: collected, FinishedAt: collected, Outcome: supplychain.OutcomePublished, SnapshotID: &snapshotID}}
	}
	store.jobs[7] = supplychain.Job{ID: 7, RepositoryID: 2, StreamKey: supplychain.StreamGitHubSource, Reason: "manual", State: supplychain.JobQueued, MaxAttempts: 5, RunAfter: collected, CreatedAt: collected, UpdatedAt: collected}
	return store
}

func (store *supplyChainFakeStore) SupplyChainStream(_ context.Context, repositoryID int64, _ string) (supplychain.Stream, error) {
	stream, ok := store.streams[repositoryID]
	if !ok {
		return supplychain.Stream{}, pgx.ErrNoRows
	}
	return stream, nil
}

func (store *supplyChainFakeStore) SupplyChainSnapshot(_ context.Context, snapshotID int64, repositoryIDs []int64) (supplychain.Snapshot, error) {
	snapshot, ok := store.snapshots[snapshotID]
	if !ok || !containsID(repositoryIDs, snapshot.RepositoryID) {
		return supplychain.Snapshot{}, pgx.ErrNoRows
	}
	return snapshot, nil
}

func (store *supplyChainFakeStore) SupplyChainSnapshots(_ context.Context, repositoryID int64, _ string, _ int64, _ int) ([]supplychain.Snapshot, error) {
	var result []supplychain.Snapshot
	for _, snapshot := range store.snapshots {
		if snapshot.RepositoryID == repositoryID {
			result = append(result, snapshot)
		}
	}
	return result, nil
}

func (store *supplyChainFakeStore) SupplyChainDocument(_ context.Context, snapshotID int64, repositoryIDs []int64) ([]byte, string, []byte, error) {
	snapshot, ok := store.snapshots[snapshotID]
	if !ok || !containsID(repositoryIDs, snapshot.RepositoryID) {
		return nil, "", nil, pgx.ErrNoRows
	}
	return store.documents[snapshotID], "application/json; charset=utf-8", snapshot.DocumentSHA256, nil
}

func (store *supplyChainFakeStore) SupplyChainComponents(_ context.Context, snapshotID int64, afterOrdinal int, limit int, search string) ([]supplychain.Component, error) {
	var result []supplychain.Component
	for _, component := range store.components[snapshotID] {
		if component.Ordinal <= afterOrdinal || len(result) == limit {
			continue
		}
		if search != "" && !strings.Contains(strings.ToLower(component.Name), strings.ToLower(search)) {
			continue
		}
		result = append(result, component)
	}
	return result, nil
}

func (store *supplyChainFakeStore) SupplyChainRelationships(_ context.Context, snapshotID int64, _ string, _ int) ([]supplychain.Relationship, error) {
	return store.edges[snapshotID], nil
}

func (store *supplyChainFakeStore) SupplyChainCollections(_ context.Context, repositoryID int64, _ string, _ int64, limit int) ([]supplychain.Collection, error) {
	collections := store.collections[repositoryID]
	if len(collections) > limit {
		collections = collections[:limit]
	}
	return collections, nil
}

func (store *supplyChainFakeStore) SupplyChainJob(_ context.Context, jobID int64, repositoryIDs []int64) (supplychain.Job, error) {
	job, ok := store.jobs[jobID]
	if !ok || !containsID(repositoryIDs, job.RepositoryID) {
		return supplychain.Job{}, pgx.ErrNoRows
	}
	return job, nil
}

func (store *supplyChainFakeStore) SupplyChainActiveJob(_ context.Context, repositoryID int64, _ string) (supplychain.Job, bool, error) {
	for _, job := range store.jobs {
		if job.RepositoryID == repositoryID && (job.State == supplychain.JobQueued || job.State == supplychain.JobRunning) {
			return job, true, nil
		}
	}
	return supplychain.Job{}, false, nil
}

func (store *supplyChainFakeStore) EnqueueSupplyChainJob(_ context.Context, repositoryID int64, streamKey, reason, requestedBy string, priority int, runAfter time.Time) (supplychain.Job, bool, error) {
	store.enqueued++
	for _, job := range store.jobs {
		if job.RepositoryID == repositoryID && job.State == supplychain.JobQueued {
			return job, false, nil
		}
	}
	job := supplychain.Job{ID: int64(100 + store.enqueued), RepositoryID: repositoryID, StreamKey: streamKey, Reason: reason, State: supplychain.JobQueued, Priority: priority, MaxAttempts: 5, RunAfter: runAfter, RequestedBy: requestedBy}
	store.jobs[job.ID] = job
	return job, true, nil
}

func containsID(ids []int64, id int64) bool {
	for _, candidate := range ids {
		if candidate == id {
			return true
		}
	}
	return false
}

// supplyChainFakeAuthorizer grants GitHub 101 (row 1) to "user" and both
// repositories to "admin".
type supplyChainFakeAuthorizer struct{}

var supplyChainRepositories = map[int64]repository.Repository{
	101: {ID: 1, InstallationID: 10, GitHubID: 101, Name: "acme/widgets", Enabled: true},
	202: {ID: 2, InstallationID: 10, GitHubID: 202, Name: "acme/gadgets", Enabled: true},
}

func (supplyChainFakeAuthorizer) AllAuthorizedRepositories(_ context.Context, principal authn.Principal) ([]repository.Repository, error) {
	if principal.DelegationOnly {
		return nil, nil
	}
	var result []repository.Repository
	for githubID, repo := range supplyChainRepositories {
		if principal.Administrator || containsID(principal.RepositoryIDs, githubID) {
			result = append(result, repo)
		}
	}
	return result, nil
}

func (supplyChainFakeAuthorizer) AuthorizedRepository(_ context.Context, principal authn.Principal, githubID int64) (repository.Repository, error) {
	repo, ok := supplyChainRepositories[githubID]
	if !ok || principal.DelegationOnly || !(principal.Administrator || containsID(principal.RepositoryIDs, githubID)) {
		return repository.Repository{}, pgx.ErrNoRows
	}
	return repo, nil
}

func supplyChainMux(t *testing.T) (*http.ServeMux, *supplyChainFakeStore) {
	t.Helper()
	store := newSupplyChainFakeStore(t)
	service := &supplychain.Service{Store: store, Authorizer: supplyChainFakeAuthorizer{}, Interval: 24 * time.Hour, MaxResults: 100,
		Now: func() time.Time { return time.Date(2026, 9, 22, 10, 0, 0, 0, time.UTC) }}
	authenticator := requestAuthenticator(authn.NewStatic(map[string]authn.Principal{
		"user":   {Subject: "user", Method: "api_token", InstallationID: 10, RepositoryIDs: []int64{101}},
		"admin":  {Subject: "admin", Method: "session", Administrator: true},
		"broker": {Subject: "broker", Method: "api_token", Administrator: true, DelegationOnly: true},
	}))
	mux := http.NewServeMux()
	RegisterSupplyChain(mux, authenticator, service, 100, 256<<10)
	return mux, store
}

func TestSupplyChainStatusAndComponents(t *testing.T) {
	mux, _ := supplyChainMux(t)
	response := repositoryRequest(t, mux, http.MethodGet, "/v1/supply-chain/repositories/101", "", "user", "")
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", response.Code, response.Body.String())
	}
	var status api.SupplyChainRepositoryStatus
	decodeRepositoryResponse(t, response, &status)
	if status.Repository != "acme/widgets" || status.Collection != "current" || status.LatestSnapshot == nil || status.LatestSnapshot.ComponentCount != 7 || status.FreshnessSeconds == nil || *status.FreshnessSeconds != 86400 {
		t.Fatalf("status = %+v", status)
	}
	if len(status.Documents) != 1 || status.Documents[0].Path != "/v1/supply-chain/snapshots/11/document" || len(status.Documents[0].SHA256) != 64 {
		t.Fatalf("documents = %+v", status.Documents)
	}
	if status.LatestSnapshot.SubjectAssurance != "unknown" || status.LatestSnapshot.SubjectRevision != "" {
		t.Fatalf("GHES snapshot must not claim a subject revision: %+v", status.LatestSnapshot)
	}

	response = repositoryRequest(t, mux, http.MethodGet, "/v1/supply-chain/repositories/101/components?limit=3", "", "user", "")
	var page api.SupplyChainComponentList
	decodeRepositoryResponse(t, response, &page)
	if response.Code != http.StatusOK || len(page.Components) != 3 || !page.Truncated || page.NextCursor == "" || page.SnapshotID != 11 {
		t.Fatalf("page = %d %+v", response.Code, page)
	}
	if page.Components[0].Scope != "root" || page.Components[1].Scope != "direct" || page.Components[1].Ecosystem != "npm" || *page.Components[1].LicenseDeclaredRaw != "NOASSERTION" {
		t.Fatalf("components = %+v", page.Components)
	}
	response = repositoryRequest(t, mux, http.MethodGet, "/v1/supply-chain/repositories/101/components?limit=3&cursor="+page.NextCursor, "", "user", "")
	var second api.SupplyChainComponentList
	decodeRepositoryResponse(t, response, &second)
	if response.Code != http.StatusOK || len(second.Components) != 3 || second.Components[0].Ordinal != 3 || !second.Truncated {
		t.Fatalf("second page = %d %+v", response.Code, second)
	}
	response = repositoryRequest(t, mux, http.MethodGet, "/v1/supply-chain/repositories/101/components?limit=3&cursor="+second.NextCursor, "", "user", "")
	var third api.SupplyChainComponentList
	decodeRepositoryResponse(t, response, &third)
	if response.Code != http.StatusOK || len(third.Components) != 1 || third.Truncated || third.NextCursor != "" || third.Components[0].PURL != nil || third.Components[0].Scope != "direct" {
		t.Fatalf("third page = %d %+v", response.Code, third)
	}
	// A cursor is bound to its search term and snapshot; reuse with another query is rejected.
	response = repositoryRequest(t, mux, http.MethodGet, "/v1/supply-chain/repositories/101/components?q=npm&cursor="+page.NextCursor, "", "user", "")
	if response.Code != http.StatusBadRequest {
		t.Fatalf("cursor with different search = %d", response.Code)
	}
	response = repositoryRequest(t, mux, http.MethodGet, "/v1/supply-chain/repositories/101/components?q=npm", "", "user", "")
	var filtered api.SupplyChainComponentList
	decodeRepositoryResponse(t, response, &filtered)
	if response.Code != http.StatusOK || len(filtered.Components) != 1 || filtered.Components[0].PackageName != "left-pad" {
		t.Fatalf("filtered = %d %+v", response.Code, filtered)
	}
}

func TestSupplyChainAuthorizationBoundary(t *testing.T) {
	mux, _ := supplyChainMux(t)
	for _, target := range []string{
		"/v1/supply-chain/repositories/202", "/v1/supply-chain/repositories/202/components", "/v1/supply-chain/repositories/202/snapshots",
		"/v1/supply-chain/repositories/202/collections", "/v1/supply-chain/snapshots/22/document", "/v1/supply-chain/jobs/7",
		"/v1/supply-chain/repositories/303", "/v1/supply-chain/snapshots/999/document",
	} {
		response := repositoryRequest(t, mux, http.MethodGet, target, "", "user", "")
		if response.Code != http.StatusNotFound {
			t.Fatalf("%s for unauthorized user = %d body = %s", target, response.Code, response.Body.String())
		}
		assertRepositoryError(t, response.Body.String(), "not_found")
	}
	// The same targets succeed for the administrator, so 404 above was authorization, not absence.
	for _, target := range []string{"/v1/supply-chain/repositories/202", "/v1/supply-chain/snapshots/22/document", "/v1/supply-chain/jobs/7"} {
		if response := repositoryRequest(t, mux, http.MethodGet, target, "", "admin", ""); response.Code != http.StatusOK {
			t.Fatalf("%s for admin = %d body = %s", target, response.Code, response.Body.String())
		}
	}
	// Delegation-only tokens are confined before any handler runs.
	if response := repositoryRequest(t, mux, http.MethodGet, "/v1/supply-chain/repositories/101", "", "broker", ""); response.Code != http.StatusForbidden {
		t.Fatalf("delegation-only = %d", response.Code)
	}
	if response := repositoryRequest(t, mux, http.MethodGet, "/v1/supply-chain/repositories/101", "", "", ""); response.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous = %d", response.Code)
	}
	if response := repositoryRequest(t, mux, http.MethodGet, "/v1/supply-chain/repositories/101", "", "wrong", ""); response.Code != http.StatusUnauthorized {
		t.Fatalf("bad token = %d", response.Code)
	}
}

func TestSupplyChainDocumentDownload(t *testing.T) {
	mux, store := supplyChainMux(t)
	response := repositoryRequest(t, mux, http.MethodGet, "/v1/supply-chain/snapshots/11/document", "", "user", "")
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", response.Code, response.Body.String())
	}
	if response.Body.String() != string(store.documents[11]) {
		t.Fatal("document bytes were altered in transit")
	}
	headers := response.Header()
	if headers.Get("Content-Type") != "application/json; charset=utf-8" || headers.Get("Content-Disposition") != `attachment; filename="graphnest-sbom-github-repo1-snapshot11.json"` ||
		headers.Get("X-Content-Type-Options") != "nosniff" || headers.Get("Cache-Control") != "private, no-store" || len(headers.Get("X-Content-SHA256")) != 64 || headers.Get("X-GraphNest-Snapshot-ID") != "11" {
		t.Fatalf("headers = %v", headers)
	}
	for _, path := range []string{"/v1/supply-chain/snapshots/11", "/v1/supply-chain/snapshots/abc/document", "/v1/supply-chain/snapshots/0/document", "/v1/supply-chain/snapshots/11/document/extra"} {
		if response := repositoryRequest(t, mux, http.MethodGet, path, "", "user", ""); response.Code != http.StatusBadRequest {
			t.Fatalf("%s = %d", path, response.Code)
		}
	}
	if response := repositoryRequest(t, mux, http.MethodPost, "/v1/supply-chain/snapshots/11/document", "", "user", ""); response.Code != http.StatusMethodNotAllowed {
		t.Fatalf("POST document = %d", response.Code)
	}
}

func TestSupplyChainRefreshRequiresAdministratorAndEnqueues(t *testing.T) {
	mux, store := supplyChainMux(t)
	response := repositoryRequest(t, mux, http.MethodPost, "/v1/supply-chain/repositories/101/refresh", "", "user", "")
	if response.Code != http.StatusForbidden || store.enqueued != 0 {
		t.Fatalf("user refresh = %d enqueued = %d", response.Code, store.enqueued)
	}
	assertRepositoryError(t, response.Body.String(), "forbidden")
	response = repositoryRequest(t, mux, http.MethodPost, "/v1/supply-chain/repositories/101/refresh", "", "admin", "")
	if response.Code != http.StatusAccepted {
		t.Fatalf("admin refresh = %d body = %s", response.Code, response.Body.String())
	}
	var refresh api.SupplyChainRefreshResponse
	decodeRepositoryResponse(t, response, &refresh)
	if !refresh.Created || refresh.Job.RepositoryID != 101 || refresh.Job.Reason != "manual" || refresh.Job.State != "queued" {
		t.Fatalf("refresh = %+v", refresh)
	}
	response = repositoryRequest(t, mux, http.MethodPost, "/v1/supply-chain/repositories/101/refresh", "", "admin", "")
	decodeRepositoryResponse(t, response, &refresh)
	if response.Code != http.StatusAccepted || refresh.Created {
		t.Fatalf("second refresh = %d %+v", response.Code, refresh)
	}
	if response := repositoryRequest(t, mux, http.MethodGet, "/v1/supply-chain/repositories/101/refresh", "", "admin", ""); response.Code != http.StatusMethodNotAllowed {
		t.Fatalf("GET refresh = %d", response.Code)
	}
	if response := repositoryRequest(t, mux, http.MethodPost, "/v1/supply-chain/repositories/101/refresh", `{"x":1}`, "admin", "application/json"); response.Code != http.StatusBadRequest {
		t.Fatalf("refresh with body = %d", response.Code)
	}
	if response := repositoryRequest(t, mux, http.MethodPost, "/v1/supply-chain/repositories/101/refresh?stream=import:artifact", "", "admin", ""); response.Code != http.StatusBadRequest {
		t.Fatalf("refresh unknown stream = %d", response.Code)
	}
	// Job status is visible to the authorized repository's readers and reports the GitHub ID.
	response = repositoryRequest(t, mux, http.MethodGet, "/v1/supply-chain/jobs/7", "", "admin", "")
	var job api.SupplyChainJob
	decodeRepositoryResponse(t, response, &job)
	if response.Code != http.StatusOK || job.RepositoryID != 202 || job.State != "queued" {
		t.Fatalf("job = %d %+v", response.Code, job)
	}
}

func TestSupplyChainInvalidRequests(t *testing.T) {
	mux, _ := supplyChainMux(t)
	for _, path := range []string{
		"/v1/supply-chain/repositories/", "/v1/supply-chain/repositories/abc", "/v1/supply-chain/repositories/0", "/v1/supply-chain/repositories/101/components/extra",
		"/v1/supply-chain/repositories/101/components?limit=0", "/v1/supply-chain/repositories/101/components?limit=101", "/v1/supply-chain/repositories/101/components?cursor=",
		"/v1/supply-chain/repositories/101/components?cursor=not-base64!", "/v1/supply-chain/repositories/101/components?cursor=eyJ2IjoyfQ", "/v1/supply-chain/repositories/101/components?snapshot_id=x", "/v1/supply-chain/repositories/101?stream=artifact",
		"/v1/supply-chain/repositories/101/components?q=" + strings.Repeat("a", 201), "/v1/supply-chain/jobs/x", "/v1/supply-chain/jobs/1/2",
	} {
		response := repositoryRequest(t, mux, http.MethodGet, path, "", "admin", "")
		if response.Code != http.StatusBadRequest {
			t.Fatalf("%s = %d body = %s", path, response.Code, response.Body.String())
		}
	}
	if response := repositoryRequest(t, mux, http.MethodGet, "/v1/supply-chain/repositories/101/unknown", "", "admin", ""); response.Code != http.StatusNotFound {
		t.Fatalf("unknown action = %d", response.Code)
	}
}

func TestSupplyChainStatusWithoutInventory(t *testing.T) {
	mux, store := supplyChainMux(t)
	delete(store.streams, 1)
	response := repositoryRequest(t, mux, http.MethodGet, "/v1/supply-chain/repositories/101", "", "user", "")
	var status api.SupplyChainRepositoryStatus
	decodeRepositoryResponse(t, response, &status)
	if response.Code != http.StatusOK || status.Collection != "never" || status.LatestSnapshot != nil || status.FreshnessSeconds != nil {
		t.Fatalf("status = %d %+v", response.Code, status)
	}
	response = repositoryRequest(t, mux, http.MethodGet, "/v1/supply-chain/repositories/101/components", "", "user", "")
	if response.Code != http.StatusNotFound {
		t.Fatalf("components without inventory = %d", response.Code)
	}
	assertRepositoryError(t, response.Body.String(), "no_inventory")
}

func TestSupplyChainStatusReportsFailedRefreshWithRetainedInventory(t *testing.T) {
	mux, store := supplyChainMux(t)
	status403 := 403
	store.collections[1] = append([]supplychain.Collection{{ID: 999, RepositoryID: 1, Producer: supplychain.ProducerGitHub, StreamKey: supplychain.StreamGitHubSource,
		StartedAt: time.Now(), FinishedAt: time.Now(), Outcome: supplychain.OutcomeForbidden, HTTPStatus: &status403, ErrorCode: "github_forbidden", Message: "GitHub returned 403"}}, store.collections[1]...)
	stream := store.streams[1]
	stream.LastOutcome = supplychain.OutcomeForbidden
	store.streams[1] = stream
	response := repositoryRequest(t, mux, http.MethodGet, "/v1/supply-chain/repositories/101", "", "user", "")
	var status api.SupplyChainRepositoryStatus
	decodeRepositoryResponse(t, response, &status)
	if response.Code != http.StatusOK || status.Collection != "failed" || status.LatestSnapshot == nil || status.LastCollection == nil || status.LastCollection.Outcome != "forbidden" || *status.LastCollection.HTTPStatus != 403 {
		t.Fatalf("status = %d %+v", response.Code, status)
	}
	found := false
	for _, note := range status.Notes {
		found = found || strings.Contains(note, "last successful observation")
	}
	if !found {
		t.Fatalf("notes = %v", status.Notes)
	}
	raw, _ := json.Marshal(status)
	if !strings.Contains(string(raw), `"collection":"failed"`) {
		t.Fatalf("wire = %s", raw)
	}
}
