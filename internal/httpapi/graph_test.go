package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/balcsida/graphnest/internal/authn"
	"github.com/balcsida/graphnest/internal/graphartifact"
	graphv1 "github.com/balcsida/graphnest/internal/graphartifact/v1"
	graphv2 "github.com/balcsida/graphnest/internal/graphartifact/v2"
	"github.com/balcsida/graphnest/internal/graphingest"
	"github.com/balcsida/graphnest/internal/postgres"
	"github.com/balcsida/graphnest/internal/repository"
	"github.com/balcsida/graphnest/pkg/api"
	"github.com/jackc/pgx/v5"
	"google.golang.org/protobuf/proto"
)

const graphTestSHA = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

func TestGraphUploadContract(t *testing.T) {
	data := graphArtifactBytes(t, 101)
	tests := []struct {
		name, method, target, token, contentType string
		body                                     []byte
		max                                      int64
		want                                     int
	}{
		{"exact method", http.MethodPut, graphUploadTarget(graphTestSHA), "admin", "application/vnd.graphnest.graph.v1+protobuf", data, int64(len(data)), http.StatusMethodNotAllowed},
		{"administrator required", http.MethodPost, graphUploadTarget(graphTestSHA), "user", "application/vnd.graphnest.graph.v1+protobuf", data, int64(len(data)), http.StatusForbidden},
		{"exact content type", http.MethodPost, graphUploadTarget(graphTestSHA), "admin", "application/vnd.graphnest.graph.v1+protobuf; charset=binary", data, int64(len(data)), http.StatusUnsupportedMediaType},
		{"missing repository", http.MethodPost, "/v1/graph/uploads?commit=" + graphTestSHA, "admin", "application/vnd.graphnest.graph.v1+protobuf", data, int64(len(data)), http.StatusBadRequest},
		{"duplicate repository", http.MethodPost, "/v1/graph/uploads?repository_id=101&repository_id=101&commit=" + graphTestSHA, "admin", "application/vnd.graphnest.graph.v1+protobuf", data, int64(len(data)), http.StatusBadRequest},
		{"unknown query", http.MethodPost, graphUploadTarget(graphTestSHA) + "&extra=1", "admin", "application/vnd.graphnest.graph.v1+protobuf", data, int64(len(data)), http.StatusBadRequest},
		{"uppercase commit", http.MethodPost, graphUploadTarget(strings.ToUpper(graphTestSHA)), "admin", "application/vnd.graphnest.graph.v1+protobuf", data, int64(len(data)), http.StatusBadRequest},
		{"exact body limit", http.MethodPost, graphUploadTarget(graphTestSHA), "admin", "application/vnd.graphnest.graph.v1+protobuf", data, int64(len(data)), http.StatusNoContent},
		{"body over limit", http.MethodPost, graphUploadTarget(graphTestSHA), "admin", "application/vnd.graphnest.graph.v1+protobuf", data, int64(len(data) - 1), http.StatusRequestEntityTooLarge},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			response := graphRequest(graphHandler(&graphStoreStub{}, test.max, 1024), test.method, test.target, test.body, test.token, test.contentType)
			if response.Code != test.want {
				t.Fatalf("status=%d want=%d body=%q", response.Code, test.want, response.Body.String())
			}
			if test.want == http.StatusMethodNotAllowed && response.Header().Get("Allow") != http.MethodPost {
				t.Fatalf("Allow=%q", response.Header().Get("Allow"))
			}
		})
	}
}

func TestGraphUploadRejectsUnauthorizedBodyBeforeRead(t *testing.T) {
	body := &countingReader{Reader: bytes.NewReader(graphArtifactBytes(t, 101))}
	request := httptest.NewRequest(http.MethodPost, graphUploadTarget(graphTestSHA), body)
	request.Header.Set("Content-Type", "application/vnd.graphnest.graph.v1+protobuf")
	recorder := httptest.NewRecorder()
	graphHandler(&graphStoreStub{}, 1<<20, 1024).ServeHTTP(recorder, request)
	if recorder.Code != http.StatusUnauthorized || body.reads != 0 {
		t.Fatalf("status=%d reads=%d", recorder.Code, body.reads)
	}
}

func TestGraphUploadRejectsStaleCommitBeforeRead(t *testing.T) {
	body := &countingReader{Reader: bytes.NewReader(graphArtifactBytes(t, 101))}
	request := httptest.NewRequest(http.MethodPost, graphUploadTarget(strings.Repeat("b", 40)), body)
	request.Header.Set("Authorization", "Bearer admin")
	request.Header.Set("Content-Type", "application/vnd.graphnest.graph.v1+protobuf")
	recorder := httptest.NewRecorder()
	graphHandler(&graphStoreStub{}, 1<<20, 1024).ServeHTTP(recorder, request)
	if recorder.Code != http.StatusConflict || body.reads != 0 {
		t.Fatalf("status=%d reads=%d body=%q", recorder.Code, body.reads, recorder.Body.String())
	}
}

func TestGraphUploadDeadlineSequence(t *testing.T) {
	store := &graphStoreStub{}
	recorder := &graphDeadlineRecorder{ResponseRecorder: httptest.NewRecorder()}
	store.readDeadlineCleared = &recorder.readDeadlineCleared
	store.writeDeadlineSet = &recorder.writeDeadlineSet
	request := httptest.NewRequest(http.MethodPost, graphUploadTarget(graphTestSHA), bytes.NewReader(graphArtifactBytes(t, 101)))
	request.Header.Set("Authorization", "Bearer admin")
	request.Header.Set("Content-Type", "application/vnd.graphnest.graph.v1+protobuf")
	graphHandler(store, 1<<20, 1024).ServeHTTP(recorder, request)
	if recorder.Code != http.StatusNoContent || !store.authorizedBeforeDeadlineClear.Load() || store.authorizedCalls.Load() != 3 {
		t.Fatalf("status=%d authorizedBeforeClear=%t calls=%d", recorder.Code, store.authorizedBeforeDeadlineClear.Load(), store.authorizedCalls.Load())
	}
	if !recorder.readDeadlineCleared.Load() || !recorder.writeDeadlineSet.Load() || store.writeDeadlineSetDuringReplace.Load() {
		t.Fatalf("readCleared=%t writeSet=%t writeDuringReplace=%t", recorder.readDeadlineCleared.Load(), recorder.writeDeadlineSet.Load(), store.writeDeadlineSetDuringReplace.Load())
	}
}

func TestGraphStatusContractAndBound(t *testing.T) {
	store := &graphStoreStub{status: api.GraphStatus{
		RepositoryID: 101, Commit: graphTestSHA, State: api.GraphStateFallback,
		Source: api.GraphSourceExternal, SCIPFallback: &api.SCIPFallbackStatus{Commit: graphTestSHA},
	}}
	handler := graphHandler(store, 1024, 1024)
	response := graphRequest(handler, http.MethodGet, "/v1/graph/repositories/101/status", nil, "user", "")
	var got api.GraphStatus
	if err := json.Unmarshal(response.Body.Bytes(), &got); err != nil || got.Publication == nil {
		t.Fatalf("status=%s err=%v", response.Body.String(), err)
	}
	got.Publication = nil
	if response.Code != http.StatusOK || !reflect.DeepEqual(got, store.status) {
		t.Fatalf("status=%d response=%#v", response.Code, got)
	}
	for _, test := range []struct {
		name, method, target string
		want                 int
	}{
		{"exact method", http.MethodPost, "/v1/graph/repositories/101/status", http.StatusMethodNotAllowed},
		{"positive repository", http.MethodGet, "/v1/graph/repositories/0/status", http.StatusBadRequest},
		{"exact path", http.MethodGet, "/v1/graph/repositories/101/status/extra", http.StatusBadRequest},
	} {
		t.Run(test.name, func(t *testing.T) {
			got := graphRequest(handler, test.method, test.target, nil, "user", "")
			if got.Code != test.want {
				t.Fatalf("status=%d want=%d body=%q", got.Code, test.want, got.Body.String())
			}
		})
	}
	bounded := graphRequest(graphHandler(store, 1024, 1), http.MethodGet, "/v1/graph/repositories/101/status", nil, "user", "")
	if bounded.Code != http.StatusInternalServerError || bounded.Body.Len() != 0 {
		t.Fatalf("bounded status=%d body=%q", bounded.Code, bounded.Body.String())
	}
}

func TestGraphErrorsAreSafe(t *testing.T) {
	for _, test := range []struct {
		name      string
		err       error
		want      int
		code      string
		message   string
		retryable bool
	}{
		{"forbidden", graphingest.ErrForbidden, http.StatusForbidden, "forbidden", "graph publication is not permitted", false},
		{"generation changed", graphingest.ErrConflict, http.StatusConflict, "generation_conflict", "graph generation or indexed commit changed", false},
		{"producer changed", graphingest.ErrProducerConflict, http.StatusConflict, "producer_conflict", "replacing another producer requires replace_producer=true", false},
		{"credential revoked", graphingest.ErrUnauthenticated, http.StatusUnauthorized, "unauthenticated", "authentication required", false},
		{"invalid artifact", graphingest.ErrInvalidArtifact, http.StatusBadRequest, "invalid_request", "request is invalid", false},
		{"stale", graphingest.ErrNotIndexed, http.StatusConflict, "not_indexed", "repository is not indexed", false},
		{"missing", pgx.ErrNoRows, http.StatusNotFound, "not_found", "repository not found", false},
		{"unavailable", errors.New("database password"), http.StatusServiceUnavailable, "unavailable", "graph service is unavailable", true},
	} {
		t.Run(test.name, func(t *testing.T) {
			response := httptest.NewRecorder()
			writeGraphError(response, test.err)
			if response.Code != test.want {
				t.Fatalf("status=%d body=%q", response.Code, response.Body.String())
			}
			assertSafeError(t, response.Body.String(), "password", test.code, test.message, test.retryable)
		})
	}
}

func graphHandler(store *graphStoreStub, maxUpload, maxResponse int64) http.Handler {
	mux := http.NewServeMux()
	grants := &UploadGrants{
		Set: func(_ context.Context, repositoryID int64, subject, grantedBy string, allow bool) error {
			store.grants = append(store.grants, fmt.Sprintf("%d %s %s %t", repositoryID, subject, grantedBy, allow))
			return nil
		},
		Resolve: func(_ context.Context, _ authn.Principal, githubID int64) (int64, error) {
			if githubID != 101 {
				return 0, pgx.ErrNoRows
			}
			return 1, nil
		},
	}
	RegisterGraphIngestion(mux, authn.NewStatic(map[string]authn.Principal{
		"user":    {InstallationID: 10, RepositoryIDs: []int64{101}},
		"grantee": {Subject: "42", Method: "api_token", InstallationID: 10, RepositoryIDs: []int64{101}},
		"admin":   {Subject: "1", Method: "api_token", InstallationID: 10, RepositoryIDs: []int64{101}, Administrator: true},
	}), &graphingest.Service{Store: store, Limits: graphartifact.Limits{MaxNodes: 2, MaxEdges: 1}}, grants, maxUpload, maxResponse)
	return mux
}

const graphV2Type = "application/vnd.graphnest.graph.v2+protobuf"

func TestGraphV2UploadContract(t *testing.T) {
	data := graphV2ArtifactBytes(t)
	target := graphUploadTarget(graphTestSHA) + "&expected_generation=7"
	for _, test := range []struct {
		name, target, token string
		granted             bool
		want                int
	}{
		{"read access only", target, "grantee", false, http.StatusForbidden},
		{"no publisher identity", target, "user", true, http.StatusForbidden},
		{"grantee", target, "grantee", true, http.StatusOK},
		{"administrator without grant", target, "admin", false, http.StatusOK},
		{"missing expected generation", graphUploadTarget(graphTestSHA), "admin", false, http.StatusBadRequest},
		{"negative expected generation", graphUploadTarget(graphTestSHA) + "&expected_generation=-1", "admin", false, http.StatusBadRequest},
		{"duplicate expected generation", target + "&expected_generation=7", "admin", false, http.StatusBadRequest},
		{"replace producer must be true", target + "&replace_producer=yes", "admin", false, http.StatusBadRequest},
		{"empty replace producer", target + "&replace_producer=", "admin", false, http.StatusBadRequest},
		{"unknown query", target + "&extra=1", "admin", false, http.StatusBadRequest},
	} {
		t.Run(test.name, func(t *testing.T) {
			store := &graphStoreStub{}
			if test.granted {
				store.granted = map[string]bool{"42": true, "": true}
			}
			response := graphRequest(graphHandler(store, int64(len(data)), 1024), http.MethodPost, test.target, data, test.token, graphV2Type)
			if response.Code != test.want {
				t.Fatalf("status=%d want=%d body=%q", response.Code, test.want, response.Body.String())
			}
		})
	}
	store := &graphStoreStub{granted: map[string]bool{"42": true}}
	response := graphRequest(graphHandler(store, int64(len(data)), 1024), http.MethodPost, target+"&replace_producer=true", data, "grantee", graphV2Type)
	var result api.GraphPublicationResult
	if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil || response.Code != http.StatusOK || result.Generation != 9 || result.ReplacedGeneration != 7 || result.RepositoryID != 101 || len(result.ContentHash) != 64 {
		t.Fatalf("status=%d result=%#v err=%v", response.Code, result, err)
	}
	if !reflect.DeepEqual(store.publication, postgres.GraphPublication{Publisher: "api_token:42", ExpectedActiveID: 7, AllowProviderChange: true}) {
		t.Fatalf("publication=%#v", store.publication)
	}
	v1WithIntent := graphRequest(graphHandler(&graphStoreStub{}, 1<<20, 1024), http.MethodPost, target, graphArtifactBytes(t, 101), "admin", "application/vnd.graphnest.graph.v1+protobuf")
	if v1WithIntent.Code != http.StatusBadRequest {
		t.Fatalf("v1 accepted v2 parameters: %d", v1WithIntent.Code)
	}
	conflict := graphRequest(graphHandler(&graphStoreStub{replaceV2Err: postgres.ErrGraphPrecondition}, 1<<20, 1024), http.MethodPost, target, data, "admin", graphV2Type)
	if conflict.Code != http.StatusConflict || !strings.Contains(conflict.Body.String(), "generation_conflict") {
		t.Fatalf("conflict=%d %s", conflict.Code, conflict.Body.String())
	}
}

func TestGraphV2UploadRejectsUnpermittedBodyBeforeRead(t *testing.T) {
	body := &countingReader{Reader: bytes.NewReader(graphV2ArtifactBytes(t))}
	request := httptest.NewRequest(http.MethodPost, graphUploadTarget(graphTestSHA)+"&expected_generation=0", body)
	request.Header.Set("Authorization", "Bearer grantee")
	request.Header.Set("Content-Type", graphV2Type)
	recorder := httptest.NewRecorder()
	graphHandler(&graphStoreStub{}, 1<<20, 1024).ServeHTTP(recorder, request)
	if recorder.Code != http.StatusForbidden || body.reads != 0 {
		t.Fatalf("status=%d reads=%d", recorder.Code, body.reads)
	}
}

func TestGraphPublicationGrantRoute(t *testing.T) {
	for _, test := range []struct {
		name, method, token, body string
		want                      int
		grants                    []string
	}{
		{"administrator grants", http.MethodPut, "admin", `{"repository_id":101,"subject":"42","allow":true}`, http.StatusNoContent, []string{"1 42 1 true"}},
		{"administrator revokes", http.MethodPut, "admin", `{"repository_id":101,"subject":"42","allow":false}`, http.StatusNoContent, []string{"1 42 1 false"}},
		{"grantee cannot grant", http.MethodPut, "grantee", `{"repository_id":101,"subject":"43","allow":true}`, http.StatusForbidden, nil},
		{"unknown repository", http.MethodPut, "admin", `{"repository_id":102,"subject":"42","allow":true}`, http.StatusNotFound, nil},
		{"empty subject", http.MethodPut, "admin", `{"repository_id":101,"subject":"","allow":true}`, http.StatusBadRequest, nil},
		{"oversized subject", http.MethodPut, "admin", `{"repository_id":101,"subject":"` + strings.Repeat("x", 257) + `","allow":true}`, http.StatusBadRequest, nil},
		{"unknown field", http.MethodPut, "admin", `{"repository_id":101,"subject":"42","allow":true,"scope":"all"}`, http.StatusBadRequest, nil},
		{"exact method", http.MethodPost, "admin", `{"repository_id":101,"subject":"42","allow":true}`, http.StatusMethodNotAllowed, nil},
	} {
		t.Run(test.name, func(t *testing.T) {
			store := &graphStoreStub{}
			response := graphRequest(graphHandler(store, 1024, 1024), test.method, "/v1/graph/publication-grants", []byte(test.body), test.token, "application/json")
			if response.Code != test.want || !reflect.DeepEqual(store.grants, test.grants) {
				t.Fatalf("status=%d grants=%v body=%q", response.Code, store.grants, response.Body.String())
			}
		})
	}
}

func graphV2ArtifactBytes(t *testing.T) []byte {
	t.Helper()
	data, err := graphartifact.MarshalV2(&graphv2.Artifact{SchemaVersion: 2, Repository: "101", Commit: graphTestSHA, Producer: &graphv2.Producer{Name: "codegraph", Version: "0.7.0", Configuration: "portable"},
		Nodes: []*graphv2.Node{{SourceId: "a", Occurrence: "declaration:1", Kind: "function"}, {SourceId: "b", Occurrence: "declaration:2", Kind: "class"}}}, graphartifact.Limits{})
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func graphRequest(handler http.Handler, method, target string, body []byte, token, contentType string) *httptest.ResponseRecorder {
	response := httptest.NewRecorder()
	request := httptest.NewRequest(method, target, bytes.NewReader(body))
	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	if contentType != "" {
		request.Header.Set("Content-Type", contentType)
	}
	handler.ServeHTTP(response, request)
	return response
}

func graphUploadTarget(commit string) string {
	return "/v1/graph/uploads?repository_id=101&commit=" + commit
}

func graphArtifactBytes(t *testing.T, repositoryID int64) []byte {
	t.Helper()
	data, err := proto.Marshal(&graphv1.Artifact{
		SchemaVersion: 1, RepositoryId: repositoryID, Commit: graphTestSHA,
		ContentHash: bytes.Repeat([]byte{1}, 32), Analyzer: &graphv1.Analyzer{Name: "test", Version: "1"},
		Nodes: []*graphv1.Node{
			{Uid: "repository", Kind: graphv1.NodeKind_NODE_KIND_REPOSITORY},
			{Uid: "symbol", Kind: graphv1.NodeKind_NODE_KIND_SYMBOL, Path: "a.go", Language: "go", QualifiedName: "Thing", Range: &graphv1.Range{EndCharacter: 1}},
		},
		Edges: []*graphv1.Edge{{SourceUid: "repository", TargetUid: "symbol", Kind: graphv1.EdgeKind_EDGE_KIND_CONTAINS, Path: "a.go", Confidence: 1}},
	})
	if err != nil {
		t.Fatal(err)
	}
	return data
}

type countingReader struct {
	*bytes.Reader
	reads int
}

func (reader *countingReader) Read(data []byte) (int, error) {
	reader.reads++
	return reader.Reader.Read(data)
}

type graphStoreStub struct {
	status                        api.GraphStatus
	granted                       map[string]bool
	active                        *api.GraphActiveGeneration
	replaceV2Err                  error
	publication                   postgres.GraphPublication
	grants                        []string
	authorizedCalls               atomic.Int64
	authorizedBeforeDeadlineClear atomic.Bool
	writeDeadlineSetDuringReplace atomic.Bool
	readDeadlineCleared           *atomic.Bool
	writeDeadlineSet              *atomic.Bool
}

func (store *graphStoreStub) AuthorizedRepository(context.Context, int64, []int64, int64) (repository.Repository, error) {
	store.authorizedCalls.Add(1)
	if store.readDeadlineCleared == nil || !store.readDeadlineCleared.Load() {
		store.authorizedBeforeDeadlineClear.Store(true)
	}
	return repository.Repository{ID: 1, GitHubID: 101, InstallationID: 10, IndexedSHA: graphTestSHA}, nil
}

func (store *graphStoreStub) ReplaceGraph(context.Context, int64, postgres.GraphSource, graphartifact.Artifact) (postgres.GraphReplacement, error) {
	if store.writeDeadlineSet != nil && store.writeDeadlineSet.Load() {
		store.writeDeadlineSetDuringReplace.Store(true)
	}
	return postgres.GraphReplacement{Applied: true}, nil
}

func (store *graphStoreStub) GraphStatus(context.Context, int64) (api.GraphStatus, error) {
	return store.status, nil
}

func (store *graphStoreStub) GraphPublicationAllowed(_ context.Context, _ int64, subject string) (bool, error) {
	return store.granted[subject], nil
}

func (store *graphStoreStub) ActiveGraphGeneration(context.Context, int64) (*api.GraphActiveGeneration, error) {
	return store.active, nil
}

func (store *graphStoreStub) ReplaceGraphV2(_ context.Context, _ int64, publication postgres.GraphPublication, _ *graphv2.Artifact) (postgres.GraphReplacement, error) {
	if store.replaceV2Err != nil {
		return postgres.GraphReplacement{}, store.replaceV2Err
	}
	store.publication = publication
	return postgres.GraphReplacement{Upload: postgres.GraphUpload{ID: 9}, Applied: true, ReplacedID: publication.ExpectedActiveID}, nil
}

type graphDeadlineRecorder struct {
	*httptest.ResponseRecorder
	readDeadlineCleared atomic.Bool
	writeDeadlineSet    atomic.Bool
}

func (recorder *graphDeadlineRecorder) SetReadDeadline(deadline time.Time) error {
	if deadline.IsZero() {
		recorder.readDeadlineCleared.Store(true)
	}
	return nil
}

func (recorder *graphDeadlineRecorder) SetWriteDeadline(time.Time) error {
	recorder.writeDeadlineSet.Store(true)
	return nil
}
