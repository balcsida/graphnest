package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/grepnest/grepnest/internal/authn"
	"github.com/grepnest/grepnest/internal/githubapp"
	"github.com/grepnest/grepnest/internal/repository"
	"github.com/grepnest/grepnest/internal/scipgraph"
	"github.com/grepnest/grepnest/pkg/api"
	"github.com/jackc/pgx/v5"
	"github.com/scip-code/scip/bindings/go/scip"
	"google.golang.org/protobuf/proto"
)

const scipTestSHA = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

func TestSCIPUploadContract(t *testing.T) {
	store := &scipStoreStub{repository: scipRepository()}
	handler := scipHandler(store, 8)

	tests := []struct {
		name, method, target, token, contentType string
		body                                     []byte
		want                                     int
	}{
		{"administrator required", http.MethodPost, "/v1/scip/uploads?repository_id=101&commit=" + scipTestSHA, "user", "application/vnd.scip+protobuf", []byte("index"), http.StatusForbidden},
		{"exact method", http.MethodPut, "/v1/scip/uploads?repository_id=101&commit=" + scipTestSHA, "admin", "application/vnd.scip+protobuf", []byte("index"), http.StatusMethodNotAllowed},
		{"exact content type", http.MethodPost, "/v1/scip/uploads?repository_id=101&commit=" + scipTestSHA, "admin", "application/octet-stream", []byte("index"), http.StatusUnsupportedMediaType},
		{"missing repository", http.MethodPost, "/v1/scip/uploads?commit=" + scipTestSHA, "admin", "application/vnd.scip+protobuf", []byte("index"), http.StatusBadRequest},
		{"duplicate repository", http.MethodPost, "/v1/scip/uploads?repository_id=101&repository_id=101&commit=" + scipTestSHA, "admin", "application/vnd.scip+protobuf", []byte("index"), http.StatusBadRequest},
		{"unknown query", http.MethodPost, "/v1/scip/uploads?repository_id=101&commit=" + scipTestSHA + "&extra=1", "admin", "application/vnd.scip+protobuf", []byte("index"), http.StatusBadRequest},
		{"uppercase commit", http.MethodPost, "/v1/scip/uploads?repository_id=101&commit=" + strings.ToUpper(scipTestSHA), "admin", "application/vnd.scip+protobuf", []byte("index"), http.StatusBadRequest},
		{"bounded upload", http.MethodPost, "/v1/scip/uploads?repository_id=101&commit=" + scipTestSHA, "admin", "application/vnd.scip+protobuf", []byte("123456789"), http.StatusRequestEntityTooLarge},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			response := scipRequest(handler, test.method, test.target, test.body, test.token, test.contentType)
			if response.Code != test.want {
				t.Fatalf("status = %d, want %d, body = %q", response.Code, test.want, response.Body.String())
			}
		})
	}
}

func TestSCIPUploadAcceptsValidIndex(t *testing.T) {
	store := &scipStoreStub{repository: scipRepository()}
	data, err := proto.Marshal(&scip.Index{Metadata: &scip.Metadata{ProjectRoot: "file:///workspace", ToolInfo: &scip.ToolInfo{Name: "scip-go", Version: "1"}}})
	if err != nil {
		t.Fatal(err)
	}
	response := scipRequest(scipHandler(store, 1<<20), http.MethodPost, "/v1/scip/uploads?repository_id=101&commit="+scipTestSHA, data, "admin", "application/vnd.scip+protobuf")
	if response.Code != http.StatusNoContent || store.replacedCommit != scipTestSHA {
		t.Fatalf("status = %d, commit = %q, body = %q", response.Code, store.replacedCommit, response.Body.String())
	}
}

func TestSCIPJSONRoutesAreStrictAndAuthenticated(t *testing.T) {
	store := &scipStoreStub{repository: scipRepository(), locations: []scipgraph.Location{{
		RepositoryID: 101, RepositoryName: "acme/one", Commit: scipTestSHA, Path: "target.go", Symbol: "sym", StartLine: 4,
	}}}
	handler := scipHandler(store, 64)

	navigation := []byte(`{"repository_id":101,"path":"main.go","line":1,"character":0,"operation":"definitions"}`)
	response := scipRequest(handler, http.MethodPost, "/v1/scip/navigation", navigation, "user", "application/json")
	if response.Code != http.StatusOK {
		t.Fatalf("navigation status = %d, body = %q", response.Code, response.Body.String())
	}
	var got api.SCIPNavigationResponse
	if err := json.Unmarshal(response.Body.Bytes(), &got); err != nil || len(got.Locations) != 1 || got.Locations[0].Path != "target.go" {
		t.Fatalf("navigation = %#v, err = %v", got, err)
	}

	for _, body := range [][]byte{
		[]byte(`{"repository_id":101,"path":"main.go","line":1,"character":0,"operation":"definitions","extra":true}`),
		append(navigation, []byte(` {}`)...),
	} {
		response = scipRequest(handler, http.MethodPost, "/v1/scip/navigation", body, "user", "application/json")
		if response.Code != http.StatusBadRequest {
			t.Fatalf("strict JSON status = %d, body = %q", response.Code, response.Body.String())
		}
	}

	dependencies := []byte(`{"repository_id":101,"provides":[],"depends_on":[]}`)
	response = scipRequest(handler, http.MethodPut, "/v1/scip/dependencies", dependencies, "user", "application/json")
	if response.Code != http.StatusForbidden {
		t.Fatalf("dependencies status = %d", response.Code)
	}
	response = scipRequest(handler, http.MethodPost, "/v1/scip/dependencies/github", []byte(`{"repository_id":101}`), "user", "application/json")
	if response.Code != http.StatusForbidden {
		t.Fatalf("GitHub dependencies status = %d", response.Code)
	}
}

func TestSCIPNavigationResponseIsBounded(t *testing.T) {
	store := &scipStoreStub{repository: scipRepository(), locations: []scipgraph.Location{{
		RepositoryID: 101, RepositoryName: "acme/one", Commit: scipTestSHA, Path: "target.go",
	}}}
	mux := http.NewServeMux()
	RegisterSCIP(mux, authn.NewStatic(map[string]authn.Principal{"user": {
		InstallationID: 10, RepositoryIDs: []int64{101},
	}}), &scipgraph.Service{Store: store}, 1024, 1024, 1)
	response := scipRequest(mux, http.MethodPost, "/v1/scip/navigation", []byte(`{"repository_id":101,"path":"main.go","line":1,"character":0,"operation":"definitions"}`), "user", "application/json")
	if response.Code != http.StatusInternalServerError || response.Body.Len() != 0 {
		t.Fatalf("status = %d, body = %q", response.Code, response.Body.String())
	}
}

func TestSCIPErrorClassification(t *testing.T) {
	for _, test := range []struct {
		name string
		err  error
		want int
	}{
		{"forbidden", scipgraph.ErrForbidden, http.StatusForbidden},
		{"invalid request", scipgraph.ErrInvalidRequest, http.StatusBadRequest},
		{"invalid index", scipgraph.ErrInvalidIndex, http.StatusBadRequest},
		{"not indexed", scipgraph.ErrNotIndexed, http.StatusConflict},
		{"missing repository", pgx.ErrNoRows, http.StatusNotFound},
		{"backend", errors.New("secret backend"), http.StatusServiceUnavailable},
	} {
		t.Run(test.name, func(t *testing.T) {
			response := httptest.NewRecorder()
			writeSCIPError(response, test.err)
			if response.Code != test.want || strings.Contains(response.Body.String(), "secret") {
				t.Fatalf("status = %d, body = %q", response.Code, response.Body.String())
			}
		})
	}
}

func scipHandler(store *scipStoreStub, maxUpload int64) http.Handler {
	mux := http.NewServeMux()
	RegisterSCIP(mux, authn.NewStatic(map[string]authn.Principal{
		"user":  {InstallationID: 10, RepositoryIDs: []int64{101}},
		"admin": {InstallationID: 10, RepositoryIDs: []int64{101}, Administrator: true},
	}), &scipgraph.Service{Store: store, GitHub: scipDependencyReader{}}, 1024, maxUpload, 1024)
	return mux
}

func scipRequest(handler http.Handler, method, target string, body []byte, token, contentType string) *httptest.ResponseRecorder {
	response := httptest.NewRecorder()
	request := httptest.NewRequest(method, target, bytes.NewReader(body))
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("Content-Type", contentType)
	handler.ServeHTTP(response, request)
	return response
}

func scipRepository() repository.Repository {
	return repository.Repository{ID: 1, GitHubID: 101, InstallationID: 10, Name: "acme/one", IndexedSHA: scipTestSHA}
}

type scipStoreStub struct {
	repository     repository.Repository
	locations      []scipgraph.Location
	replacedCommit string
}

func (store *scipStoreStub) AuthorizedRepository(_ context.Context, _ int64, ids []int64, id int64) (repository.Repository, error) {
	if id == store.repository.GitHubID && len(ids) == 1 && ids[0] == id {
		return store.repository, nil
	}
	return repository.Repository{}, pgx.ErrNoRows
}
func (store *scipStoreStub) ReplaceSCIP(_ context.Context, _ int64, commit string, _ scipgraph.Upload) error {
	store.replacedCommit = commit
	return nil
}
func (*scipStoreStub) OccurrenceAt(context.Context, int64, string, string, int, int) (scipgraph.StoredOccurrence, error) {
	return scipgraph.StoredOccurrence{RepositoryID: 101}, nil
}
func (store *scipStoreStub) Locations(context.Context, authn.Principal, scipgraph.StoredOccurrence, string, int) ([]scipgraph.Location, bool, error) {
	return store.locations, false, nil
}
func (*scipStoreStub) ReplacePackages(context.Context, int64, string, []scipgraph.PackageMapping) error {
	return nil
}

type scipDependencyReader struct{}

func (scipDependencyReader) DependencySBOM(context.Context, int64, string, string) (githubapp.SBOM, bool, error) {
	return githubapp.SBOM{}, false, nil
}
