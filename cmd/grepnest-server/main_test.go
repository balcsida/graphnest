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
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/grepnest/grepnest/internal/authn"
	"github.com/grepnest/grepnest/internal/config"
	"github.com/grepnest/grepnest/internal/githubapp"
	"github.com/grepnest/grepnest/internal/observability"
	"github.com/grepnest/grepnest/internal/repository"
	"github.com/grepnest/grepnest/internal/webhook"
)

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
	handler := newAPIHandler(config.Config{Limits: config.Limits{MaxRequestBytes: 1024, MaxResponseBytes: 1024, MaxResults: 100}}, observability.New(), authenticator, nil, &repository.Service{Store: repositoryStoreStub{}}, []byte("secret"), webhookProcessorStub{}, nil)

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
	for _, route := range []string{"/v1/repositories", "/webhooks/github"} {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, route, nil))
		if response.Code != http.StatusNotFound {
			t.Fatalf("static %s status=%d", route, response.Code)
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
