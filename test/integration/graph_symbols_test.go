//go:build integration

package integration

import (
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/balcsida/graphnest/internal/authn"
	"github.com/balcsida/graphnest/internal/graphartifact"
	"github.com/balcsida/graphnest/internal/graphquery"
	"github.com/balcsida/graphnest/internal/graphservice"
	"github.com/balcsida/graphnest/internal/httpapi"
	"github.com/balcsida/graphnest/internal/mcpserver"
	"github.com/balcsida/graphnest/internal/postgres"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// TestGraphSymbolToolsRESTAndMCP serves the real CodeGraph fixture from
// PostgreSQL and checks that REST and MCP return the same name-addressed
// answers, and that a repository outside the caller's grant stays hidden.
func TestGraphSymbolToolsRESTAndMCP(t *testing.T) {
	path := os.Getenv("GRAPHNEST_TEST_CODEGRAPH_V2_FIXTURE")
	if path == "" {
		t.Skip("set GRAPHNEST_TEST_CODEGRAPH_V2_FIXTURE to the exported real oracle artifact")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	artifact, err := graphartifact.ParseV2(data, graphartifact.Limits{})
	if err != nil {
		t.Fatal(err)
	}
	h := newPostgresHarness(t)
	repositoryID := h.seedRepository(t, 10, 101)
	setGraphCommit(t, h, repositoryID, artifact.Commit)
	if _, err := h.store.ReplaceGraphV2(t.Context(), repositoryID, postgres.GraphPublication{Publisher: "symbol-contract"}, artifact); err != nil {
		t.Fatal(err)
	}
	h.seedRepository(t, 10, 202)
	authenticator := authn.NewStatic(map[string]authn.Principal{"user": {InstallationID: 10, RepositoryIDs: []int64{101}}})
	service := &graphservice.Service{Store: h.store, Backend: &graphquery.Service{Store: h.store}, Limits: graphservice.Limits{MaxResponseBytes: 256 << 10}}
	mux := http.NewServeMux()
	httpapi.RegisterGraphQueries(mux, authenticator, service, 64<<10, 256<<10)
	mux.Handle("/mcp", httpapi.AuthenticateBearer(authenticator, mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server {
		return mcpserver.NewWithLimits(mcpserver.Services{Graph: service}, mcpserver.Limits{MaxOutputBytes: 256 << 10, GraphMaxOutputBytes: 256 << 10})
	}, nil)))
	server := httptest.NewServer(mux)
	defer server.Close()

	for _, test := range []struct {
		rest, tool string
		input      map[string]any
		related    int
	}{
		{"callers", "graph_callers", map[string]any{"repo": 101, "symbol": "normalize", "file": "core.ts"}, 6},
		{"callees", "graph_callees", map[string]any{"repo": "acme/repo-101", "symbol": "run"}, 3},
		{"impact-radius", "graph_impact_radius", map[string]any{"repo": 101, "symbol": "normalize", "file": "core.ts", "depth": 1}, 0},
	} {
		rest := graphREST(t, server, test.rest, test.input)
		if mcpValue := graphMCP(t, server, test.tool, test.input); !reflect.DeepEqual(rest, mcpValue) {
			t.Fatalf("%s REST/MCP mismatch:\nREST: %#v\nMCP: %#v", test.rest, rest, mcpValue)
		}
		result := rest.(map[string]any)
		definitions := result["definitions"].([]any)
		if result["status"] != "ok" || len(definitions) != 1 {
			t.Fatalf("%s=%#v", test.rest, result)
		}
		definition := definitions[0].(map[string]any)
		related, _ := definition["related"].([]any)
		entities, _ := definition["entities"].([]any)
		if test.related > 0 && len(related) != test.related || test.rest == "impact-radius" && len(entities) != 7 {
			t.Fatalf("%s definition=%#v", test.rest, definition)
		}
	}
	request := httptest.NewRequest(http.MethodPost, "/v1/graph/callers", strings.NewReader(`{"repo":202,"symbol":"normalize"}`))
	request.Header.Set("Authorization", "Bearer user")
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	mux.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("unauthorized repository=%d %s", recorder.Code, recorder.Body.String())
	}
}
