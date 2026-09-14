package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/balcsida/graphnest/internal/authn"
	graphv2 "github.com/balcsida/graphnest/internal/graphartifact/v2"
	"github.com/balcsida/graphnest/internal/graphprotocol"
	"github.com/balcsida/graphnest/internal/graphquery"
	"github.com/balcsida/graphnest/internal/graphservice"
	"github.com/balcsida/graphnest/internal/repository"
	"github.com/balcsida/graphnest/pkg/api"
)

func TestGraphQueryContracts(t *testing.T) {
	handler := graphQueryHandler(&graphQueryStore{repositories: []repository.Repository{graphQueryRepository("acme/one")}}, graphQueryEngine{}, 256, 16<<10)
	routes := []struct {
		path string
		body any
	}{
		{"/v1/graph/context", api.GraphContextRequest{GraphSymbolSelector: api.GraphSymbolSelector{UID: "symbol:a"}}},
		{"/v1/graph/impact", api.GraphImpactRequest{TargetUID: "symbol:a", Direction: "downstream"}},
		{"/v1/graph/trace", api.GraphTraceRequest{SourceUID: "symbol:a", TargetUID: "symbol:b"}},
		{"/v1/graph/discover", api.GraphDiscoverRequest{Query: "symbol"}},
		{"/v1/graph/explore", api.GraphExploreRequest{Query: "symbol"}},
		{"/v1/graph/files", api.GraphFilesRequest{}},
		{"/v1/graph/capabilities", api.GraphCapabilitiesRequest{}},
	}
	for _, route := range routes {
		t.Run(route.path, func(t *testing.T) {
			body, err := json.Marshal(route.body)
			if err != nil {
				t.Fatal(err)
			}
			for _, test := range []struct {
				name, method, token, contentType string
				body                             []byte
				want                             int
			}{
				{"valid", http.MethodPost, "admin", "application/json", body, http.StatusOK},
				{"method", http.MethodPut, "admin", "application/json", body, http.StatusMethodNotAllowed},
				{"authentication", http.MethodPost, "", "application/json", body, http.StatusUnauthorized},
				{"content type", http.MethodPost, "admin", "application/json; charset=utf-8", body, http.StatusUnsupportedMediaType},
				{"unknown field", http.MethodPost, "admin", "application/json", append(append([]byte{}, body[:len(body)-1]...), []byte(`,"extra":true}`)...), http.StatusBadRequest},
				{"multiple values", http.MethodPost, "admin", "application/json", append(append([]byte{}, body...), []byte(` {}`)...), http.StatusBadRequest},
				{"body cap", http.MethodPost, "admin", "application/json", bytes.Repeat([]byte(" "), 257), http.StatusRequestEntityTooLarge},
			} {
				t.Run(test.name, func(t *testing.T) {
					response := graphQueryRequest(handler, test.method, route.path, test.body, test.token, test.contentType)
					if response.Code != test.want {
						t.Fatalf("status=%d want=%d body=%q", response.Code, test.want, response.Body.String())
					}
					if test.want == http.StatusMethodNotAllowed && response.Header().Get("Allow") != http.MethodPost {
						t.Fatalf("Allow=%q", response.Header().Get("Allow"))
					}
				})
			}
		})
	}
}

func TestGraphQueriesReportAmbiguityAndBranchRejection(t *testing.T) {
	store := &graphQueryStore{repositories: []repository.Repository{graphQueryRepository("acme/one"), graphQueryRepository("acme/two")}}
	handler := graphQueryHandler(store, graphQueryEngine{}, 1024, 1024)
	for _, test := range []struct {
		name, body, code string
	}{
		{"ambiguous", `{"target_uid":"symbol:a","direction":"downstream"}`, "ambiguous"},
		{"branch", `{"repo":"acme/one","branch":"other","target_uid":"symbol:a","direction":"downstream"}`, "branch_not_indexed"},
	} {
		t.Run(test.name, func(t *testing.T) {
			response := graphQueryRequest(handler, http.MethodPost, "/v1/graph/impact", []byte(test.body), "user", "application/json")
			assertGraphQueryError(t, response, http.StatusConflict, test.code, "secret")
		})
	}
}

func TestGraphQueryResponseIsBounded(t *testing.T) {
	handler := graphQueryHandler(&graphQueryStore{repositories: []repository.Repository{graphQueryRepository("acme/one")}}, graphQueryEngine{}, 1024, 1)
	response := graphQueryRequest(handler, http.MethodPost, "/v1/graph/trace", []byte(`{"source_uid":"symbol:a","target_uid":"symbol:b"}`), "user", "application/json")
	if response.Code != http.StatusInternalServerError || response.Body.Len() != 0 {
		t.Fatalf("status=%d body=%q", response.Code, response.Body.String())
	}
}

func TestGraphQueryRejectsCredentialRevokedDuringRequest(t *testing.T) {
	authenticator := &graphQueryAuthenticator{}
	mux := http.NewServeMux()
	RegisterGraphQueries(mux, authenticator, &graphservice.Service{Store: &graphQueryStore{repositories: []repository.Repository{graphQueryRepository("acme/one")}}, Backend: graphQueryEngine{}}, 1024, 16<<10)
	response := graphQueryRequest(mux, http.MethodPost, "/v1/graph/discover", []byte(`{"query":"symbol"}`), "fresh", "application/json")
	assertGraphQueryError(t, response, http.StatusUnauthorized, "unauthenticated", "fresh")
}

func TestNewGraphTransportFailures(t *testing.T) {
	principal := authn.Principal{InstallationID: 10, RepositoryIDs: []int64{101}}
	repositoryOne := graphQueryRepository("acme/one")
	repositoryTwo := graphQueryRepository("acme/two")
	repositoryTwo.ID, repositoryTwo.GitHubID = 2, 102
	for _, test := range []struct {
		name                  string
		path                  string
		body                  string
		initial               graphQueryAuthResult
		fresh                 graphQueryAuthResult
		repositories          []repository.Repository
		discoverErr           error
		generationErr         error
		cancel                bool
		sourceErr             error
		wantStatus            int
		wantCode, sourceState string
	}{
		{"invalid starting credential", "/v1/graph/discover", `{"query":"symbol"}`, graphQueryAuthResult{err: authn.ErrUnauthenticated}, graphQueryAuthResult{}, []repository.Repository{repositoryOne}, nil, nil, false, nil, http.StatusUnauthorized, "unauthenticated", ""},
		{"expired starting credential", "/v1/graph/discover", `{"query":"symbol"}`, graphQueryAuthResult{err: authn.ErrUnauthenticated}, graphQueryAuthResult{}, []repository.Repository{repositoryOne}, nil, nil, false, nil, http.StatusUnauthorized, "unauthenticated", ""},
		{"unauthorized explicit repository", "/v1/graph/discover", `{"repo":102,"query":"symbol"}`, graphQueryAuthResult{principal: principal}, graphQueryAuthResult{principal: principal}, []repository.Repository{repositoryOne, repositoryTwo}, nil, nil, false, nil, http.StatusNotFound, "not_found", ""},
		{"fresh grant loss", "/v1/graph/discover", `{"repo":101,"query":"symbol"}`, graphQueryAuthResult{principal: principal}, graphQueryAuthResult{principal: authn.Principal{InstallationID: 10, RepositoryIDs: []int64{102}}}, []repository.Repository{repositoryOne}, nil, nil, false, nil, http.StatusNotFound, "not_found", ""},
		{"fresh revocation", "/v1/graph/discover", `{"query":"symbol"}`, graphQueryAuthResult{principal: principal}, graphQueryAuthResult{err: authn.ErrUnauthenticated}, []repository.Repository{repositoryOne}, nil, nil, false, nil, http.StatusUnauthorized, "unauthenticated", ""},
		{"generation change", "/v1/graph/discover", `{"query":"symbol"}`, graphQueryAuthResult{principal: principal}, graphQueryAuthResult{principal: principal}, []repository.Repository{repositoryOne}, nil, graphquery.ErrGenerationChanged, false, nil, http.StatusConflict, "graph_not_ready", ""},
		{"deadline", "/v1/graph/discover", `{"query":"symbol"}`, graphQueryAuthResult{principal: principal}, graphQueryAuthResult{principal: principal}, []repository.Repository{repositoryOne}, context.DeadlineExceeded, nil, false, nil, http.StatusGatewayTimeout, "timeout", ""},
		{"cancellation", "/v1/graph/discover", `{"query":"symbol"}`, graphQueryAuthResult{principal: principal}, graphQueryAuthResult{principal: principal}, []repository.Repository{repositoryOne}, nil, nil, true, nil, http.StatusServiceUnavailable, "unavailable", ""},
		{"source read failure", "/v1/graph/explore", `{"query":"symbol","files":["a.go"]}`, graphQueryAuthResult{principal: principal}, graphQueryAuthResult{principal: principal}, []repository.Repository{repositoryOne}, nil, nil, false, errors.New("private source failure"), http.StatusOK, "", "unreadable"},
	} {
		t.Run(test.name, func(t *testing.T) {
			authenticator := &graphQuerySequenceAuthenticator{results: []graphQueryAuthResult{test.initial, test.fresh}}
			backend := graphQueryFailureEngine{discoverErr: test.discoverErr, generationErr: test.generationErr}
			service := &graphservice.Service{Store: &graphQueryStore{repositories: test.repositories}, Backend: backend}
			if test.sourceErr != nil {
				service.Files = graphQueryReader(func(context.Context, authn.Principal, api.ReadFileRequest, string) (api.ReadFileResponse, error) {
					return api.ReadFileResponse{}, test.sourceErr
				})
			}
			mux := http.NewServeMux()
			RegisterGraphQueries(mux, authenticator, service, 1024, 16<<10)
			request := httptest.NewRequest(http.MethodPost, test.path, strings.NewReader(test.body))
			request.Header.Set("Authorization", "Bearer token")
			request.Header.Set("Content-Type", "application/json")
			if test.cancel {
				ctx, cancel := context.WithCancel(request.Context())
				cancel()
				request = request.WithContext(ctx)
			}
			response := httptest.NewRecorder()
			mux.ServeHTTP(response, request)
			if response.Code != test.wantStatus {
				t.Fatalf("status=%d want=%d body=%q", response.Code, test.wantStatus, response.Body.String())
			}
			if test.wantCode != "" {
				assertGraphQueryError(t, response, test.wantStatus, test.wantCode, "private source failure")
				return
			}
			var result graphservice.ExploreResponse
			if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
				t.Fatal(err)
			}
			if result.Complete || len(result.Files) != 1 || result.Files[0].Status != test.sourceState || strings.Contains(response.Body.String(), "private source failure") {
				t.Fatalf("source result=%#v body=%q", result, response.Body.String())
			}
		})
	}
}

func TestGraphQueryErrorClassificationIsSafe(t *testing.T) {
	for _, test := range []struct {
		err       error
		status    int
		code      string
		retryable bool
	}{
		{graphservice.ErrInvalidRequest, http.StatusBadRequest, "invalid_request", false},
		{graphservice.ErrRepositoryNotFound, http.StatusNotFound, "not_found", false},
		{graphservice.ErrRepositoryRequired, http.StatusConflict, "ambiguous", false},
		{graphservice.ErrBranchNotIndexed, http.StatusConflict, "branch_not_indexed", false},
		{graphservice.ErrGraphNotReady, http.StatusConflict, "graph_not_ready", true},
		{graphquery.ErrGenerationChanged, http.StatusConflict, "graph_not_ready", true},
		{graphquery.ErrDiscoveryUnavailable, http.StatusConflict, "graph_not_ready", true},
		{graphquery.ErrInvalidRequest, http.StatusBadRequest, "invalid_request", false},
		{graphquery.ErrQuerySize, http.StatusRequestEntityTooLarge, "response_too_large", false},
		{authn.ErrUnauthenticated, http.StatusUnauthorized, "unauthenticated", false},
		{context.DeadlineExceeded, http.StatusGatewayTimeout, "timeout", true},
		{errors.New("PostgreSQL password=secret"), http.StatusServiceUnavailable, "unavailable", true},
	} {
		t.Run(test.code, func(t *testing.T) {
			response := httptest.NewRecorder()
			writeGraphQueryError(response, test.err)
			assertGraphQueryError(t, response, test.status, test.code, "secret")
			var value struct {
				Error struct {
					Retryable bool `json:"retryable"`
				} `json:"error"`
			}
			if err := json.Unmarshal(response.Body.Bytes(), &value); err != nil || value.Error.Retryable != test.retryable {
				t.Fatalf("error=%#v decode=%v want retryable=%t", value, err, test.retryable)
			}
		})
	}
}

func graphQueryHandler(store *graphQueryStore, engine graphQueryEngine, maxRequest, maxResponse int64) http.Handler {
	mux := http.NewServeMux()
	RegisterGraphQueries(mux, authn.NewStatic(map[string]authn.Principal{
		"user":  {InstallationID: 10, RepositoryIDs: []int64{101, 102}},
		"admin": {InstallationID: 10, RepositoryIDs: []int64{101, 102}, Administrator: true},
	}), &graphservice.Service{Store: store, Backend: engine}, maxRequest, maxResponse)
	return mux
}

func graphQueryRequest(handler http.Handler, method, path string, body []byte, token, contentType string) *httptest.ResponseRecorder {
	response := httptest.NewRecorder()
	request := httptest.NewRequest(method, path, bytes.NewReader(body))
	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	request.Header.Set("Content-Type", contentType)
	handler.ServeHTTP(response, request)
	return response
}

func assertGraphQueryError(t *testing.T, response *httptest.ResponseRecorder, status int, code, secret string) {
	t.Helper()
	if response.Code != status {
		t.Fatalf("status=%d want=%d body=%q", response.Code, status, response.Body.String())
	}
	if strings.Contains(response.Body.String(), secret) {
		t.Fatalf("leaked %q in %q", secret, response.Body.String())
	}
	var value struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &value); err != nil || value.Error.Code != code {
		t.Fatalf("error=%#v decode=%v want=%q", value, err, code)
	}
}

type graphQueryStore struct{ repositories []repository.Repository }

type graphQueryAuthResult struct {
	principal authn.Principal
	err       error
}

type graphQuerySequenceAuthenticator struct {
	results []graphQueryAuthResult
	calls   int
}

func (authenticator *graphQuerySequenceAuthenticator) Authenticate(context.Context, string) (authn.Principal, error) {
	index := min(authenticator.calls, len(authenticator.results)-1)
	authenticator.calls++
	return authenticator.results[index].principal, authenticator.results[index].err
}

type graphQueryFailureEngine struct {
	graphQueryEngine
	discoverErr, generationErr error
}

func (engine graphQueryFailureEngine) Discover(ctx context.Context, request graphprotocol.DiscoverRequest) (graphprotocol.DiscoverResponse, error) {
	if err := ctx.Err(); err != nil {
		return graphprotocol.DiscoverResponse{}, err
	}
	if engine.discoverErr != nil {
		return graphprotocol.DiscoverResponse{}, engine.discoverErr
	}
	return engine.graphQueryEngine.Discover(ctx, request)
}

func (engine graphQueryFailureEngine) ValidateGenerations(context.Context, graphprotocol.Scope, []graphprotocol.Generation) error {
	return engine.generationErr
}

type graphQueryReader func(context.Context, authn.Principal, api.ReadFileRequest, string) (api.ReadFileResponse, error)

func (reader graphQueryReader) ReadFileAt(ctx context.Context, principal authn.Principal, request api.ReadFileRequest, sha string) (api.ReadFileResponse, error) {
	return reader(ctx, principal, request, sha)
}

type graphQueryAuthenticator struct{ calls int }

func (authenticator *graphQueryAuthenticator) Authenticate(context.Context, string) (authn.Principal, error) {
	authenticator.calls++
	if authenticator.calls > 1 {
		return authn.Principal{}, authn.ErrUnauthenticated
	}
	return authn.Principal{InstallationID: 10, RepositoryIDs: []int64{101}}, nil
}

func (store *graphQueryStore) GraphRepositories(_ context.Context, principal authn.Principal) ([]repository.Repository, error) {
	return slices.DeleteFunc(append([]repository.Repository(nil), store.repositories...), func(value repository.Repository) bool {
		return !principal.Administrator && !slices.Contains(principal.RepositoryIDs, value.GitHubID)
	}), nil
}

type graphQueryEngine struct{}

func (graphQueryEngine) Context(context.Context, graphprotocol.ContextRequest) (graphprotocol.ContextResponse, error) {
	return graphprotocol.ContextResponse{Status: graphprotocol.StatusNotFound, Commits: graphQueryCommits()}, nil
}
func (graphQueryEngine) Impact(context.Context, graphprotocol.ImpactRequest) (graphprotocol.ImpactResponse, error) {
	return graphprotocol.ImpactResponse{Status: graphprotocol.StatusNotFound, ByDepth: map[int][]graphprotocol.Symbol{}, Commits: graphQueryCommits()}, nil
}
func (graphQueryEngine) Trace(context.Context, graphprotocol.TraceRequest) (graphprotocol.TraceResponse, error) {
	return graphprotocol.TraceResponse{Status: graphprotocol.StatusNoPath, Commits: graphQueryCommits()}, nil
}
func (graphQueryEngine) Discover(_ context.Context, request graphprotocol.DiscoverRequest) (graphprotocol.DiscoverResponse, error) {
	return graphprotocol.DiscoverResponse{Status: "candidates", Matches: []graphprotocol.DiscoveryMatch{{Entity: graphQueryEntity()}}, Generations: []graphprotocol.Generation{graphQueryGeneration(request.Scope)}}, nil
}
func (graphQueryEngine) Entities(_ context.Context, request graphprotocol.EntitiesRequest) (graphprotocol.EntitiesResponse, error) {
	return graphprotocol.EntitiesResponse{Entities: []graphprotocol.Entity{graphQueryEntity()}, Generations: []graphprotocol.Generation{graphQueryGeneration(request.Scope)}}, nil
}
func (graphQueryEngine) Traverse(_ context.Context, request graphprotocol.TraverseRequest) (graphprotocol.TraverseResponse, error) {
	return graphprotocol.TraverseResponse{Status: "ok", Entities: []graphprotocol.Entity{graphQueryEntity()}, Generations: []graphprotocol.Generation{graphQueryGeneration(request.Scope)}}, nil
}
func (graphQueryEngine) IndexedFiles(_ context.Context, request graphprotocol.FilesRequest) (graphprotocol.FilesResponse, error) {
	total := int64(1)
	return graphprotocol.FilesResponse{Files: []graphprotocol.IndexedFile{{RepositoryID: 101, Fact: &graphv2.File{Path: "a.go"}}}, Generations: []graphprotocol.Generation{graphQueryGeneration(request.Scope)}, TotalFiles: &total}, nil
}
func (graphQueryEngine) ValidateGenerations(context.Context, graphprotocol.Scope, []graphprotocol.Generation) error {
	return nil
}
func graphQueryEntity() graphprotocol.Entity {
	return graphprotocol.Entity{RepositoryID: 101, ID: "symbol:a", Fact: &graphv2.Node{
		Occurrence: "symbol:a", Name: "a", Path: stringPointer("a.go"),
		Location: &graphv2.Location{Path: stringPointer("a.go"), Start: &graphv2.Position{Line: int32Pointer(0)}, End: &graphv2.Position{Line: int32Pointer(0), Character: int32Pointer(1)}},
	}}
}
func graphQueryGeneration(scope graphprotocol.Scope) graphprotocol.Generation {
	return graphprotocol.Generation{RepositoryID: 101, UploadID: 1, Commit: scope.Repositories[0].Commit}
}
func stringPointer(value string) *string { return &value }
func int32Pointer(value int32) *int32    { return &value }
func graphQueryRepository(name string) repository.Repository {
	return repository.Repository{ID: 1, GitHubID: 101, Name: name, Branch: "main", IndexedSHA: graphTestSHA}
}

func graphQueryCommits() map[string]string { return map[string]string{"acme/one": graphTestSHA} }
