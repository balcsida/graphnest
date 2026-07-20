//go:build e2e

package e2e

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/grepnest/grepnest/internal/authn"
	"github.com/grepnest/grepnest/internal/authz"
	"github.com/grepnest/grepnest/internal/httpapi"
	"github.com/grepnest/grepnest/internal/mcpserver"
	"github.com/grepnest/grepnest/internal/observability"
	"github.com/grepnest/grepnest/internal/repository"
	"github.com/grepnest/grepnest/internal/search"
	"github.com/grepnest/grepnest/internal/zoekt"
	"github.com/grepnest/grepnest/pkg/api"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const (
	fixtureName = "fixture/repository"
	needle      = "GrepNestFixtureNeedle"
	token       = "e2e-secret"
)

func TestPinnedFixtureSearch(t *testing.T) {
	zoektGitIndex, zoektWebserver := requiredExecutables(t)
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()

	repo := filepath.Join(t.TempDir(), "repository")
	index := filepath.Join(t.TempDir(), "index")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	copyFixture(t, repo)
	run(t, ctx, "git", "init", "--initial-branch=main", repo)
	run(t, ctx, "git", "-C", repo, "config", "user.name", "GrepNest Test")
	run(t, ctx, "git", "-C", repo, "config", "user.email", "test@grepnest.invalid")
	run(t, ctx, "git", "-C", repo, "config", "zoekt.repoid", "7")
	run(t, ctx, "git", "-C", repo, "config", "zoekt.name", fixtureName)
	run(t, ctx, "git", "-C", repo, "add", ".")
	run(t, ctx, "git", "-C", repo, "-c", "commit.gpgsign=false", "commit", "-m", "fixture")
	shaBytes, err := os.ReadFile(filepath.Join(repo, ".git", "refs", "heads", "main"))
	if err != nil {
		t.Fatal(err)
	}
	sha := strings.TrimSpace(string(shaBytes))
	run(t, ctx, zoektGitIndex, "-index", index, "-branches", "main", "-submodules=false", "-incremental=false", repo)

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().String()
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	webserver := exec.CommandContext(ctx, zoektWebserver, "-index", index, "-listen", address, "-rpc", "-html=false")
	process := startProcess(t, webserver)
	t.Cleanup(func() { process.stop(t) })

	client, err := zoekt.New("http://"+address, &http.Client{Timeout: time.Second}, 256<<10, observability.New())
	if err != nil {
		t.Fatal(err)
	}
	waitReady(t, ctx, client, process)
	waitIndexed(t, ctx, client, process)
	backend := &recordingBackend{SearchBackend: client}
	registry, err := repository.NewStatic([]repository.Repository{
		{ID: 1, ZoektID: 7, Name: fixtureName, Branch: "main", IndexedSHA: sha, WebURL: "https://example.invalid/fixture/repository"},
		{ID: 2, ZoektID: 8, Name: "forbidden/repository", Branch: "main"},
	})
	if err != nil {
		t.Fatal(err)
	}
	service := search.NewService(backend, authz.NewStatic(registry), search.Limits{MaxResults: 100, MaxResponseBytes: 256 << 10})
	authenticator := authn.NewStatic(map[string]authn.Principal{token: {Subject: "fixture-user", RepositoryNames: []string{fixtureName}}})
	mux := http.NewServeMux()
	httpapi.RegisterSearch(mux, authenticator, service, 64<<10, 256<<10)
	mux.Handle("/mcp", httpapi.AuthenticateBearer(authenticator, mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server {
		return mcpserver.New(service)
	}, nil)))
	server := httptest.NewServer(mux)
	defer server.Close()

	t.Run("REST", func(t *testing.T) {
		response := restSearch(t, server, api.SearchRequest{Query: needle, Repositories: []string{fixtureName}})
		assertFixtureMatch(t, response.Matches, sha)
	})
	t.Run("MCP", func(t *testing.T) {
		session, err := mcp.NewClient(&mcp.Implementation{Name: "e2e", Version: "1"}, nil).Connect(t.Context(), &mcp.StreamableClientTransport{
			Endpoint: server.URL + "/mcp", HTTPClient: bearerClient(server.Client()), DisableStandaloneSSE: true,
		}, nil)
		if err != nil {
			t.Fatal(err)
		}
		defer session.Close()
		result, err := session.CallTool(t.Context(), &mcp.CallToolParams{Name: "search_code", Arguments: map[string]any{
			"query": needle, "repositories": []string{fixtureName},
		}})
		if err != nil {
			t.Fatal(err)
		}
		var response api.SearchResponse
		decode(t, result.StructuredContent, &response)
		assertFixtureMatch(t, response.Matches, sha)
	})
	t.Run("authorization", func(t *testing.T) {
		before := backend.callCount()
		response := restSearch(t, server, api.SearchRequest{Query: needle, Repositories: []string{"forbidden/repository"}})
		if len(response.Matches) != 0 || backend.callCount() != before {
			t.Fatalf("forbidden matches = %d, backend calls = %d, want 0 and %d", len(response.Matches), backend.callCount(), before)
		}
		response = restSearch(t, server, api.SearchRequest{Query: needle, Repositories: []string{fixtureName, "forbidden/repository"}})
		assertFixtureMatch(t, response.Matches, sha)
		if got := backend.lastRepoIDs(); !slices.Equal(got, []uint32{7}) {
			t.Fatalf("Zoekt RepoIDs = %v, want [7]", got)
		}
	})
}

func TestManagedProcessCapturesEarlyExit(t *testing.T) {
	process := startProcess(t, exec.Command("/bin/sh", "-c", "printf boom; exit 7"))
	select {
	case <-process.done:
		if process.err == nil || process.logs.String() != "boom" {
			t.Fatalf("error = %v, logs = %q", process.err, process.logs.String())
		}
	case <-time.After(time.Second):
		t.Fatal("process exit was not reported")
	}
}

func requiredExecutables(t *testing.T) (string, string) {
	t.Helper()
	names := []string{"ZOEKT_GIT_INDEX", "ZOEKT_WEBSERVER"}
	paths := make([]string, len(names))
	var missing []string
	for index, name := range names {
		paths[index] = os.Getenv(name)
		if paths[index] == "" {
			missing = append(missing, name)
		}
	}
	if len(missing) != 0 {
		t.Fatalf("missing executable paths: %s", strings.Join(missing, ", "))
	}
	for index, path := range paths {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("%s path %q: %v", names[index], path, err)
		}
	}
	return paths[0], paths[1]
}

func copyFixture(t *testing.T, destination string) {
	t.Helper()
	source := filepath.Join("..", "fixtures", "repository")
	entries, err := os.ReadDir(source)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		data, err := os.ReadFile(filepath.Join(source, entry.Name()))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(destination, entry.Name()), data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func run(t *testing.T, ctx context.Context, name string, args ...string) string {
	t.Helper()
	output, err := exec.CommandContext(ctx, name, args...).CombinedOutput()
	if err != nil {
		t.Fatalf("%s %v: %v\n%s", name, args, err, output)
	}
	return string(output)
}

type managedProcess struct {
	command *exec.Cmd
	logs    bytes.Buffer
	done    chan struct{}
	err     error
}

func startProcess(t *testing.T, command *exec.Cmd) *managedProcess {
	t.Helper()
	process := &managedProcess{command: command, done: make(chan struct{})}
	command.Stdout, command.Stderr = &process.logs, &process.logs
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	go func() {
		process.err = command.Wait()
		close(process.done)
	}()
	return process
}

func waitReady(t *testing.T, ctx context.Context, client *zoekt.Client, process *managedProcess) {
	t.Helper()
	ticker := time.NewTicker(25 * time.Millisecond)
	defer ticker.Stop()
	for {
		if err := client.Health(ctx); err == nil {
			return
		}
		select {
		case <-process.done:
			t.Fatalf("zoekt-webserver exited before readiness: %v\n%s", process.err, process.logs.String())
		case <-ctx.Done():
			t.Fatalf("zoekt-webserver readiness: %v", ctx.Err())
		case <-ticker.C:
		}
	}
}

func waitIndexed(t *testing.T, ctx context.Context, client *zoekt.Client, process *managedProcess) {
	t.Helper()
	ticker := time.NewTicker(25 * time.Millisecond)
	defer ticker.Stop()
	for {
		response, err := client.Search(ctx, search.BackendRequest{
			Query: needle, RepositoryIDs: []uint32{7}, Limit: 1, Timeout: time.Second,
		})
		if err == nil && len(response.Matches) == 1 && response.Matches[0].Path == "main.go" {
			return
		}
		select {
		case <-process.done:
			t.Fatalf("zoekt-webserver exited before index visibility: %v\n%s", process.err, process.logs.String())
		case <-ctx.Done():
			t.Fatalf("fixture index visibility: %v (last search: %v)", ctx.Err(), err)
		case <-ticker.C:
		}
	}
}

func (process *managedProcess) stop(t *testing.T) {
	t.Helper()
	select {
	case <-process.done:
		return
	default:
	}
	_ = process.command.Process.Signal(os.Interrupt)
	select {
	case <-process.done:
	case <-time.After(2 * time.Second):
		_ = process.command.Process.Kill()
		<-process.done
		t.Errorf("zoekt-webserver required SIGKILL: %s", process.logs.String())
	}
}

func restSearch(t *testing.T, server *httptest.Server, input api.SearchRequest) api.SearchResponse {
	t.Helper()
	body, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	request, err := http.NewRequestWithContext(t.Context(), http.MethodPost, server.URL+"/v1/search", bytes.NewReader(body))
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
	if response.StatusCode != http.StatusOK {
		t.Fatalf("POST /v1/search status = %d: %s", response.StatusCode, data)
	}
	var output api.SearchResponse
	if err := json.Unmarshal(data, &output); err != nil {
		t.Fatal(err)
	}
	return output
}

func assertFixtureMatch(t *testing.T, matches []api.SearchMatch, sha string) {
	t.Helper()
	if len(matches) != 1 {
		t.Fatalf("matches = %#v, want one", matches)
	}
	match := matches[0]
	if match.Repository.ID != 1 || match.Repository.Name != fixtureName || match.Repository.Branch != "main" || match.Repository.IndexedSHA != sha || match.Path != "main.go" || match.SHA != sha || match.LineNumber != 3 || match.Preview != "const Needle = \"GrepNestFixtureNeedle\"\n" {
		t.Fatalf("match = %#v", match)
	}
}

func decode(t *testing.T, value, target any) {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, target); err != nil {
		t.Fatal(err)
	}
}

type bearerTransport struct{ base http.RoundTripper }

func (transport bearerTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	request = request.Clone(request.Context())
	request.Header.Set("Authorization", "Bearer "+token)
	return transport.base.RoundTrip(request)
}

func bearerClient(client *http.Client) *http.Client {
	copy := *client
	copy.Transport = bearerTransport{base: client.Transport}
	return &copy
}

type recordingBackend struct {
	search.SearchBackend
	mu      sync.Mutex
	calls   int
	repoIDs []uint32
}

func (backend *recordingBackend) Search(ctx context.Context, request search.BackendRequest) (api.SearchResponse, error) {
	backend.mu.Lock()
	backend.calls++
	backend.repoIDs = append([]uint32(nil), request.RepositoryIDs...)
	backend.mu.Unlock()
	return backend.SearchBackend.Search(ctx, request)
}

func (backend *recordingBackend) callCount() int {
	backend.mu.Lock()
	defer backend.mu.Unlock()
	return backend.calls
}

func (backend *recordingBackend) lastRepoIDs() []uint32 {
	backend.mu.Lock()
	defer backend.mu.Unlock()
	return append([]uint32(nil), backend.repoIDs...)
}
