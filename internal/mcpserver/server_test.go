package mcpserver

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"

	"github.com/grepnest/grepnest/internal/authn"
	"github.com/grepnest/grepnest/internal/authz"
	"github.com/grepnest/grepnest/internal/httpapi"
	"github.com/grepnest/grepnest/internal/observability"
	"github.com/grepnest/grepnest/internal/repository"
	"github.com/grepnest/grepnest/internal/search"
	"github.com/grepnest/grepnest/internal/zoekt"
	"github.com/grepnest/grepnest/pkg/api"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestSearchToolsUseAuthenticatedService(t *testing.T) {
	backend := &recordingBackend{response: api.SearchResponse{Matches: []api.SearchMatch{{
		Path: "main.go", SHA: "abc123", LineNumber: 3, Preview: "needle\n", ZoektID: 7,
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

func TestSearchCodeLimitsCanonicalOutputThroughZoekt(t *testing.T) {
	preview := "needle with enough surrounding source to make each match material"
	wireBody, err := json.Marshal(map[string]any{"Result": map[string]any{"Files": []any{map[string]any{
		"FileName": "main.go", "Version": "abc", "RepositoryID": 7,
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
	registry, err := repository.NewStatic([]repository.Repository{{ID: 1, ZoektID: 7, Name: "acme/one"}})
	if err != nil {
		t.Fatal(err)
	}
	service := search.NewService(backend, authz.NewStatic(registry), search.Limits{MaxResults: 100, MaxResponseBytes: 256 << 10})
	expected := api.SearchResponse{Matches: []api.SearchMatch{{
		Repository: api.Repository{ID: 1, Name: "acme/one"}, Path: "main.go", SHA: "abc", LineNumber: 1, Preview: preview,
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
		{ID: 1, ZoektID: 7, Name: "acme/one"}, {ID: 2, ZoektID: 8, Name: "acme/two"},
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
}

func (backend *recordingBackend) Search(_ context.Context, request search.BackendRequest) (api.SearchResponse, error) {
	backend.calls++
	backend.request = request
	return backend.response, nil
}

func (*recordingBackend) Health(context.Context) error { return nil }
