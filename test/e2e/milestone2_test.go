//go:build e2e && unix

package e2e

import (
	"bufio"
	"bytes"
	"context"
	"crypto"
	"crypto/hmac"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/textproto"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/grepnest/grepnest/internal/authn"
	"github.com/grepnest/grepnest/internal/authz"
	"github.com/grepnest/grepnest/internal/githubapp"
	"github.com/grepnest/grepnest/internal/httpapi"
	"github.com/grepnest/grepnest/internal/indexer"
	"github.com/grepnest/grepnest/internal/mcpserver"
	"github.com/grepnest/grepnest/internal/observability"
	"github.com/grepnest/grepnest/internal/postgres"
	"github.com/grepnest/grepnest/internal/repository"
	"github.com/grepnest/grepnest/internal/search"
	"github.com/grepnest/grepnest/internal/webhook"
	"github.com/grepnest/grepnest/internal/zoekt"
	"github.com/grepnest/grepnest/pkg/api"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const (
	milestoneInstallationID = int64(10)
	milestoneRepositoryID   = int64(101)
	emptyRepositoryID       = int64(102)
	milestoneNeedle         = "MilestoneTwoNeedle"
	milestoneGitToken       = "installation-token-value"
	milestoneWebhookSecret  = "webhook-secret-value"
	milestonePayloadSecret  = "raw-webhook-payload-secret-value"
	rawStderrMarker         = "raw-stderr-marker"
)

func TestMilestone2Vertical(t *testing.T) {
	zoektGitIndex, zoektWebserver := requiredExecutables(t)
	ctx, cancel := context.WithTimeout(t.Context(), 90*time.Second)
	defer cancel()
	database := newMilestoneDatabase(t)
	root := t.TempDir()

	primary := newSmartGitOrigin(t, ctx, root, "acme/source", []string{
		"package fixture\nconst Value = \"one\"\n",
		"package fixture\nconst Value = \"two\"\n",
		"package fixture\nconst Needle = \"" + milestoneNeedle + "\"\n",
	})
	empty := newSmartGitOrigin(t, ctx, root, "acme/empty", nil)
	privateKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	privateKeyPEM := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(privateKey)})
	github := newFakeGHES(t, root, primary, empty, 7, &privateKey.PublicKey)
	caPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: github.server.Certificate().Raw})
	caFile := filepath.Join(root, "github-ca.pem")
	if err := os.WriteFile(caFile, caPEM, 0o600); err != nil {
		t.Fatal(err)
	}
	base, err := url.Parse(github.server.URL)
	if err != nil {
		t.Fatal(err)
	}
	apiBase := *base
	apiBase.Path = "/api/v3"
	endpoints := githubapp.Endpoints{Web: base, API: &apiBase, Upload: base, Git: base}
	httpClient, err := githubapp.NewHTTPClient(caPEM, endpoints, 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := githubapp.NewSigner(7, privateKeyPEM, nil)
	if err != nil {
		t.Fatal(err)
	}
	github.assertRejectsMutatedCredentials(t, signer)
	metrics := observability.New()
	githubClient := githubapp.NewClient(endpoints, httpClient, signer, "2022-11-28", 2<<20, nil, metrics)
	reconciler := githubapp.NewReconciler(githubClient, database.store)
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
	processor := webhook.NewGitHubProcessor(database.store, reconcileRequests, metrics)

	indexDir := filepath.Join(root, "index")
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
	if err := database.store.UpsertSearchNode(ctx, "primary", "http://"+zoektAddress); err != nil {
		t.Fatal(err)
	}

	principal := authn.Principal{Subject: "pilot", InstallationID: milestoneInstallationID, RepositoryIDs: []int64{milestoneRepositoryID, emptyRepositoryID}}
	authenticator := authn.NewStatic(map[string]authn.Principal{token: principal})
	searchService := search.NewService(zoektClient, authz.NewPostgres(database.store), search.Limits{MaxResults: 100, MaxResponseBytes: 256 << 10})
	repositoryService := &repository.Service{Store: database.store, GitHub: githubClient}
	mux := http.NewServeMux()
	httpapi.RegisterSearch(mux, authenticator, searchService, 64<<10, 256<<10)
	httpapi.RegisterRepositories(mux, authenticator, repositoryService, 64<<10, 100, 256<<10)
	httpapi.RegisterGitHubWebhook(mux, []byte(milestoneWebhookSecret), 1<<20, processor)
	mux.Handle("/mcp", httpapi.AuthenticateBearer(authenticator, mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server {
		return mcpserver.New(searchService, repositoryService)
	}, nil)))
	server := httptest.NewServer(mux)
	defer server.Close()

	installationBody := []byte(`{"action":"created","installation":{"id":10},"audit_secret":"` + milestonePayloadSecret + `"}`)
	sendGitHubWebhook(t, server, "installation-one", "installation", installationBody)
	awaitReconciliation(t, reconcileResults)
	assertQueuedTarget(t, database.pool, milestoneRepositoryID, primary.shas[0], 1)
	sendGitHubWebhook(t, server, "installation-one", "installation", installationBody)
	assertQueuedTarget(t, database.pool, milestoneRepositoryID, primary.shas[0], 1)
	sendPush(t, server, "push-two", milestoneRepositoryID, primary.shas[1])
	sendPush(t, server, "push-three", milestoneRepositoryID, primary.shas[2])
	github.setPrimarySHA(primary.shas[2])
	assertQueuedTarget(t, database.pool, milestoneRepositoryID, primary.shas[2], 1)
	primaryRepository := authorizedRepository(t, database.store, milestoneRepositoryID)

	indexerExecutable := filepath.Join(root, "grepnest-indexer")
	libraryPath := os.Getenv("DYLD_LIBRARY_PATH")
	if libraryPath == "" {
		libraryPath = os.Getenv("LD_LIBRARY_PATH")
	}
	buildIndexer := exec.CommandContext(ctx, "go", "build", "-o", indexerExecutable, "../../cmd/grepnest-indexer")
	buildIndexer.Env = append(os.Environ(), "CGO_LDFLAGS="+os.Getenv("CGO_LDFLAGS")+" -Wl,-rpath,"+libraryPath)
	if output, err := buildIndexer.CombinedOutput(); err != nil {
		t.Fatalf("build indexer askpass: %v\n%s", err, output)
	}
	queue := &publicationBarrier{Store: database.store, reached: make(chan struct{}), release: make(chan struct{}), block: true}
	worker := &indexer.Worker{
		ID: "e2e-worker", Queue: queue, Store: database.store, Tokens: githubClient,
		Git: &indexer.Git{
			Binary: "git", BaseURL: github.server.URL, AskPass: indexerExecutable, CABundle: caFile,
			MirrorsDir: filepath.Join(root, "data", "mirrors"), WorktreesDir: filepath.Join(root, "data", "worktrees"),
			Runner: indexer.Runner{MaxOutput: 64 << 10, KillGrace: 100 * time.Millisecond}, CommandTimeout: 10 * time.Second,
		},
		Zoekt: &indexer.ZoektIndexer{
			Binary: zoektGitIndex, IndexDir: indexDir, Runner: indexer.Runner{MaxOutput: 64 << 10, KillGrace: 100 * time.Millisecond},
			Client: zoektClient, IndexTimeout: 15 * time.Second, VisibilityTimeout: 10 * time.Second,
		},
		RenewEvery: time.Second, CleanupTimeout: 5 * time.Second,
	}
	type workerResult struct {
		worked bool
		err    error
	}
	workerDone := make(chan workerResult, 1)
	go func() {
		worked, err := worker.RunOne(ctx)
		workerDone <- workerResult{worked: worked, err: err}
	}()
	select {
	case <-queue.reached:
	case result := <-workerDone:
		var state, code string
		_ = database.pool.QueryRow(t.Context(), "select state,coalesce(error_code,'') from index_jobs order by updated_at desc limit 1").Scan(&state, &code)
		listed, listErr := zoektClient.List(t.Context(), primaryRepository.ZoektID)
		rawList := rawZoektList(t, zoektAddress, fmt.Sprintf("repoid:%d", primaryRepository.ZoektID))
		entries, _ := os.ReadDir(indexDir)
		t.Fatalf("worker exited before publication barrier: worked=%v err=%v job=%s/%s repoid=%d list=%#v/%v raw=%s index=%v zoekt=%s", result.worked, result.err, state, code, primaryRepository.ZoektID, listed, listErr, rawList, entries, zoektProcess.logs.String())
	case <-ctx.Done():
		t.Fatalf("wait for atomic publication barrier: %v", ctx.Err())
	}
	assertVisible(t, ctx, zoektClient, primaryRepository.ZoektID, "main", primary.shas[2])
	if matches := restSearch(t, server, api.SearchRequest{Query: milestoneNeedle}).Matches; len(matches) != 0 {
		t.Fatalf("unpublished Zoekt shard leaked: %#v", matches)
	}
	close(queue.release)
	if result := <-workerDone; result.err != nil || !result.worked {
		t.Fatalf("publish worker: worked=%v err=%v", result.worked, result.err)
	}
	assertPublished(t, database.pool, milestoneRepositoryID, primary.shas[2])

	github.addEmpty()
	sendGitHubWebhook(t, server, "add-empty", "installation_repositories", []byte(`{"action":"added","installation":{"id":10}}`))
	awaitReconciliation(t, reconcileResults)
	queue.block = false
	if worked, err := worker.RunOne(ctx); err != nil || !worked {
		t.Fatalf("index empty repository: worked=%v err=%v", worked, err)
	}
	emptyRepository := authorizedRepository(t, database.store, emptyRepositoryID)
	if repositories, err := zoektClient.List(ctx, emptyRepository.ZoektID); err != nil || !containsVisible(repositories, emptyRepository.ZoektID, "main", empty.shas[0]) {
		entries, _ := os.ReadDir(indexDir)
		var state, code string
		_ = database.pool.QueryRow(t.Context(), "select state,coalesce(error_code,'') from index_jobs j join repositories r on r.id=j.repository_id where r.github_id=$1 order by j.id desc limit 1", emptyRepositoryID).Scan(&state, &code)
		t.Fatalf("empty repository visibility: list=%#v err=%v job=%s/%s raw=%s index=%v zoekt=%s", repositories, err, state, code, rawZoektList(t, zoektAddress, ""), entries, zoektProcess.logs.String())
	}
	assertPublished(t, database.pool, emptyRepositoryID, empty.shas[0])

	assertRESTSurface(t, server, milestoneRepositoryID, primary.shas[2], primary.blobSHA)
	assertMCPSurface(t, server, milestoneRepositoryID, primary.shas[2], primary.blobSHA)

	github.renamePrimary("acme/renamed")
	renameBody := []byte(`{"action":"renamed","installation":{"id":10},"repository":{"id":101,"name":"renamed","clone_url":"https://example.invalid/acme/renamed.git","html_url":"https://example.invalid/acme/renamed","owner":{"login":"acme"}}}`)
	sendGitHubWebhook(t, server, "rename-one", "repository", renameBody)
	renamed := authorizedRepository(t, database.store, milestoneRepositoryID)
	if renamed.ID != primaryRepository.ID || renamed.ZoektID != primaryRepository.ZoektID || renamed.Name != "acme/renamed" {
		t.Fatalf("rename changed identity: before=%#v after=%#v", primaryRepository, renamed)
	}
	if matches := restSearch(t, server, api.SearchRequest{Query: milestoneNeedle, Repositories: []string{"acme/source"}}).Matches; len(matches) != 0 {
		t.Fatalf("old-name reuse broadened authorization: %#v", matches)
	}

	github.archivePrimary()
	archiveBody := []byte(`{"action":"archived","installation":{"id":10},"repository":{"id":101}}`)
	sendGitHubWebhook(t, server, "archive-one", "repository", archiveBody)
	if matches := restSearch(t, server, api.SearchRequest{Query: milestoneNeedle}).Matches; len(matches) != 0 {
		t.Fatalf("disabled repository remained searchable: %#v", matches)
	}
	assertDisabledRead(t, server, milestoneRepositoryID)
	backendLogs := github.backendLogs()
	if !strings.Contains(backendLogs, rawStderrMarker) {
		t.Fatalf("git-http-backend stderr marker was not captured: %q", backendLogs)
	}
	forbidden := []string{milestoneGitToken, milestoneWebhookSecret, token, string(privateKeyPEM), string(installationBody), "Authorization:", "x-access-token@"}
	forbidden = append(forbidden, milestonePayloadSecret)
	assertNoSecrets(t, database.pool, map[string]string{
		"Git mirrors":        filepath.Join(root, "data", "mirrors"),
		"Git worktrees":      filepath.Join(root, "data", "worktrees"),
		"Zoekt index shards": indexDir,
	}, map[string]string{
		"git-http-backend stderr": backendLogs,
		"Zoekt process logs":      zoektProcess.logs.String(),
	}, forbidden...)
	remote := strings.TrimSpace(run(t, ctx, "git", "--git-dir", filepath.Join(root, "data", "mirrors", strconv.FormatInt(primaryRepository.ID, 10)+".git"), "config", "--get", "remote.origin.url"))
	if remote != github.server.URL+"/acme/source.git" || strings.Contains(remote, milestoneGitToken) || strings.Contains(remote, "@") {
		t.Fatalf("persisted remote contains credentials: %q", remote)
	}
	github.assertRequests(t)
}

type publicationBarrier struct {
	*postgres.Store
	reached chan struct{}
	release chan struct{}
	block   bool
	once    sync.Once
}

func (queue *publicationBarrier) CompleteIndex(ctx context.Context, id int64, owner string) error {
	if !queue.block {
		return queue.Store.CompleteIndex(ctx, id, owner)
	}
	queue.once.Do(func() { close(queue.reached) })
	select {
	case <-queue.release:
		return queue.Store.CompleteIndex(ctx, id, owner)
	case <-ctx.Done():
		return ctx.Err()
	}
}

type milestoneDatabase struct {
	pool  *pgxpool.Pool
	store *postgres.Store
}

func newMilestoneDatabase(t *testing.T) milestoneDatabase {
	t.Helper()
	dsn := os.Getenv("GREPNEST_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Fatal("GREPNEST_TEST_POSTGRES_DSN is required for Milestone 2 E2E")
	}
	random := make([]byte, 8)
	if _, err := rand.Read(random); err != nil {
		t.Fatal(err)
	}
	schema := "grepnest_e2e_" + hex.EncodeToString(random)
	admin, err := pgxpool.New(t.Context(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	identifier := pgx.Identifier{schema}.Sanitize()
	if _, err := admin.Exec(t.Context(), "create schema "+identifier); err != nil {
		t.Fatal(err)
	}
	config, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	config.AfterConnect = func(ctx context.Context, connection *pgx.Conn) error {
		_, err := connection.Exec(ctx, "set search_path to "+identifier)
		return err
	}
	pool, err := pgxpool.NewWithConfig(t.Context(), config)
	if err != nil {
		t.Fatal(err)
	}
	if err := postgres.Migrate(t.Context(), pool); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		pool.Close()
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if _, err := admin.Exec(ctx, "drop schema "+identifier+" cascade"); err != nil {
			t.Errorf("drop schema: %v", err)
		}
		admin.Close()
	})
	return milestoneDatabase{pool: pool, store: postgres.New(pool)}
}

type smartGitOrigin struct {
	name, path, content, blobSHA string
	shas                         []string
}

func newSmartGitOrigin(t *testing.T, ctx context.Context, root, name string, versions []string) smartGitOrigin {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(name)+".git")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	run(t, ctx, "git", "init", "--bare", path)
	work := filepath.Join(t.TempDir(), "work")
	run(t, ctx, "git", "init", "--initial-branch=main", work)
	run(t, ctx, "git", "-C", work, "config", "user.name", "GrepNest Test")
	run(t, ctx, "git", "-C", work, "config", "user.email", "test@grepnest.invalid")
	origin := smartGitOrigin{name: name, path: path}
	if versions == nil {
		run(t, ctx, "git", "-C", work, "-c", "commit.gpgsign=false", "commit", "--allow-empty", "-m", "empty")
		origin.shas = append(origin.shas, strings.TrimSpace(run(t, ctx, "git", "-C", work, "rev-parse", "HEAD")))
	} else {
		for index, content := range versions {
			if err := os.WriteFile(filepath.Join(work, "main.go"), []byte(content), 0o600); err != nil {
				t.Fatal(err)
			}
			run(t, ctx, "git", "-C", work, "add", "main.go")
			run(t, ctx, "git", "-C", work, "-c", "commit.gpgsign=false", "commit", "-m", fmt.Sprintf("version %d", index+1))
			origin.shas = append(origin.shas, strings.TrimSpace(run(t, ctx, "git", "-C", work, "rev-parse", "HEAD")))
			origin.content = content
		}
		origin.blobSHA = strings.TrimSpace(run(t, ctx, "git", "-C", work, "rev-parse", "HEAD:main.go"))
	}
	run(t, ctx, "git", "-C", work, "remote", "add", "origin", path)
	run(t, ctx, "git", "-C", work, "push", "origin", "main")
	return origin
}

type fakeRepository struct {
	id       int64
	name     string
	sha      string
	content  string
	blobSHA  string
	archived bool
}

type fakeGHES struct {
	server        *httptest.Server
	gitRoot       string
	mu            sync.Mutex
	repositories  []fakeRepository
	empty         fakeRepository
	apiAuth       int
	gitAuth       int
	appID         string
	publicKey     *rsa.PublicKey
	backendStderr bytes.Buffer
}

func newFakeGHES(t *testing.T, gitRoot string, primary, empty smartGitOrigin, appID int64, publicKey *rsa.PublicKey) *fakeGHES {
	t.Helper()
	github := &fakeGHES{
		gitRoot: gitRoot, appID: strconv.FormatInt(appID, 10), publicKey: publicKey,
		repositories: []fakeRepository{
			{id: milestoneRepositoryID, name: primary.name, sha: primary.shas[0], content: primary.content, blobSHA: primary.blobSHA},
		},
		empty: fakeRepository{id: emptyRepositoryID, name: empty.name, sha: empty.shas[0]},
	}
	github.server = httptest.NewTLSServer(http.HandlerFunc(github.serveHTTP))
	t.Cleanup(github.server.Close)
	return github
}

func (github *fakeGHES) serveHTTP(writer http.ResponseWriter, request *http.Request) {
	if strings.HasPrefix(request.URL.Path, "/api/v3/") {
		github.serveAPI(writer, request)
		return
	}
	github.serveGit(writer, request)
}

func (github *fakeGHES) serveAPI(writer http.ResponseWriter, request *http.Request) {
	appEndpoint := request.URL.Path == "/api/v3/app/installations" || request.URL.Path == "/api/v3/app/installations/10/access_tokens"
	if !github.validAPIHeaders(request) || appEndpoint != github.validAppAuthorization(request) || (!appEndpoint && !github.validInstallationAuthorization(request)) {
		writer.WriteHeader(http.StatusUnauthorized)
		return
	}
	github.mu.Lock()
	github.apiAuth++
	repositories := append([]fakeRepository(nil), github.repositories...)
	github.mu.Unlock()
	writer.Header().Set("Content-Type", "application/json")
	switch {
	case request.Method == http.MethodGet && request.URL.Path == "/api/v3/app/installations":
		_, _ = io.WriteString(writer, `[{"id":10,"account":{"login":"acme","type":"Organization"},"suspended_at":null}]`)
	case request.Method == http.MethodPost && request.URL.Path == "/api/v3/app/installations/10/access_tokens":
		_ = json.NewEncoder(writer).Encode(map[string]any{"token": milestoneGitToken, "expires_at": time.Now().Add(time.Hour).UTC()})
	case request.Method == http.MethodGet && request.URL.Path == "/api/v3/installation/repositories":
		response := struct {
			Repositories []map[string]any `json:"repositories"`
		}{}
		for _, repository := range repositories {
			owner, name, _ := strings.Cut(repository.name, "/")
			response.Repositories = append(response.Repositories, map[string]any{
				"id": repository.id, "full_name": repository.name, "owner": map[string]string{"login": owner}, "name": name,
				"clone_url": github.server.URL + "/" + repository.name + ".git", "html_url": github.server.URL + "/" + repository.name,
				"default_branch": "main", "private": true, "archived": repository.archived, "disabled": false,
			})
		}
		_ = json.NewEncoder(writer).Encode(response)
	case request.Method == http.MethodGet && strings.Contains(request.URL.Path, "/branches/main"):
		name := strings.TrimSuffix(strings.TrimPrefix(request.URL.Path, "/api/v3/repos/"), "/branches/main")
		for _, repository := range repositories {
			if repository.name == name {
				_ = json.NewEncoder(writer).Encode(map[string]any{"commit": map[string]string{"sha": repository.sha}})
				return
			}
		}
		writer.WriteHeader(http.StatusNotFound)
	case request.Method == http.MethodGet && strings.Contains(request.URL.Path, "/contents/"):
		nameAndPath := strings.TrimPrefix(request.URL.Path, "/api/v3/repos/")
		parts := strings.SplitN(nameAndPath, "/contents/", 2)
		if len(parts) != 2 || request.URL.Query().Get("ref") == "" {
			writer.WriteHeader(http.StatusBadRequest)
			return
		}
		for _, repository := range repositories {
			if repository.name == parts[0] && request.URL.Query().Get("ref") == repository.sha && parts[1] == "main.go" {
				_ = json.NewEncoder(writer).Encode(githubapp.Content{Type: "file", Encoding: "base64", Content: base64.StdEncoding.EncodeToString([]byte(repository.content)), SHA: repository.blobSHA, Size: int64(len(repository.content))})
				return
			}
		}
		writer.WriteHeader(http.StatusNotFound)
	default:
		writer.WriteHeader(http.StatusNotFound)
	}
}

func (github *fakeGHES) validAPIHeaders(request *http.Request) bool {
	return request.Header.Get("Accept") == "application/vnd.github+json" &&
		request.Header.Get("X-GitHub-Api-Version") == "2022-11-28" &&
		request.Header.Get("User-Agent") != ""
}

func (github *fakeGHES) validInstallationAuthorization(request *http.Request) bool {
	values := request.Header.Values("Authorization")
	return len(values) == 1 && values[0] == "Bearer "+milestoneGitToken
}

func (github *fakeGHES) validAppAuthorization(request *http.Request) bool {
	values := request.Header.Values("Authorization")
	if len(values) != 1 || !strings.HasPrefix(values[0], "Bearer ") {
		return false
	}
	parts := strings.Split(strings.TrimPrefix(values[0], "Bearer "), ".")
	if len(parts) != 3 {
		return false
	}
	unsigned := parts[0] + "." + parts[1]
	signature, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		return false
	}
	hash := sha256.Sum256([]byte(unsigned))
	if rsa.VerifyPKCS1v15(github.publicKey, crypto.SHA256, hash[:], signature) != nil {
		return false
	}
	var header struct {
		Algorithm string `json:"alg"`
		Type      string `json:"typ"`
	}
	var claims struct {
		IssuedAt  int64  `json:"iat"`
		ExpiresAt int64  `json:"exp"`
		Issuer    string `json:"iss"`
	}
	if !decodeJWTPart(parts[0], &header) || !decodeJWTPart(parts[1], &claims) || header.Algorithm != "RS256" || header.Type != "JWT" || claims.Issuer != github.appID {
		return false
	}
	now := time.Now().Unix()
	return claims.IssuedAt >= now-2*60 && claims.IssuedAt <= now && claims.ExpiresAt > now && claims.ExpiresAt <= now+10*60 && claims.ExpiresAt-claims.IssuedAt <= 10*60
}

func decodeJWTPart(part string, target any) bool {
	data, err := base64.RawURLEncoding.DecodeString(part)
	if err != nil {
		return false
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	if err := decoder.Decode(target); err != nil {
		return false
	}
	return decoder.Decode(&struct{}{}) == io.EOF
}

func (github *fakeGHES) serveGit(writer http.ResponseWriter, request *http.Request) {
	username, password, ok := request.BasicAuth()
	if !ok || username != "x-access-token" || password != milestoneGitToken {
		writer.Header().Set("WWW-Authenticate", `Basic realm="GrepNest"`)
		writer.WriteHeader(http.StatusUnauthorized)
		return
	}
	github.mu.Lock()
	github.gitAuth++
	github.mu.Unlock()
	command := exec.CommandContext(request.Context(), "git", "http-backend")
	command.Env = append(os.Environ(),
		"GIT_PROJECT_ROOT="+github.gitRoot, "GIT_HTTP_EXPORT_ALL=1", "PATH_INFO="+request.URL.Path,
		"REQUEST_METHOD="+request.Method, "QUERY_STRING="+request.URL.RawQuery,
		"CONTENT_TYPE="+request.Header.Get("Content-Type"), "CONTENT_LENGTH="+strconv.FormatInt(request.ContentLength, 10),
		"REMOTE_USER=x-access-token", "REMOTE_ADDR=127.0.0.1", "SERVER_PROTOCOL=HTTP/1.1",
		"GIT_TRACE2_EVENT=1", "GIT_TRACE2_PARENT_SID="+rawStderrMarker,
	)
	command.Stdin = request.Body
	var output, stderr bytes.Buffer
	command.Stdout, command.Stderr = &output, &stderr
	err := command.Run()
	github.mu.Lock()
	_, _ = github.backendStderr.Write(stderr.Bytes())
	github.mu.Unlock()
	if err != nil {
		writer.WriteHeader(http.StatusInternalServerError)
		return
	}
	reader := bufio.NewReader(&output)
	headers, err := textproto.NewReader(reader).ReadMIMEHeader()
	if err != nil {
		writer.WriteHeader(http.StatusInternalServerError)
		return
	}
	status := http.StatusOK
	if value := headers.Get("Status"); value != "" {
		status, _ = strconv.Atoi(strings.Fields(value)[0])
		headers.Del("Status")
	}
	for name, values := range headers {
		for _, value := range values {
			writer.Header().Add(name, value)
		}
	}
	writer.WriteHeader(status)
	_, _ = io.Copy(writer, reader)
}

func (github *fakeGHES) backendLogs() string {
	github.mu.Lock()
	defer github.mu.Unlock()
	return github.backendStderr.String()
}

func (github *fakeGHES) renamePrimary(name string) {
	github.mu.Lock()
	defer github.mu.Unlock()
	github.repositories[0].name = name
	github.repositories = append(github.repositories, fakeRepository{id: 103, name: "acme/source", sha: github.empty.sha})
}

func (github *fakeGHES) addEmpty() {
	github.mu.Lock()
	defer github.mu.Unlock()
	github.repositories = append(github.repositories, github.empty)
}

func (github *fakeGHES) setPrimarySHA(sha string) {
	github.mu.Lock()
	defer github.mu.Unlock()
	github.repositories[0].sha = sha
}

func (github *fakeGHES) archivePrimary() {
	github.mu.Lock()
	defer github.mu.Unlock()
	github.repositories[0].archived = true
}

func (github *fakeGHES) assertRequests(t *testing.T) {
	t.Helper()
	github.mu.Lock()
	defer github.mu.Unlock()
	if github.apiAuth == 0 || github.gitAuth == 0 {
		t.Fatalf("authenticated API/Git requests = %d/%d", github.apiAuth, github.gitAuth)
	}
}

func (github *fakeGHES) assertRejectsMutatedCredentials(t *testing.T, appSigner *githubapp.Signer) {
	t.Helper()
	wrongKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	wrongPEM := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(wrongKey)})
	wrongSigner, err := githubapp.NewSigner(7, wrongPEM, nil)
	if err != nil {
		t.Fatal(err)
	}
	wrongJWT, err := wrongSigner.JWT()
	if err != nil {
		t.Fatal(err)
	}
	appJWT, err := appSigner.JWT()
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name, method, path, credential string
	}{
		{"wrong signer", http.MethodGet, "/api/v3/app/installations", wrongJWT},
		{"installation token on App endpoint", http.MethodGet, "/api/v3/app/installations", milestoneGitToken},
		{"App JWT on installation endpoint", http.MethodGet, "/api/v3/installation/repositories", appJWT},
		{"wrong installation token", http.MethodGet, "/api/v3/installation/repositories", "wrong-installation-token"},
	} {
		t.Run(test.name, func(t *testing.T) {
			request, err := http.NewRequestWithContext(t.Context(), test.method, github.server.URL+test.path, nil)
			if err != nil {
				t.Fatal(err)
			}
			request.Header.Set("Authorization", "Bearer "+test.credential)
			request.Header.Set("Accept", "application/vnd.github+json")
			request.Header.Set("X-GitHub-Api-Version", "2022-11-28")
			request.Header.Set("User-Agent", "GrepNest-authenticity-mutation")
			response, err := github.server.Client().Do(request)
			if err != nil {
				t.Fatal(err)
			}
			defer response.Body.Close()
			if response.StatusCode != http.StatusUnauthorized {
				t.Fatalf("status = %d, want %d", response.StatusCode, http.StatusUnauthorized)
			}
		})
	}
}

func sendGitHubWebhook(t *testing.T, server *httptest.Server, delivery, event string, body []byte) {
	t.Helper()
	mac := hmac.New(sha256.New, []byte(milestoneWebhookSecret))
	_, _ = mac.Write(body)
	request, err := http.NewRequestWithContext(t.Context(), http.MethodPost, server.URL+"/webhooks/github", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("X-Hub-Signature-256", "sha256="+hex.EncodeToString(mac.Sum(nil)))
	request.Header.Set("X-GitHub-Event", event)
	request.Header.Set("X-GitHub-Delivery", delivery)
	response, err := server.Client().Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusAccepted {
		data, _ := io.ReadAll(response.Body)
		t.Fatalf("webhook %s status=%d body=%s", delivery, response.StatusCode, data)
	}
}

func sendPush(t *testing.T, server *httptest.Server, delivery string, repositoryID int64, sha string) {
	t.Helper()
	body := []byte(fmt.Sprintf(`{"installation":{"id":10},"repository":{"id":%d,"size":1},"ref":"refs/heads/main","after":%q}`, repositoryID, sha))
	sendGitHubWebhook(t, server, delivery, "push", body)
}

func assertQueuedTarget(t *testing.T, pool *pgxpool.Pool, githubID int64, sha string, count int) {
	t.Helper()
	var gotSHA string
	var gotCount int
	err := pool.QueryRow(t.Context(), `select min(j.target_sha), count(*) from index_jobs j join repositories r on r.id=j.repository_id where r.github_id=$1 and j.state='queued'`, githubID).Scan(&gotSHA, &gotCount)
	if err != nil || gotSHA != sha || gotCount != count {
		t.Fatalf("queued target=%q count=%d err=%v, want %q/%d", gotSHA, gotCount, err, sha, count)
	}
}

func awaitReconciliation(t *testing.T, results <-chan error) {
	t.Helper()
	select {
	case err := <-results:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("reconciliation did not complete")
	}
}

func authorizedRepository(t *testing.T, store *postgres.Store, githubID int64) repository.Repository {
	t.Helper()
	repository, err := store.AuthorizedRepository(t.Context(), milestoneInstallationID, []int64{githubID}, githubID)
	if err != nil {
		t.Fatal(err)
	}
	return repository
}

func assertVisible(t *testing.T, ctx context.Context, client *zoekt.Client, repositoryID uint32, branch, sha string) {
	t.Helper()
	repositories, err := client.List(ctx, repositoryID)
	if err != nil {
		t.Fatal(err)
	}
	if containsVisible(repositories, repositoryID, branch, sha) {
		return
	}
	t.Fatalf("/api/list repositories=%#v, want %d/%s/%s", repositories, repositoryID, branch, sha)
}

func containsVisible(repositories []zoekt.IndexedRepository, repositoryID uint32, branch, sha string) bool {
	for _, repository := range repositories {
		if repository.RepoID == repositoryID && repository.Branch == branch && repository.Version == sha {
			return true
		}
	}
	return false
}

func assertPublished(t *testing.T, pool *pgxpool.Pool, githubID int64, sha string) {
	t.Helper()
	var desired, indexed, status string
	if err := pool.QueryRow(t.Context(), "select desired_sha,indexed_sha,status from repositories where github_id=$1", githubID).Scan(&desired, &indexed, &status); err != nil || desired != sha || indexed != sha || status != "ready" {
		t.Fatalf("published desired=%q indexed=%q status=%q err=%v", desired, indexed, status, err)
	}
}

func assertRESTSurface(t *testing.T, server *httptest.Server, repositoryID int64, sha, blobSHA string) {
	t.Helper()
	searchResponse := restSearch(t, server, api.SearchRequest{Query: milestoneNeedle})
	if len(searchResponse.Matches) != 1 || searchResponse.Matches[0].SHA != sha || searchResponse.Matches[0].Repository.ID != repositoryID {
		t.Fatalf("REST search=%#v", searchResponse)
	}
	repositoryID = searchResponse.Matches[0].Repository.ID
	var list struct {
		Repositories []api.RepositorySummary `json:"repositories"`
	}
	restJSON(t, server, http.MethodGet, "/v1/repositories", nil, &list)
	if len(list.Repositories) != 2 {
		t.Fatalf("REST repositories=%#v", list.Repositories)
	}
	var status api.RepositorySummary
	restJSON(t, server, http.MethodGet, fmt.Sprintf("/v1/repositories/%d/status", repositoryID), nil, &status)
	if status.IndexedSHA != sha || status.Status != "ready" {
		t.Fatalf("REST status=%#v", status)
	}
	var file api.ReadFileResponse
	restJSON(t, server, http.MethodPost, "/v1/files/read", api.ReadFileRequest{RepositoryID: repositoryID, Path: "main.go"}, &file)
	if file.IndexedSHA != sha || file.BlobSHA != blobSHA || !strings.Contains(file.Content, milestoneNeedle) {
		t.Fatalf("REST file=%#v", file)
	}
}

func assertMCPSurface(t *testing.T, server *httptest.Server, repositoryID int64, sha, blobSHA string) {
	t.Helper()
	session, err := mcp.NewClient(&mcp.Implementation{Name: "milestone2-e2e", Version: "1"}, nil).Connect(t.Context(), &mcp.StreamableClientTransport{
		Endpoint: server.URL + "/mcp", HTTPClient: bearerClient(server.Client()), DisableStandaloneSSE: true,
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	call := func(name string, arguments map[string]any, target any) {
		result, err := session.CallTool(t.Context(), &mcp.CallToolParams{Name: name, Arguments: arguments})
		if err != nil {
			t.Fatalf("MCP %s: %v", name, err)
		}
		decode(t, result.StructuredContent, target)
	}
	var searched api.SearchResponse
	call("search_code", map[string]any{"query": milestoneNeedle}, &searched)
	if len(searched.Matches) != 1 || searched.Matches[0].SHA != sha || searched.Matches[0].Repository.ID != repositoryID {
		t.Fatalf("MCP search=%#v", searched)
	}
	repositoryID = searched.Matches[0].Repository.ID
	var list struct {
		Repositories []api.RepositorySummary `json:"repositories"`
	}
	call("list_repositories", map[string]any{}, &list)
	if len(list.Repositories) != 2 {
		t.Fatalf("MCP repositories=%#v", list.Repositories)
	}
	var status api.RepositorySummary
	call("get_repository_status", map[string]any{"repository_id": repositoryID}, &status)
	if status.IndexedSHA != sha {
		t.Fatalf("MCP status=%#v", status)
	}
	var file api.ReadFileResponse
	call("read_file", map[string]any{"repository_id": repositoryID, "path": "main.go"}, &file)
	if file.IndexedSHA != sha || file.BlobSHA != blobSHA || !strings.Contains(file.Content, milestoneNeedle) {
		t.Fatalf("MCP file=%#v", file)
	}
}

func restJSON(t *testing.T, server *httptest.Server, method, path string, input, output any) int {
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
	request.Header.Set("Authorization", "Bearer "+token)
	if input != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	response, err := server.Client().Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if output != nil && response.StatusCode >= 200 && response.StatusCode < 300 {
		if err := json.NewDecoder(response.Body).Decode(output); err != nil {
			t.Fatal(err)
		}
	}
	return response.StatusCode
}

func assertDisabledRead(t *testing.T, server *httptest.Server, repositoryID int64) {
	t.Helper()
	status := restJSON(t, server, http.MethodPost, "/v1/files/read", api.ReadFileRequest{RepositoryID: repositoryID, Path: "main.go"}, nil)
	if status != http.StatusNotFound {
		t.Fatalf("disabled read status=%d, want 404", status)
	}
}

func assertNoSecrets(t *testing.T, pool *pgxpool.Pool, roots, logs map[string]string, forbidden ...string) {
	t.Helper()
	checked := make(map[string]bool, 1+len(roots)+len(logs))
	check := func(name string, data []byte) {
		t.Helper()
		checked[name] = true
		for _, secret := range forbidden {
			if secret != "" && bytes.Contains(data, []byte(secret)) {
				t.Fatalf("secret found in %s: %q", name, secret)
			}
		}
	}
	var databaseText string
	if err := pool.QueryRow(t.Context(), `select concat_ws('|',
		(select string_agg(concat_ws('|',account_login,account_type,status), '|') from installations),
		(select string_agg(concat_ws('|',owner,name,clone_url,web_url,default_branch,desired_sha,indexed_sha,status,error_code), '|') from repositories),
		(select string_agg(concat_ws('|',target_sha,state,lease_owner,error_code,error_message), '|') from index_jobs),
		(select string_agg(concat_ws('|',delivery_id,event_name,state,error_code), '|') from webhook_deliveries),
		(select string_agg(concat_ws('|',node_id,base_url,state), '|') from search_nodes))`).Scan(&databaseText); err != nil {
		t.Fatal(err)
	}
	check("PostgreSQL persisted fields", []byte(databaseText))
	for name, root := range roots {
		var persisted bytes.Buffer
		if err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
			if err != nil || entry.IsDir() {
				return err
			}
			data, readErr := os.ReadFile(path)
			if readErr == nil {
				persisted.Write(data)
			}
			return readErr
		}); err != nil {
			t.Fatalf("scan %s: %v", name, err)
		}
		check(name, persisted.Bytes())
	}
	for name, contents := range logs {
		check(name, []byte(contents))
	}
	for _, required := range []string{"PostgreSQL persisted fields", "Git mirrors", "Git worktrees", "Zoekt index shards", "git-http-backend stderr", "Zoekt process logs"} {
		if !checked[required] {
			t.Fatalf("secret scanner did not execute surface %q", required)
		}
	}
}

func freeAddress(t *testing.T) string {
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

func rawZoektList(t *testing.T, address, query string) string {
	t.Helper()
	body := strings.NewReader(fmt.Sprintf(`{"Q":%q,"Opts":{"Field":2}}`, query))
	response, err := http.Post("http://"+address+"/api/list", "application/json", body)
	if err != nil {
		return err.Error()
	}
	defer response.Body.Close()
	data, _ := io.ReadAll(response.Body)
	return string(data)
}
