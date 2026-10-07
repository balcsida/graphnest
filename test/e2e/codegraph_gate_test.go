//go:build e2e && unix

package e2e

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/balcsida/graphnest/internal/authn"
	"github.com/balcsida/graphnest/internal/authz"
	"github.com/balcsida/graphnest/internal/cli"
	"github.com/balcsida/graphnest/internal/githubapp"
	"github.com/balcsida/graphnest/internal/graphingest"
	"github.com/balcsida/graphnest/internal/graphquery"
	"github.com/balcsida/graphnest/internal/graphservice"
	"github.com/balcsida/graphnest/internal/httpapi"
	"github.com/balcsida/graphnest/internal/indexer"
	"github.com/balcsida/graphnest/internal/mcpserver"
	"github.com/balcsida/graphnest/internal/observability"
	"github.com/balcsida/graphnest/internal/repository"
	"github.com/balcsida/graphnest/internal/scipgraph"
	"github.com/balcsida/graphnest/internal/search"
	"github.com/balcsida/graphnest/internal/webhook"
	"github.com/balcsida/graphnest/internal/zoekt"
	"github.com/balcsida/graphnest/pkg/api"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const (
	gateAdminToken     = "gate-admin-token"
	gatePublisherToken = "gate-publisher-token"
	gateReaderToken    = "gate-reader-token"
	gatePublisherID    = int64(42)
)

// TestCodeGraphImportGate is the Stage 2 gate: GraphNest indexes a repository
// through the fake GitHub, then the graphnest command imports a CodeGraph index
// of that repository against the running server.
//
// Environment, all optional:
//
//	GRAPHNEST_GATE_REPO         git repository to index (default: built from test/fixtures/codegraph/source)
//	GRAPHNEST_GATE_COMMIT       commit to index (default: HEAD of that repository)
//	GRAPHNEST_GATE_CODEGRAPH_DB CodeGraph index of that commit (default: test/fixtures/codegraph/reference.db)
//	GRAPHNEST_GATE_TRANSCRIPT   Markdown file every step is appended to (default: t.Log)
func TestCodeGraphImportGate(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Minute)
	defer cancel()
	g := &gate{t: t, ctx: ctx, githubID: milestoneRepositoryID, database: newMilestoneDatabase(t), root: t.TempDir()}
	g.openTranscript()
	g.prepareRepository()
	g.startServer()
	g.indexRepository()
	g.grantPublication()
	g.dryRun()
	g.status()
}

type gate struct {
	t        *testing.T
	ctx      context.Context
	root     string
	database milestoneDatabase

	serverURL    string
	githubID     int64
	commit       string
	checkoutPath string
	indexPath    string
	// defaultFixture is true when neither the repository nor the index was overridden.
	defaultFixture bool
	transcript     transcript

	server           *httptest.Server
	worker           *indexer.Worker
	origin           smartGitOrigin
	reconcileResults <-chan error
}

// transcript appends one Markdown section per step to w.
type transcript struct{ w io.Writer }

type logWriter struct{ t *testing.T }

func (writer logWriter) Write(data []byte) (int, error) {
	writer.t.Log("\n" + string(data))
	return len(data), nil
}

func (log transcript) step(name string, command, output any) {
	var section strings.Builder
	fmt.Fprintf(&section, "## %s\n\n```\n%s\n```\n\n```json\n%s\n```\n\n", name, command, prettyJSON(output))
	_, _ = io.WriteString(log.w, section.String())
}

// prettyJSON renders any JSON-encodable value with sorted keys and indentation.
func prettyJSON(value any) string {
	data, err := json.Marshal(value)
	if err != nil {
		return fmt.Sprintf("%v", value)
	}
	var decoded any
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	if err := decoder.Decode(&decoded); err != nil {
		return string(data)
	}
	pretty, err := json.MarshalIndent(decoded, "", "  ")
	if err != nil {
		return string(data)
	}
	return string(pretty)
}

func (g *gate) openTranscript() {
	g.t.Helper()
	path := os.Getenv("GRAPHNEST_GATE_TRANSCRIPT")
	if path == "" {
		g.transcript = transcript{w: logWriter{g.t}}
		return
	}
	file, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		g.t.Fatal(err)
	}
	g.t.Cleanup(func() { file.Close() })
	g.transcript = transcript{w: file}
}

func gateGit(t *testing.T, ctx context.Context, environment []string, args ...string) string {
	t.Helper()
	command := exec.CommandContext(ctx, "git", args...)
	command.Env = append(os.Environ(), environment...)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, output)
	}
	return strings.TrimSpace(string(output))
}

// prepareRepository resolves the repository, commit and index inputs, then
// builds the bare mirror the fake GitHub serves and the checkout the command
// reads. Nothing is written into the input repository or the fixtures.
func (g *gate) prepareRepository() {
	t, ctx := g.t, g.ctx
	t.Helper()
	input := os.Getenv("GRAPHNEST_GATE_REPO")
	g.defaultFixture = input == "" && os.Getenv("GRAPHNEST_GATE_CODEGRAPH_DB") == ""
	if input == "" {
		input = filepath.Join(t.TempDir(), "source")
		copyGateFixture(t, filepath.Join("..", "fixtures", "codegraph", "source"), input)
		fixed := []string{
			"GIT_AUTHOR_NAME=GraphNest Test", "GIT_AUTHOR_EMAIL=test@graphnest.invalid", "GIT_AUTHOR_DATE=2026-01-02T03:04:05+00:00",
			"GIT_COMMITTER_NAME=GraphNest Test", "GIT_COMMITTER_EMAIL=test@graphnest.invalid", "GIT_COMMITTER_DATE=2026-01-02T03:04:05+00:00",
		}
		gateGit(t, ctx, nil, "init", "--initial-branch=main", input)
		gateGit(t, ctx, nil, "-C", input, "add", ".")
		gateGit(t, ctx, fixed, "-C", input, "-c", "commit.gpgsign=false", "commit", "-m", "CodeGraph fixture")
	}
	revision := os.Getenv("GRAPHNEST_GATE_COMMIT")
	if revision == "" {
		revision = "HEAD"
	}
	g.commit = gateGit(t, ctx, nil, "-C", input, "rev-parse", "--verify", revision+"^{commit}")

	indexPath := os.Getenv("GRAPHNEST_GATE_CODEGRAPH_DB")
	if indexPath == "" {
		indexPath = filepath.Join("..", "fixtures", "codegraph", "reference.db")
	}
	var err error
	if g.indexPath, err = filepath.Abs(indexPath); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(g.indexPath); err != nil {
		t.Fatal(err)
	}

	mirror := filepath.Join(g.root, "acme", "gate.git")
	if err := os.MkdirAll(filepath.Dir(mirror), 0o700); err != nil {
		t.Fatal(err)
	}
	gateGit(t, ctx, nil, "clone", "--bare", input, mirror)
	gateGit(t, ctx, nil, "--git-dir", mirror, "rev-parse", "--verify", g.commit+"^{commit}")
	g.checkoutPath = filepath.Join(t.TempDir(), "checkout")
	gateGit(t, ctx, nil, "clone", input, g.checkoutPath)
	gateGit(t, ctx, nil, "-C", g.checkoutPath, "checkout", "--detach", g.commit)

	files := strings.Split(gateGit(t, ctx, nil, "--git-dir", mirror, "ls-tree", "-r", "--name-only", "-z", g.commit), "\x00")
	if len(files) == 0 || files[0] == "" {
		t.Fatalf("commit %s has no files", g.commit)
	}
	g.origin = smartGitOrigin{
		name: "acme/gate", path: mirror, shas: []string{g.commit},
		content: gateGit(t, ctx, nil, "--git-dir", mirror, "cat-file", "blob", g.commit+":"+files[0]),
		blobSHA: gateGit(t, ctx, nil, "--git-dir", mirror, "rev-parse", g.commit+":"+files[0]),
	}
}

func copyGateFixture(t *testing.T, source, destination string) {
	t.Helper()
	if err := filepath.WalkDir(source, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		relative, err := filepath.Rel(source, path)
		if err != nil {
			return err
		}
		target := filepath.Join(destination, relative)
		if entry.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(target, data, 0o644)
	}); err != nil {
		t.Fatal(err)
	}
}

// startServer composes the production services in-process behind an HTTP
// server, with the fake GitHub, Zoekt and the indexer worker.
func (g *gate) startServer() {
	t, ctx := g.t, g.ctx
	t.Helper()
	_, zoektWebserver := requiredExecutables(t)
	zoektIndex := requiredExecutable(t, "ZOEKT_INDEX")
	store := g.database.store

	empty := newSmartGitOrigin(t, ctx, g.root, "acme/empty", nil)
	privateKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	privateKeyPEM := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(privateKey)})
	github := newFakeGHES(t, g.root, g.origin, empty, 7, &privateKey.PublicKey)
	caPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: github.server.Certificate().Raw})
	base, err := url.Parse(github.server.URL)
	if err != nil {
		t.Fatal(err)
	}
	apiBase := *base
	apiBase.Path = "/api/v3"
	endpoints := githubapp.Endpoints{Web: base, API: &apiBase, Upload: base, Git: base, Archive: base}
	httpClient, err := githubapp.NewHTTPClient(caPEM, endpoints, 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := githubapp.NewSigner(7, privateKeyPEM, nil)
	if err != nil {
		t.Fatal(err)
	}
	metrics := observability.New()
	githubClient := githubapp.NewClient(endpoints, httpClient, signer, "2022-11-28", 2<<20, nil, metrics)
	reconciler := githubapp.NewReconciler(githubClient, store)
	reconcileRequests := make(chan int64, 64)
	reconcileResults := make(chan error, 64)
	reconcileDone := make(chan struct{})
	go func() {
		defer close(reconcileDone)
		for {
			select {
			case <-ctx.Done():
				return
			case installationID := <-reconcileRequests:
				reconcileResults <- reconciler.Installation(ctx, installationID)
			}
		}
	}()
	t.Cleanup(func() { <-reconcileDone })
	g.reconcileResults = reconcileResults
	processor := webhook.NewGitHubProcessor(store, reconcileRequests, metrics)

	indexDir := filepath.Join(g.root, "index")
	if err := os.MkdirAll(indexDir, 0o700); err != nil {
		t.Fatal(err)
	}
	zoektAddress := freeAddress(t)
	zoektProcess := startProcess(t, exec.CommandContext(ctx, zoektWebserver, "-index", indexDir, "-listen", zoektAddress, "-rpc", "-html=false"))
	t.Cleanup(func() { zoektProcess.stop(t) })
	zoektClient, err := zoekt.New("http://"+zoektAddress, &http.Client{Timeout: 3 * time.Second}, 256<<10, metrics)
	if err != nil {
		t.Fatal(err)
	}
	waitReady(t, ctx, zoektClient, zoektProcess)
	if err := store.UpsertSearchNode(ctx, "primary", "http://"+zoektAddress); err != nil {
		t.Fatal(err)
	}

	repositoryIDs := []int64{g.githubID}
	authenticator := authn.NewStatic(map[string]authn.Principal{
		gateAdminToken:     {Subject: "1", Method: "api_token", Administrator: true, InstallationID: milestoneInstallationID, RepositoryIDs: repositoryIDs},
		gatePublisherToken: {Subject: strconv.FormatInt(gatePublisherID, 10), Method: "api_token", InstallationID: milestoneInstallationID, RepositoryIDs: repositoryIDs},
		gateReaderToken:    {Subject: "43", Method: "api_token", InstallationID: milestoneInstallationID, RepositoryIDs: repositoryIDs},
	})
	requestAuth := authn.RequestAuthenticator{Bearer: authenticator}
	searchService := search.NewService(zoektClient, authz.NewPostgres(store), search.Limits{MaxResults: 100, MaxResponseBytes: 256 << 10})
	repositoryService := &repository.Service{Store: store, GitHub: githubClient, SCIP: store, Graph: store}
	scipService := &scipgraph.Service{Store: store, GitHub: githubClient, MaxResults: 100}
	graphLimits := graphservice.Limits{
		PerCategory: 100, DefaultImpactDepth: 3, MaxDepth: 32, DefaultTraceDepth: 10, MaxTraceDepth: 30,
		MaxRows: 1_000, MaxNodes: 1_000, MaxEdges: 5_000, MaxFanout: 100, MaxResponseBytes: 256 << 10,
	}
	graphQueries := &graphservice.Service{Store: store, Backend: &graphquery.Service{Store: store, Limits: graphquery.Limits{
		PerCategory: graphLimits.PerCategory, DefaultImpactDepth: graphLimits.DefaultImpactDepth, MaxDepth: graphLimits.MaxDepth,
		DefaultTraceDepth: graphLimits.DefaultTraceDepth, MaxTraceDepth: graphLimits.MaxTraceDepth, MaxRows: graphLimits.MaxRows,
		MaxNodes: graphLimits.MaxNodes, MaxEdges: graphLimits.MaxEdges, MaxFanout: graphLimits.MaxFanout,
	}}, Limits: graphLimits}
	graphIngest := &graphingest.Service{Store: store, MaxUploadBytes: 64 << 20}
	authorizer := authz.NewPostgres(store)
	grants := &httpapi.UploadGrants{Set: store.SetGraphPublicationGrant, Resolve: func(ctx context.Context, principal authn.Principal, githubID int64) (int64, error) {
		repo, err := authorizer.AuthorizedRepository(ctx, principal, githubID)
		return repo.ID, err
	}}

	mux := http.NewServeMux()
	httpapi.RegisterRepositories(mux, requestAuth, repositoryService, 64<<10, 100, 256<<10)
	httpapi.RegisterGraphIngestion(mux, authenticator, graphIngest, grants, 64<<20, 256<<10)
	httpapi.RegisterGraphQueries(mux, authenticator, graphQueries, 64<<10, 256<<10)
	httpapi.RegisterSCIP(mux, requestAuth, scipService, 64<<10, 64<<20, 256<<10)
	httpapi.RegisterGitHubWebhook(mux, []byte(milestoneWebhookSecret), 1<<20, processor)
	mcpServer := mcpserver.NewWithLimits(mcpserver.Services{Search: searchService, Repositories: repositoryService, SCIP: scipService, Graph: graphQueries},
		mcpserver.Limits{MaxItems: 100, MaxOutputBytes: 256 << 10, GraphMaxOutputBytes: 256 << 10})
	mux.Handle("/mcp", httpapi.AuthenticateBearer(authenticator, mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return mcpServer }, nil)))
	g.server = httptest.NewServer(mux)
	t.Cleanup(g.server.Close)
	g.serverURL = g.server.URL

	g.worker = &indexer.Worker{
		ID: "gate-worker", Queue: store, Store: store, Tokens: githubClient,
		Snapshots: indexer.ArchiveSnapshotProvider{Client: githubClient, WorkspacesDir: filepath.Join(g.root, "data", "archives"), Limits: indexer.ArchiveLimits{
			MaxDownloadBytes: 256 << 20, MaxExtractedBytes: 1 << 30, MaxFileBytes: 8 << 20, MaxFiles: 200_000, MaxPathBytes: 1024,
		}},
		Zoekt: &indexer.ZoektIndexer{
			Binary: zoektIndex, IndexDir: indexDir, Runner: indexer.Runner{MaxOutput: 64 << 10, KillGrace: 100 * time.Millisecond},
			Client: zoektClient, IndexTimeout: 60 * time.Second, VisibilityTimeout: 30 * time.Second,
		},
		RenewEvery: time.Second, CleanupTimeout: 5 * time.Second,
	}
}

// indexRepository delivers the webhooks and runs the worker until the
// repository is indexed at the commit.
func (g *gate) indexRepository() {
	t, ctx := g.t, g.ctx
	t.Helper()
	sendGitHubWebhook(t, g.server, "gate-installation", "installation", []byte(`{"action":"created","installation":{"id":10}}`))
	awaitReconciliation(t, g.reconcileResults)
	sendPush(t, g.server, "gate-push", g.githubID, g.commit)
	deadline := time.Now().Add(2 * time.Minute)
	for {
		var indexed, status string
		if err := g.database.pool.QueryRow(ctx, "select coalesce(indexed_sha,''),status from repositories where github_id=$1", g.githubID).Scan(&indexed, &status); err != nil {
			t.Fatal(err)
		}
		if indexed == g.commit && status == "ready" {
			break
		}
		if time.Now().After(deadline) || ctx.Err() != nil {
			t.Fatalf("repository not indexed: indexed_sha=%q status=%q want %q/ready", indexed, status, g.commit)
		}
		worked, err := g.worker.RunOne(ctx)
		if err != nil {
			t.Fatalf("index worker: %v", err)
		}
		if !worked {
			time.Sleep(100 * time.Millisecond)
		}
	}
	var summary api.RepositorySummary
	path := fmt.Sprintf("/v1/repositories/%d", g.githubID)
	status, raw := g.request(http.MethodGet, path, gatePublisherToken, nil, &summary)
	if status != http.StatusOK || summary.IndexedSHA != g.commit || summary.Status != "ready" {
		t.Fatalf("repository status=%d %s", status, raw)
	}
	g.transcript.step("Index the repository", "GET "+path+"  (publisher)", json.RawMessage(raw))
}

// request calls the server as the holder of token and decodes a 2xx JSON body
// into output when it is non-nil. It returns the status and the raw body.
func (g *gate) request(method, path, token string, body any, output any) (int, []byte) {
	t := g.t
	t.Helper()
	var reader io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		reader = bytes.NewReader(data)
	}
	request, err := http.NewRequestWithContext(g.ctx, method, g.serverURL+path, reader)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer "+token)
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	response, err := g.server.Client().Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	raw, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	if output != nil && response.StatusCode >= 200 && response.StatusCode < 300 {
		if err := json.Unmarshal(raw, output); err != nil {
			t.Fatalf("%s %s: %v\n%s", method, path, err, raw)
		}
	}
	return response.StatusCode, raw
}

// grantPublication lets the publisher publish, then checks the preflight.
func (g *gate) grantPublication() {
	t := g.t
	t.Helper()
	grant := map[string]any{"repository_id": g.githubID, "subject": strconv.FormatInt(gatePublisherID, 10), "allow": true}
	status, raw := g.request(http.MethodPut, "/v1/graph/publication-grants", gateAdminToken, grant, nil)
	if status != http.StatusNoContent {
		t.Fatalf("grant status=%d %s", status, raw)
	}
	g.transcript.step("Grant publication", "PUT /v1/graph/publication-grants  (admin)", map[string]any{"request": grant, "response_status": status})

	var graphStatus api.GraphStatus
	path := fmt.Sprintf("/v1/graph/repositories/%d/status", g.githubID)
	status, raw = g.request(http.MethodGet, path, gatePublisherToken, nil, &graphStatus)
	if status != http.StatusOK || graphStatus.Publication == nil || !graphStatus.Publication.Permitted || graphStatus.Publication.ActiveGeneration != nil {
		t.Fatalf("publication preflight status=%d %s", status, raw)
	}
	g.transcript.step("Publication preflight", "GET "+path+"  (publisher)", json.RawMessage(raw))
}

// graphnest runs the command as the publisher and returns its stdout.
func (g *gate) graphnest(args ...string) []byte {
	t := g.t
	t.Helper()
	environment := cli.OSEnvironment()
	variables := map[string]string{"GRAPHNEST_SERVER_URL": g.serverURL, "GRAPHNEST_TOKEN": gatePublisherToken}
	environment.Getenv = func(name string) string { return variables[name] }
	var stdout, stderr bytes.Buffer
	if code := cli.Run(g.ctx, args, environment, &stdout, &stderr); code != 0 || stderr.Len() != 0 {
		t.Fatalf("graphnest %v: exit %d\nstdout: %s\nstderr: %s", args, code, stdout.String(), stderr.String())
	}
	return stdout.Bytes()
}

func (g *gate) dryRun() {
	t := g.t
	t.Helper()
	args := []string{"graph", "import", "codegraph", "--dry-run", "--repo", g.checkoutPath, "--index", g.indexPath, "--repository-id", strconv.FormatInt(g.githubID, 10)}
	stdout := g.graphnest(args...)
	var report struct {
		DryRun    bool   `json:"dry_run"`
		Published bool   `json:"published"`
		Commit    string `json:"commit"`
		Counts    struct {
			Nodes int `json:"nodes"`
			Edges int `json:"edges"`
			Files int `json:"files"`
		} `json:"counts"`
	}
	decoder := json.NewDecoder(bytes.NewReader(stdout))
	if err := decoder.Decode(&report); err != nil || decoder.More() {
		t.Fatalf("dry run output is not one JSON document: %v\n%s", err, stdout)
	}
	if !report.DryRun || report.Published || report.Commit != g.commit || report.Counts.Nodes <= 0 {
		t.Fatalf("dry run report=%+v, want dry_run, unpublished, commit %s, nodes > 0", report, g.commit)
	}
	if g.defaultFixture && (report.Counts.Nodes != 68 || report.Counts.Edges != 93 || report.Counts.Files != 13) {
		t.Fatalf("fixture counts nodes/edges/files = %d/%d/%d, want 68/93/13", report.Counts.Nodes, report.Counts.Edges, report.Counts.Files)
	}
	g.transcript.step("Dry run", "graphnest "+strings.Join(args, " "), json.RawMessage(stdout))
}

type tokenTransport struct {
	base  http.RoundTripper
	token string
}

func (transport tokenTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	request = request.Clone(request.Context())
	request.Header.Set("Authorization", "Bearer "+transport.token)
	return transport.base.RoundTrip(request)
}

func (g *gate) status() {
	t := g.t
	t.Helper()
	client := *g.server.Client()
	client.Transport = tokenTransport{base: client.Transport, token: gatePublisherToken}
	session, err := mcp.NewClient(&mcp.Implementation{Name: "codegraph-gate", Version: "1"}, nil).Connect(g.ctx, &mcp.StreamableClientTransport{
		Endpoint: g.serverURL + "/mcp", HTTPClient: &client, DisableStandaloneSSE: true,
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	arguments := map[string]any{"repository_id": g.githubID}
	result, err := session.CallTool(g.ctx, &mcp.CallToolParams{Name: "get_repository_status", Arguments: arguments})
	if err != nil || result.IsError {
		t.Fatalf("MCP get_repository_status result=%#v err=%v", result, err)
	}
	var summary api.RepositorySummary
	decode(t, result.StructuredContent, &summary)
	if summary.GraphStatus != api.GraphStatusAbsent {
		t.Fatalf("MCP graph_status=%q, want %q", summary.GraphStatus, api.GraphStatusAbsent)
	}
	call, _ := json.Marshal(arguments)
	g.transcript.step("MCP repository status", "MCP tools/call get_repository_status "+string(call)+"  (publisher)", result.StructuredContent)

	args := []string{"graph", "status", "--repository-id", strconv.FormatInt(g.githubID, 10)}
	g.transcript.step("graphnest graph status", "graphnest "+strings.Join(args, " "), json.RawMessage(g.graphnest(args...)))
}
