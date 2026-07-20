package httpapi

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/grepnest/grepnest/internal/authn"
	"github.com/grepnest/grepnest/internal/githubapp"
	"github.com/grepnest/grepnest/internal/repository"
	"github.com/grepnest/grepnest/pkg/api"
	"github.com/jackc/pgx/v5"
)

func TestRepositoriesRoutes(t *testing.T) {
	service := repositoryHTTPService()
	mux := http.NewServeMux()
	RegisterRepositories(mux, repositoryAuthenticator(), service, 128)

	response := repositoryRequest(t, mux, http.MethodGet, "/v1/repositories", "", "secret", "")
	if response.Code != http.StatusOK || response.Header().Get("Content-Type") != "application/json" {
		t.Fatalf("list status = %d, content type = %q", response.Code, response.Header().Get("Content-Type"))
	}
	var output struct {
		Repositories []api.RepositorySummary `json:"repositories"`
	}
	decodeRepositoryResponse(t, response, &output)
	if len(output.Repositories) != 1 || output.Repositories[0].GitHubID != 101 || output.Repositories[0].Status != "ready" {
		t.Fatalf("repositories = %#v", output.Repositories)
	}

	for _, path := range []string{"/v1/repositories/101", "/v1/repositories/101/status"} {
		response = repositoryRequest(t, mux, http.MethodGet, path, "", "secret", "")
		var summary api.RepositorySummary
		decodeRepositoryResponse(t, response, &summary)
		if response.Code != http.StatusOK || summary.GitHubID != 101 || summary.IndexedSHA != strings.Repeat("a", 40) {
			t.Fatalf("%s = %d %#v", path, response.Code, summary)
		}
	}
}

func TestRepositoriesRoutesEnforceTransportContract(t *testing.T) {
	mux := http.NewServeMux()
	RegisterRepositories(mux, repositoryAuthenticator(), repositoryHTTPService(), 64)

	tests := []struct {
		name, method, path, body, token, contentType string
		status                                       int
		allow, code                                  string
	}{
		{"list method", http.MethodPost, "/v1/repositories", "", "", "", 405, http.MethodGet, "invalid_request"},
		{"detail method", http.MethodDelete, "/v1/repositories/101", "", "", "", 405, http.MethodGet, "invalid_request"},
		{"status method", http.MethodPost, "/v1/repositories/101/status", "", "", "", 405, http.MethodGet, "invalid_request"},
		{"read method", http.MethodGet, "/v1/files/read", "", "", "", 405, http.MethodPost, "invalid_request"},
		{"missing bearer", http.MethodGet, "/v1/repositories", "", "", "", 401, "", "unauthenticated"},
		{"invalid id", http.MethodGet, "/v1/repositories/not-an-id", "", "secret", "", 400, "", "invalid_request"},
		{"unknown repository", http.MethodGet, "/v1/repositories/999", "", "secret", "", 404, "", "not_found"},
		{"unauthorized repository", http.MethodGet, "/v1/repositories/102/status", "", "secret", "", 404, "", "not_found"},
		{"wrong content type", http.MethodPost, "/v1/files/read", `{}`, "secret", "text/plain", 415, "", "invalid_request"},
		{"unknown field", http.MethodPost, "/v1/files/read", `{"repository_id":101,"path":"main.go","extra":true}`, "secret", "application/json", 400, "", "invalid_request"},
		{"trailing JSON", http.MethodPost, "/v1/files/read", `{"repository_id":101,"path":"main.go"}{}`, "secret", "application/json", 400, "", "invalid_request"},
		{"oversized body", http.MethodPost, "/v1/files/read", `{"repository_id":101,"path":"` + strings.Repeat("a", 80) + `"}`, "secret", "application/json", 413, "", "invalid_request"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			response := repositoryRequest(t, mux, test.method, test.path, test.body, test.token, test.contentType)
			if response.Code != test.status || response.Header().Get("Allow") != test.allow {
				t.Fatalf("status = %d, Allow = %q", response.Code, response.Header().Get("Allow"))
			}
			assertRepositoryError(t, response.Body.String(), test.code)
		})
	}
}

func TestReadFileRouteUsesIndexedContentAndSafeErrors(t *testing.T) {
	mux := http.NewServeMux()
	RegisterRepositories(mux, repositoryAuthenticator(), repositoryHTTPService(), 256)
	response := repositoryRequest(t, mux, http.MethodPost, "/v1/files/read", `{"repository_id":101,"path":"main.go","start_line":2,"end_line":3}`, "secret", "application/json")
	var file api.ReadFileResponse
	decodeRepositoryResponse(t, response, &file)
	if response.Code != http.StatusOK || file.RepositoryID != 101 || file.Content != "two\nthree" || file.IndexedSHA != strings.Repeat("a", 40) {
		t.Fatalf("response = %d %#v", response.Code, file)
	}

	response = repositoryRequest(t, mux, http.MethodPost, "/v1/files/read", `{"repository_id":101,"path":"../secret"}`, "secret", "application/json")
	if response.Code != http.StatusBadRequest {
		t.Fatalf("invalid path status = %d", response.Code)
	}
	assertRepositoryError(t, response.Body.String(), "invalid_request")

	const secret = "upstream-token"
	service := repositoryHTTPService()
	service.GitHub = repositoryContentReader{err: errors.New(secret)}
	mux = http.NewServeMux()
	RegisterRepositories(mux, repositoryAuthenticator(), service, 256)
	response = repositoryRequest(t, mux, http.MethodPost, "/v1/files/read", `{"repository_id":101,"path":"main.go"}`, "secret", "application/json")
	if response.Code != http.StatusServiceUnavailable || strings.Contains(response.Body.String(), secret) {
		t.Fatalf("unsafe response = %d %s", response.Code, response.Body.String())
	}
	assertRepositoryError(t, response.Body.String(), "unavailable")
}

func TestReadFileRejectsInvalidTransportBeforeService(t *testing.T) {
	tests := []struct {
		name, body string
	}{
		{"null", `null`},
		{"empty object", `{}`},
		{"missing repository", `{"path":"main.go"}`},
		{"zero repository", `{"repository_id":0,"path":"main.go"}`},
		{"negative repository", `{"repository_id":-1,"path":"main.go"}`},
		{"missing path", `{"repository_id":101}`},
		{"empty path", `{"repository_id":101,"path":""}`},
		{"null start line", `{"repository_id":101,"path":"main.go","start_line":null}`},
		{"zero start line", `{"repository_id":101,"path":"main.go","start_line":0}`},
		{"negative start line", `{"repository_id":101,"path":"main.go","start_line":-1}`},
		{"null end line", `{"repository_id":101,"path":"main.go","end_line":null}`},
		{"zero end line", `{"repository_id":101,"path":"main.go","end_line":0}`},
		{"negative end line", `{"repository_id":101,"path":"main.go","end_line":-1}`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			service := repositoryHTTPService()
			store := service.Store.(*repositoryHTTPStore)
			mux := http.NewServeMux()
			RegisterRepositories(mux, repositoryAuthenticator(), service, 256)

			response := repositoryRequest(t, mux, http.MethodPost, "/v1/files/read", test.body, "secret", "application/json")
			if response.Code != http.StatusBadRequest || store.calls != 0 {
				t.Fatalf("status = %d, service calls = %d", response.Code, store.calls)
			}
			assertRepositoryError(t, response.Body.String(), "invalid_request")
		})
	}
}

func repositoryHTTPService() *repository.Service {
	sha := strings.Repeat("a", 40)
	return &repository.Service{
		Store: &repositoryHTTPStore{repository: repository.Repository{
			ID: 1, InstallationID: 10, GitHubID: 101, Name: "acme/one", Branch: "main",
			DesiredSHA: sha, IndexedSHA: sha, Status: "ready", SearchNode: "node-a",
		}},
		GitHub: repositoryContentReader{content: githubapp.Content{
			Type: "file", Encoding: "base64", Content: base64.StdEncoding.EncodeToString([]byte("one\ntwo\nthree")), SHA: "blob", Size: 13,
		}},
		MaxLines: 2,
	}
}

func repositoryAuthenticator() authn.Authenticator {
	return authn.NewStatic(map[string]authn.Principal{"secret": {InstallationID: 10, RepositoryIDs: []int64{101}}})
}

type repositoryHTTPStore struct {
	repository repository.Repository
	calls      int
}

func (store *repositoryHTTPStore) AuthorizedRepositories(_ context.Context, _ int64, ids []int64, _ []string) ([]repository.Repository, error) {
	store.calls++
	if len(ids) == 1 && ids[0] == store.repository.GitHubID {
		return []repository.Repository{store.repository}, nil
	}
	return []repository.Repository{}, nil
}

func (store *repositoryHTTPStore) AuthorizedRepository(_ context.Context, _ int64, ids []int64, id int64) (repository.Repository, error) {
	store.calls++
	if id == store.repository.GitHubID && len(ids) == 1 && ids[0] == id {
		return store.repository, nil
	}
	return repository.Repository{}, pgx.ErrNoRows
}

type repositoryContentReader struct {
	content githubapp.Content
	err     error
}

func (reader repositoryContentReader) ReadContents(context.Context, int64, string, string, string, string, int64) (githubapp.Content, error) {
	return reader.content, reader.err
}

func repositoryRequest(t *testing.T, handler http.Handler, method, path, body, token, contentType string) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(method, path, strings.NewReader(body))
	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	if contentType != "" {
		request.Header.Set("Content-Type", contentType)
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}

func decodeRepositoryResponse(t *testing.T, response *httptest.ResponseRecorder, target any) {
	t.Helper()
	if err := json.Unmarshal(response.Body.Bytes(), target); err != nil {
		t.Fatalf("decode response: %v; body=%q", err, response.Body.String())
	}
}

func assertRepositoryError(t *testing.T, body, code string) {
	t.Helper()
	var envelope struct {
		Error struct {
			Code      string `json:"code"`
			Message   string `json:"message"`
			RequestID string `json:"request_id"`
			Retryable bool   `json:"retryable"`
		} `json:"error"`
	}
	if err := json.Unmarshal([]byte(body), &envelope); err != nil || envelope.Error.Code != code || envelope.Error.Message == "" || envelope.Error.RequestID == "" {
		t.Fatalf("error = %q", body)
	}
}
