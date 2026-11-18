//go:build integration

package integration

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/balcsida/graphnest/internal/authn"
	"github.com/balcsida/graphnest/internal/graphartifact"
	graphv2 "github.com/balcsida/graphnest/internal/graphartifact/v2"
	"github.com/balcsida/graphnest/internal/graphquery"
	"github.com/balcsida/graphnest/internal/graphservice"
	"github.com/balcsida/graphnest/internal/httpapi"
	"github.com/balcsida/graphnest/internal/mcpserver"
	"github.com/balcsida/graphnest/internal/postgres"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"google.golang.org/protobuf/proto"
)

const (
	legacySHAX = "6666666666666666666666666666666666666666"
	legacySHAY = "7777777777777777777777777777777777777777"
	legacySHAZ = "8888888888888888888888888888888888888888"
)

// TestLegacyGraphContractIsTheSameFromV1AndV2 publishes one call graph as a
// managed v1 graph and as a v2 generation and requires byte-for-byte the same
// context, impact and trace answers over REST and MCP: fields, types, order,
// boundaries, error codes and repository scope.
func TestLegacyGraphContractIsTheSameFromV1AndV2(t *testing.T) {
	for _, generation := range []struct {
		name    string
		publish func(*testing.T, *postgresHarness, int64, int64, string)
	}{
		{"v1", publishLegacyV1},
		{"v2", publishLegacyV2},
	} {
		t.Run(generation.name, func(t *testing.T) {
			h := newPostgresHarness(t)
			repositoryID := h.seedRepository(t, 10, 101)
			emptyID := h.seedRepository(t, 10, 102)
			h.seedRepository(t, 10, 103) // never indexed
			hiddenID := h.seedRepository(t, 20, 202)
			for _, id := range []int64{repositoryID, emptyID, hiddenID} {
				setGraphCommit(t, h, id, legacySHAX)
			}
			generation.publish(t, h, repositoryID, 101, legacySHAX)
			generation.publish(t, h, hiddenID, 202, legacySHAX)
			server := legacyGraphServer(t, h, map[string]authn.Principal{
				"user":      {Subject: "user", InstallationID: 10, RepositoryIDs: []int64{101, 102, 103}},
				"delegated": {Subject: "delegated", Method: "api_token", Delegated: true, InstallationID: 10, RepositoryIDs: []int64{101}},
				"other":     {Subject: "other", InstallationID: 20, RepositoryIDs: []int64{202}},
			})

			symbol := func(id, name string, line int) string {
				return fmt.Sprintf(`{"uid":"symbol:%s","name":"%s","kind":"function","file_path":"pkg/graph.go","language":"go","signature":"func %s()","repository_id":101,"range":{"start_line":%d,"start_character":5,"end_line":%d,"end_character":9},"test":false}`, id, name, name, line, line)
			}
			call := func(source, target string, line int, confidence string) string {
				return fmt.Sprintf(`{"source_repository_id":101,"target_repository_id":101,"source_uid":"symbol:%s","target_uid":"symbol:%s","kind":"calls","path":"pkg/graph.go","range":{"start_line":%d,"start_character":2,"end_line":%d,"end_character":8},"confidence":%s,"resolution_reason":"fixture"}`, source, target, line, line, confidence)
			}
			a, b, c, d := symbol("A", "Alpha", 2), symbol("B", "Beta", 6), symbol("C", "Gamma", 10), symbol("D", "Delta", 14)
			ab, bc, bd := call("A", "B", 3, "0.75"), call("B", "C", 7, "1"), call("B", "D", 8, "0.5")
			// Repository 102 is in scope and indexed but has no graph.
			tail := `"boundaries":[{"reason":"graph_missing","count":1}],"commits":{"acme/repo-101":"` + legacySHAX + `"}`
			for _, test := range []struct {
				operation string
				request   map[string]any
				want      string
			}{
				{"context", map[string]any{"repo": "acme/repo-101", "uid": "symbol:B", "relations": []string{"calls", "references"}},
					`{"status":"found","symbol":` + b + `,"incoming":{"calls":[` + ab + `]},"outgoing":{"calls":[` + bc + `,` + bd + `]},` + tail + `}`},
				{"context", map[string]any{"repo": 101, "name": "Beta", "kind": "function", "file_path": "pkg/graph.go", "relations": []string{"calls"}, "per_category_limit": 1, "per_category_offset": 1},
					`{"status":"found","symbol":` + b + `,"outgoing":{"calls":[` + bd + `]},` + tail + `}`},
				{"context", map[string]any{"repo": 101, "name": "Missing"}, `{"status":"not_found",` + tail + `}`},
				{"impact", map[string]any{"repo": 101, "target_uid": "symbol:A", "direction": "downstream", "relations": []string{"calls"}, "max_depth": 2},
					`{"status":"found","by_depth":{"1":[` + b + `],"2":[` + c + `,` + d + `]},"relations":[` + ab + `,` + bc + `,` + bd + `],` + tail + `,"partial":false}`},
				{"impact", map[string]any{"repo": 101, "target_uid": "symbol:D", "direction": "upstream", "min_confidence": 0.6, "max_depth": 2},
					`{"status":"found","by_depth":{},` + tail + `,"partial":false}`},
				{"trace", map[string]any{"repo": "acme/repo-101", "source_uid": "symbol:A", "target_uid": "symbol:C"},
					`{"status":"ok","nodes":[` + a + `,` + b + `,` + c + `],"relations":[` + ab + `,` + bc + `],` + tail + `}`},
				{"trace", map[string]any{"repo": 101, "source_uid": "symbol:C", "target_uid": "symbol:A"}, `{"status":"no_path",` + tail + `}`},
				// An indexed repository without a graph answers 200 with a boundary, not 409.
				{"context", map[string]any{"repo": 102, "name": "Beta"},
					`{"status":"not_found","boundaries":[{"repository_id":102,"repository":"acme/repo-102","reason":"graph_missing"}],"commits":{"acme/repo-101":"` + legacySHAX + `"}}`},
			} {
				status, body := legacyGraphREST(t, server, "user", test.operation, test.request)
				var got, want any
				if err := json.Unmarshal(body, &got); err != nil {
					t.Fatalf("%s %v: %v: %s", test.operation, test.request, err, body)
				}
				if err := json.Unmarshal([]byte(test.want), &want); err != nil {
					t.Fatalf("golden %s: %v", test.want, err)
				}
				if status != http.StatusOK || !reflect.DeepEqual(got, want) {
					t.Errorf("%s %v\n status=%d\n got  %s\n want %s", test.operation, test.request, status, bytes.TrimSpace(body), test.want)
					continue
				}
				if structured, message := legacyGraphMCP(t, server, "user", test.operation, test.request); message != "" || !reflect.DeepEqual(structured, want) {
					t.Errorf("%s %v MCP=%#v error=%q, want %s", test.operation, test.request, structured, message, test.want)
				}
			}

			// Errors and repository scope: same status, code and MCP message.
			for _, test := range []struct {
				token, operation string
				request          map[string]any
				status           int
				code, mcp        string
			}{
				{"user", "context", map[string]any{"repo": 202, "name": "Secret"}, http.StatusNotFound, "not_found", "repository not found"},
				{"user", "context", map[string]any{"repo": "acme/repo-202", "name": "Secret"}, http.StatusNotFound, "not_found", "repository not found"},
				{"user", "context", map[string]any{"repo": "repo-101", "name": "Beta"}, http.StatusNotFound, "not_found", "repository not found"},
				{"delegated", "impact", map[string]any{"repo": 102, "target_uid": "symbol:A", "direction": "upstream"}, http.StatusNotFound, "not_found", "repository not found"},
				{"other", "trace", map[string]any{"repo": 101, "source_uid": "symbol:A", "target_uid": "symbol:C"}, http.StatusNotFound, "not_found", "repository not found"},
				{"user", "context", map[string]any{"name": "Beta"}, http.StatusConflict, "ambiguous", "repository selection is ambiguous"},
				{"user", "context", map[string]any{"repo": 101, "branch": "release", "name": "Beta"}, http.StatusConflict, "branch_not_indexed", "branch is not indexed"},
				{"user", "context", map[string]any{"repo": 103, "name": "Beta"}, http.StatusConflict, "not_indexed", "repository is not indexed yet: graph queries need an indexed default branch; wait for indexing to finish"},
				{"user", "context", map[string]any{"repo": 101}, http.StatusBadRequest, "invalid_request", "graph request is invalid"},
				{"user", "impact", map[string]any{"repo": 101, "target_uid": "symbol:A", "direction": "sideways"}, http.StatusBadRequest, "invalid_request", "does not equal any of: [upstream downstream]"},
			} {
				status, body := legacyGraphREST(t, server, test.token, test.operation, test.request)
				var failure struct {
					Error struct {
						Code string `json:"code"`
					} `json:"error"`
				}
				if err := json.Unmarshal(body, &failure); err != nil || status != test.status || failure.Error.Code != test.code {
					t.Errorf("%s %s %v: status=%d body=%s, want %d %s", test.token, test.operation, test.request, status, bytes.TrimSpace(body), test.status, test.code)
				}
				if _, message := legacyGraphMCP(t, server, test.token, test.operation, test.request); !strings.Contains(message, test.mcp) {
					t.Errorf("%s %s %v: MCP error=%q, want %q", test.token, test.operation, test.request, message, test.mcp)
				}
			}

			// A delegated token narrowed to one repository reads that repository
			// and sees no other repository's graph, not even as a boundary.
			status, body := legacyGraphREST(t, server, "delegated", "context", map[string]any{"name": "Beta", "relations": []string{"calls"}})
			if want := `{"status":"found","symbol":` + b + `,"incoming":{"calls":[` + ab + `]},"outgoing":{"calls":[` + bc + `,` + bd + `]},"commits":{"acme/repo-101":"` + legacySHAX + `"}}`; status != http.StatusOK || !sameJSON(t, body, want) {
				t.Errorf("delegated context: status=%d body=%s", status, body)
			}
			// Another installation's graph never answers, by name or by UID.
			for _, request := range []map[string]any{{"repo": 101, "name": "Secret"}, {"repo": 101, "uid": "symbol:SECRET"}} {
				if status, body := legacyGraphREST(t, server, "user", "context", request); status != http.StatusOK || !sameJSON(t, body, `{"status":"not_found",`+tail+`}`) {
					t.Errorf("hidden symbol %v: status=%d body=%s", request, status, body)
				}
			}
			if status, body := legacyGraphREST(t, server, "other", "context", map[string]any{"name": "Secret"}); status != http.StatusOK || !bytes.Contains(body, []byte(`"uid":"symbol:SECRET"`)) || bytes.Contains(body, []byte("repo-101")) {
				t.Errorf("other installation: status=%d body=%s", status, body)
			}
		})
	}
}

// TestLegacyGraphQueriesFollowTheIndexedCommit walks one repository through
// the staleness cases: whichever generation is at the indexed commit answers,
// and with none there the answer is 200 not_found with a graph_missing
// boundary, as it has always been for the v1 calls.
func TestLegacyGraphQueriesFollowTheIndexedCommit(t *testing.T) {
	h := newPostgresHarness(t)
	repositoryID := h.seedRepository(t, 10, 101)
	setGraphCommit(t, h, repositoryID, legacySHAX)
	server := legacyGraphServer(t, h, map[string]authn.Principal{"user": {Subject: "user", InstallationID: 10, RepositoryIDs: []int64{101}}})
	request := map[string]any{"repo": 101, "name": "Beta", "relations": []string{"calls"}}
	expect := func(step, wantStatus, wantCommit, wantBoundary string) {
		t.Helper()
		status, body := legacyGraphREST(t, server, "user", "context", request)
		var response struct {
			Status     string            `json:"status"`
			Commits    map[string]string `json:"commits"`
			Boundaries []struct {
				Reason     string `json:"reason"`
				Repository string `json:"repository"`
			} `json:"boundaries"`
			Symbol *struct {
				UID string `json:"uid"`
			} `json:"symbol"`
		}
		if err := json.Unmarshal(body, &response); err != nil || status != http.StatusOK || response.Status != wantStatus || response.Commits["acme/repo-101"] != wantCommit {
			t.Fatalf("%s: status=%d body=%s err=%v", step, status, body, err)
		}
		if wantBoundary == "" && len(response.Boundaries) != 0 || wantBoundary != "" && (len(response.Boundaries) != 1 || response.Boundaries[0].Reason != wantBoundary || response.Boundaries[0].Repository != "acme/repo-101") {
			t.Fatalf("%s: boundaries=%#v, want %q", step, response.Boundaries, wantBoundary)
		}
		if wantStatus == "found" && (response.Symbol == nil || response.Symbol.UID != "symbol:B") {
			t.Fatalf("%s: symbol=%#v", step, response.Symbol)
		}
	}

	expect("no graph yet", "not_found", "", "graph_missing")
	publishLegacyV1(t, h, repositoryID, 101, legacySHAX)
	expect("v1 at the indexed commit", "found", legacySHAX, "")

	setGraphCommit(t, h, repositoryID, legacySHAY)
	expect("v1 stale, no v2", "not_found", "", "graph_missing")
	publishLegacyV2(t, h, repositoryID, 101, legacySHAY)
	expect("v2 at the indexed commit, v1 stale", "found", legacySHAY, "")

	setGraphCommit(t, h, repositoryID, legacySHAZ)
	expect("both stale", "not_found", "", "graph_missing")
	publishLegacyV1(t, h, repositoryID, 101, legacySHAZ)
	expect("v1 at the indexed commit, v2 stale", "found", legacySHAZ, "")
}

func legacyGraphServer(t *testing.T, h *postgresHarness, principals map[string]authn.Principal) *httptest.Server {
	t.Helper()
	authenticator := authn.NewStatic(principals)
	service := &graphservice.Service{Store: h.store, Backend: &graphquery.Service{Store: h.store}, Limits: graphservice.Limits{MaxResponseBytes: 256 << 10}}
	mux := http.NewServeMux()
	httpapi.RegisterGraphQueries(mux, authenticator, service, 64<<10, 256<<10)
	mux.Handle("/mcp", httpapi.AuthenticateBearer(authenticator, mcpserver.HTTPHandler(mcpserver.NewWithLimits(mcpserver.Services{Graph: service}, mcpserver.Limits{MaxOutputBytes: 256 << 10, GraphMaxOutputBytes: 256 << 10}))))
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	return server
}

func legacyGraphREST(t *testing.T, server *httptest.Server, token, operation string, input any) (int, []byte) {
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
	return response.StatusCode, data
}

// legacyGraphMCP returns the tool's structured result, or its error text.
func legacyGraphMCP(t *testing.T, server *httptest.Server, token, operation string, input map[string]any) (any, string) {
	t.Helper()
	client := *server.Client()
	client.Transport = tokenTransport{base: client.Transport, token: token}
	session, err := mcp.NewClient(&mcp.Implementation{Name: "graph-legacy", Version: "1"}, nil).Connect(t.Context(), &mcp.StreamableClientTransport{
		Endpoint: server.URL + "/mcp", HTTPClient: &client, DisableStandaloneSSE: true,
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	result, err := session.CallTool(t.Context(), &mcp.CallToolParams{Name: operation, Arguments: input})
	if err != nil {
		return nil, err.Error()
	}
	if result.IsError {
		text := "tool error"
		for _, content := range result.Content {
			if value, ok := content.(*mcp.TextContent); ok {
				text = value.Text
			}
		}
		return nil, text
	}
	return result.StructuredContent, ""
}

type tokenTransport struct {
	base  http.RoundTripper
	token string
}

func (transport tokenTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	cloned := request.Clone(request.Context())
	cloned.Header.Set("Authorization", "Bearer "+transport.token)
	return transport.base.RoundTrip(cloned)
}

func sameJSON(t *testing.T, got []byte, want string) bool {
	t.Helper()
	var left, right any
	if err := json.Unmarshal(got, &left); err != nil {
		return false
	}
	if err := json.Unmarshal([]byte(want), &right); err != nil {
		t.Fatalf("golden %s: %v", want, err)
	}
	return reflect.DeepEqual(left, right)
}

// legacyFunctions is the call graph both fixtures publish: Alpha calls Beta,
// Beta calls Gamma and Delta. Repository 202 holds one unrelated symbol.
var legacyFunctions = []struct {
	id, name string
	line     int32
}{{"A", "Alpha", 2}, {"B", "Beta", 6}, {"C", "Gamma", 10}, {"D", "Delta", 14}}

var legacyCalls = []struct {
	source, target string
	line           int32
	confidence     float64
}{{"A", "B", 3, 0.75}, {"B", "C", 7, 1}, {"B", "D", 8, 0.5}}

func publishLegacyV1(t *testing.T, h *postgresHarness, repositoryID, githubID int64, commit string) {
	t.Helper()
	artifact := graphartifact.Artifact{
		SchemaVersion: 1, RepositoryID: repositoryID, Commit: commit,
		Analyzer: graphartifact.Analyzer{Name: "contract", Version: "1"}, ContentHash: bytes.Repeat([]byte{byte(repositoryID), commit[0]}, 16),
		Nodes: []graphartifact.Node{{UID: "repository", Kind: graphartifact.NodeRepository}, {UID: "file:pkg/graph.go", Kind: graphartifact.NodeFile, Path: "pkg/graph.go", QualifiedName: "graph.go"}},
		Edges: []graphartifact.Edge{{SourceUID: "repository", TargetUID: "file:pkg/graph.go", Kind: graphartifact.EdgeContains, Confidence: 1}},
	}
	if githubID == 202 {
		artifact.Nodes = append(artifact.Nodes, graphartifact.Node{UID: "symbol:SECRET", Kind: graphartifact.NodeSymbol, Path: "pkg/graph.go", Language: "go", SymbolKind: "function", QualifiedName: "Secret"})
	} else {
		for _, function := range legacyFunctions {
			artifact.Nodes = append(artifact.Nodes, graphartifact.Node{UID: "symbol:" + function.id, Kind: graphartifact.NodeSymbol, Path: "pkg/graph.go", Language: "go", SymbolKind: "function", QualifiedName: function.name, Signature: "func " + function.name + "()",
				Range: graphartifact.Range{StartLine: function.line, StartCharacter: 5, EndLine: function.line, EndCharacter: 9}})
			// File-to-symbol edges must never surface as symbol relations.
			artifact.Edges = append(artifact.Edges, graphartifact.Edge{SourceUID: "file:pkg/graph.go", TargetUID: "symbol:" + function.id, Kind: graphartifact.EdgeReferences, Path: "pkg/graph.go", Confidence: 1})
		}
		for _, call := range legacyCalls {
			artifact.Edges = append(artifact.Edges, graphartifact.Edge{SourceUID: "symbol:" + call.source, TargetUID: "symbol:" + call.target, Kind: graphartifact.EdgeCalls, Path: "pkg/graph.go", Confidence: float32(call.confidence), ResolutionReason: "fixture",
				Range: graphartifact.Range{StartLine: call.line, StartCharacter: 2, EndLine: call.line, EndCharacter: 8}})
		}
	}
	if replacement, err := h.store.ReplaceGraph(t.Context(), repositoryID, postgres.GraphSourceManaged, artifact); err != nil || !replacement.Applied {
		t.Fatalf("ReplaceGraph()=%#v err=%v", replacement, err)
	}
}

func publishLegacyV2(t *testing.T, h *postgresHarness, repositoryID, githubID int64, commit string) {
	t.Helper()
	where := func(line, startCharacter, endCharacter int32) *graphv2.Location {
		return &graphv2.Location{Path: proto.String("pkg/graph.go"), Start: &graphv2.Position{Line: proto.Int32(line), Character: proto.Int32(startCharacter)}, End: &graphv2.Position{Line: proto.Int32(line), Character: proto.Int32(endCharacter)}}
	}
	artifact := &graphv2.Artifact{
		SchemaVersion: 2, Repository: fmt.Sprint(githubID), Commit: commit,
		Producer: &graphv2.Producer{Name: "codegraph", Version: "1.6.0", Configuration: "portable"},
		Nodes:    []*graphv2.Node{{SourceId: "file", Occurrence: "file:pkg/graph.go", Kind: "file", Name: "graph.go", QualifiedName: "pkg/graph.go", Path: proto.String("pkg/graph.go"), Language: "go"}},
	}
	if githubID == 202 {
		artifact.Nodes = append(artifact.Nodes, &graphv2.Node{SourceId: "SECRET", Occurrence: "SECRET", Kind: "function", Name: "Secret", QualifiedName: "pkg.Secret", Path: proto.String("pkg/graph.go"), Language: "go"})
	} else {
		for _, function := range legacyFunctions {
			artifact.Nodes = append(artifact.Nodes, &graphv2.Node{SourceId: function.id, Occurrence: function.id, Kind: "function", Name: function.name, QualifiedName: "pkg." + function.name, Path: proto.String("pkg/graph.go"), Language: "go",
				Signature: proto.String("func " + function.name + "()"), Location: where(function.line, 5, 9)})
			artifact.Edges = append(artifact.Edges, &graphv2.Edge{SourceId: "file-" + function.id, Occurrence: "file-" + function.id, Source: "file:pkg/graph.go", Target: function.id, Kind: graphv2.EdgeKind_EDGE_KIND_REFERENCES, Confidence: proto.Float64(1)})
		}
		for _, call := range legacyCalls {
			id := "call-" + call.source + call.target
			artifact.Edges = append(artifact.Edges, &graphv2.Edge{SourceId: id, Occurrence: id, Source: call.source, Target: call.target, Kind: graphv2.EdgeKind_EDGE_KIND_CALLS, Confidence: proto.Float64(call.confidence), ResolutionReason: proto.String("fixture"), Location: where(call.line, 2, 8)})
		}
	}
	var expected int64
	if active, err := h.store.ActiveGraphGeneration(t.Context(), repositoryID); err != nil {
		t.Fatal(err)
	} else if active != nil {
		expected = active.ID
	}
	if replacement, err := h.store.ReplaceGraphV2(t.Context(), repositoryID, postgres.GraphPublication{Publisher: "api_token:1", ExpectedActiveID: expected}, artifact); err != nil || !replacement.Applied {
		t.Fatalf("ReplaceGraphV2()=%#v err=%v", replacement, err)
	}
}
