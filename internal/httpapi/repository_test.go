package httpapi

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/balcsida/graphnest/internal/authn"
	"github.com/balcsida/graphnest/internal/githubapp"
	"github.com/balcsida/graphnest/internal/repository"
	"github.com/balcsida/graphnest/pkg/api"
	"github.com/jackc/pgx/v5"
)

func TestRepositoriesRoutes(t *testing.T) {
	service := repositoryHTTPService()
	mux := http.NewServeMux()
	RegisterRepositories(mux, repositoryAuthenticator(), service, 128, 100, 256<<10)

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

func TestRepositoryRoutesAllowAdministratorsAcrossInstallations(t *testing.T) {
	service := repositoryHTTPService()
	store := service.Store.(*repositoryHTTPStore)
	second := repository.Repository{ID: 2, InstallationID: 20, GitHubID: 202, Name: "other/two", Branch: "main", DesiredSHA: strings.Repeat("b", 40), IndexedSHA: strings.Repeat("b", 40), Status: "ready", SearchNode: "node-a"}
	store.globalRepositories = []repository.Repository{store.repository, second}
	store.globalRepositoriesByID = map[int64]repository.Repository{101: store.repository, 202: second}
	mux := http.NewServeMux()
	RegisterRepositories(mux, requestAuthenticator(authn.NewStatic(map[string]authn.Principal{"admin": {Administrator: true, InstallationID: 10, RepositoryIDs: []int64{101}}})), service, 128, 100, 256<<10)

	response := repositoryRequest(t, mux, http.MethodGet, "/v1/repositories", "", "admin", "")
	var list struct {
		Repositories []api.RepositorySummary `json:"repositories"`
	}
	decodeRepositoryResponse(t, response, &list)
	if response.Code != http.StatusOK || len(list.Repositories) != 2 || list.Repositories[1].GitHubID != 202 {
		t.Fatalf("list status=%d repositories=%#v", response.Code, list.Repositories)
	}
	for _, target := range []struct{ method, path, body, contentType string }{
		{http.MethodGet, "/v1/repositories/202", "", ""},
		{http.MethodGet, "/v1/repositories/202/status", "", ""},
		{http.MethodPost, "/v1/files/read", `{"repository_id":202,"path":"main.go"}`, "application/json"},
	} {
		response = repositoryRequest(t, mux, target.method, target.path, target.body, "admin", target.contentType)
		if response.Code != http.StatusOK {
			t.Fatalf("%s status=%d body=%q", target.path, response.Code, response.Body.String())
		}
	}
}

func TestRepositoryListBoundsWireResponse(t *testing.T) {
	items := []repository.Repository{
		{ID: 1, InstallationID: 10, GitHubID: 101, Name: strings.Repeat("a", 80), Branch: "main", DesiredSHA: strings.Repeat("a", 40), IndexedSHA: strings.Repeat("a", 40), Status: "ready", SearchNode: "node-a"},
		{ID: 2, InstallationID: 10, GitHubID: 102, Name: strings.Repeat("b", 80), Branch: "main", DesiredSHA: strings.Repeat("b", 40), IndexedSHA: strings.Repeat("b", 40), Status: "ready", SearchNode: "node-a"},
		{ID: 3, InstallationID: 10, GitHubID: 103, Name: strings.Repeat("c", 80), Branch: "main", DesiredSHA: strings.Repeat("c", 40), IndexedSHA: strings.Repeat("c", 40), Status: "ready", SearchNode: "node-a"},
	}
	service := repositoryHTTPService()
	service.Store = &repositoryHTTPStore{repositories: items}
	first := api.RepositorySummary{ID: 101, GitHubID: 101, Name: items[0].Name, Branch: "main", DesiredSHA: items[0].DesiredSHA, IndexedSHA: items[0].IndexedSHA, Status: "ready", SearchNode: "node-a", SCIPStatus: api.SCIPStatusUnknown}
	// The budget covers the whole truncated envelope, cursor included.
	budgetBody, err := json.Marshal(struct {
		Repositories []api.RepositorySummary `json:"repositories"`
		Truncated    bool                    `json:"truncated"`
		NextCursor   string                  `json:"next_cursor"`
	}{Repositories: []api.RepositorySummary{first}, Truncated: true, NextCursor: EncodeRepositoryCursor(first.Name)})
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	RegisterRepositories(mux, requestAuthenticator(authn.NewStatic(map[string]authn.Principal{"secret": {InstallationID: 10, RepositoryIDs: []int64{101, 102, 103}}})), service, 128, 2, int64(len(budgetBody)+1))

	response := repositoryRequest(t, mux, http.MethodGet, "/v1/repositories", "", "secret", "")
	var output struct {
		Repositories []api.RepositorySummary `json:"repositories"`
		Truncated    bool                    `json:"truncated"`
	}
	decodeRepositoryResponse(t, response, &output)
	if response.Code != http.StatusOK || len(response.Body.Bytes()) > len(budgetBody)+1 || len(output.Repositories) != 1 || !output.Truncated || !strings.Contains(response.Body.String(), `"next_cursor":"`) {
		t.Fatalf("status=%d bytes=%d repositories=%d truncated=%v body=%s", response.Code, len(response.Body.Bytes()), len(output.Repositories), output.Truncated, response.Body.String())
	}
	// One byte less than the cursor-inclusive envelope must not squeeze the cursor out.
	mux = http.NewServeMux()
	RegisterRepositories(mux, requestAuthenticator(authn.NewStatic(map[string]authn.Principal{"secret": {InstallationID: 10, RepositoryIDs: []int64{101, 102, 103}}})), service, 128, 2, int64(len(budgetBody)))
	response = repositoryRequest(t, mux, http.MethodGet, "/v1/repositories", "", "secret", "")
	decodeRepositoryResponse(t, response, &output)
	if response.Code != http.StatusOK || len(output.Repositories) != 0 || !output.Truncated || strings.Contains(response.Body.String(), "next_cursor") {
		t.Fatalf("under-budget status=%d body=%s", response.Code, response.Body.String())
	}

	mux = http.NewServeMux()
	RegisterRepositories(mux, requestAuthenticator(authn.NewStatic(map[string]authn.Principal{"secret": {InstallationID: 10, RepositoryIDs: []int64{101, 102, 103}}})), service, 128, 1, 256<<10)
	response = repositoryRequest(t, mux, http.MethodGet, "/v1/repositories", "", "secret", "")
	decodeRepositoryResponse(t, response, &output)
	if response.Code != http.StatusOK || len(output.Repositories) != 1 || !output.Truncated {
		t.Fatalf("result cap status=%d repositories=%d truncated=%v", response.Code, len(output.Repositories), output.Truncated)
	}
}

func TestRepositoryListPaginatesWithOpaqueCursor(t *testing.T) {
	items := make([]repository.Repository, 0, 7)
	ids := make([]int64, 0, 7)
	for index := range 7 {
		owner := "acme"
		if index%2 == 1 {
			owner = "beta"
		}
		id := int64(101 + index)
		ids = append(ids, id)
		items = append(items, repository.Repository{ID: id, InstallationID: 10, GitHubID: id, Name: owner + "/repo-" + strconv.Itoa(index), Branch: "main", DesiredSHA: strings.Repeat("a", 40), IndexedSHA: strings.Repeat("a", 40), Status: "ready", SearchNode: "node-a"})
	}
	// The store returns its natural order; pagination must not depend on the wire order matching the cursor order.
	slices.SortFunc(items, func(a, b repository.Repository) int { return strings.Compare(a.Name, b.Name) })
	service := repositoryHTTPService()
	service.Store = &repositoryHTTPStore{repositories: items}
	mux := http.NewServeMux()
	RegisterRepositories(mux, requestAuthenticator(authn.NewStatic(map[string]authn.Principal{"secret": {InstallationID: 10, RepositoryIDs: ids}})), service, 128, 3, 256<<10)

	var names []string
	cursor := ""
	for page := 0; ; page++ {
		path := "/v1/repositories"
		if cursor != "" {
			path += "?cursor=" + cursor
		}
		response := repositoryRequest(t, mux, http.MethodGet, path, "", "secret", "")
		var output struct {
			Repositories []api.RepositorySummary `json:"repositories"`
			Truncated    bool                    `json:"truncated"`
			NextCursor   string                  `json:"next_cursor"`
		}
		decodeRepositoryResponse(t, response, &output)
		if response.Code != http.StatusOK {
			t.Fatalf("page %d status=%d body=%s", page, response.Code, response.Body.String())
		}
		for _, item := range output.Repositories {
			names = append(names, item.Name)
		}
		if !output.Truncated {
			if output.NextCursor != "" || page != 2 || len(output.Repositories) != 1 {
				t.Fatalf("final page=%d repositories=%d cursor=%q", page, len(output.Repositories), output.NextCursor)
			}
			if strings.Contains(response.Body.String(), "next_cursor") {
				t.Fatalf("final page carries next_cursor: %s", response.Body.String())
			}
			break
		}
		if len(output.Repositories) != 3 || output.NextCursor == "" {
			t.Fatalf("page %d repositories=%d cursor=%q", page, len(output.Repositories), output.NextCursor)
		}
		cursor = output.NextCursor
	}
	want := []string{"acme/repo-0", "acme/repo-2", "acme/repo-4", "acme/repo-6", "beta/repo-1", "beta/repo-3", "beta/repo-5"}
	if !slices.Equal(names, want) {
		t.Fatalf("paginated names = %v", names)
	}

	encode := func(value string) string { return base64.RawURLEncoding.EncodeToString([]byte(value)) }
	for name, cursor := range map[string]string{
		"empty":               "",
		"not base64":          "not-base64",
		"unsupported version": encode(`{"v":2,"name":"acme/repo-0"}`),
		"missing name":        encode(`{"v":1}`),
		"second JSON value":   encode(`{"v":1,"name":"acme/repo-0"} {}`),
	} {
		t.Run(name, func(t *testing.T) {
			response := repositoryRequest(t, mux, http.MethodGet, "/v1/repositories?cursor="+cursor, "", "secret", "")
			if response.Code != http.StatusBadRequest || !strings.Contains(response.Body.String(), `"code":"invalid_request"`) {
				t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
			}
		})
	}
	// A cursor past the end is an empty final page, never an error that reveals repository names.
	response := repositoryRequest(t, mux, http.MethodGet, "/v1/repositories?cursor="+encode(`{"v":1,"name":"zzz/last"}`), "", "secret", "")
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"repositories":[]`) || !strings.Contains(response.Body.String(), `"truncated":false`) {
		t.Fatalf("past-end status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestRepositoryListRejectsBudgetSmallerThanEmptyEnvelope(t *testing.T) {
	mux := http.NewServeMux()
	RegisterRepositories(mux, repositoryAuthenticator(), repositoryHTTPService(), 128, 100, 1)

	response := repositoryRequest(t, mux, http.MethodGet, "/v1/repositories", "", "secret", "")
	if response.Code != http.StatusInternalServerError || response.Body.Len() != 0 {
		t.Fatalf("status=%d body=%q", response.Code, response.Body.String())
	}
}

func TestRepositoryStatusAndReadFileRespectWireResponseBudget(t *testing.T) {
	service := repositoryHTTPService()
	service.GitHub = repositoryContentReader{content: githubapp.Content{
		Type: "file", Encoding: "base64", Content: base64.StdEncoding.EncodeToString([]byte("\x01\n\x01")), SHA: "blob", Size: 3,
	}}

	tests := []struct {
		name, method, path, body, contentType string
	}{
		{"status", http.MethodGet, "/v1/repositories/101/status", "", ""},
		{"read file with escaped content", http.MethodPost, "/v1/files/read", `{"repository_id":101,"path":"main.go"}`, "application/json"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			wideMux := http.NewServeMux()
			RegisterRepositories(wideMux, repositoryAuthenticator(), service, 256, 100, 256<<10)
			wide := repositoryRequest(t, wideMux, test.method, test.path, test.body, "secret", test.contentType)
			if wide.Code != http.StatusOK {
				t.Fatalf("wide status=%d body=%q", wide.Code, wide.Body.String())
			}
			payload := append([]byte(nil), wide.Body.Bytes()[:wide.Body.Len()-1]...)

			mux := http.NewServeMux()
			RegisterRepositories(mux, repositoryAuthenticator(), service, 256, 100, int64(len(payload)+1))
			response := repositoryRequest(t, mux, test.method, test.path, test.body, "secret", test.contentType)
			if response.Code != http.StatusOK || response.Body.Len() > len(payload)+1 {
				t.Fatalf("bounded status=%d bytes=%d limit=%d", response.Code, response.Body.Len(), len(payload)+1)
			}

			mux = http.NewServeMux()
			RegisterRepositories(mux, repositoryAuthenticator(), service, 256, 100, int64(len(payload)))
			response = repositoryRequest(t, mux, test.method, test.path, test.body, "secret", test.contentType)
			if response.Code != http.StatusInternalServerError || response.Body.Len() != 0 {
				t.Fatalf("impossible status=%d body=%q", response.Code, response.Body.String())
			}
		})
	}
}

func TestRepositoriesRoutesEnforceTransportContract(t *testing.T) {
	mux := http.NewServeMux()
	RegisterRepositories(mux, repositoryAuthenticator(), repositoryHTTPService(), 64, 100, 256<<10)

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
	RegisterRepositories(mux, repositoryAuthenticator(), repositoryHTTPService(), 256, 100, 256<<10)
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
	RegisterRepositories(mux, repositoryAuthenticator(), service, 256, 100, 256<<10)
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
			RegisterRepositories(mux, repositoryAuthenticator(), service, 256, 100, 256<<10)

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

func repositoryAuthenticator() authn.RequestAuthenticator {
	return requestAuthenticator(authn.NewStatic(map[string]authn.Principal{"secret": {InstallationID: 10, RepositoryIDs: []int64{101}}}))
}

type repositoryHTTPStore struct {
	repository             repository.Repository
	repositories           []repository.Repository
	globalRepositories     []repository.Repository
	globalRepositoriesByID map[int64]repository.Repository
	calls                  int
}

func (store *repositoryHTTPStore) AuthorizedRepositories(_ context.Context, _ int64, ids []int64, _ []string) ([]repository.Repository, error) {
	store.calls++
	if store.repositories != nil {
		return store.repositories, nil
	}
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

func (store *repositoryHTTPStore) AllAuthorizedRepositories(context.Context, []string) ([]repository.Repository, error) {
	store.calls++
	return store.globalRepositories, nil
}

func (store *repositoryHTTPStore) AnyAuthorizedRepository(_ context.Context, id int64) (repository.Repository, error) {
	store.calls++
	item, ok := store.globalRepositoriesByID[id]
	if !ok {
		return repository.Repository{}, pgx.ErrNoRows
	}
	return item, nil
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
