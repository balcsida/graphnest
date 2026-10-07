//go:build integration

package integration

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/balcsida/graphnest/internal/authn"
	"github.com/balcsida/graphnest/internal/authz"
	"github.com/balcsida/graphnest/internal/cli"
	"github.com/balcsida/graphnest/internal/graphimport"
	"github.com/balcsida/graphnest/internal/graphingest"
	"github.com/balcsida/graphnest/internal/graphquery"
	"github.com/balcsida/graphnest/internal/graphservice"
	"github.com/balcsida/graphnest/internal/httpapi"
	"github.com/balcsida/graphnest/internal/mcpserver"
	"github.com/balcsida/graphnest/internal/repository"
	"github.com/balcsida/graphnest/internal/scipgraph"
	"github.com/balcsida/graphnest/pkg/api"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const (
	cliPublishSHA     = "4444444444444444444444444444444444444444"
	cliPublishNextSHA = "5555555555555555555555555555555555555555"
	cliPublishGitHub  = 101
	cliPublishRepo    = "101"
	cliPublisherToken = "publisher-token"
	cliAdminToken     = "admin"
	cliCodeGraphIndex = "../fixtures/codegraph/reference.db"
	cliCodeGraphTree  = "../fixtures/codegraph/source"
)

// cliFixtureRepository serves the CodeGraph fixture sources as the commit's archive.
type cliFixtureRepository struct{}

func (cliFixtureRepository) Archive(context.Context, string, string) (io.ReadCloser, error) {
	var buffer bytes.Buffer
	writer := tar.NewWriter(&buffer)
	err := filepath.WalkDir(cliCodeGraphTree, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		relative, _ := filepath.Rel(cliCodeGraphTree, path)
		if relative == "." {
			return nil
		}
		name := filepath.ToSlash(relative)
		if entry.IsDir() {
			return writer.WriteHeader(&tar.Header{Typeflag: tar.TypeDir, Name: name + "/", Mode: 0o755})
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if err := writer.WriteHeader(&tar.Header{Typeflag: tar.TypeReg, Name: name, Size: int64(len(data)), Mode: 0o644}); err != nil {
			return err
		}
		_, err = writer.Write(data)
		return err
	})
	if err == nil {
		err = writer.Close()
	}
	if err != nil {
		return nil, err
	}
	return io.NopCloser(&buffer), nil
}

func (cliFixtureRepository) Ignored(ctx context.Context, patterns string, ignoreCase bool, paths []string) ([]string, error) {
	return graphimport.ExecGit{}.Ignored(ctx, patterns, ignoreCase, paths)
}

// publishFront counts uploads and lets a test act just before the server sees one.
type publishFront struct {
	next       http.Handler
	posts      atomic.Int32
	beforePost func(number int32)
}

func (front *publishFront) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	if request.Method == http.MethodPost && request.URL.Path == "/v1/graph/uploads" {
		number := front.posts.Add(1)
		if front.beforePost != nil {
			front.beforePost(number)
		}
	}
	front.next.ServeHTTP(writer, request)
}

// cliPublishFixture is the real server pieces behind one HTTP front with the principals
// admin (administrator), publisher (subject 42, token publisher-token) and reader (token user).
type cliPublishFixture struct {
	h          *postgresHarness
	front      *publishFront
	server     *httptest.Server
	internalID int64
}

func newCLIPublishFixture(t *testing.T, commit string) *cliPublishFixture {
	t.Helper()
	h := newPostgresHarness(t)
	internalID := h.seedRepository(t, 10, cliPublishGitHub)
	setGraphCommit(t, h, internalID, commit)
	if err := h.store.UpsertSearchNode(t.Context(), "node-a", "http://search.invalid"); err != nil { // repository status needs a search node
		t.Fatal(err)
	}
	scope := []int64{cliPublishGitHub}
	authenticator := authn.NewStatic(map[string]authn.Principal{
		cliAdminToken:     {Subject: "admin", Method: "api_token", Administrator: true, InstallationID: 10, RepositoryIDs: scope},
		cliPublisherToken: {Subject: "42", Method: "api_token", InstallationID: 10, RepositoryIDs: scope},
		"user":            {Subject: "reader", Method: "api_token", InstallationID: 10, RepositoryIDs: scope}, // the token graphMCP presents
	})
	authorizer := authz.NewPostgres(h.store)
	grants := &httpapi.UploadGrants{Set: h.store.SetGraphPublicationGrant, Resolve: func(ctx context.Context, principal authn.Principal, githubID int64) (int64, error) {
		repo, err := authorizer.AuthorizedRepository(ctx, principal, githubID)
		return repo.ID, err
	}}
	requestAuth := authn.RequestAuthenticator{Bearer: authenticator}
	repositories := &repository.Service{Store: h.store, SCIP: h.store, Graph: h.store}
	scipService := &scipgraph.Service{Store: h.store}
	graphService := &graphservice.Service{Store: h.store, Backend: &graphquery.Service{Store: h.store}, Limits: graphservice.Limits{MaxResponseBytes: 256 << 10}}
	mux := http.NewServeMux()
	httpapi.RegisterGraphIngestion(mux, authenticator, &graphingest.Service{Store: h.store, MaxUploadBytes: 8 << 20}, grants, 8<<20, 1<<20)
	httpapi.RegisterRepositories(mux, requestAuth, repositories, 64<<10, 100, 256<<10)
	httpapi.RegisterSCIP(mux, requestAuth, scipService, 64<<10, 64<<20, 256<<10)
	httpapi.RegisterGraphQueries(mux, authenticator, graphService, 64<<10, 256<<10)
	mux.Handle("/mcp", httpapi.AuthenticateBearer(authenticator, mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server {
		return mcpserver.NewWithLimits(mcpserver.Services{Graph: graphService, SCIP: scipService}, mcpserver.Limits{MaxOutputBytes: 256 << 10, GraphMaxOutputBytes: 256 << 10})
	}, nil)))
	front := &publishFront{next: mux}
	server := httptest.NewServer(front)
	t.Cleanup(server.Close)
	return &cliPublishFixture{h: h, front: front, server: server, internalID: internalID}
}

func (f *cliPublishFixture) grant(t *testing.T, allow bool) {
	t.Helper()
	if err := f.h.store.SetGraphPublicationGrant(t.Context(), f.internalID, "42", "admin", allow); err != nil {
		t.Fatal(err)
	}
}

func (f *cliPublishFixture) active(t *testing.T) *api.GraphActiveGeneration {
	t.Helper()
	active, err := f.h.store.ActiveGraphGeneration(t.Context(), f.internalID)
	if err != nil {
		t.Fatal(err)
	}
	return active
}

func (f *cliPublishFixture) entityGeneration(t *testing.T, commit string) int64 {
	t.Helper()
	generations, err := f.h.store.EntityGenerations(t.Context(), []graphquery.QuerySnapshot{{RepositoryID: f.internalID, Commit: commit}})
	if err != nil || len(generations) != 1 {
		t.Fatalf("entity generations=%v err=%v", generations, err)
	}
	return generations[0].UploadID
}

// adminPublish publishes through the HTTP API as the administrator.
func (f *cliPublishFixture) adminPublish(t *testing.T, commit string, expected int64, extra string, artifact []byte) api.GraphPublicationResult {
	t.Helper()
	target := f.server.URL + "/v1/graph/uploads?repository_id=" + cliPublishRepo + "&commit=" + commit + "&expected_generation=" + strconv.FormatInt(expected, 10) + extra
	request, err := http.NewRequestWithContext(t.Context(), http.MethodPost, target, bytes.NewReader(artifact))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer "+cliAdminToken)
	request.Header.Set("Content-Type", api.GraphArtifactV2ContentType)
	response, err := f.server.Client().Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	var result api.GraphPublicationResult
	if body, _ := io.ReadAll(response.Body); response.StatusCode != http.StatusOK || json.Unmarshal(body, &result) != nil {
		t.Fatalf("admin publication=%d %s", response.StatusCode, body)
	}
	return result
}

func (f *cliPublishFixture) uploadCount(t *testing.T) int {
	t.Helper()
	var count int
	if err := f.h.pool.QueryRow(t.Context(), `select count(*) from graph_uploads where repository_id=$1`, f.internalID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}

// cliOutcome is what graph import and graph upload print on success.
type cliOutcome struct {
	Published bool `json:"published"`
	Server    *struct {
		IndexedSHA string                     `json:"indexed_sha"`
		Permitted  bool                       `json:"permitted"`
		Before     *api.GraphActiveGeneration `json:"active_generation_before"`
		After      *api.GraphActiveGeneration `json:"active_generation_after"`
	} `json:"server"`
	Publication *struct {
		ExpectedGeneration int64                      `json:"expected_generation"`
		ReplaceProducer    bool                       `json:"replace_producer"`
		Attempts           int                        `json:"attempts"`
		Result             api.GraphPublicationResult `json:"result"`
	} `json:"publication"`
}

// runCLI runs graphnest in-process against url with the given bearer token.
func runCLI(t *testing.T, url, bearer string, args ...string) (code int, stdout, stderr string) {
	t.Helper()
	env := cli.OSEnvironment()
	env.Getenv = func(key string) string {
		return map[string]string{"GRAPHNEST_SERVER_URL": url, "GRAPHNEST_TOKEN": bearer}[key]
	}
	env.Repository = cliFixtureRepository{}
	env.Sleep = func(context.Context, time.Duration) error { return nil }
	var out, errOut bytes.Buffer
	code = cli.Run(t.Context(), args, env, &out, &errOut)
	if strings.Contains(out.String()+errOut.String(), bearer) {
		t.Fatalf("the token was printed: %s %s", out.String(), errOut.String())
	}
	return code, out.String(), errOut.String()
}

func cliImportArgs(commit string, extra ...string) []string {
	return append([]string{"graph", "import", "codegraph", "--index", cliCodeGraphIndex, "--commit", commit, "--repository-id", cliPublishRepo}, extra...)
}

func decodeOutcome(t *testing.T, stdout string) cliOutcome {
	t.Helper()
	var outcome cliOutcome
	if err := json.Unmarshal([]byte(stdout), &outcome); err != nil {
		t.Fatalf("%v: %q", err, stdout)
	}
	return outcome
}

// writeArtifact has the command write the fixture's artifact for commit and returns its path.
func (f *cliPublishFixture) writeArtifact(t *testing.T, commit string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "graph.pb")
	if code, _, stderr := runCLI(t, f.server.URL, cliPublisherToken, cliImportArgs(commit, "--output", path)...); code != 0 {
		t.Fatalf("--output code=%d %s", code, stderr)
	}
	return path
}

func cliUploadArgs(path string, extra ...string) []string {
	return append([]string{"graph", "upload", path, "--repository-id", cliPublishRepo}, extra...)
}

// TestCLIImportPublishesAndRetries covers an exact-SHA publication and its retry.
func TestCLIImportPublishesAndRetries(t *testing.T) {
	f := newCLIPublishFixture(t, cliPublishSHA)
	f.grant(t, true)

	code, stdout, stderr := runCLI(t, f.server.URL, cliPublisherToken, cliImportArgs(cliPublishSHA)...)
	if code != 0 {
		t.Fatalf("code=%d stderr=%s", code, stderr)
	}
	first := decodeOutcome(t, stdout)
	active := f.active(t)
	if !first.Published || first.Publication == nil || first.Publication.Result.Generation == 0 || first.Publication.Result.Deduplicated || first.Publication.Attempts != 1 {
		t.Fatalf("outcome=%s", stdout)
	}
	if active == nil || active.ID != first.Publication.Result.Generation || active.Producer != "codegraph" || active.Commit != cliPublishSHA || active.SchemaVersion != 2 {
		t.Fatalf("active=%#v", active)
	}
	if first.Server == nil || first.Server.Before != nil || first.Server.After == nil || first.Server.After.ID != active.ID || !first.Server.Permitted || first.Server.IndexedSHA != cliPublishSHA {
		t.Fatalf("server block=%#v", first.Server)
	}
	if got := f.entityGeneration(t, cliPublishSHA); got != active.ID {
		t.Fatalf("entity generation=%d active=%d", got, active.ID)
	}
	var publisher string
	if err := f.h.pool.QueryRow(t.Context(), `select publisher from graph_uploads where id=$1`, active.ID).Scan(&publisher); err != nil || publisher != "api_token:42" {
		t.Fatalf("publisher=%q err=%v", publisher, err)
	}
	callers := graphMCP(t, f.server, "graph_callers", map[string]any{"repo": cliPublishGitHub, "symbol": "createService"}).(map[string]any)
	definitions, _ := callers["definitions"].([]any)
	if callers["status"] != "ok" || len(definitions) == 0 {
		t.Fatalf("graph_callers=%#v", callers)
	}
	if related, _ := definitions[0].(map[string]any)["related"].([]any); len(related) == 0 {
		t.Fatalf("createService has no callers: %#v", callers)
	}

	// Running it again is the retry after a lost response: same generation, no new row.
	rows := f.uploadCount(t)
	code, stdout, stderr = runCLI(t, f.server.URL, cliPublisherToken, cliImportArgs(cliPublishSHA)...)
	if code != 0 {
		t.Fatalf("retry code=%d stderr=%s", code, stderr)
	}
	retry := decodeOutcome(t, stdout)
	if !retry.Publication.Result.Deduplicated || retry.Publication.Result.Generation != active.ID || retry.Publication.ExpectedGeneration != active.ID || f.active(t).ID != active.ID || f.uploadCount(t) != rows {
		t.Fatalf("retry=%s rows %d -> %d", stdout, rows, f.uploadCount(t))
	}
}

// TestCLIUploadRefusesAdvancedCommit: the server moved on, so the old artifact is never sent.
func TestCLIUploadRefusesAdvancedCommit(t *testing.T) {
	f := newCLIPublishFixture(t, cliPublishSHA)
	f.grant(t, true)
	if code, _, stderr := runCLI(t, f.server.URL, cliPublisherToken, cliImportArgs(cliPublishSHA)...); code != 0 {
		t.Fatalf("code=%d %s", code, stderr)
	}
	published := f.active(t)
	artifact := f.writeArtifact(t, cliPublishSHA)
	setGraphCommit(t, f.h, f.internalID, cliPublishNextSHA)
	posts, rows := f.front.posts.Load(), f.uploadCount(t)

	code, stdout, stderr := runCLI(t, f.server.URL, cliPublisherToken, cliUploadArgs(artifact)...)
	if code != 1 || stdout != "" || !strings.Contains(stderr, "the server indexes "+cliPublishNextSHA+" but the artifact is for "+cliPublishSHA) {
		t.Fatalf("code=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
	if f.front.posts.Load() != posts || f.uploadCount(t) != rows || f.active(t).ID != published.ID {
		t.Fatalf("the refused upload changed the server: posts %d -> %d, active %#v", posts, f.front.posts.Load(), f.active(t))
	}
}

// TestCLIUploadRevokedGrant: without the grant the preflight says so and nothing is sent.
func TestCLIUploadRevokedGrant(t *testing.T) {
	f := newCLIPublishFixture(t, cliPublishSHA)
	f.grant(t, true)
	artifact := f.writeArtifact(t, cliPublishSHA)
	f.grant(t, false)

	code, stdout, stderr := runCLI(t, f.server.URL, cliPublisherToken, cliUploadArgs(artifact)...)
	if code != 1 || stdout != "" || !strings.Contains(stderr, "this token may not publish to repository 101; an administrator can grant it with PUT /v1/graph/publication-grants") {
		t.Fatalf("code=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
	if f.front.posts.Load() != 0 || f.active(t) != nil || f.uploadCount(t) != 0 {
		t.Fatalf("posts=%d active=%#v rows=%d", f.front.posts.Load(), f.active(t), f.uploadCount(t))
	}

	// The same holds for a caller that never had the grant: read access alone does not publish.
	if code, _, stderr = runCLI(t, f.server.URL, "user", cliUploadArgs(artifact)...); code != 1 || !strings.Contains(stderr, "may not publish") || f.front.posts.Load() != 0 {
		t.Fatalf("reader code=%d stderr=%q posts=%d", code, stderr, f.front.posts.Load())
	}
}

// TestCLIUploadGenerationConflict: an expectation that went stale is refused locally, and one that
// goes stale between the preflight and the upload is refused by the server; the other generation survives.
func TestCLIUploadGenerationConflict(t *testing.T) {
	f := newCLIPublishFixture(t, cliPublishSHA)
	f.grant(t, true)
	artifact := f.writeArtifact(t, cliPublishSHA)
	previous := f.adminPublish(t, cliPublishSHA, 0, "", publicationArtifact(t, cliPublishRepo, cliPublishSHA, "previous"))
	admin := f.adminPublish(t, cliPublishSHA, previous.Generation, "", publicationArtifact(t, cliPublishRepo, cliPublishSHA, "admin"))
	f.front.posts.Store(0)

	code, stdout, stderr := runCLI(t, f.server.URL, cliPublisherToken, cliUploadArgs(artifact, "--expected-generation", strconv.FormatInt(previous.Generation, 10))...)
	want := "the active published generation is " + strconv.FormatInt(admin.Generation, 10) + ", not " + strconv.FormatInt(previous.Generation, 10)
	if code != 1 || stdout != "" || !strings.Contains(stderr, want) || f.front.posts.Load() != 0 || f.active(t).ID != admin.Generation {
		t.Fatalf("local refusal: code=%d stdout=%q stderr=%q posts=%d", code, stdout, stderr, f.front.posts.Load())
	}

	// The administrator publishes right after the publisher's preflight.
	var racing api.GraphPublicationResult
	f.front.beforePost = func(number int32) {
		if number == 1 {
			f.front.beforePost = nil
			racing = f.adminPublish(t, cliPublishSHA, admin.Generation, "", publicationArtifact(t, cliPublishRepo, cliPublishSHA, "racing"))
		}
	}
	code, stdout, stderr = runCLI(t, f.server.URL, cliPublisherToken, cliUploadArgs(artifact)...)
	if code != 1 || stdout != "" || !strings.Contains(stderr, "another publication or a new indexed commit landed since the preflight; read the status and retry") {
		t.Fatalf("server conflict: code=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
	if racing.Generation == 0 || f.active(t).ID != racing.Generation || f.front.posts.Load() != 2 { // the admin's post and the publisher's single attempt
		t.Fatalf("racing=%#v active=%#v posts=%d", racing, f.active(t), f.front.posts.Load())
	}
}

// TestCLIUploadProducerReplacement: another producer's generation needs --replace-producer.
func TestCLIUploadProducerReplacement(t *testing.T) {
	f := newCLIPublishFixture(t, cliPublishSHA)
	f.grant(t, true)
	artifact := f.writeArtifact(t, cliPublishSHA)
	other := f.adminPublish(t, cliPublishSHA, 0, "", publicationArtifactFor(t, cliPublishRepo, cliPublishSHA, "label", "other"))
	f.front.posts.Store(0)

	code, stdout, stderr := runCLI(t, f.server.URL, cliPublisherToken, cliUploadArgs(artifact)...)
	if code != 1 || stdout != "" || !strings.Contains(stderr, "the active generation was published by other; pass --replace-producer to replace it") {
		t.Fatalf("code=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
	if f.front.posts.Load() != 0 || f.active(t).ID != other.Generation {
		t.Fatalf("posts=%d active=%#v", f.front.posts.Load(), f.active(t))
	}

	code, stdout, stderr = runCLI(t, f.server.URL, cliPublisherToken, cliUploadArgs(artifact, "--replace-producer")...)
	if code != 0 {
		t.Fatalf("code=%d stderr=%s", code, stderr)
	}
	replaced := decodeOutcome(t, stdout)
	active := f.active(t)
	if replaced.Publication.Result.ReplacedGeneration != other.Generation || !replaced.Publication.ReplaceProducer || active.ID != replaced.Publication.Result.Generation || active.Producer != "codegraph" {
		t.Fatalf("outcome=%s active=%#v", stdout, active)
	}
}

// TestCLIUploadInterrupted: a connection cut halfway through the first upload leaves the previous
// generation active, and the command's retry publishes.
func TestCLIUploadInterrupted(t *testing.T) {
	f := newCLIPublishFixture(t, cliPublishSHA)
	f.grant(t, true)
	artifact := f.writeArtifact(t, cliPublishSHA)
	previous := f.adminPublish(t, cliPublishSHA, 0, "", publicationArtifact(t, cliPublishRepo, cliPublishSHA, "previous"))
	f.front.posts.Store(0)

	upstream, err := url.Parse(f.server.URL)
	if err != nil {
		t.Fatal(err)
	}
	passthrough := httputil.NewSingleHostReverseProxy(upstream)
	var cut atomic.Bool
	var halfReached atomic.Bool
	proxy := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPost || cut.Swap(true) {
			passthrough.ServeHTTP(writer, request)
			return
		}
		half, err := io.ReadAll(io.LimitReader(request.Body, request.ContentLength/2))
		if err != nil {
			t.Errorf("read half body: %v", err)
		}
		// Forward the half upload with a body that ends early, as a dying connection would.
		forwarded, _ := http.NewRequestWithContext(request.Context(), http.MethodPost, upstream.String()+request.URL.RequestURI(), io.MultiReader(bytes.NewReader(half), errReader{}))
		forwarded.Header = request.Header.Clone()
		forwarded.ContentLength = request.ContentLength
		if response, err := http.DefaultClient.Do(forwarded); err == nil {
			halfReached.Store(response.StatusCode == http.StatusOK)
			response.Body.Close()
		}
		if active, err := f.h.store.ActiveGraphGeneration(request.Context(), f.internalID); err != nil || active == nil || active.ID != previous.Generation {
			t.Errorf("a half upload changed the active generation: %#v err=%v", active, err)
		}
		conn, _, err := writer.(http.Hijacker).Hijack()
		if err != nil {
			t.Errorf("hijack: %v", err)
			return
		}
		conn.Close()
	}))
	defer proxy.Close()

	code, stdout, stderr := runCLI(t, proxy.URL, cliPublisherToken, cliUploadArgs(artifact)...)
	if code != 0 {
		t.Fatalf("code=%d stderr=%s", code, stderr)
	}
	outcome := decodeOutcome(t, stdout)
	active := f.active(t)
	if halfReached.Load() || !cut.Load() || outcome.Publication.Attempts != 2 || outcome.Publication.Result.ReplacedGeneration != previous.Generation || active.ID != outcome.Publication.Result.Generation || active.ID == previous.Generation {
		t.Fatalf("outcome=%s active=%#v half reached=%t", stdout, active, halfReached.Load())
	}
}

type errReader struct{}

func (errReader) Read([]byte) (int, error) { return 0, errors.New("connection cut") }

// TestCLIPublishKeepsSCIP: publishing leaves the SCIP-derived generation answering navigation,
// and the published generation is the one entity queries use.
func TestCLIPublishKeepsSCIP(t *testing.T) {
	f := newCLIPublishFixture(t, scipDemoSHA)
	f.grant(t, true)
	index, err := os.ReadFile("../fixtures/scip/go-demo/index.scip")
	if err != nil {
		t.Fatal(err)
	}
	uploadSCIPIndex(t, f.server, cliPublishGitHub, index)

	code, stdout, stderr := runCLI(t, f.server.URL, cliPublisherToken, cliImportArgs(scipDemoSHA)...)
	if code != 0 {
		t.Fatalf("code=%d stderr=%s", code, stderr)
	}
	published := decodeOutcome(t, stdout).Publication.Result.Generation
	generations, err := f.h.store.ActiveGraphGenerations(t.Context(), f.internalID)
	if err != nil || len(generations) != 2 || generations[0].ID != published || generations[0].Source != api.GraphSourceExternal || generations[0].Producer != "codegraph" || generations[1].Source != api.GraphSourceSCIP {
		t.Fatalf("generations=%#v err=%v", generations, err)
	}
	if got := f.entityGeneration(t, scipDemoSHA); got != published {
		t.Fatalf("entity generation=%d want the published %d", got, published)
	}
	references := graphMCP(t, f.server, "navigate_symbol", map[string]any{"repository_id": cliPublishGitHub, "path": "greet/greet.go", "line": 11, "character": 5, "operation": "references"}).(map[string]any)
	if locations, _ := references["locations"].([]any); len(locations) != 2 {
		t.Fatalf("navigate_symbol after publication=%#v", references)
	}
	if !slices.ContainsFunc(generations, func(g api.GraphActiveGeneration) bool {
		return g.Source == api.GraphSourceSCIP && g.Commit == scipDemoSHA
	}) {
		t.Fatalf("SCIP generation lost: %#v", generations)
	}
}
