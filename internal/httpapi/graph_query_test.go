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
	"github.com/grepnest/grepnest/internal/graphprotocol"
	"github.com/grepnest/grepnest/internal/graphservice"
	"github.com/grepnest/grepnest/internal/repository"
	"github.com/grepnest/grepnest/pkg/api"
)

func TestGraphQueryContracts(t *testing.T) {
	handler := graphQueryHandler(&graphQueryStore{repositories: []repository.Repository{graphQueryRepository("acme/one")}}, graphQueryEngine{}, 256, 1024)
	routes := []struct {
		path string
		body any
	}{
		{"/v1/graph/context", api.GraphContextRequest{GraphSymbolSelector: api.GraphSymbolSelector{UID: "symbol:a"}}},
		{"/v1/graph/impact", api.GraphImpactRequest{TargetUID: "symbol:a", Direction: "downstream"}},
		{"/v1/graph/trace", api.GraphTraceRequest{SourceUID: "symbol:a", TargetUID: "symbol:b"}},
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

func (store *graphQueryStore) GraphRepositories(context.Context, authn.Principal) ([]repository.Repository, error) {
	return append([]repository.Repository(nil), store.repositories...), nil
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
func graphQueryRepository(name string) repository.Repository {
	return repository.Repository{ID: 1, GitHubID: 101, Name: name, Branch: "main", IndexedSHA: graphTestSHA}
}

func graphQueryCommits() map[string]string { return map[string]string{"acme/one": graphTestSHA} }
