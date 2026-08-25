//go:build integration

package integration

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
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

const graphContractSHA = "1111111111111111111111111111111111111111"

func TestGraphPublicContractUsesPostgres(t *testing.T) {
	h := newPostgresHarness(t)
	repositoryID := h.seedRepository(t, 10, 101)
	hiddenID := h.seedRepository(t, 20, 202)
	setGraphCommit(t, h, repositoryID, graphContractSHA)
	setGraphCommit(t, h, hiddenID, graphContractSHA)
	replaceContractGraph(t, h, contractArtifact(repositoryID, graphContractSHA, false))
	replaceContractGraph(t, h, contractArtifact(hiddenID, graphContractSHA, true))

	backend := &graphquery.Service{Store: h.store, Limits: graphquery.Limits{
		PerCategory: 1, DefaultImpactDepth: 1, MaxDepth: 1,
		DefaultTraceDepth: 2, MaxTraceDepth: 2, MaxNodes: 10, MaxEdges: 10, MaxFanout: 10,
	}}
	user := authn.Principal{InstallationID: 10, RepositoryIDs: []int64{101}}
	authenticator := authn.NewStatic(map[string]authn.Principal{"user": user})
	service := &graphservice.Service{Store: h.store, Backend: backend, Limits: graphservice.Limits{
		PerCategory: 1, DefaultImpactDepth: 1, MaxDepth: 1,
		DefaultTraceDepth: 2, MaxTraceDepth: 2, MaxNodes: 10, MaxEdges: 10, MaxFanout: 10, MaxResponseBytes: 256 << 10,
	}}
	mux := http.NewServeMux()
	httpapi.RegisterGraphQueries(mux, authenticator, service, 64<<10, 256<<10)
	mux.Handle("/mcp", httpapi.AuthenticateBearer(authenticator, mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server {
		return mcpserver.NewWithLimits(mcpserver.Services{Graph: service}, mcpserver.Limits{MaxOutputBytes: 256 << 10, GraphMaxOutputBytes: 256 << 10})
	}, nil)))
	server := httptest.NewServer(mux)
	defer server.Close()

	requests := map[string]map[string]any{
		"context": {"repo": "acme/repo-101", "uid": "B", "relations": []string{"calls"}, "per_category_limit": 1},
		"impact":  {"repo": 101, "target_uid": "A", "direction": "downstream", "relations": []string{"calls"}, "max_depth": 99, "limit": 1},
		"trace":   {"repo": "acme/repo-101", "source_uid": "A", "target_uid": "C", "max_depth": 2},
	}
	for _, name := range []string{"context", "impact", "trace"} {
		rest := graphREST(t, server, name, requests[name])
		mcpValue := graphMCP(t, server, name, requests[name])
		if !reflect.DeepEqual(rest, mcpValue) {
			t.Fatalf("%s REST/MCP mismatch:\nREST: %#v\nMCP: %#v", name, rest, mcpValue)
		}
	}
	contextValue := graphREST(t, server, "context", requests["context"]).(map[string]any)
	if contextValue["status"] != "found" || contextValue["commits"].(map[string]any)["acme/repo-101"] != graphContractSHA {
		t.Fatalf("context=%#v", contextValue)
	}
	trace := graphREST(t, server, "trace", requests["trace"]).(map[string]any)
	if nodes := trace["nodes"].([]any); trace["status"] != "ok" || len(nodes) != 3 {
		t.Fatalf("trace=%#v", trace)
	}
	hidden := graphREST(t, server, "context", map[string]any{"uid": "SECRET"})
	hiddenJSON, err := json.Marshal(hidden)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(hiddenJSON, []byte("hidden.Secret")) {
		t.Fatalf("unauthorized graph row leaked: %s", hiddenJSON)
	}
}

func graphREST(t *testing.T, server *httptest.Server, operation string, input any) any {
	t.Helper()
	body, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	request, err := http.NewRequestWithContext(t.Context(), http.MethodPost, server.URL+"/v1/graph/"+operation, bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer user")
	request.Header.Set("Content-Type", "application/json")
	response, err := server.Client().Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	data, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusOK {
		t.Fatalf("%s status=%d body=%s", operation, response.StatusCode, data)
	}
	var value any
	if err := json.Unmarshal(data, &value); err != nil {
		t.Fatal(err)
	}
	return value
}

func graphMCP(t *testing.T, server *httptest.Server, operation string, input map[string]any) any {
	t.Helper()
	client := *server.Client()
	client.Transport = graphBearerTransport{base: client.Transport}
	session, err := mcp.NewClient(&mcp.Implementation{Name: "graph-contract", Version: "1"}, nil).Connect(t.Context(), &mcp.StreamableClientTransport{
		Endpoint: server.URL + "/mcp", HTTPClient: &client, DisableStandaloneSSE: true,
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	result, err := session.CallTool(t.Context(), &mcp.CallToolParams{Name: operation, Arguments: input})
	if err != nil || result.IsError {
		t.Fatalf("%s MCP result=%#v err=%v", operation, result, err)
	}
	return result.StructuredContent
}

type graphBearerTransport struct{ base http.RoundTripper }

func (transport graphBearerTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	cloned := request.Clone(request.Context())
	cloned.Header.Set("Authorization", "Bearer user")
	return transport.base.RoundTrip(cloned)
}

func contractArtifact(repositoryID int64, commit string, hidden bool) graphartifact.Artifact {
	names := []string{"A", "B", "C", "D"}
	if hidden {
		names = []string{"SECRET"}
	}
	artifact := graphartifact.Artifact{
		SchemaVersion: 1, RepositoryID: repositoryID, Commit: commit,
		Analyzer: graphartifact.Analyzer{Name: "contract", Version: "1"}, ContentHash: bytes.Repeat([]byte{byte(repositoryID)}, 32),
		Nodes: []graphartifact.Node{{UID: "repository", Kind: graphartifact.NodeRepository}, {UID: "file", Kind: graphartifact.NodeFile, Path: "graph.go"}},
		Edges: []graphartifact.Edge{{SourceUID: "repository", TargetUID: "file", Kind: graphartifact.EdgeContains, Confidence: 1}},
	}
	for _, name := range names {
		qualified := "contract." + name
		if hidden {
			qualified = "hidden.Secret"
		}
		artifact.Nodes = append(artifact.Nodes, graphartifact.Node{UID: name, Kind: graphartifact.NodeSymbol, Path: "graph.go", Language: "go", SymbolKind: "Function", QualifiedName: qualified})
		artifact.Edges = append(artifact.Edges, graphartifact.Edge{SourceUID: "file", TargetUID: name, Kind: graphartifact.EdgeContains, Confidence: 1})
	}
	if !hidden {
		for _, pair := range [][2]string{{"A", "B"}, {"B", "C"}, {"B", "D"}} {
			artifact.Edges = append(artifact.Edges, graphartifact.Edge{SourceUID: pair[0], TargetUID: pair[1], Kind: graphartifact.EdgeCalls, Path: "graph.go", Confidence: 1, ResolutionReason: "fixture"})
		}
	}
	return artifact
}

func replaceContractGraph(t *testing.T, h *postgresHarness, artifact graphartifact.Artifact) {
	t.Helper()
	if _, err := h.store.ReplaceGraph(t.Context(), artifact.RepositoryID, postgres.GraphSourceManaged, artifact); err != nil {
		t.Fatal(err)
	}
}

func setGraphCommit(t *testing.T, h *postgresHarness, repositoryID int64, commit string) {
	t.Helper()
	if _, err := h.pool.Exec(t.Context(), "update repositories set indexed_sha=$2, status='ready' where id=$1", repositoryID, commit); err != nil {
		t.Fatal(err)
	}
}
