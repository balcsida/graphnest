package mcpserver

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/grepnest/grepnest/internal/authn"
	"github.com/grepnest/grepnest/internal/authz"
	"github.com/grepnest/grepnest/internal/githubapp"
	"github.com/grepnest/grepnest/internal/httpapi"
	"github.com/grepnest/grepnest/internal/observability"
	"github.com/grepnest/grepnest/internal/repository"
	"github.com/grepnest/grepnest/internal/search"
	"github.com/grepnest/grepnest/internal/zoekt"
	"github.com/grepnest/grepnest/pkg/api"
	"github.com/jackc/pgx/v5"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestRepositoryToolsUseAuthenticatedService(t *testing.T) {
	repositoryService := mcpRepositoryService()
	server := New(testService(t, &recordingBackend{}), repositoryService)
	handler := httpapi.AuthenticateBearer(
		authn.NewStatic(map[string]authn.Principal{"secret": {InstallationID: 10, RepositoryIDs: []int64{101}}}),
		mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return server }, nil),
	)
	httpServer := httptest.NewServer(handler)
	defer httpServer.Close()
	session, err := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "1"}, nil).Connect(
		t.Context(),
		&mcp.StreamableClientTransport{Endpoint: httpServer.URL, HTTPClient: bearerClient(httpServer.Client(), "secret"), DisableStandaloneSSE: true},
		nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()

	tools, err := session.ListTools(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	names := make([]string, len(tools.Tools))
	for index, tool := range tools.Tools {
		names[index] = tool.Name
	}
	for _, name := range []string{"list_repositories", "get_repository_status", "read_file"} {
		if !slices.Contains(names, name) {
			t.Fatalf("tools = %v", names)
		}
	}
	for toolName, fields := range map[string][]string{
		"list_repositories":     {"limit", "max_output_bytes"},
		"get_repository_status": {"repository_id", "max_output_bytes"},
		"read_file":             {"repository_id", "start_line", "end_line", "max_output_bytes"},
	} {
		schema := repositoryToolSchema(t, tools.Tools, toolName)
		properties := schema["properties"].(map[string]any)
		for _, field := range fields {
			if properties[field].(map[string]any)["minimum"] != float64(1) {
				t.Fatalf("%s.%s schema = %#v", toolName, field, properties[field])
			}
		}
	}
	readSchema := repositoryToolSchema(t, tools.Tools, "read_file")
	if readSchema["properties"].(map[string]any)["path"].(map[string]any)["minLength"] != float64(1) {
		t.Fatalf("read_file.path schema = %#v", readSchema["properties"])
	}

	result, err := session.CallTool(t.Context(), &mcp.CallToolParams{Name: "list_repositories", Arguments: map[string]any{}})
	if err != nil {
		t.Fatal(err)
	}
	var list struct {
		Repositories []api.RepositorySummary `json:"repositories"`
	}
	decodeStructured(t, result.StructuredContent, &list)
	wantList, err := repositoryService.List(t.Context(), authn.Principal{InstallationID: 10, RepositoryIDs: []int64{101}})
	if err != nil || !slices.EqualFunc(list.Repositories, wantList, func(a, b api.RepositorySummary) bool { return a == b }) {
		t.Fatalf("list = %#v, want %#v, err %v", list.Repositories, wantList, err)
	}

	result, err = session.CallTool(t.Context(), &mcp.CallToolParams{Name: "get_repository_status", Arguments: map[string]any{"repository_id": 101}})
	if err != nil {
		t.Fatal(err)
	}
	var status api.RepositorySummary
	decodeStructured(t, result.StructuredContent, &status)
	if status.GitHubID != 101 || status.Status != "ready" || status.SearchNode != "node-a" {
		t.Fatalf("status = %#v", status)
	}

	result, err = session.CallTool(t.Context(), &mcp.CallToolParams{Name: "read_file", Arguments: map[string]any{
		"repository_id": 101, "path": "main.go", "start_line": 2, "end_line": 99,
	}})
	if err != nil {
		t.Fatal(err)
	}
	var file api.ReadFileResponse
	decodeStructured(t, result.StructuredContent, &file)
	if file.Content != "two\nthree" || file.StartLine != 2 || file.EndLine != 3 || file.IndexedSHA != strings.Repeat("a", 40) {
		t.Fatalf("file = %#v", file)
	}

	store := repositoryService.Store.(*mcpRepositoryStore)
	store.calls = 0
	for _, call := range []*mcp.CallToolParams{
		{Name: "get_repository_status", Arguments: map[string]any{"repository_id": 0}},
		{Name: "read_file", Arguments: map[string]any{"repository_id": 101, "path": ""}},
		{Name: "read_file", Arguments: map[string]any{"repository_id": 101, "path": "main.go", "start_line": 0}},
	} {
		result, err := session.CallTool(t.Context(), call)
		if err != nil || !result.IsError {
			t.Fatalf("invalid %s result = %#v, err = %v", call.Name, result, err)
		}
	}
	if store.calls != 0 {
		t.Fatalf("invalid tool service calls = %d", store.calls)
	}
}

func TestRepositoryToolBudgetsBoundStructuredOutput(t *testing.T) {
	items := []api.RepositorySummary{
		{GitHubID: 101, Name: strings.Repeat("a", 80)},
		{GitHubID: 102, Name: strings.Repeat("b", 80)},
		{GitHubID: 103, Name: strings.Repeat("c", 80)},
	}
	list, err := limitRepositoryList(items, listInput{Limit: 2, MaxOutputBytes: 400}, Limits{MaxItems: 100, MaxOutputBytes: 256 << 10})
	if err != nil || len(list.Repositories) != 1 || !list.Truncated || structuredSize(t, list) > 400 {
		t.Fatalf("list=%#v size=%d err=%v", list, structuredSize(t, list), err)
	}

	file := api.ReadFileResponse{RepositoryID: 101, Path: "main.go", IndexedSHA: strings.Repeat("a", 40), BlobSHA: "blob", Content: "one\ntwo\n" + strings.Repeat("x", 400), StartLine: 1, EndLine: 3}
	limited, err := limitReadFile(file, 200, Limits{MaxOutputBytes: 256 << 10})
	if err != nil || limited.Content != "one\ntwo" || limited.EndLine != 2 || !limited.Truncated || structuredSize(t, limited) > 200 {
		t.Fatalf("file=%#v size=%d err=%v", limited, structuredSize(t, limited), err)
	}
	if _, err := limitReadFile(file, 1, Limits{MaxOutputBytes: 256 << 10}); !errors.Is(err, errOutputBudget) {
		t.Fatalf("tiny budget error=%v", err)
	}
	configured, err := limitReadFile(file, 0, Limits{MaxOutputBytes: 200})
	if err != nil || structuredSize(t, configured) > 200 || !configured.Truncated {
		t.Fatalf("configured file=%#v size=%d err=%v", configured, structuredSize(t, configured), err)
	}
	list, err = limitRepositoryList(items, listInput{}, Limits{MaxItems: 2, MaxOutputBytes: 256 << 10})
	if err != nil || len(list.Repositories) != 2 || !list.Truncated {
		t.Fatalf("configured item limit list=%#v err=%v", list, err)
	}
}

func TestReadFileBudgetFindsLargestWholeLinePrefix(t *testing.T) {
	lines := make([]string, 1000)
	for index := range lines {
		lines[index] = "x"
	}
	file := api.ReadFileResponse{RepositoryID: 101, Path: "main.go", IndexedSHA: strings.Repeat("a", 40), BlobSHA: "blob", Content: strings.Join(lines, "\n"), StartLine: 1, EndLine: len(lines)}
	want := file
	want.Content = strings.Join(lines[:537], "\n")
	want.EndLine = 537
	want.Truncated = true

	got, err := limitReadFile(file, int64(structuredSize(t, want)), Limits{MaxOutputBytes: 256 << 10})
	if err != nil || got.Content != want.Content || got.EndLine != want.EndLine || !got.Truncated {
		t.Fatalf("lines=%d end=%d size=%d err=%v", strings.Count(got.Content, "\n")+1, got.EndLine, structuredSize(t, got), err)
	}
}

func structuredSize(t *testing.T, value any) int {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return len(data)
}

func TestRepositoryToolErrorsAreSafe(t *testing.T) {
	const secret = "upstream-token"
	service := mcpRepositoryService()
	service.GitHub = mcpContentReader{err: errors.New(secret)}
	server := New(testService(t, &recordingBackend{}), service)
	handler := httpapi.AuthenticateBearer(
		authn.NewStatic(map[string]authn.Principal{"secret": {InstallationID: 10, RepositoryIDs: []int64{101}}}),
		mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return server }, nil),
	)
	httpServer := httptest.NewServer(handler)
	defer httpServer.Close()
	session, err := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "1"}, nil).Connect(
		t.Context(), &mcp.StreamableClientTransport{Endpoint: httpServer.URL, HTTPClient: bearerClient(httpServer.Client(), "secret"), DisableStandaloneSSE: true}, nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()

	for _, test := range []struct {
		name, tool, message string
		arguments           map[string]any
	}{
		{"unknown repository", "get_repository_status", "repository not found", map[string]any{"repository_id": 999}},
		{"invalid path", "read_file", "file request is invalid", map[string]any{"repository_id": 101, "path": "../secret"}},
		{"upstream failure", "read_file", "repository service is unavailable", map[string]any{"repository_id": 101, "path": "main.go"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			result, err := session.CallTool(t.Context(), &mcp.CallToolParams{Name: test.tool, Arguments: test.arguments})
			if err != nil {
				t.Fatal(err)
			}
			encoded, err := json.Marshal(result)
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(string(encoded), secret) || !result.IsError || len(result.Content) != 1 {
				t.Fatalf("result = %s", encoded)
			}
			content, ok := result.Content[0].(*mcp.TextContent)
			if !ok || content.Text != test.message {
				t.Fatalf("content = %#v", result.Content)
			}
		})
	}
}

func mcpRepositoryService() *repository.Service {
	sha := strings.Repeat("a", 40)
	return &repository.Service{
		Store: &mcpRepositoryStore{repository: repository.Repository{
			ID: 1, InstallationID: 10, GitHubID: 101, Name: "acme/one", Branch: "main",
			DesiredSHA: sha, IndexedSHA: sha, Status: "ready", SearchNode: "node-a",
		}},
		GitHub: mcpContentReader{content: githubapp.Content{
			Type: "file", Encoding: "base64", Content: base64.StdEncoding.EncodeToString([]byte("one\ntwo\nthree")), SHA: "blob", Size: 13,
		}},
		MaxLines: 2,
	}
}

type mcpRepositoryStore struct {
	repository repository.Repository
	calls      int
}

func (store *mcpRepositoryStore) AuthorizedRepositories(_ context.Context, _ int64, ids []int64, _ []string) ([]repository.Repository, error) {
	store.calls++
	if len(ids) == 1 && ids[0] == store.repository.GitHubID {
		return []repository.Repository{store.repository}, nil
	}
	return []repository.Repository{}, nil
}

func (store *mcpRepositoryStore) AuthorizedRepository(_ context.Context, _ int64, ids []int64, id int64) (repository.Repository, error) {
	store.calls++
	if id == store.repository.GitHubID && len(ids) == 1 && ids[0] == id {
		return store.repository, nil
	}
	return repository.Repository{}, pgx.ErrNoRows
}

type mcpContentReader struct {
	content githubapp.Content
	err     error
}

func (reader mcpContentReader) ReadContents(context.Context, int64, string, string, string, string, int64) (githubapp.Content, error) {
	return reader.content, reader.err
}

func repositoryToolSchema(t *testing.T, tools []*mcp.Tool, name string) map[string]any {
	t.Helper()
	for _, tool := range tools {
		if tool.Name == name {
			data, err := json.Marshal(tool.InputSchema)
			if err != nil {
				t.Fatal(err)
			}
			var schema map[string]any
			if err := json.Unmarshal(data, &schema); err != nil {
				t.Fatal(err)
			}
			return schema
		}
	}
	t.Fatalf("tool %q not found", name)
	return nil
}

func TestSearchToolsUseAuthenticatedService(t *testing.T) {
	backend := &recordingBackend{response: api.SearchResponse{Matches: []api.SearchMatch{{
		Path: "main.go", SHA: "abc123", Branches: []string{"main"}, LineNumber: 3, Preview: "needle\n", ZoektID: 7,
	}}}}
	service := testService(t, backend)
	server := New(service)
	handler := httpapi.AuthenticateBearer(
		authn.NewStatic(map[string]authn.Principal{"secret": {Subject: "user", RepositoryNames: []string{"acme/one"}}}),
		mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return server }, nil),
	)
	httpServer := httptest.NewServer(handler)
	defer httpServer.Close()

	session, err := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "1"}, nil).Connect(
		t.Context(),
		&mcp.StreamableClientTransport{Endpoint: httpServer.URL, HTTPClient: bearerClient(httpServer.Client(), "secret"), DisableStandaloneSSE: true},
		nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()

	tools, err := session.ListTools(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	names := make([]string, len(tools.Tools))
	for index, tool := range tools.Tools {
		names[index] = tool.Name
	}
	if !slices.Equal(names, []string{"find_files", "search_code"}) {
		t.Fatalf("tools = %v", names)
	}

	result, err := session.CallTool(t.Context(), &mcp.CallToolParams{
		Name:      "search_code",
		Arguments: map[string]any{"query": "needle", "repositories": []string{"acme/one"}, "max_output_bytes": 4096},
	})
	if err != nil {
		t.Fatal(err)
	}
	var output struct {
		Matches   []api.SearchMatch `json:"matches"`
		Truncated bool              `json:"truncated"`
	}
	decodeStructured(t, result.StructuredContent, &output)
	if len(output.Matches) != 1 || output.Matches[0].Repository.Name != "acme/one" || output.Matches[0].Path != "main.go" || output.Truncated {
		t.Fatalf("output = %#v", output)
	}
	backend.calls = 0
	result, err = session.CallTool(t.Context(), &mcp.CallToolParams{
		Name: "search_code", Arguments: map[string]any{"query": "needle", "repositories": []string{"acme/two"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	decodeStructured(t, result.StructuredContent, &output)
	if len(output.Matches) != 0 || backend.calls != 0 {
		t.Fatalf("matches = %d, backend calls = %d", len(output.Matches), backend.calls)
	}

	backend.response.Truncated = true
	result, err = session.CallTool(t.Context(), &mcp.CallToolParams{
		Name: "search_code", Arguments: map[string]any{"query": "needle", "repositories": []string{"acme/one"}, "limit": 1},
	})
	if err != nil {
		t.Fatal(err)
	}
	decodeStructured(t, result.StructuredContent, &output)
	if !output.Truncated {
		t.Fatalf("search_code truncated = %v, want true", output.Truncated)
	}

	result, err = session.CallTool(t.Context(), &mcp.CallToolParams{
		Name: "find_files", Arguments: map[string]any{"pattern": "\\.go$", "repositories": []string{"acme/one"}, "limit": 5, "max_output_bytes": 2048},
	})
	if err != nil {
		t.Fatal(err)
	}
	decodeStructured(t, result.StructuredContent, &output)
	if !output.Truncated {
		t.Fatalf("find_files truncated = %v, want true", output.Truncated)
	}
	if backend.request.Query != `file:\.go$` || backend.request.Limit != 5 {
		t.Fatalf("backend request = %#v", backend.request)
	}
}

func TestSearchToolErrorsAreSafe(t *testing.T) {
	const secret = "https://token@zoekt.internal.invalid/search"
	backend := &recordingBackend{err: errors.New(secret)}
	server := New(testService(t, backend))
	handler := httpapi.AuthenticateBearer(
		authn.NewStatic(map[string]authn.Principal{"secret": {Subject: "user", RepositoryNames: []string{"acme/one"}}}),
		mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return server }, nil),
	)
	httpServer := httptest.NewServer(handler)
	defer httpServer.Close()
	session, err := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "1"}, nil).Connect(
		t.Context(),
		&mcp.StreamableClientTransport{Endpoint: httpServer.URL, HTTPClient: bearerClient(httpServer.Client(), "secret"), DisableStandaloneSSE: true},
		nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()

	for _, test := range []struct {
		name, query, message string
	}{
		{"invalid query", " ", "search query is invalid"},
		{"backend failure", "needle", "search service is unavailable"},
	} {
		t.Run(test.name, func(t *testing.T) {
			result, err := session.CallTool(t.Context(), &mcp.CallToolParams{Name: "search_code", Arguments: map[string]any{"query": test.query}})
			if err != nil {
				if strings.Contains(err.Error(), secret) {
					t.Fatalf("protocol error leaked backend detail: %v", err)
				}
				t.Fatalf("protocol error = %v", err)
			}
			encoded, err := json.Marshal(result)
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(string(encoded), secret) {
				t.Fatalf("tool result leaked backend detail: %s", encoded)
			}
			if !result.IsError || len(result.Content) != 1 {
				t.Fatalf("result = %#v", result)
			}
			content, ok := result.Content[0].(*mcp.TextContent)
			if !ok || content.Text != test.message {
				t.Fatalf("content = %#v, want %q", result.Content, test.message)
			}
		})
	}
}

func TestSearchCodeLimitsCanonicalOutputThroughZoekt(t *testing.T) {
	preview := "needle with enough surrounding source to make each match material"
	wireBody, err := json.Marshal(map[string]any{"Result": map[string]any{"Files": []any{map[string]any{
		"FileName": "main.go", "Version": "abc", "Branches": []string{"main"}, "RepositoryID": 7,
		"LineMatches": []any{
			map[string]any{"Line": base64.StdEncoding.EncodeToString([]byte(preview)), "LineNumber": 1},
			map[string]any{"Line": base64.StdEncoding.EncodeToString([]byte(preview)), "LineNumber": 2},
		},
	}}}})
	if err != nil {
		t.Fatal(err)
	}
	zoektServer := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) { _, _ = writer.Write(wireBody) }))
	defer zoektServer.Close()
	backend, err := zoekt.New(zoektServer.URL, zoektServer.Client(), 256<<10, observability.New())
	if err != nil {
		t.Fatal(err)
	}
	registry, err := repository.NewStatic([]repository.Repository{{ID: 1, ZoektID: 7, Name: "acme/one", Branch: "main", IndexedSHA: "abc"}})
	if err != nil {
		t.Fatal(err)
	}
	service := search.NewService(backend, authz.NewStatic(registry), search.Limits{MaxResults: 100, MaxResponseBytes: 256 << 10})
	expected := api.SearchResponse{Matches: []api.SearchMatch{{
		Repository: api.Repository{ID: 1, Name: "acme/one", Branch: "main", IndexedSHA: "abc"}, Path: "main.go", SHA: "abc", LineNumber: 1, Preview: preview,
	}}, Truncated: true}
	budget := marshaledSize(t, expected)
	if len(wireBody) <= budget {
		t.Fatalf("wire body = %d, output budget = %d", len(wireBody), budget)
	}

	server := New(service)
	handler := httpapi.AuthenticateBearer(
		authn.NewStatic(map[string]authn.Principal{"secret": {Subject: "user", RepositoryNames: []string{"acme/one"}}}),
		mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return server }, nil),
	)
	httpServer := httptest.NewServer(handler)
	defer httpServer.Close()
	session, err := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "1"}, nil).Connect(
		t.Context(),
		&mcp.StreamableClientTransport{Endpoint: httpServer.URL, HTTPClient: bearerClient(httpServer.Client(), "secret"), DisableStandaloneSSE: true},
		nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	result, err := session.CallTool(t.Context(), &mcp.CallToolParams{Name: "search_code", Arguments: map[string]any{
		"query": "needle", "repositories": []string{"acme/one"}, "limit": 1, "max_output_bytes": 256 << 10,
	}})
	if err != nil {
		t.Fatal(err)
	}
	var output api.SearchResponse
	decodeStructured(t, result.StructuredContent, &output)
	if len(output.Matches) != 1 || !output.Truncated {
		t.Fatalf("limited normalized output = %#v", output)
	}

	result, err = session.CallTool(t.Context(), &mcp.CallToolParams{Name: "search_code", Arguments: map[string]any{
		"query": "needle", "repositories": []string{"acme/one"}, "max_output_bytes": budget,
	}})
	if err != nil {
		t.Fatal(err)
	}
	decodeStructured(t, result.StructuredContent, &output)
	structured, err := json.Marshal(result.StructuredContent)
	if err != nil {
		t.Fatal(err)
	}
	if len(output.Matches) != 1 || !output.Truncated || len(structured) > budget {
		t.Fatalf("output = %#v, size = %d, budget = %d", output, len(structured), budget)
	}
}

func marshaledSize(t *testing.T, value any) int {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return len(data)
}

func testService(t *testing.T, backend *recordingBackend) *search.Service {
	t.Helper()
	registry, err := repository.NewStatic([]repository.Repository{
		{ID: 1, ZoektID: 7, Name: "acme/one", Branch: "main", IndexedSHA: "abc123"}, {ID: 2, ZoektID: 8, Name: "acme/two", Branch: "main", IndexedSHA: "def456"},
	})
	if err != nil {
		t.Fatal(err)
	}
	return search.NewService(backend, authz.NewStatic(registry), search.Limits{MaxResults: 100, MaxResponseBytes: 256 << 10})
}

func decodeStructured(t *testing.T, value any, target any) {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, target); err != nil {
		t.Fatal(err)
	}
}

type bearerTransport struct {
	token string
	base  http.RoundTripper
}

func (transport bearerTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	request = request.Clone(request.Context())
	request.Header.Set("Authorization", "Bearer "+transport.token)
	return transport.base.RoundTrip(request)
}

func bearerClient(client *http.Client, token string) *http.Client {
	copy := *client
	copy.Transport = bearerTransport{token: token, base: client.Transport}
	return &copy
}

type recordingBackend struct {
	calls    int
	request  search.BackendRequest
	response api.SearchResponse
	err      error
}

func (backend *recordingBackend) Search(_ context.Context, request search.BackendRequest) (api.SearchResponse, error) {
	backend.calls++
	backend.request = request
	return backend.response, backend.err
}

func (*recordingBackend) Health(context.Context) error { return nil }
