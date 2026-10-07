//go:build integration

package integration

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/balcsida/graphnest/internal/authn"
	graphv2 "github.com/balcsida/graphnest/internal/graphartifact/v2"
	"github.com/balcsida/graphnest/internal/graphquery"
	"github.com/balcsida/graphnest/internal/graphservice"
	"github.com/balcsida/graphnest/internal/httpapi"
	"github.com/balcsida/graphnest/internal/mcpserver"
	"github.com/balcsida/graphnest/internal/postgres"
	"github.com/balcsida/graphnest/internal/repository"
	"github.com/balcsida/graphnest/internal/scipgraph"
	"github.com/balcsida/graphnest/pkg/api"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const (
	precedenceSHAX = "4444444444444444444444444444444444444444"
	precedenceSHAY = "5555555555555555555555555555555555555555"
)

// TestSCIPUploadAfterPublishedGenerationAdvances pins the two-slot rule: a
// publisher's v2 generation (producer codegraph) is active for commit X, the
// default branch advances to Y, and a SCIP upload for Y arrives. The stale
// published generation stays in its slot, the SCIP-derived generation for Y
// fills the other, and every v2 graph tool answers from the latter. A
// publisher that then catches up to Y takes precedence again.
func TestSCIPUploadAfterPublishedGenerationAdvances(t *testing.T) {
	h := newPostgresHarness(t)
	repositoryID := h.seedRepository(t, 10, 101)
	setGraphCommit(t, h, repositoryID, precedenceSHAX)
	if err := h.store.UpsertSearchNode(t.Context(), "node-a", "http://zoekt.invalid"); err != nil {
		t.Fatal(err)
	}

	// A publisher's CodeGraph generation at X.
	published, err := h.store.ReplaceGraphV2(t.Context(), repositoryID, postgres.GraphPublication{Publisher: "api_token:1"}, precedenceArtifact(precedenceSHAX))
	if err != nil || !published.Applied {
		t.Fatalf("publish=%#v err=%v", published, err)
	}

	// The default branch advances to Y and the CI job uploads a SCIP index for Y.
	setGraphCommit(t, h, repositoryID, precedenceSHAY)
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
	request, err := http.NewRequestWithContext(t.Context(), http.MethodPost, fmt.Sprintf("%s/v1/scip/uploads?repository_id=101&commit=%s", server.URL, precedenceSHAY), bytes.NewReader(index))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer admin")
	request.Header.Set("Content-Type", "application/vnd.scip+protobuf")
	response, err := server.Client().Do(request)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusNoContent {
		t.Fatalf("SCIP upload for Y: status=%d", response.StatusCode)
	}

	// 1. The stale CodeGraph generation is still the published generation, and
	//    the derived SCIP generation for Y sits in its own slot.
	active, err := h.store.ActiveGraphGeneration(t.Context(), repositoryID)
	if err != nil || active == nil || active.ID != published.Upload.ID || active.Producer != "codegraph" || active.Commit != precedenceSHAX {
		t.Fatalf("published generation=%#v err=%v", active, err)
	}
	both, err := h.store.ActiveGraphGenerations(t.Context(), repositoryID)
	if err != nil || len(both) != 2 || both[0].ID != active.ID || both[1].Source != api.GraphSourceSCIP || both[1].Commit != precedenceSHAY {
		t.Fatalf("active v2 generations=%#v err=%v", both, err)
	}
	var v1Source, v1Commit string
	if err := h.pool.QueryRow(t.Context(), `select source, commit from graph_uploads where repository_id=$1 and active and schema_version=1`, repositoryID).Scan(&v1Source, &v1Commit); err != nil || v1Source != "scip" || v1Commit != precedenceSHAY {
		t.Fatalf("active v1 generation=%s@%s err=%v", v1Source, v1Commit, err)
	}

	// 2. Repository status reports the generation the tools use: SCIP-derived at Y.
	principal := authn.Principal{Subject: "user", InstallationID: 10, RepositoryIDs: []int64{101}}
	repositories := &repository.Service{Store: h.store, SCIP: h.store, Graph: h.store}
	status, err := repositories.Status(t.Context(), principal, 101)
	if err != nil {
		t.Fatal(err)
	}
	if status.GraphStatus != api.GraphStatusCurrent || status.GraphCommit != precedenceSHAY || status.GraphProducer != "scip" || status.SCIPStatus != api.SCIPStatusCurrent || status.SCIPCommit != precedenceSHAY {
		t.Fatalf("status=%#v", status)
	}

	// 3. The v2 graph tools are ready for Y, answered by the SCIP-derived generation.
	capabilities := httptest.NewRequest(http.MethodPost, "/v1/graph/capabilities", strings.NewReader(`{"repo":101}`))
	capabilities.Header.Set("Authorization", "Bearer user")
	capabilities.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	mux.ServeHTTP(recorder, capabilities)
	var ready struct {
		Status               string `json:"status"`
		CurrentIndexedCommit string `json:"current_indexed_commit"`
		Generation           struct {
			Generation int64 `json:"generation"`
			Producer   struct {
				Name string `json:"name"`
			} `json:"producer"`
		} `json:"generation"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &ready); err != nil || recorder.Code != http.StatusOK || ready.Status != "ready" || ready.CurrentIndexedCommit != precedenceSHAY || ready.Generation.Generation != both[1].ID || ready.Generation.Producer.Name != "scip" {
		t.Fatalf("graph_capabilities: status=%d body=%s err=%v", recorder.Code, recorder.Body.String(), err)
	}

	// 4. SCIP navigation answers for Y.
	references := graphMCP(t, server, "navigate_symbol", map[string]any{"repository_id": 101, "path": "greet/greet.go", "line": 11, "character": 5, "operation": "references"}).(map[string]any)
	if locations, _ := references["locations"].([]any); len(locations) != 2 {
		t.Fatalf("navigate_symbol references=%#v", references)
	}

	// 5. The publisher catches up to Y against its own slot, with no producer
	//    change, and its generation takes precedence again.
	caughtUp, err := h.store.ReplaceGraphV2(t.Context(), repositoryID, postgres.GraphPublication{Publisher: "api_token:1", ExpectedActiveID: published.Upload.ID}, precedenceArtifact(precedenceSHAY))
	if err != nil || !caughtUp.Applied || caughtUp.ReplacedID != published.Upload.ID {
		t.Fatalf("catch-up=%#v err=%v", caughtUp, err)
	}
	if status, err = repositories.Status(t.Context(), principal, 101); err != nil || status.GraphStatus != api.GraphStatusCurrent || status.GraphCommit != precedenceSHAY || status.GraphProducer != "codegraph" {
		t.Fatalf("status after catch-up=%#v err=%v", status, err)
	}
	generations, err := h.store.EntityGenerations(t.Context(), []graphquery.QuerySnapshot{{RepositoryID: repositoryID, Commit: precedenceSHAY}})
	if err != nil || len(generations) != 1 || generations[0].UploadID != caughtUp.Upload.ID {
		t.Fatalf("generations after catch-up=%#v err=%v", generations, err)
	}
}

func precedenceArtifact(commit string) *graphv2.Artifact {
	return &graphv2.Artifact{
		SchemaVersion: 2, Repository: "101", Commit: commit,
		Producer: &graphv2.Producer{Name: "codegraph", Version: "1.6.0", Configuration: "portable"},
		Nodes:    []*graphv2.Node{{SourceId: "a", Occurrence: "declaration:1", Kind: "function", Name: "Hello"}, {SourceId: "b", Occurrence: "declaration:2", Kind: "class"}},
	}
}
