package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/grepnest/grepnest/internal/admin"
	"github.com/grepnest/grepnest/internal/authn"
	"github.com/grepnest/grepnest/internal/config"
	"github.com/grepnest/grepnest/internal/githubapp"
	"github.com/grepnest/grepnest/internal/observability"
	"github.com/grepnest/grepnest/internal/repository"
	"github.com/grepnest/grepnest/internal/scipgraph"
	"github.com/grepnest/grepnest/internal/webhook"
	"github.com/scip-code/scip/bindings/go/scip"
	"google.golang.org/protobuf/proto"
)

func TestAdminRoutesRegisterOnlyWithDurableService(t *testing.T) {
	authenticator := authn.NewStatic(map[string]authn.Principal{"admin": {Administrator: true, InstallationID: 10, RepositoryIDs: []int64{101}}})
	settings := config.Config{Limits: config.Limits{MaxRequestBytes: 1024, MaxResponseBytes: 4096, MaxResults: 10}}
	static := newAPIHandler(settings, observability.New(), authenticator, nil, nil, nil, nil, nil, nil, nil)
	request := httptest.NewRequest(http.MethodGet, "/v1/admin/overview", nil)
	request.Header.Set("Authorization", "Bearer admin")
	response := httptest.NewRecorder()
	static.ServeHTTP(response, request)
	if response.Code != http.StatusNotFound {
		t.Fatalf("static status=%d", response.Code)
	}

	durable := newAPIHandler(settings, observability.New(), authenticator, nil, nil, nil, nil, nil,
		&admin.Service{Store: mainAdminStore{}}, nil)
	response = httptest.NewRecorder()
	durable.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("durable status=%d body=%q", response.Code, response.Body.String())
	}
}

type mainAdminStore struct{ admin.Store }

func (mainAdminStore) AdminOverview(context.Context, int64, []int64) (admin.Overview, error) {
	return admin.Overview{}, nil
}

func TestDurableGitHubLimitReadsNearMaximumFile(t *testing.T) {
	content := bytes.Repeat([]byte("x"), (1<<20)-1)
	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		switch request.URL.Path {
		case "/api/v3/app/installations/10/access_tokens":
			_ = json.NewEncoder(writer).Encode(map[string]any{"token": "opaque", "expires_at": time.Now().Add(time.Hour)})
		case "/api/v3/repos/acme/one/contents/main.go":
			_ = json.NewEncoder(writer).Encode(githubapp.Content{Type: "file", Encoding: "base64", Content: base64.StdEncoding.EncodeToString(content), SHA: "blob", Size: int64(len(content))})
		default:
			writer.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()
	base, err := url.Parse(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	api := *base
	api.Path = "/api/v3"
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := githubapp.NewSigner(7, pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)}), nil)
	if err != nil {
		t.Fatal(err)
	}
	client := githubapp.NewClient(githubapp.Endpoints{Web: base, API: &api, Upload: base, Git: base}, server.Client(), signer, "2022-11-28", maxGitHubResponseBytes, nil)
	got, err := client.ReadContents(t.Context(), 10, "acme", "one", "main.go", strings.Repeat("a", 40), maxGitHubResponseBytes)
	if err != nil || got.Size != int64(len(content)) {
		t.Fatalf("content size=%d err=%v", got.Size, err)
	}
}

func TestServeHTTPKeepsRuntimeOpenUntilShutdownCompletes(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	server := &blockingShutdownServer{
		listening:       make(chan struct{}),
		shutdownStarted: make(chan struct{}),
		releaseShutdown: make(chan struct{}),
	}
	runtimeClosed := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		defer close(runtimeClosed)
		done <- serveHTTP(ctx, server, slog.New(slog.NewTextHandler(io.Discard, nil)))
	}()
	<-server.listening
	cancel()
	<-server.shutdownStarted
	remaining := time.Until(server.shutdownDeadline)
	if !server.hasDeadline || remaining < 9*time.Second || remaining > 10*time.Second {
		t.Fatalf("shutdown deadline remaining=%s present=%t", remaining, server.hasDeadline)
	}
	select {
	case <-runtimeClosed:
		t.Fatal("runtime closed before shutdown completed")
	case <-time.After(20 * time.Millisecond):
	}
	close(server.releaseShutdown)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	<-runtimeClosed
}

func TestServeHTTPReturnsUnexpectedListenErrorWithoutCancellation(t *testing.T) {
	want := errors.New("listen failed")
	server := &failedListenServer{err: want}
	done := make(chan error, 1)
	go func() {
		done <- serveHTTP(context.Background(), server, slog.New(slog.NewTextHandler(io.Discard, nil)))
	}()
	select {
	case err := <-done:
		if !errors.Is(err, want) {
			t.Fatalf("error=%v, want %v", err, want)
		}
	case <-time.After(time.Second):
		t.Fatal("listen error deadlocked without cancellation")
	}
	if server.shutdownCalled {
		t.Fatal("shutdown called after listener failed")
	}
}

func TestServerDeadlinesIsolateSCIPUploads(t *testing.T) {
	commit := strings.Repeat("a", 40)
	store := &deadlineSCIPStore{repository: repository.Repository{ID: 1, GitHubID: 101, InstallationID: 10, IndexedSHA: commit}}
	authenticator := authn.NewStatic(map[string]authn.Principal{
		"user":  {Subject: "user", InstallationID: 10, RepositoryIDs: []int64{101}},
		"admin": {Subject: "admin", Administrator: true, InstallationID: 10, RepositoryIDs: []int64{101}},
	})
	settings := config.Config{Limits: config.Limits{MaxRequestBytes: 1024, SCIPMaxUploadBytes: 1024, MaxResponseBytes: 1024, MaxResults: 100}}
	handler := newAPIHandler(settings, observability.New(), authenticator, nil, nil, &scipgraph.Service{Store: store}, nil, nil, nil, nil)
	server := newHTTPServer("127.0.0.1:0", handler)
	if server.ReadTimeout != 10*time.Second || server.WriteTimeout != 10*time.Second || server.ReadHeaderTimeout != 5*time.Second || server.IdleTimeout != time.Minute {
		t.Fatalf("deadlines = read %s, write %s, header %s, idle %s", server.ReadTimeout, server.WriteTimeout, server.ReadHeaderTimeout, server.IdleTimeout)
	}
	server.ReadTimeout = 20 * time.Millisecond
	server.WriteTimeout = 50 * time.Millisecond
	listener, err := net.Listen("tcp4", server.Addr)
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		_ = server.Serve(listener)
	}()
	defer server.Close()

	data, err := proto.Marshal(&scip.Index{Metadata: &scip.Metadata{ToolInfo: &scip.ToolInfo{Name: "test"}}})
	if err != nil {
		t.Fatal(err)
	}
	staleResponse := &deadlineResponseRecorder{ResponseRecorder: httptest.NewRecorder()}
	store.readDeadlineCleared = &staleResponse.readDeadlineCleared
	store.writeDeadlineSet = &staleResponse.writeDeadlineSet
	staleRequest := httptest.NewRequest(http.MethodPost, "/v1/scip/uploads?repository_id=101&commit="+strings.Repeat("b", 40), bytes.NewReader(data))
	staleRequest.Header.Set("Authorization", "Bearer admin")
	staleRequest.Header.Set("Content-Type", "application/vnd.scip+protobuf")
	handler.ServeHTTP(staleResponse, staleRequest)
	if staleResponse.Code != http.StatusConflict {
		t.Fatalf("stale preflight status = %d", staleResponse.Code)
	}
	if staleResponse.readDeadlineCleared.Load() {
		t.Fatal("stale upload cleared the read deadline before validating the current commit")
	}
	if !staleResponse.writeDeadlineSet.Load() {
		t.Fatal("stale upload response deadline was not bounded")
	}
	store.authorizedBeforeDeadlineClear.Store(false)
	store.authorizationCalls.Store(0)

	deadlineResponse := &deadlineResponseRecorder{ResponseRecorder: httptest.NewRecorder()}
	store.readDeadlineCleared = &deadlineResponse.readDeadlineCleared
	store.writeDeadlineSet = &deadlineResponse.writeDeadlineSet
	preflightRequest := httptest.NewRequest(http.MethodPost, "/v1/scip/uploads?repository_id=101&commit="+commit, bytes.NewReader(data))
	preflightRequest.Header.Set("Authorization", "Bearer admin")
	preflightRequest.Header.Set("Content-Type", "application/vnd.scip+protobuf")
	handler.ServeHTTP(deadlineResponse, preflightRequest)
	store.readDeadlineCleared = nil
	store.writeDeadlineSet = nil
	if deadlineResponse.Code != http.StatusNoContent {
		t.Fatalf("preflight upload status = %d", deadlineResponse.Code)
	}
	if !store.authorizedBeforeDeadlineClear.Load() {
		t.Fatal("upload repository was not authorized before clearing the read deadline")
	}
	if calls := store.authorizationCalls.Load(); calls != 2 {
		t.Fatalf("upload repository authorization calls = %d, want preflight and post-body validation", calls)
	}
	if store.writeDeadlineSetDuringReplace.Load() {
		t.Fatal("upload response deadline was set before SCIP replacement finished")
	}
	store.replaceStarted = make(chan struct{})
	store.releaseReplace = make(chan struct{})

	bodyReader, bodyWriter := io.Pipe()
	go func() {
		_, _ = bodyWriter.Write(data[:1])
		time.Sleep(2 * server.ReadTimeout)
		_, _ = bodyWriter.Write(data[1:])
		_ = bodyWriter.Close()
	}()
	request, err := http.NewRequest(http.MethodPost, "http://"+listener.Addr().String()+"/v1/scip/uploads?repository_id=101&commit="+commit, bodyReader)
	if err != nil {
		t.Fatal(err)
	}
	request.ContentLength = int64(len(data))
	request.Header.Set("Authorization", "Bearer admin")
	request.Header.Set("Content-Type", "application/vnd.scip+protobuf")
	uploadResult := make(chan struct {
		response *http.Response
		err      error
	}, 1)
	go func() {
		response, err := (&http.Client{Timeout: time.Second}).Do(request)
		uploadResult <- struct {
			response *http.Response
			err      error
		}{response, err}
	}()
	select {
	case <-store.replaceStarted:
	case <-time.After(time.Second):
		t.Fatal("SCIP replacement did not start")
	}
	time.Sleep(2 * server.WriteTimeout)
	close(store.releaseReplace)
	result := <-uploadResult
	if result.err != nil {
		t.Fatal(result.err)
	}
	response := result.response
	defer response.Body.Close()
	if response.StatusCode != http.StatusNoContent {
		t.Fatalf("status = %d", response.StatusCode)
	}

	slowReader, slowWriter := io.Pipe()
	go func() {
		_, _ = io.WriteString(slowWriter, `{`)
		time.Sleep(2 * server.ReadTimeout)
		_, _ = io.WriteString(slowWriter, `"repository_id":101,"path":"main.go","line":1,"character":0,"operation":"definitions"}`)
		_ = slowWriter.Close()
	}()
	slowRequest, err := http.NewRequest(http.MethodPost, "http://"+listener.Addr().String()+"/v1/scip/navigation", slowReader)
	if err != nil {
		t.Fatal(err)
	}
	slowRequest.Header.Set("Authorization", "Bearer user")
	slowRequest.Header.Set("Content-Type", "application/json")
	clientTimeout := time.Second
	started := time.Now()
	response, err = (&http.Client{Timeout: clientTimeout}).Do(slowRequest)
	elapsed := time.Since(started)
	if err != nil {
		t.Fatalf("slow JSON request failed after %s: %v", elapsed, err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusBadRequest {
		t.Fatalf("slow JSON status = %d", response.StatusCode)
	}
	if elapsed < server.ReadTimeout || elapsed >= server.WriteTimeout {
		t.Fatalf("slow JSON elapsed = %s, want [%s, %s)", elapsed, server.ReadTimeout, server.WriteTimeout)
	}
}

type deadlineResponseRecorder struct {
	*httptest.ResponseRecorder
	readDeadlineCleared atomic.Bool
	writeDeadlineSet    atomic.Bool
}

func (recorder *deadlineResponseRecorder) SetReadDeadline(deadline time.Time) error {
	recorder.readDeadlineCleared.Store(deadline.IsZero())
	return nil
}

func (recorder *deadlineResponseRecorder) SetWriteDeadline(deadline time.Time) error {
	recorder.writeDeadlineSet.Store(!deadline.IsZero())
	return nil
}

type deadlineSCIPStore struct {
	repository                    repository.Repository
	readDeadlineCleared           *atomic.Bool
	authorizedBeforeDeadlineClear atomic.Bool
	authorizationCalls            atomic.Int32
	writeDeadlineSet              *atomic.Bool
	writeDeadlineSetDuringReplace atomic.Bool
	replaceStarted                chan struct{}
	releaseReplace                chan struct{}
}

func (store *deadlineSCIPStore) AuthorizedRepository(_ context.Context, _ int64, _ []int64, id int64) (repository.Repository, error) {
	store.authorizationCalls.Add(1)
	if store.readDeadlineCleared != nil && !store.readDeadlineCleared.Load() {
		store.authorizedBeforeDeadlineClear.Store(true)
	}
	if id == store.repository.GitHubID {
		return store.repository, nil
	}
	return repository.Repository{}, errors.New("not found")
}
func (store *deadlineSCIPStore) ReplaceSCIP(context.Context, int64, string, scipgraph.Upload) error {
	if store.writeDeadlineSet != nil && store.writeDeadlineSet.Load() {
		store.writeDeadlineSetDuringReplace.Store(true)
	}
	if store.replaceStarted != nil {
		close(store.replaceStarted)
		<-store.releaseReplace
	}
	return nil
}
func (*deadlineSCIPStore) OccurrenceAt(context.Context, int64, string, string, int, scipgraph.OccurrencePosition) (scipgraph.StoredOccurrence, error) {
	return scipgraph.StoredOccurrence{}, errors.New("not found")
}
func (*deadlineSCIPStore) Locations(context.Context, authn.Principal, scipgraph.StoredOccurrence, string, int) ([]scipgraph.Location, bool, error) {
	return nil, false, nil
}
func (*deadlineSCIPStore) ReplacePackages(context.Context, int64, string, []scipgraph.PackageMapping) error {
	return nil
}

type blockingShutdownServer struct {
	listening, shutdownStarted, releaseShutdown chan struct{}
	shutdownDeadline                            time.Time
	hasDeadline                                 bool
}

func (server *blockingShutdownServer) ListenAndServe() error {
	close(server.listening)
	<-server.shutdownStarted
	return http.ErrServerClosed
}

func (server *blockingShutdownServer) Shutdown(ctx context.Context) error {
	server.shutdownDeadline, server.hasDeadline = ctx.Deadline()
	close(server.shutdownStarted)
	<-server.releaseShutdown
	return nil
}

type failedListenServer struct {
	err            error
	shutdownCalled bool
}

func (server *failedListenServer) ListenAndServe() error { return server.err }
func (server *failedListenServer) Shutdown(context.Context) error {
	server.shutdownCalled = true
	return nil
}

func TestDurableSecretReadsAreBounded(t *testing.T) {
	path := filepath.Join(t.TempDir(), "secret")
	if err := os.WriteFile(path, []byte("12345"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := readBoundedFile(path, 4); err == nil {
		t.Fatal("oversized secret accepted")
	}
	got, err := readBoundedFile(path, 5)
	if err != nil || string(got) != "12345" {
		t.Fatalf("secret=%q err=%v", got, err)
	}
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := readBoundedFile(path, 5); err == nil {
		t.Fatal("empty secret accepted")
	}
}

func TestDurableReconciliationStartsSynchronouslyAndStops(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	var reconciles, refreshes, failures atomic.Int64
	done, err := startPeriodic(ctx, time.Millisecond, func(context.Context) error {
		if reconciles.Add(1) == 2 {
			return context.DeadlineExceeded
		}
		return nil
	}, func(context.Context) error {
		refreshes.Add(1)
		return nil
	}, func(error) { failures.Add(1) })
	if err != nil {
		t.Fatal(err)
	}
	if reconciles.Load() != 1 || refreshes.Load() != 1 {
		t.Fatalf("startup reconciles=%d refreshes=%d", reconciles.Load(), refreshes.Load())
	}
	deadline := time.Now().Add(time.Second)
	for reconciles.Load() < 3 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if failures.Load() != 1 || reconciles.Load() < 3 {
		t.Fatalf("periodic retries=%d failures=%d", reconciles.Load(), failures.Load())
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("periodic reconciliation did not stop")
	}
	stopped := reconciles.Load()
	time.Sleep(5 * time.Millisecond)
	if reconciles.Load() != stopped {
		t.Fatal("reconciliation continued after cancellation")
	}
}

func TestReconcileRequestsAreLifecycleOwned(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	requests := make(chan int64, 1)
	reconciled := make(chan int64, 1)
	done := startReconcileRequests(ctx, requests, func(_ context.Context, installationID int64) error {
		reconciled <- installationID
		return nil
	}, func(err error) { t.Errorf("unexpected reconcile error: %v", err) })
	requests <- 10
	select {
	case installationID := <-reconciled:
		if installationID != 10 {
			t.Fatalf("reconciled installation = %d", installationID)
		}
	case <-time.After(time.Second):
		t.Fatal("reconciliation request was not consumed")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("reconciliation request loop did not stop")
	}
}

func TestDurableReadinessRequiresDatabaseAndZoekt(t *testing.T) {
	database := &healthStub{}
	backend := &healthStub{}
	checker := durableReadiness{pool: database, zoekt: backend}
	if err := checker.Health(context.Background()); err != nil {
		t.Fatal(err)
	}
	if database.calls != 1 || backend.calls != 1 {
		t.Fatalf("database calls=%d backend calls=%d", database.calls, backend.calls)
	}
	database.err = context.DeadlineExceeded
	if err := checker.Health(context.Background()); err == nil || backend.calls != 1 {
		t.Fatalf("error=%v backend calls=%d", err, backend.calls)
	}
}

type healthStub struct {
	calls int
	err   error
}

func (stub *healthStub) Ping(context.Context) error {
	stub.calls++
	return stub.err
}

func (stub *healthStub) Health(context.Context) error {
	stub.calls++
	return stub.err
}

func TestDurableRoutesKeepWebhookOutsideBearerAuthentication(t *testing.T) {
	authenticator := authn.NewStatic(map[string]authn.Principal{"user": {Subject: "user"}})
	handler := newAPIHandler(config.Config{Limits: config.Limits{MaxRequestBytes: 1024, MaxResponseBytes: 1024, MaxResults: 100}}, observability.New(), authenticator, nil, &repository.Service{Store: repositoryStoreStub{}}, nil, []byte("secret"), webhookProcessorStub{}, nil, nil)

	webhookResponse := httptest.NewRecorder()
	handler.ServeHTTP(webhookResponse, httptest.NewRequest(http.MethodPost, "/webhooks/github", nil))
	if webhookResponse.Code != http.StatusBadRequest {
		t.Fatalf("webhook status=%d, want %d", webhookResponse.Code, http.StatusBadRequest)
	}
	for _, path := range []string{"/v1/repositories", "/mcp"} {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, path, nil))
		if response.Code != http.StatusUnauthorized {
			t.Fatalf("%s status=%d, want %d", path, response.Code, http.StatusUnauthorized)
		}
	}
	authorized := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/v1/repositories", nil)
	request.Header.Set("Authorization", "Bearer user")
	handler.ServeHTTP(authorized, request)
	if authorized.Code != http.StatusOK {
		t.Fatalf("authorized repositories status=%d body=%q", authorized.Code, authorized.Body.String())
	}
}

type webhookProcessorStub struct{}

func (webhookProcessorStub) Process(context.Context, webhook.Delivery) (bool, error) {
	return true, nil
}

type repositoryStoreStub struct{}

func (repositoryStoreStub) AuthorizedRepositories(context.Context, int64, []int64, []string) ([]repository.Repository, error) {
	return nil, nil
}

func (repositoryStoreStub) AuthorizedRepository(context.Context, int64, []int64, int64) (repository.Repository, error) {
	return repository.Repository{}, nil
}

func (repositoryStoreStub) AllAuthorizedRepositories(context.Context, []string) ([]repository.Repository, error) {
	return nil, nil
}

func (repositoryStoreStub) AnyAuthorizedRepository(context.Context, int64) (repository.Repository, error) {
	return repository.Repository{}, nil
}

func TestStaticHandlerRegistersSystemRoutes(t *testing.T) {
	path := filepath.Join(t.TempDir(), "repositories.json")
	if err := os.WriteFile(path, []byte(`[{"id":1,"zoekt_id":7,"name":"acme/one"}]`), 0o600); err != nil {
		t.Fatal(err)
	}
	handler, err := newHandler(config.Config{
		ZoektURL: "http://zoekt.invalid", RepositoriesFile: path,
		UserToken: "user", AdminToken: "admin",
		Limits: config.Limits{MaxRequestBytes: 1024, MaxResponseBytes: 1024, MaxResults: 100, MaxContextLines: 20},
	})
	if err != nil {
		t.Fatal(err)
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if response.Code != http.StatusOK || response.Body.String() != "ok\n" {
		t.Fatalf("status=%d body=%q", response.Code, response.Body.String())
	}
	for _, route := range []string{"/v1/repositories", "/v1/scip/navigation", "/webhooks/github"} {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, route, nil))
		if response.Code != http.StatusNotFound {
			t.Fatalf("static %s status=%d", route, response.Code)
		}
	}
}

func TestSCIPRoutesRegisterOnlyWithDurableService(t *testing.T) {
	authenticator := authn.NewStatic(map[string]authn.Principal{"user": {Subject: "user"}})
	settings := config.Config{Limits: config.Limits{MaxRequestBytes: 1024, SCIPMaxUploadBytes: 1024, MaxResponseBytes: 1024, MaxResults: 100}}
	without := newAPIHandler(settings, observability.New(), authenticator, nil, nil, nil, nil, nil, nil, nil)
	with := newAPIHandler(settings, observability.New(), authenticator, nil, nil, &scipgraph.Service{}, nil, nil, nil, nil)
	for name, handler := range map[string]http.Handler{"static": without, "durable": with} {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/v1/scip/navigation", nil))
		want := http.StatusNotFound
		if name == "durable" {
			want = http.StatusUnauthorized
		}
		if response.Code != want {
			t.Fatalf("%s status = %d, want %d", name, response.Code, want)
		}
	}
}

func TestAPIHandlerMountsWebUIWithoutFallback(t *testing.T) {
	authenticator := authn.NewStatic(map[string]authn.Principal{"user": {Subject: "user"}})
	handler := newAPIHandler(
		config.Config{Limits: config.Limits{MaxRequestBytes: 1024, MaxResponseBytes: 1024, MaxResults: 100}},
		observability.New(), authenticator, nil, nil, nil, nil, nil, nil, nil,
	)
	for _, path := range []string{"/", "/index.html"} {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, path, nil))
		if response.Code != http.StatusOK {
			t.Fatalf("%s status=%d", path, response.Code)
		}
	}
	for _, path := range []string{"/missing", "/index.html/", "/assets/app.js"} {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, path, nil))
		if response.Code != http.StatusNotFound {
			t.Fatalf("%s status=%d", path, response.Code)
		}
	}
}

func TestStaticHandlerProtectsMCPRoute(t *testing.T) {
	path := filepath.Join(t.TempDir(), "repositories.json")
	if err := os.WriteFile(path, []byte(`[{"id":1,"zoekt_id":7,"name":"acme/one"}]`), 0o600); err != nil {
		t.Fatal(err)
	}
	handler, err := newHandler(config.Config{
		ZoektURL: "http://zoekt.invalid", RepositoriesFile: path,
		UserToken: "user", AdminToken: "admin",
		Limits: config.Limits{MaxRequestBytes: 1024, MaxResponseBytes: 1024, MaxResults: 100, MaxContextLines: 20},
	})
	if err != nil {
		t.Fatal(err)
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/mcp", nil))
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusUnauthorized)
	}
}

func TestStaticHandlerLimitsAuthenticatedMCPRequestBody(t *testing.T) {
	path := filepath.Join(t.TempDir(), "repositories.json")
	if err := os.WriteFile(path, []byte(`[{"id":1,"zoekt_id":7,"name":"acme/one"}]`), 0o600); err != nil {
		t.Fatal(err)
	}
	const maxBytes = int64(128)
	handler, err := newHandler(config.Config{
		ZoektURL: "http://zoekt.invalid", RepositoriesFile: path,
		UserToken: "user", AdminToken: "admin",
		Limits: config.Limits{MaxRequestBytes: maxBytes, MaxResponseBytes: 1024, MaxResults: 100, MaxContextLines: 20},
	})
	if err != nil {
		t.Fatal(err)
	}
	body := `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-03-26","capabilities":{},"clientInfo":{"name":"` + strings.Repeat("x", 1024) + `","version":"1"}}}`
	reader := &countingReader{Reader: strings.NewReader(body)}
	request := httptest.NewRequest(http.MethodPost, "/mcp", reader)
	request.Header.Set("Authorization", "Bearer user")
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json, text/event-stream")
	handler.ServeHTTP(httptest.NewRecorder(), request)

	if reader.bytes > maxBytes+1 {
		t.Fatalf("SDK read %d request bytes, want at most %d", reader.bytes, maxBytes+1)
	}
}

type countingReader struct {
	io.Reader
	bytes int64
}

func (reader *countingReader) Read(buffer []byte) (int, error) {
	read, err := reader.Reader.Read(buffer)
	reader.bytes += int64(read)
	return read, err
}
