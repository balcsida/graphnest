package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
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
	if err := json.Unmarshal(response.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
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
		{"forbidden", graphingest.ErrForbidden, http.StatusForbidden, "forbidden", "administrator access required", false},
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
	RegisterGraphIngestion(mux, authn.NewStatic(map[string]authn.Principal{
		"user":  {InstallationID: 10, RepositoryIDs: []int64{101}},
		"admin": {InstallationID: 10, RepositoryIDs: []int64{101}, Administrator: true},
	}), &graphingest.Service{Store: store, Limits: graphartifact.Limits{MaxNodes: 2, MaxEdges: 1}}, maxUpload, maxResponse)
	return mux
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
