//go:build e2e && unix

package e2e

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/grepnest/grepnest/internal/authn"
	"github.com/grepnest/grepnest/internal/githubapp"
	"github.com/grepnest/grepnest/internal/graphartifact"
	"github.com/grepnest/grepnest/internal/graphclient"
	"github.com/grepnest/grepnest/internal/graphingest"
	"github.com/grepnest/grepnest/internal/graphquery"
	"github.com/grepnest/grepnest/internal/graphruntime"
	"github.com/grepnest/grepnest/internal/graphscan"
	"github.com/grepnest/grepnest/internal/graphscan/golang"
	"github.com/grepnest/grepnest/internal/graphscan/java"
	"github.com/grepnest/grepnest/internal/graphscan/javascript"
	"github.com/grepnest/grepnest/internal/graphscan/kotlin"
	"github.com/grepnest/grepnest/internal/graphscan/rust"
	"github.com/grepnest/grepnest/internal/graphscanner"
	"github.com/grepnest/grepnest/internal/graphservice"
	"github.com/grepnest/grepnest/internal/httpapi"
	"github.com/grepnest/grepnest/internal/mcpserver"
	"github.com/grepnest/grepnest/internal/postgres"
	"github.com/grepnest/grepnest/internal/repository"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestGraphLanguageFixturesResolveCrossFileCalls(t *testing.T) {
	parsers := map[string]graphscan.Parser{
		".go": golang.Parse, ".js": javascript.Parse, ".ts": javascript.Parse,
		".tsx": javascript.Parse, ".java": java.Parse, ".kt": kotlin.Parse,
		".rs": rust.Parse,
	}
	for _, test := range []struct {
		name         string
		relationship graphartifact.EdgeKind
	}{
		{"go", graphartifact.EdgeImplements},
		{"typescript", graphartifact.EdgeExtends},
		{"javascript", graphartifact.EdgeImports},
		{"java", graphartifact.EdgeImplements},
		{"kotlin", graphartifact.EdgeImplements},
		{"rust", graphartifact.EdgeImplements},
	} {
		t.Run(test.name, func(t *testing.T) {
			artifact, err := graphscan.Scan(t.Context(), graphscan.Request{
				RepositoryID: 101,
				Commit:       strings.Repeat("a", 40),
				Root:         filepath.Join("..", "fixtures", "graph", test.name),
			}, parsers, graphscan.Limits{
				MaxFileBytes: 1 << 20, MaxTotalBytes: 8 << 20, MaxFiles: 100,
				MaxNodes: 1000, MaxEdges: 2000, ParseTimeout: 5 * time.Second,
			})
			if err != nil {
				t.Fatal(err)
			}
			if !hasGraphEdge(artifact, graphartifact.EdgeCalls) {
				t.Fatalf("fixture has no resolved cross-file call: %#v", artifact.Edges)
			}
			if !hasGraphEdge(artifact, test.relationship) {
				t.Fatalf("fixture has no %v relationship: %#v", test.relationship, artifact.Edges)
			}
			if test.name == "typescript" &&
				(!hasGraphPathLanguage(artifact, "service.ts", "typescript") ||
					!hasGraphPathLanguage(artifact, "component.tsx", "typescript")) {
				t.Fatalf("fixture did not exercise TS and TSX: %#v", artifact.Nodes)
			}
		})
	}
}

func TestGraphLanguageFixturesReachRESTAndMCP(t *testing.T) {
	database := newMilestoneDatabase(t)
	parsers := graphFixtureParsers()
	type fixtureResult struct {
		githubID int64
		name     string
		commit   string
		source   string
		target   string
		artifact graphartifact.Artifact
	}
	tests := []struct {
		name, source, target string
	}{
		{"go", "fixture.Call", "fixture.CrossFile"},
		{"typescript", "Service.run", "work"},
		{"javascript", "run", "work"},
		{"java", "fixture.Main.call", "fixture.Helper.execute"},
		{"kotlin", "fixture.call", "fixture.Runner"},
		{"rust", "lib::call", "worker::run"},
	}
	results := make([]fixtureResult, 0, len(tests))
	for index, test := range tests {
		githubID := int64(1101 + index)
		checkout, commit := graphFixtureRepository(t, filepath.Join("..", "fixtures", "graph", test.name))
		repositoryRow := graphFixtureRepositoryRow(t, database, githubID, test.name, commit)
		if _, err := database.pool.Exec(t.Context(), `insert into graph_jobs(repository_id,target_sha,state,max_attempts) values($1,$2,'queued',3)`, repositoryRow.ID, commit); err != nil {
			t.Fatal(err)
		}
		analyzer := &graphFixtureAnalyzer{parsers: parsers}
		worker := &graphscanner.Worker{
			ID: "fixture-" + test.name, Queue: database.store, Store: database.store,
			Tokens: graphFixtureTokens{}, Git: &graphFixtureCheckout{repository: checkout, root: t.TempDir()},
			Analyzer: analyzer, RenewEvery: time.Hour,
		}
		if worked, err := worker.RunOne(t.Context()); err != nil || !worked {
			t.Fatalf("%s managed scan worked=%t err=%v", test.name, worked, err)
		}
		if gotSource, gotTarget := graphCallNames(analyzer.artifact); gotSource != test.source || gotTarget != test.target {
			t.Fatalf("%s call=%q -> %q, want %q -> %q", test.name, gotSource, gotTarget, test.source, test.target)
		}
		results = append(results, fixtureResult{
			githubID: githubID, name: "fixtures/" + test.name, commit: commit,
			source: test.source, target: test.target, artifact: analyzer.artifact,
		})
	}

	secret := []byte("graph-e2e-secret")
	address := freeAddress(t)
	graphRuntime, err := graphruntime.New(t.Context(), graphruntime.Config{
		DatabasePath: filepath.Join(t.TempDir(), "grepnest.lbug"), ListenAddress: address,
		InternalSecret: secret, ReadConnections: 2, SyncInterval: 20 * time.Millisecond,
		QueryTimeout: 5 * time.Second, InterruptGrace: 2 * time.Second,
		QueryLimits: graphquery.Limits{
			PerCategory: 10, DefaultImpactDepth: 3, MaxDepth: 5,
			DefaultTraceDepth: 3, MaxTraceDepth: 5, MaxRows: 100,
			MaxNodes: 100, MaxEdges: 100, MaxFanout: 20,
		},
	}, database.store, nil)
	if err != nil {
		t.Fatal(err)
	}
	runtimeCtx, stopRuntime := context.WithCancel(t.Context())
	runtimeDone := make(chan error, 1)
	go func() { runtimeDone <- graphRuntime.Run(runtimeCtx) }()
	t.Cleanup(func() {
		stopRuntime()
		if err := <-runtimeDone; err != nil {
			t.Error(err)
		}
		if err := graphRuntime.Close(); err != nil {
			t.Error(err)
		}
	})
	graphWaitReady(t, address)
	backend, err := graphclient.New("http://"+address, secret, http.DefaultClient, 256<<10)
	if err != nil {
		t.Fatal(err)
	}
	repositoryIDs := make([]int64, len(results))
	for index := range results {
		repositoryIDs[index] = results[index].githubID
	}
	principal := authn.Principal{InstallationID: 10, RepositoryIDs: repositoryIDs}
	authenticator := authn.NewStatic(map[string]authn.Principal{"user": principal})
	graphService := &graphservice.Service{Store: database.store, Backend: backend}
	graphIngest := &graphingest.Service{Store: database.store}
	mux := http.NewServeMux()
	httpapi.RegisterGraphQueries(mux, authenticator, graphService, 64<<10, 256<<10)
	httpapi.RegisterGraphIngestion(mux, authenticator, graphIngest, 1<<20, 256<<10)
	mux.Handle("/mcp", httpapi.AuthenticateBearer(authenticator, mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server {
		return mcpserver.NewWithLimits(mcpserver.Services{Graph: graphService}, mcpserver.Limits{MaxOutputBytes: 256 << 10, GraphMaxOutputBytes: 256 << 10})
	}, nil)))
	server := httptest.NewServer(mux)
	defer server.Close()

	for _, result := range results {
		sourceUID := graphUID(result.artifact, result.source)
		input := map[string]any{"repo": result.githubID, "uid": sourceUID, "relations": []string{"calls"}}
		rest := graphFixtureREST(t, server, result.name, input)
		mcpValue := graphFixtureMCP(t, server, input)
		if !reflect.DeepEqual(rest, mcpValue) {
			t.Fatalf("%s REST/MCP mismatch:\nREST=%#v\nMCP=%#v", result.name, rest, mcpValue)
		}
		if rest["status"] != "found" ||
			rest["commits"].(map[string]any)[result.name] != result.commit ||
			graphContextTarget(rest) != graphUID(result.artifact, result.target) {
			t.Fatalf("%s context=%#v", result.name, rest)
		}
		status := graphFixtureStatus(t, server, result.githubID)
		if status["state"] != "ready" || status["source"] != "managed" || status["commit"] != result.commit {
			t.Fatalf("%s status=%#v", result.name, status)
		}
	}
}

func hasGraphEdge(artifact graphartifact.Artifact, kind graphartifact.EdgeKind) bool {
	for _, edge := range artifact.Edges {
		if edge.Kind == kind {
			return true
		}
	}
	return false
}

func hasGraphPathLanguage(artifact graphartifact.Artifact, path, language string) bool {
	for _, node := range artifact.Nodes {
		if node.Path == path && node.Language == language {
			return true
		}
	}
	return false
}

func graphFixtureParsers() map[string]graphscan.Parser {
	return map[string]graphscan.Parser{
		".go": golang.Parse, ".js": javascript.Parse, ".ts": javascript.Parse,
		".tsx": javascript.Parse, ".java": java.Parse, ".kt": kotlin.Parse, ".rs": rust.Parse,
	}
}

type graphFixtureAnalyzer struct {
	parsers  map[string]graphscan.Parser
	artifact graphartifact.Artifact
}

func (analyzer *graphFixtureAnalyzer) Scan(ctx context.Context, request graphscan.Request) (graphartifact.Artifact, error) {
	artifact, err := graphscan.Scan(ctx, request, analyzer.parsers, graphscan.Limits{
		MaxFileBytes: 1 << 20, MaxTotalBytes: 8 << 20, MaxFiles: 100,
		MaxNodes: 1000, MaxEdges: 2000, ParseTimeout: 5 * time.Second,
	})
	analyzer.artifact = artifact
	return artifact, err
}

type graphFixtureTokens struct{}

func (graphFixtureTokens) InstallationToken(context.Context, int64, []int64) (githubapp.Token, error) {
	return githubapp.Token{Value: "fixture-token"}, nil
}

type graphFixtureCheckout struct {
	repository string
	root       string
	worktree   string
}

func (checkout *graphFixtureCheckout) PrepareCommit(ctx context.Context, _ repository.Repository, jobID int64, commit, _ string) (string, string, error) {
	checkout.worktree = filepath.Join(checkout.root, fmt.Sprintf("%d", jobID))
	command := exec.CommandContext(ctx, "git", "-C", checkout.repository, "worktree", "add", "--detach", checkout.worktree, commit)
	if output, err := command.CombinedOutput(); err != nil {
		return "", "", fmt.Errorf("git worktree add: %w: %s", err, output)
	}
	return checkout.repository, checkout.worktree, nil
}

func (checkout *graphFixtureCheckout) Cleanup(ctx context.Context, _, _ int64) error {
	if checkout.worktree == "" {
		return nil
	}
	command := exec.CommandContext(ctx, "git", "-C", checkout.repository, "worktree", "remove", "--force", checkout.worktree)
	if output, err := command.CombinedOutput(); err != nil {
		return fmt.Errorf("git worktree remove: %w: %s", err, output)
	}
	return nil
}

func graphFixtureRepository(t *testing.T, fixture string) (string, string) {
	t.Helper()
	root := t.TempDir()
	entries, err := os.ReadDir(fixture)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if entry.IsDir() {
			t.Fatalf("nested fixture directory %s", entry.Name())
		}
		data, err := os.ReadFile(filepath.Join(fixture, entry.Name()))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, entry.Name()), data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	run(t, t.Context(), "git", "-C", root, "init", "-q")
	run(t, t.Context(), "git", "-C", root, "config", "user.name", "Graph Fixture")
	run(t, t.Context(), "git", "-C", root, "config", "user.email", "graph@example.invalid")
	run(t, t.Context(), "git", "-C", root, "add", ".")
	run(t, t.Context(), "git", "-C", root, "-c", "commit.gpgsign=false", "commit", "-q", "-m", "fixture")
	return root, strings.TrimSpace(run(t, t.Context(), "git", "-C", root, "rev-parse", "HEAD"))
}

func graphFixtureRepositoryRow(t *testing.T, database milestoneDatabase, githubID int64, name, commit string) repository.Repository {
	t.Helper()
	if err := database.store.UpsertInstallation(t.Context(), postgres.InstallationUpdate{
		GitHubID: 10, AccountLogin: "fixtures", AccountType: "Organization", Status: "active",
	}); err != nil {
		t.Fatal(err)
	}
	value, err := database.store.UpsertRepository(t.Context(), postgres.RepositoryUpdate{
		GitHubID: githubID, InstallationID: 10, Owner: "fixtures", Name: name,
		CloneURL: "https://example.invalid/" + name + ".git", WebURL: "https://example.invalid/" + name,
		DefaultBranch: "main", Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.pool.Exec(t.Context(), `update repositories set indexed_sha=$2, status='ready' where id=$1`, value.ID, commit); err != nil {
		t.Fatal(err)
	}
	value.IndexedSHA = commit
	return value
}

func graphCallNames(artifact graphartifact.Artifact) (string, string) {
	names := map[string]string{}
	for _, node := range artifact.Nodes {
		names[node.UID] = node.QualifiedName
	}
	for _, edge := range artifact.Edges {
		if edge.Kind == graphartifact.EdgeCalls {
			return names[edge.SourceUID], names[edge.TargetUID]
		}
	}
	return "", ""
}

func graphUID(artifact graphartifact.Artifact, qualifiedName string) string {
	for _, node := range artifact.Nodes {
		if node.QualifiedName == qualifiedName {
			return node.UID
		}
	}
	return ""
}

func graphWaitReady(t *testing.T, address string) {
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
			t.Fatalf("graph runtime did not become ready: %v", err)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func graphFixtureREST(t *testing.T, server *httptest.Server, name string, input map[string]any) map[string]any {
	t.Helper()
	return graphFixtureRequest(t, server, http.MethodPost, "/v1/graph/context", input, name)
}

func graphFixtureStatus(t *testing.T, server *httptest.Server, repositoryID int64) map[string]any {
	t.Helper()
	return graphFixtureRequest(t, server, http.MethodGet, fmt.Sprintf("/v1/graph/repositories/%d/status", repositoryID), nil, "")
}

func graphFixtureRequest(t *testing.T, server *httptest.Server, method, path string, input any, name string) map[string]any {
	t.Helper()
	var body io.Reader
	if input != nil {
		data, err := json.Marshal(input)
		if err != nil {
			t.Fatal(err)
		}
		body = bytes.NewReader(data)
	}
	request, err := http.NewRequestWithContext(t.Context(), method, server.URL+path, body)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer user")
	if input != nil {
		request.Header.Set("Content-Type", "application/json")
	}
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
		t.Fatalf("%s %s status=%d body=%s", name, path, response.StatusCode, data)
	}
	var value map[string]any
	if err := json.Unmarshal(data, &value); err != nil {
		t.Fatal(err)
	}
	return value
}

func graphFixtureMCP(t *testing.T, server *httptest.Server, input map[string]any) map[string]any {
	t.Helper()
	client := *server.Client()
	client.Transport = graphBearerTransport{base: client.Transport}
	session, err := mcp.NewClient(&mcp.Implementation{Name: "graph-e2e", Version: "1"}, nil).Connect(t.Context(), &mcp.StreamableClientTransport{
		Endpoint: server.URL + "/mcp", HTTPClient: &client, DisableStandaloneSSE: true,
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	result, err := session.CallTool(t.Context(), &mcp.CallToolParams{Name: "context", Arguments: input})
	if err != nil || result.IsError {
		t.Fatalf("MCP context result=%#v err=%v", result, err)
	}
	data, err := json.Marshal(result.StructuredContent)
	if err != nil {
		t.Fatal(err)
	}
	var value map[string]any
	if err := json.Unmarshal(data, &value); err != nil {
		t.Fatal(err)
	}
	return value
}

type graphBearerTransport struct{ base http.RoundTripper }

func (transport graphBearerTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	if transport.base == nil {
		return nil, errors.New("HTTP transport is required")
	}
	cloned := request.Clone(request.Context())
	cloned.Header.Set("Authorization", "Bearer user")
	return transport.base.RoundTrip(cloned)
}

func graphContextTarget(value map[string]any) string {
	outgoing, _ := value["outgoing"].(map[string]any)
	calls, _ := outgoing["calls"].([]any)
	if len(calls) != 1 {
		return ""
	}
	target, _ := calls[0].(map[string]any)["target_uid"].(string)
	return target
}
