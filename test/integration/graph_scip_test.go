//go:build integration

package integration

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"sort"
	"testing"

	"github.com/balcsida/graphnest/internal/authn"
	"github.com/balcsida/graphnest/internal/graphquery"
	"github.com/balcsida/graphnest/internal/graphservice"
	"github.com/balcsida/graphnest/internal/httpapi"
	"github.com/balcsida/graphnest/internal/mcpserver"
	"github.com/balcsida/graphnest/internal/scipgraph"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const scipDemoSHA = "cccccccccccccccccccccccccccccccccccccccc"

// TestSCIPUploadServesGraphTools uploads the real scip-go fixture and checks
// that the graph tools answer from it. The fixture's module path
// (github.com/oldorg/demo) differs from the stored repository name
// (acme/repo-101), which covers a repository transferred after indexing.
func TestSCIPUploadServesGraphTools(t *testing.T) {
	h := newPostgresHarness(t)
	repositoryID := h.seedRepository(t, 10, 101)
	setGraphCommit(t, h, repositoryID, scipDemoSHA)
	authenticator := authn.NewStatic(map[string]authn.Principal{
		"user":  {Subject: "user", InstallationID: 10, RepositoryIDs: []int64{101}},
		"admin": {Subject: "admin", InstallationID: 10, RepositoryIDs: []int64{101}, Administrator: true},
	})
	scipService := &scipgraph.Service{Store: h.store}
	service := &graphservice.Service{Store: h.store, Backend: &graphquery.Service{Store: h.store}, Limits: graphservice.Limits{MaxResponseBytes: 256 << 10}}
	mux := http.NewServeMux()
	httpapi.RegisterSCIP(mux, authn.RequestAuthenticator{Bearer: authenticator}, scipService, 64<<10, 64<<20, 256<<10)
	httpapi.RegisterGraphQueries(mux, authenticator, service, 64<<10, 256<<10)
	mux.Handle("/mcp", httpapi.AuthenticateBearer(authenticator, mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server {
		return mcpserver.NewWithLimits(mcpserver.Services{Graph: service, SCIP: scipService}, mcpserver.Limits{MaxOutputBytes: 256 << 10, GraphMaxOutputBytes: 256 << 10})
	}, nil)))
	server := httptest.NewServer(mux)
	defer server.Close()

	index, err := os.ReadFile("../fixtures/scip/go-demo/index.scip")
	if err != nil {
		t.Fatal(err)
	}
	uploadSCIPIndex(t, server, 101, index)

	capabilities := graphMCP(t, server, "graph_capabilities", map[string]any{"repo": 101}).(map[string]any)
	if capabilities["status"] != "ready" || capabilities["current_indexed_commit"] != scipDemoSHA {
		t.Fatalf("capabilities=%#v", capabilities)
	}

	// navigate_symbol references of Hello (greet/greet.go line 11) are the
	// locations graph_callers must report for the same function.
	references := graphMCP(t, server, "navigate_symbol", map[string]any{"repository_id": 101, "path": "greet/greet.go", "line": 11, "character": 5, "operation": "references"}).(map[string]any)
	want := map[string]bool{}
	for _, location := range references["locations"].([]any) {
		l := location.(map[string]any)
		if int(l["start_line"].(float64)) == 11 && l["path"] == "greet/greet.go" {
			continue // the definition itself
		}
		want[fmt.Sprintf("%s:%v:%v", l["path"], l["start_line"], l["start_character"])] = true
	}
	if len(want) != 2 {
		t.Fatalf("navigate_symbol references=%#v", references)
	}
	callers := graphMCP(t, server, "graph_callers", map[string]any{"repo": 101, "symbol": "Hello"}).(map[string]any)
	definitions := callers["definitions"].([]any)
	if callers["status"] != "ok" || len(definitions) != 1 {
		t.Fatalf("graph_callers=%#v", callers)
	}
	got := map[string]bool{}
	var callerNames []string
	for _, related := range definitions[0].(map[string]any)["related"].([]any) {
		r := related.(map[string]any)
		location := r["edge"].(map[string]any)["fact"].(map[string]any)["location"].(map[string]any)
		start := location["start"].(map[string]any)
		got[fmt.Sprintf("%s:%v:%v", location["path"], start["line"].(float64)+1, start["character"])] = true
		callerNames = append(callerNames, r["entity"].(map[string]any)["fact"].(map[string]any)["qualified_name"].(string))
	}
	sort.Strings(callerNames)
	if fmt.Sprint(got) != fmt.Sprint(want) || fmt.Sprint(callerNames) != "[github.com/oldorg/demo.main github.com/oldorg/demo/greet.Impl.Greet]" {
		t.Fatalf("graph_callers locations=%v want %v callers=%v", got, want, callerNames)
	}

	// The legacy context workflow answers from the same upload.
	context := graphMCP(t, server, "context", map[string]any{"repo": 101, "name": "Hello", "relations": []string{"references"}}).(map[string]any)
	incoming, _ := context["incoming"].(map[string]any)
	if references, _ := incoming["references"].([]any); context["status"] != "found" || len(references) != 2 {
		t.Fatalf("context=%#v", context)
	}
	if boundaries, _ := context["boundaries"].([]any); len(boundaries) > 0 {
		t.Fatalf("context boundaries=%#v", boundaries)
	}
}

func uploadSCIPIndex(t *testing.T, server *httptest.Server, repositoryID int64, data []byte) {
	t.Helper()
	request, err := http.NewRequestWithContext(t.Context(), http.MethodPost, fmt.Sprintf("%s/v1/scip/uploads?repository_id=%d&commit=%s", server.URL, repositoryID, scipDemoSHA), bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer admin")
	request.Header.Set("Content-Type", "application/vnd.scip+protobuf")
	response, err := server.Client().Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusNoContent {
		var body json.RawMessage
		_ = json.NewDecoder(response.Body).Decode(&body)
		t.Fatalf("upload status=%d body=%s", response.StatusCode, body)
	}
}
