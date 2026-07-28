//go:build integration && system_ladybug && unix

package integration

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/grepnest/grepnest/internal/authn"
	"github.com/grepnest/grepnest/internal/config"
	"github.com/grepnest/grepnest/internal/graphartifact"
	"github.com/grepnest/grepnest/internal/graphclient"
	"github.com/grepnest/grepnest/internal/graphcommand"
	"github.com/grepnest/grepnest/internal/graphprotocol"
	"github.com/grepnest/grepnest/internal/graphquery"
	"github.com/grepnest/grepnest/internal/graphruntime"
	"github.com/grepnest/grepnest/internal/graphservice"
	"github.com/grepnest/grepnest/internal/httpapi"
	"github.com/grepnest/grepnest/internal/mcpserver"
	"github.com/grepnest/grepnest/internal/postgres"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const (
	graphContractSHA    = "1111111111111111111111111111111111111111"
	graphContractNext   = "2222222222222222222222222222222222222222"
	graphContractHidden = "3333333333333333333333333333333333333333"
)

func TestGraphPublicContractMatchesRuntimeModes(t *testing.T) {
	h := newPostgresHarness(t)
	repositoryID := h.seedRepository(t, 10, 101)
	hiddenID := h.seedRepository(t, 20, 202)
	setGraphCommit(t, h, repositoryID, graphContractSHA)
	setGraphCommit(t, h, hiddenID, graphContractHidden)
	replaceContractGraph(t, h, contractArtifact(repositoryID, graphContractSHA, false))
	replaceContractGraph(t, h, contractArtifact(hiddenID, graphContractHidden, true))

	var want map[string]any
	for _, mode := range []string{"embedded", "separate"} {
		t.Run(mode, func(t *testing.T) {
			client := startContractRuntime(t, h, mode)
			got := exerciseGraphSurface(t, h, client, repositoryID)
			if want == nil {
				want = got
			} else if !reflect.DeepEqual(got, want) {
				t.Fatalf("%s contract differs:\ngot:  %#v\nwant: %#v", mode, got, want)
			}
		})
	}
}

func exerciseGraphSurface(t *testing.T, h *postgresHarness, backend graphprotocol.QueryEngine, repositoryID int64) map[string]any {
	t.Helper()
	user := authn.Principal{InstallationID: 10, RepositoryIDs: []int64{101}}
	admin := authn.Principal{Administrator: true}
	authenticator := authn.NewStatic(map[string]authn.Principal{"user": user, "admin": admin})
	service := &graphservice.Service{Store: h.store, Backend: backend, Limits: graphContractLimits()}
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
		"cypher":  {"repo": 101, "statement": "MATCH (s:Symbol) RETURN s.repository_id, s.qualified_name ORDER BY s.repository_id, s.qualified_name", "max_rows": 2},
	}
	got := map[string]any{}
	for _, name := range []string{"context", "impact", "trace", "cypher"} {
		token := "user"
		if name == "cypher" {
			token = "admin"
		}
		rest := graphREST(t, server, token, name, requests[name], http.StatusOK)
		mcpValue := graphMCP(t, server, token, name, requests[name], false)
		if !reflect.DeepEqual(rest, mcpValue) {
			t.Fatalf("%s REST/MCP mismatch:\nREST: %#v\nMCP:  %#v", name, rest, mcpValue)
		}
		got[name] = rest
	}

	assertContractResults(t, got, repositoryID)
	graphREST(t, server, "user", "cypher", requests["cypher"], http.StatusForbidden)
	graphMCP(t, server, "user", "cypher", requests["cypher"], true)
	hidden := graphREST(t, server, "user", "context", map[string]any{"uid": "SECRET"}, http.StatusOK)
	hiddenJSON, err := json.Marshal(hidden)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(hiddenJSON, []byte("hidden.Secret")) || bytes.Contains(hiddenJSON, []byte(graphContractHidden)) {
		t.Fatalf("unauthorized graph row leaked: %s", hiddenJSON)
	}

	stale := &staleGraphBackend{QueryEngine: backend, mutate: func() { setGraphCommit(t, h, repositoryID, graphContractNext) }}
	staleService := &graphservice.Service{Store: h.store, Backend: stale, Limits: graphContractLimits()}
	staleMux := http.NewServeMux()
	httpapi.RegisterGraphQueries(staleMux, authenticator, staleService, 64<<10, 256<<10)
	staleServer := httptest.NewServer(staleMux)
	graphREST(t, staleServer, "user", "context", requests["context"], http.StatusConflict)
	staleServer.Close()
	setGraphCommit(t, h, repositoryID, graphContractSHA)
	return got
}

func assertContractResults(t *testing.T, got map[string]any, repositoryID int64) {
	t.Helper()
	contextValue := got["context"].(map[string]any)
	if contextValue["status"] != "found" || contextValue["commits"].(map[string]any)["acme/repo-101"] != graphContractSHA {
		t.Fatalf("context=%#v", contextValue)
	}
	if boundaries := contextValue["boundaries"].([]any); len(boundaries) != 1 || boundaries[0].(map[string]any)["reason"] != "category_limit" {
		t.Fatalf("context boundaries=%#v", boundaries)
	}
	impact := got["impact"].(map[string]any)
	if impact["status"] != "found" || impact["partial"] != true {
		t.Fatalf("impact=%#v", impact)
	}
	impactBoundaries := impact["boundaries"].([]any)
	if len(impactBoundaries) != 1 ||
		impactBoundaries[0].(map[string]any)["reason"] != "depth_limit" ||
		impactBoundaries[0].(map[string]any)["depth"] != float64(1) {
		t.Fatalf("impact boundaries=%#v", impactBoundaries)
	}
	trace := got["trace"].(map[string]any)
	nodes := trace["nodes"].([]any)
	if trace["status"] != "ok" || len(nodes) != 3 ||
		nodes[0].(map[string]any)["uid"] != "A" ||
		nodes[1].(map[string]any)["uid"] != "B" ||
		nodes[2].(map[string]any)["uid"] != "C" {
		t.Fatalf("trace=%#v", trace)
	}
	cypher := got["cypher"].(map[string]any)
	rows := cypher["rows"].([]any)
	if cypher["status"] != "ok" || cypher["truncated"] != true || len(rows) != 2 ||
		rows[0].([]any)[0] != float64(repositoryID) {
		t.Fatalf("cypher=%#v", cypher)
	}
}

type staleGraphBackend struct {
	graphprotocol.QueryEngine
	once   sync.Once
	mutate func()
}

func (backend *staleGraphBackend) Context(ctx context.Context, request graphprotocol.ContextRequest) (graphprotocol.ContextResponse, error) {
	response, err := backend.QueryEngine.Context(ctx, request)
	backend.once.Do(backend.mutate)
	return response, err
}

func startContractRuntime(t *testing.T, h *postgresHarness, mode string) graphprotocol.QueryEngine {
	t.Helper()
	address := graphFreeAddress(t)
	secret := []byte("graph-contract-secret")
	root := t.TempDir()
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	if mode == "embedded" {
		runtime, err := graphruntime.New(t.Context(), graphruntime.Config{
			DatabasePath: filepath.Join(root, "grepnest.lbug"), ListenAddress: address,
			InternalSecret: secret, ReadConnections: 2, SyncInterval: 20 * time.Millisecond,
			QueryTimeout: 5 * time.Second, InterruptGrace: 2 * time.Second, QueryLimits: graphBackendLimits(),
		}, h.store, nil)
		if err != nil {
			t.Fatal(err)
		}
		go func() { done <- runtime.Run(ctx) }()
		t.Cleanup(func() { _ = runtime.Close() })
	} else {
		go func() {
			done <- graphcommand.RunStandalone(ctx, config.Graph{
				Mode: "separate", DatabaseURL: graphHarnessDSN(t, h),
				ListenAddress: address, DataDir: root, InternalSecret: secret,
				ReadConnections: 2, SyncInterval: 20 * time.Millisecond,
				QueryTimeout: 5 * time.Second, InterruptGrace: 2 * time.Second,
				QueryLimits: config.GraphQueryLimits{
					PerCategory: 1, DefaultImpactDepth: 1, MaxDepth: 1,
					DefaultTraceDepth: 2, MaxTraceDepth: 2, MaxRows: 2,
					MaxNodes: 10, MaxEdges: 10, MaxFanout: 10,
				},
			}, nil)
		}()
	}
	t.Cleanup(func() {
		cancel()
		if err := <-done; err != nil {
			t.Error(err)
		}
	})
	waitGraphReady(t, address)
	client, err := graphclient.New("http://"+address, secret, &http.Client{Timeout: 5 * time.Second}, 256<<10)
	if err != nil {
		t.Fatal(err)
	}
	return client
}

func graphHarnessDSN(t *testing.T, h *postgresHarness) string {
	t.Helper()
	parsed, err := url.Parse(h.pool.Config().ConnString())
	if err != nil {
		t.Fatal(err)
	}
	query := parsed.Query()
	query.Set("search_path", h.schema)
	parsed.RawQuery = query.Encode()
	return parsed.String()
}

func graphREST(t *testing.T, server *httptest.Server, token, operation string, input any, wantStatus int) any {
	t.Helper()
	body, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	request, err := http.NewRequestWithContext(t.Context(), http.MethodPost, server.URL+"/v1/graph/"+operation, bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer "+token)
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
	if response.StatusCode != wantStatus {
		t.Fatalf("%s status=%d want=%d body=%s", operation, response.StatusCode, wantStatus, data)
	}
	var value any
	if err := json.Unmarshal(data, &value); err != nil {
		t.Fatal(err)
	}
	return value
}

func graphMCP(t *testing.T, server *httptest.Server, token, operation string, input map[string]any, wantError bool) any {
	t.Helper()
	client := *server.Client()
	client.Transport = bearerTransport{token: token, base: client.Transport}
	session, err := mcp.NewClient(&mcp.Implementation{Name: "graph-contract", Version: "1"}, nil).Connect(t.Context(), &mcp.StreamableClientTransport{
		Endpoint: server.URL + "/mcp", HTTPClient: &client, DisableStandaloneSSE: true,
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	result, err := session.CallTool(t.Context(), &mcp.CallToolParams{Name: operation, Arguments: input})
	if err != nil {
		t.Fatal(err)
	}
	if result.IsError != wantError {
		t.Fatalf("%s MCP IsError=%t want=%t result=%#v", operation, result.IsError, wantError, result)
	}
	return result.StructuredContent
}

type bearerTransport struct {
	token string
	base  http.RoundTripper
}

func (transport bearerTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	cloned := request.Clone(request.Context())
	cloned.Header.Set("Authorization", "Bearer "+transport.token)
	return transport.base.RoundTrip(cloned)
}

func graphContractLimits() graphservice.Limits {
	return graphservice.Limits{
		PerCategory: 1, DefaultImpactDepth: 2, MaxDepth: 2,
		DefaultTraceDepth: 2, MaxTraceDepth: 2, MaxRows: 2,
		MaxNodes: 10, MaxEdges: 10, MaxFanout: 10, MaxResponseBytes: 256 << 10,
	}
}

func graphBackendLimits() graphquery.Limits {
	return graphquery.Limits{
		PerCategory: 1, DefaultImpactDepth: 1, MaxDepth: 1,
		DefaultTraceDepth: 2, MaxTraceDepth: 2, MaxRows: 2,
		MaxNodes: 10, MaxEdges: 10, MaxFanout: 10,
	}
}

func contractArtifact(repositoryID int64, commit string, hidden bool) graphartifact.Artifact {
	names := []string{"A", "B", "C", "D"}
	if hidden {
		names = []string{"SECRET"}
	}
	artifact := graphartifact.Artifact{
		SchemaVersion: 1, RepositoryID: repositoryID, Commit: commit,
		Analyzer: graphartifact.Analyzer{Name: "contract", Version: "1"},
		Nodes: []graphartifact.Node{
			{UID: "repository", Kind: graphartifact.NodeRepository},
			{UID: "file", Kind: graphartifact.NodeFile, Path: "graph.go"},
		},
		Edges: []graphartifact.Edge{{SourceUID: "repository", TargetUID: "file", Kind: graphartifact.EdgeContains, Confidence: 1}},
	}
	for _, name := range names {
		qualified := "contract." + name
		if hidden {
			qualified = "hidden.Secret"
		}
		artifact.Nodes = append(artifact.Nodes, graphartifact.Node{
			UID: name, Kind: graphartifact.NodeSymbol, Path: "graph.go", Language: "go",
			SymbolKind: "Function", QualifiedName: qualified,
		})
		artifact.Edges = append(artifact.Edges, graphartifact.Edge{SourceUID: "file", TargetUID: name, Kind: graphartifact.EdgeContains, Confidence: 1})
	}
	if !hidden {
		for _, pair := range [][2]string{{"A", "B"}, {"B", "C"}, {"B", "D"}} {
			artifact.Edges = append(artifact.Edges, graphartifact.Edge{
				SourceUID: pair[0], TargetUID: pair[1], Kind: graphartifact.EdgeCalls,
				Path: "graph.go", Confidence: 1, ResolutionReason: "fixture",
			})
		}
	}
	artifact.ContentHash = bytes.Repeat([]byte{byte(repositoryID)}, 32)
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

func graphFreeAddress(t *testing.T) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().String()
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	return address
}

func waitGraphReady(t *testing.T, address string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		response, err := http.Get("http://" + address + "/readyz")
		if err == nil {
			response.Body.Close()
			if response.StatusCode == http.StatusOK {
				return
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("graph runtime %s did not become ready: %v", address, err)
		}
		time.Sleep(20 * time.Millisecond)
	}
}
