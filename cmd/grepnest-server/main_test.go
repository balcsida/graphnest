package main

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/grepnest/grepnest/internal/authn"
	"github.com/grepnest/grepnest/internal/config"
	"github.com/grepnest/grepnest/internal/observability"
	"github.com/grepnest/grepnest/internal/repository"
	"github.com/grepnest/grepnest/internal/webhook"
)

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
	handler := newAPIHandler(config.Config{Limits: config.Limits{MaxRequestBytes: 1024}}, observability.New(), authenticator, nil, &repository.Service{Store: repositoryStoreStub{}}, []byte("secret"), webhookProcessorStub{}, nil)

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
