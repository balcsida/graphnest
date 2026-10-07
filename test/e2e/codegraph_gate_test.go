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
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/balcsida/graphnest/internal/authn"
	"github.com/balcsida/graphnest/internal/authz"
	"github.com/balcsida/graphnest/internal/cli"
	"github.com/balcsida/graphnest/internal/githubapp"
	"github.com/balcsida/graphnest/internal/graphartifact"
	graphv2 "github.com/balcsida/graphnest/internal/graphartifact/v2"
	"github.com/balcsida/graphnest/internal/graphingest"
	"github.com/balcsida/graphnest/internal/graphprotocol"
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
//	GRAPHNEST_GATE_CODEGRAPH_ANSWERS JSON file written by test/parity/gate-answers.mjs: the symbol, file and
//	                            five CodeGraph answers to compare with GraphNest's; without it the answers
//	                            stored in test/fixtures/codegraph/library-expected.json are used
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
	g.artifactOutput()
	g.upload()
	g.retryUpload()
	g.questions()
	g.refusedUpload()
	g.serverState()
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
	artifactPath string
	// artifactFiles are the paths of the files in the artifact, the indexed truth for graph_files.
	artifactFiles []string
	generation    int64
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
	} else {
		file, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
		if err != nil {
			g.t.Fatal(err)
		}
		g.t.Cleanup(func() { file.Close() })
		g.transcript = transcript{w: file}
	}
	_, _ = io.WriteString(g.transcript.w, "# CodeGraph import gate\n\n"+
		"The v1 graph job reports `enrichment_disabled`: this harness configures no scanner, which is also what a default deployment does. "+
		"The imported v2 generation does not depend on it.\n\n")
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
	}}, Files: repositoryService, Limits: graphLimits}
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

// run executes the command as the holder of token and returns its exit code, stdout and stderr.
func (g *gate) run(token string, args ...string) (int, []byte, []byte) {
	environment := cli.OSEnvironment()
	variables := map[string]string{"GRAPHNEST_SERVER_URL": g.serverURL, "GRAPHNEST_TOKEN": token}
	environment.Getenv = func(name string) string { return variables[name] }
	var stdout, stderr bytes.Buffer
	code := cli.Run(g.ctx, args, environment, &stdout, &stderr)
	return code, stdout.Bytes(), stderr.Bytes()
}

// graphnest runs the command as the publisher, which must succeed, and returns its stdout.
func (g *gate) graphnest(args ...string) []byte {
	t := g.t
	t.Helper()
	code, stdout, stderr := g.run(gatePublisherToken, args...)
	if code != 0 || len(stderr) != 0 {
		t.Fatalf("graphnest %v: exit %d\nstdout: %s\nstderr: %s", args, code, stdout, stderr)
	}
	return stdout
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

// mcpSession connects an MCP client to the server as the holder of token.
func (g *gate) mcpSession(token string) *mcp.ClientSession {
	t := g.t
	t.Helper()
	client := *g.server.Client()
	client.Transport = tokenTransport{base: client.Transport, token: token}
	session, err := mcp.NewClient(&mcp.Implementation{Name: "codegraph-gate", Version: "1"}, nil).Connect(g.ctx, &mcp.StreamableClientTransport{
		Endpoint: g.serverURL + "/mcp", HTTPClient: &client, DisableStandaloneSSE: true,
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { session.Close() })
	return session
}

// tryTool calls an MCP tool and returns its structured content, or the error a client would see.
func (g *gate) tryTool(session *mcp.ClientSession, name string, arguments map[string]any) (any, error) {
	result, err := session.CallTool(g.ctx, &mcp.CallToolParams{Name: name, Arguments: arguments})
	if err != nil {
		return nil, err
	}
	if result.IsError {
		var texts []string
		for _, content := range result.Content {
			if text, ok := content.(*mcp.TextContent); ok {
				texts = append(texts, text.Text)
			}
		}
		return nil, fmt.Errorf("tool error: %s", strings.Join(texts, " "))
	}
	return result.StructuredContent, nil
}

// callTool calls an MCP tool and returns its structured content; an error fails the test.
func (g *gate) callTool(session *mcp.ClientSession, name string, arguments map[string]any) any {
	g.t.Helper()
	structured, err := g.tryTool(session, name, arguments)
	if err != nil {
		g.t.Fatalf("MCP %s: %v", name, err)
	}
	return structured
}

func mcpCommand(name string, arguments map[string]any, role string) string {
	call, _ := json.Marshal(arguments)
	return "MCP tools/call " + name + " " + string(call) + "  (" + role + ")"
}

func (g *gate) status() {
	t := g.t
	t.Helper()
	arguments := map[string]any{"repository_id": g.githubID}
	content := g.callTool(g.mcpSession(gatePublisherToken), "get_repository_status", arguments)
	var summary api.RepositorySummary
	decode(t, content, &summary)
	if summary.GraphStatus != api.GraphStatusAbsent {
		t.Fatalf("MCP graph_status=%q, want %q", summary.GraphStatus, api.GraphStatusAbsent)
	}
	g.transcript.step("MCP repository status", mcpCommand("get_repository_status", arguments, "publisher"), content)

	args := []string{"graph", "status", "--repository-id", strconv.FormatInt(g.githubID, 10)}
	g.transcript.step("graphnest graph status", "graphnest "+strings.Join(args, " "), json.RawMessage(g.graphnest(args...)))
}

// decodeOne decodes stdout, which must be exactly one JSON document.
func (g *gate) decodeOne(stdout []byte, report any) {
	g.t.Helper()
	decoder := json.NewDecoder(bytes.NewReader(stdout))
	if err := decoder.Decode(report); err != nil || decoder.More() {
		g.t.Fatalf("output is not one JSON document: %v\n%s", err, stdout)
	}
}

// activeGeneration reads the publication preflight as the publisher.
func (g *gate) activeGeneration() (*api.GraphActiveGeneration, []byte) {
	g.t.Helper()
	var graphStatus api.GraphStatus
	status, raw := g.request(http.MethodGet, fmt.Sprintf("/v1/graph/repositories/%d/status", g.githubID), gatePublisherToken, nil, &graphStatus)
	if status != http.StatusOK || graphStatus.Publication == nil {
		g.t.Fatalf("graph status=%d %s", status, raw)
	}
	return graphStatus.Publication.ActiveGeneration, raw
}

func (g *gate) artifactOutput() {
	t := g.t
	t.Helper()
	g.artifactPath = filepath.Join(t.TempDir(), "graph.pb")
	args := []string{"graph", "import", "codegraph", "--output", g.artifactPath, "--repo", g.checkoutPath, "--index", g.indexPath, "--repository-id", strconv.FormatInt(g.githubID, 10)}
	stdout := g.graphnest(args...)
	var report struct {
		Freshness struct {
			Status string `json:"status"`
		} `json:"freshness"`
		Output *struct {
			Bytes int `json:"bytes"`
		} `json:"output"`
	}
	g.decodeOne(stdout, &report)
	info, err := os.Stat(g.artifactPath)
	if err != nil {
		t.Fatal(err)
	}
	if report.Freshness.Status != "fresh" || report.Output == nil || int64(report.Output.Bytes) != info.Size() {
		t.Fatalf("artifact output report=%+v file size=%d, want freshness fresh and output.bytes equal to the size", report, info.Size())
	}
	data, err := os.ReadFile(g.artifactPath)
	if err != nil {
		t.Fatal(err)
	}
	artifact, err := graphartifact.ParseV2(data, graphartifact.Limits{})
	if err != nil {
		t.Fatal(err)
	}
	for _, file := range artifact.Files {
		g.artifactFiles = append(g.artifactFiles, file.Path)
	}
	slices.Sort(g.artifactFiles)
	g.transcript.step("Artifact output", "graphnest "+strings.Join(args, " "), json.RawMessage(stdout))
}

type uploadResult struct {
	Published   bool `json:"published"`
	Publication *struct {
		Attempts int                        `json:"attempts"`
		Result   api.GraphPublicationResult `json:"result"`
	} `json:"publication"`
	Server *struct {
		After *api.GraphActiveGeneration `json:"active_generation_after"`
	} `json:"server"`
}

func (g *gate) upload() {
	t := g.t
	t.Helper()
	args := []string{"graph", "upload", g.artifactPath, "--repository-id", strconv.FormatInt(g.githubID, 10)}
	stdout := g.graphnest(args...)
	var report uploadResult
	g.decodeOne(stdout, &report)
	if !report.Published || report.Publication == nil || report.Publication.Attempts != 1 || report.Server == nil || report.Server.After == nil || report.Server.After.Producer != "codegraph" {
		t.Fatalf("upload report=%s, want published, one attempt, active generation after by codegraph", stdout)
	}
	g.generation = report.Publication.Result.Generation
	g.transcript.step("Upload", "graphnest "+strings.Join(args, " ")+"  (publisher)", json.RawMessage(stdout))

	arguments := map[string]any{"repository_id": g.githubID}
	content := g.callTool(g.mcpSession(gateReaderToken), "get_repository_status", arguments)
	var summary api.RepositorySummary
	decode(t, content, &summary)
	if summary.GraphStatus != api.GraphStatusCurrent || summary.GraphProducer != "codegraph" || summary.GraphCommit != g.commit {
		t.Fatalf("MCP status graph_status=%q producer=%q commit=%q, want current/codegraph/%s", summary.GraphStatus, summary.GraphProducer, summary.GraphCommit, g.commit)
	}
	g.transcript.step("MCP repository status after upload", mcpCommand("get_repository_status", arguments, "reader"), content)

	active, raw := g.activeGeneration()
	if active == nil || active.ID != g.generation || active.Producer != "codegraph" {
		t.Fatalf("preflight active generation=%+v, want %d by codegraph", active, g.generation)
	}
	g.transcript.step("Publication preflight after upload", fmt.Sprintf("GET /v1/graph/repositories/%d/status  (publisher)", g.githubID), json.RawMessage(raw))
}

func (g *gate) retryUpload() {
	t := g.t
	t.Helper()
	args := []string{"graph", "upload", g.artifactPath, "--repository-id", strconv.FormatInt(g.githubID, 10)}
	stdout := g.graphnest(args...)
	var report uploadResult
	g.decodeOne(stdout, &report)
	if !report.Published || report.Publication == nil || !report.Publication.Result.Deduplicated || report.Publication.Result.Generation != g.generation {
		t.Fatalf("retry report=%s, want deduplicated publication of generation %d", stdout, g.generation)
	}
	g.transcript.step("Idempotent retry", "graphnest "+strings.Join(args, " ")+"  (publisher)", json.RawMessage(stdout))
}

// refusedUpload shows the reader, who has no grant, cannot publish and changes nothing.
func (g *gate) refusedUpload() {
	t := g.t
	t.Helper()
	before, _ := g.activeGeneration()
	args := []string{"graph", "upload", g.artifactPath, "--repository-id", strconv.FormatInt(g.githubID, 10)}
	code, stdout, stderr := g.run(gateReaderToken, args...)
	if code != 1 || len(stdout) != 0 || !strings.Contains(string(stderr), "grant") {
		t.Fatalf("reader upload exit=%d stdout=%q stderr=%q, want exit 1, no stdout and a hint about the grant", code, stdout, stderr)
	}
	after, _ := g.activeGeneration()
	if before == nil || after == nil || *before != *after || after.ID != g.generation {
		t.Fatalf("active generation before=%+v after=%+v, want unchanged generation %d", before, after, g.generation)
	}
	g.transcript.step("Failure path", "graphnest "+strings.Join(args, " ")+"  (reader, no grant)",
		map[string]any{"exit_code": code, "stdout": string(stdout), "stderr": string(stderr), "active_generation_unchanged": after})
}

func (g *gate) serverState() {
	g.t.Helper()
	_, raw := g.activeGeneration()
	g.transcript.step("Server state: graph status", fmt.Sprintf("GET /v1/graph/repositories/%d/status  (publisher)", g.githubID), json.RawMessage(raw))
	arguments := map[string]any{"repository_id": g.githubID}
	g.transcript.step("Server state: repository status", mcpCommand("get_repository_status", arguments, "reader"),
		g.callTool(g.mcpSession(gateReaderToken), "get_repository_status", arguments))
}

// gateQuestion is one question put to both CodeGraph and GraphNest. The keys
// are what each answer names, sorted, so the answers can be compared as sets.
type gateQuestion struct {
	tool      string
	arguments map[string]any
	// upstream is CodeGraph's answer as recorded; upstreamKeys is what it names.
	upstream     any
	upstreamKeys []string
	graphnest    func(t *testing.T, structured any) []string
	// allowedExtra are keys GraphNest alone may name without failing the default fixture.
	allowedExtra []string
	// extraRule, when set, lets GraphNest name any further keys and is quoted in the difference summary.
	extraRule string
	// noCounterpart names what the CodeGraph answer carries that GraphNest has no field for, or the reverse.
	noCounterpart string
}

const (
	callsNote   = "CodeGraph's neighbor lines carry name, kind, path:line and a via label; the via label is not compared. Neighbors are keyed by name, kind and location because CodeGraph prints no qualified names for them; qualified names are compared in the definition headings."
	impactNote  = "CodeGraph prints name:line per file; GraphNest also returns kind, depth and the connecting edges, which are not compared."
	exploreNote = "CodeGraph's blast radius, symbol counts and source blocks are prose with no structured GraphNest counterpart, and GraphNest's per-file score, status, segments and relationships have none in CodeGraph's text. Only the sets of files are compared; source snippets are not. A file GraphNest returns without source is listed with the suffix [pointer]."
	// exploreRule is why GraphNest's explore may name more files than CodeGraph's.
	exploreRule = "GraphNest's answer is a superset by its own documented budget rule: docs/graph-exploration.md defines a 13,000 UTF-16 unit, four-file source budget below 150 indexed files, expanded for preferred named files, and pointer entries under the allocation cliff that consume no source slot. Every file CodeGraph showed with source must appear in GraphNest's answer with source; the extras are listed."
	filesNote   = "CodeGraph lists language and symbol counts per file, GraphNest file facts carry their own metadata. Only the paths are compared."
)

// questions asks the five questions as the reader through MCP and records both answers side by side.
func (g *gate) questions() {
	t := g.t
	t.Helper()
	session := g.mcpSession(gateReaderToken)
	asked := g.defaultQuestions()
	answersPath := os.Getenv("GRAPHNEST_GATE_CODEGRAPH_ANSWERS")
	if answersPath != "" {
		asked = g.answeredQuestions(answersPath)
	}
	for _, q := range asked {
		structured, err := g.tryTool(session, q.tool, q.arguments)
		gap := ""
		if err != nil && answersPath != "" && q.tool == "explore" {
			// GraphNest's explore refuses an answer over its response budget instead of
			// truncating it (graphquery.ErrQuerySize). On a real repository that is a gap
			// to review, recorded here, and the question is asked again within
			// CodeGraph-sized bounds so the selected files can still be compared.
			gap = "with the default bounds GraphNest answered: " + err.Error()
			q.arguments = map[string]any{"query": q.arguments["query"], "limit": 8, "candidate_limit": 32, "max_files": 4}
			structured, err = g.tryTool(session, q.tool, q.arguments)
		}
		if err != nil {
			// The answer of the other side is still recorded for review.
			g.transcript.step("Question: "+q.tool, mcpCommand(q.tool, q.arguments, "reader"), map[string]any{"codegraph": q.upstream, "graphnest_error": err.Error(), "gap": gap})
			t.Errorf("%s %v: %v", q.tool, q.arguments, err)
			continue
		}
		if q.tool == "graph_files" {
			structured = g.allFiles(session, q.arguments, structured)
		}
		got := q.graphnest(t, structured)
		onlyCodeGraph, onlyGraphNest := setDifference(q.upstreamKeys, got), setDifference(got, q.upstreamKeys)
		g.transcript.step("Question: "+q.tool, mcpCommand(q.tool, q.arguments, "reader"), map[string]any{
			"codegraph": q.upstream,
			"graphnest": structured,
			"difference": map[string]any{
				"codegraph_names": len(q.upstreamKeys), "graphnest_names": len(got),
				"only_in_codegraph": onlyCodeGraph, "only_in_graphnest": onlyGraphNest,
				"no_counterpart":  q.noCounterpart,
				"documented_rule": q.extraRule,
				"gap":             gap,
			},
		})
		if answersPath != "" {
			// A real repository's differences are reviewed from the transcript; GraphNest only has to answer.
			if structured == nil {
				t.Errorf("%s: no answer", q.tool)
			}
			continue
		}
		if len(onlyCodeGraph) != 0 || q.extraRule == "" && !subset(onlyGraphNest, q.allowedExtra) {
			t.Errorf("%s %v: only in CodeGraph %v, only in GraphNest %v (allowed %v)", q.tool, q.arguments, onlyCodeGraph, onlyGraphNest, q.allowedExtra)
		}
	}
}

func subset(part, whole []string) bool {
	for _, value := range part {
		if !slices.Contains(whole, value) {
			return false
		}
	}
	return true
}

// setDifference returns the sorted values of a missing from b.
func setDifference(a, b []string) []string {
	missing := []string{}
	for _, value := range a {
		if !slices.Contains(b, value) && !slices.Contains(missing, value) {
			missing = append(missing, value)
		}
	}
	slices.Sort(missing)
	return missing
}

// allFiles follows next_cursor so graph_files answers are complete.
func (g *gate) allFiles(session *mcp.ClientSession, arguments map[string]any, first any) any {
	t := g.t
	t.Helper()
	merged, ok := first.(map[string]any)
	if !ok {
		t.Fatalf("graph_files answer is %T", first)
	}
	page := merged
	for pages := 0; ; pages++ {
		cursor, _ := page["next_cursor"].(string)
		if cursor == "" {
			return merged
		}
		if pages > 100 {
			t.Fatal("graph_files did not end")
		}
		next := map[string]any{"cursor": cursor}
		for key, value := range arguments {
			next[key] = value
		}
		var ok bool
		if page, ok = g.callTool(session, "graph_files", next).(map[string]any); !ok {
			t.Fatal("graph_files page is not an object")
		}
		files, _ := merged["files"].([]any)
		more, _ := page["files"].([]any)
		merged["files"] = append(files, more...)
	}
}

// mcpText joins the text blocks of a recorded CodeGraph MCP answer.
func mcpText(t *testing.T, raw json.RawMessage) string {
	t.Helper()
	var answer struct {
		Content []struct {
			Text string `json:"text"`
		} `json:"content"`
	}
	if err := json.Unmarshal(raw, &answer); err != nil || len(answer.Content) == 0 {
		t.Fatalf("CodeGraph answer %s: %v", raw, err)
	}
	var texts []string
	for _, item := range answer.Content {
		texts = append(texts, item.Text)
	}
	return strings.Join(texts, "\n")
}

// defaultQuestions are the inputs whose CodeGraph answers library-expected.json stores.
//
// Callers, callees and the depth-1 impact must agree exactly, as in
// TestGraphSymbolToolsMatchCodeGraph (internal/postgres/graph_symbols_test.go).
// Impact at depth 2 may also name the nodes that test lists as corrected:
// GraphNest reports the shortest dependency depth where the pinned
// depth-first walk omits them (docs/graph-analysis.md).
//
// Explore is not equal: no Stage 1 test established file-set equality, and
// GraphNest applies its own source budget (docs/graph-exploration.md), so it
// returns main.ts with source and consumer.test.ts as a pointer beside the
// three files CodeGraph shows. The CodeGraph files must all appear with source.
func (g *gate) defaultQuestions() []gateQuestion {
	t := g.t
	data, err := os.ReadFile(filepath.Join("..", "fixtures", "codegraph", "library-expected.json"))
	if err != nil {
		t.Fatal(err)
	}
	var expected map[string]json.RawMessage
	if err := json.Unmarshal(data, &expected); err != nil {
		t.Fatal(err)
	}
	answer := func(id string) (json.RawMessage, string) { return expected[id], mcpText(t, expected[id]) }
	calls := func(id, tool, symbol string) gateQuestion {
		raw, text := answer(id)
		return gateQuestion{tool: tool, arguments: map[string]any{"symbol": symbol}, upstream: raw, upstreamKeys: codeGraphCalls(text), graphnest: graphNestCalls, noCounterpart: callsNote}
	}
	impact := func(id string, arguments map[string]any, extra ...string) gateQuestion {
		raw, text := answer(id)
		return gateQuestion{tool: "graph_impact_radius", arguments: arguments, upstream: raw, upstreamKeys: codeGraphImpact(text), graphnest: graphNestImpact, allowedExtra: extra, noCounterpart: impactNote}
	}
	exploreRaw, exploreText := answer("mcp-explore-source")
	return []gateQuestion{
		calls("mcp-callers-grouped", "graph_callers", "normalize"),
		calls("mcp-callees-grouped", "graph_callees", "greet"),
		impact("mcp-impact-grouped", map[string]any{"symbol": "identity"}),
		impact("mcp-impact-depth", map[string]any{"symbol": "normalize", "file": "core.ts", "depth": 1}),
		impact("mcp-impact-file", map[string]any{"symbol": "normalize", "file": "core.ts"}, "|consumer.ts|consumer.ts:1", "|consumer.ts|processGreeting:2"),
		{tool: "explore", arguments: map[string]any{"query": "processGreeting"}, upstream: exploreRaw, upstreamKeys: codeGraphExploreFiles(exploreText), graphnest: graphNestExploreFiles, noCounterpart: exploreNote, extraRule: exploreRule},
		{tool: "graph_files", arguments: map[string]any{"limit": 100}, upstream: map[string]any{"fixture_indexed_paths": g.artifactFiles}, upstreamKeys: g.artifactFiles, graphnest: graphNestFiles, noCounterpart: filesNote},
	}
}

// answeredQuestions are the questions in a gate-answers.mjs file, with CodeGraph's answers from it.
func (g *gate) answeredQuestions(path string) []gateQuestion {
	t := g.t
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var file struct {
		Symbol  string                     `json:"symbol"`
		File    string                     `json:"file"`
		Answers map[string]json.RawMessage `json:"answers"`
	}
	if err := json.Unmarshal(data, &file); err != nil || file.Symbol == "" || file.File == "" {
		t.Fatalf("%s: %v", path, err)
	}
	text := func(id string) string { return mcpText(t, file.Answers[id]) }
	files := map[string]any{"limit": 100}
	if directory := filepath.ToSlash(filepath.Dir(file.File)); directory != "." {
		files["directory"] = directory
	}
	return []gateQuestion{
		{tool: "graph_callers", arguments: map[string]any{"symbol": file.Symbol}, upstream: file.Answers["callers"], upstreamKeys: codeGraphCalls(text("callers")), graphnest: graphNestCalls, noCounterpart: callsNote},
		{tool: "graph_callees", arguments: map[string]any{"symbol": file.Symbol}, upstream: file.Answers["callees"], upstreamKeys: codeGraphCalls(text("callees")), graphnest: graphNestCalls, noCounterpart: callsNote},
		{tool: "graph_impact_radius", arguments: map[string]any{"symbol": file.Symbol, "depth": 2}, upstream: file.Answers["impact"], upstreamKeys: codeGraphImpact(text("impact")), graphnest: graphNestImpact, noCounterpart: impactNote},
		{tool: "explore", arguments: map[string]any{"query": file.Symbol}, upstream: file.Answers["explore"], upstreamKeys: codeGraphExploreFiles(text("explore")), graphnest: graphNestExploreFiles, noCounterpart: exploreNote},
		{tool: "graph_files", arguments: files, upstream: file.Answers["files"], upstreamKeys: codeGraphFiles(text("files")), graphnest: graphNestFiles, noCounterpart: filesNote},
	}
}

var (
	impactHeading = regexp.MustCompile(`^\*\*Impact: "(.*)" affects \d+ symbols\*\*$`)
	fileHeading   = regexp.MustCompile(`^\*\*(.+):\*\*$`)
	exploreFile   = regexp.MustCompile("(?m)^\\*\\*`([^`]+)`\\*\\*")
	filesLine     = regexp.MustCompile(`^- (\S+) \(`)
)

// codeGraphCalls keys a callers or callees answer as "definition|neighbor".
// The definition is empty when the symbol has a single definition.
func codeGraphCalls(text string) []string {
	multi := strings.Contains(text, "distinct definitions")
	section := ""
	keys := []string{}
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimRight(line, " ")
		switch {
		case multi && strings.HasPrefix(line, "**") && !strings.Contains(line, "distinct definitions"):
			section = strings.ReplaceAll(line, "**", "")
		case strings.HasPrefix(line, "- ") && !strings.HasPrefix(line, "- (no ") && !strings.HasPrefix(line, "- … +"):
			neighbor, _, _ := strings.Cut(strings.TrimPrefix(line, "- "), " — via ")
			keys = append(keys, section+"|"+neighbor)
		}
	}
	slices.Sort(keys)
	return keys
}

// codeGraphImpact keys an impact answer as "definition|path|name:line".
func codeGraphImpact(text string) []string {
	multi := strings.Contains(text, "distinct definitions")
	section, file := "", ""
	keys := []string{}
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimRight(line, " ")
		if match := impactHeading.FindStringSubmatch(line); match != nil {
			section, file = "", ""
			if multi {
				section = match[1]
			}
		} else if match := fileHeading.FindStringSubmatch(line); match != nil {
			file = match[1]
		} else if line != "" && file != "" && !strings.HasPrefix(line, ">") {
			for _, node := range strings.Split(line, ", ") {
				keys = append(keys, section+"|"+file+"|"+node)
			}
		}
	}
	slices.Sort(keys)
	return keys
}

func codeGraphExploreFiles(text string) []string {
	keys := []string{}
	for _, match := range exploreFile.FindAllStringSubmatch(text, -1) {
		keys = append(keys, match[1])
	}
	slices.Sort(keys)
	return slices.Compact(keys)
}

func codeGraphFiles(text string) []string {
	keys := []string{}
	for _, line := range strings.Split(text, "\n") {
		if match := filesLine.FindStringSubmatch(line); match != nil {
			keys = append(keys, match[1])
		}
	}
	slices.Sort(keys)
	return keys
}

// gateLine is the one-based start line CodeGraph prints; v2 positions are zero-based.
func gateLine(node *graphv2.Node) int32 { return node.GetLocation().GetStart().GetLine() + 1 }

// gateHeading is CodeGraph's heading of a definition: "qualified name (kind) — path:line".
func gateHeading(node *graphv2.Node) string {
	return fmt.Sprintf("%s (%v) — %s:%d", node.GetQualifiedName(), node.GetKind(), node.GetPath(), gateLine(node))
}

func graphNestCalls(t *testing.T, structured any) []string {
	var response graphprotocol.SymbolResponse
	decode(t, structured, &response)
	keys := []string{}
	for _, definition := range response.Definitions {
		section := ""
		if len(response.Definitions) > 1 && len(definition.Definitions) > 0 {
			section = gateHeading(definition.Definitions[0].Fact)
		}
		for _, related := range definition.Related {
			node := related.Entity.Fact
			keys = append(keys, fmt.Sprintf("%s|%s (%v) - %s:%d", section, node.GetName(), node.GetKind(), node.GetPath(), gateLine(node)))
		}
	}
	slices.Sort(keys)
	return keys
}

func graphNestImpact(t *testing.T, structured any) []string {
	var response graphprotocol.SymbolResponse
	decode(t, structured, &response)
	keys := []string{}
	for _, definition := range response.Definitions {
		section := ""
		if len(response.Definitions) > 1 && len(definition.Definitions) > 0 {
			head := definition.Definitions[0].Fact
			section = fmt.Sprintf("%s (%s:%d)", head.GetQualifiedName(), head.GetPath(), gateLine(head))
		}
		for _, entity := range definition.Entities {
			node := entity.Fact
			keys = append(keys, fmt.Sprintf("%s|%s|%s:%d", section, node.GetPath(), node.GetName(), gateLine(node)))
		}
	}
	slices.Sort(keys)
	return keys
}

func graphNestExploreFiles(t *testing.T, structured any) []string {
	var response struct {
		Files []struct {
			Path     string `json:"path"`
			Status   string `json:"status"`
			Mode     string `json:"mode"`
			Segments []any  `json:"segments"`
		} `json:"files"`
	}
	decode(t, structured, &response)
	keys := []string{}
	for _, file := range response.Files {
		switch {
		case file.Mode == "pointer" && len(file.Segments) == 0:
			keys = append(keys, file.Path+" [pointer]")
		case file.Status == "ok" && len(file.Segments) > 0:
			keys = append(keys, file.Path)
		default:
			t.Errorf("explore file %s has status %q, mode %q and %d segments: neither source nor a pointer", file.Path, file.Status, file.Mode, len(file.Segments))
		}
	}
	slices.Sort(keys)
	return keys
}

func graphNestFiles(t *testing.T, structured any) []string {
	var response graphprotocol.FilesResponse
	decode(t, structured, &response)
	keys := []string{}
	for _, file := range response.Files {
		keys = append(keys, file.Fact.GetPath())
	}
	slices.Sort(keys)
	return keys
}
