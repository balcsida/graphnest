package graphruntime

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/grepnest/grepnest/internal/graphartifact"
)

func TestLifecycleSyncsBeforeServingAndClosesAfterStop(t *testing.T) {
	var mu sync.Mutex
	var events []string
	add := func(event string) { mu.Lock(); events = append(events, event); mu.Unlock() }
	ctx, cancel := context.WithCancel(t.Context())
	runtime := &Runtime{
		syncOnce: func(context.Context) error { add("sync"); return nil },
		runSync:  func(ctx context.Context) error { add("run sync"); <-ctx.Done(); return ctx.Err() },
		serve:    func(ctx context.Context) error { add("serve"); <-ctx.Done(); return ctx.Err() },
		closeDB:  func() error { add("close"); return nil },
	}
	done := make(chan error, 1)
	go func() { done <- runtime.Run(ctx) }()
	for {
		mu.Lock()
		started := len(events) >= 3
		mu.Unlock()
		if started {
			break
		}
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if err := runtime.Close(); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if got := strings.Join(events, ","); got != "sync,serve,run sync,close" && got != "sync,run sync,serve,close" {
		t.Fatalf("events = %q", got)
	}
}

func TestLifecycleDoesNotServeWhenInitialSyncFails(t *testing.T) {
	want := errors.New("sync")
	served := false
	runtime := &Runtime{
		syncOnce: func(context.Context) error { return want },
		serve:    func(context.Context) error { served = true; return nil },
		closeDB:  func() error { return nil },
	}
	if err := runtime.Run(t.Context()); !errors.Is(err, want) || served {
		t.Fatalf("error=%v served=%v", err, served)
	}
}

func TestCloseIsIdempotent(t *testing.T) {
	closes := 0
	runtime := &Runtime{closeDB: func() error { closes++; return nil }}
	if err := runtime.Close(); err != nil {
		t.Fatal(err)
	}
	if err := runtime.Close(); err != nil {
		t.Fatal(err)
	}
	if closes != 1 {
		t.Fatalf("close calls = %d", closes)
	}
}

func TestHandlerReportsGraphHealthOnly(t *testing.T) {
	unhealthy := errors.New("unhealthy")
	runtime := &Runtime{handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, unhealthy.Error(), http.StatusServiceUnavailable)
	})}
	recorder := httptest.NewRecorder()
	runtime.Handler().ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d", recorder.Code)
	}
}

func TestEmbeddedAndStandaloneHandlersReturnIdenticalBytes(t *testing.T) {
	source := emptySource{}
	newRuntime := func(name string) *Runtime {
		t.Helper()
		runtime, err := New(t.Context(), Config{
			DatabasePath:  t.TempDir() + "/" + name + ".lbug",
			ListenAddress: "127.0.0.1:1", InternalSecret: []byte("graph-secret"),
			SyncInterval: time.Hour,
		}, source, nil)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = runtime.Close() })
		return runtime
	}
	embedded, standalone := newRuntime("embedded"), newRuntime("standalone")
	const body = `{"scope":{"repositories":[{"id":1,"name":"acme/repo","commit":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}]},"uid":"symbol"}`
	for _, path := range []string{"/internal/v1/graph/context"} {
		left := httptest.NewRecorder()
		right := httptest.NewRecorder()
		request := func() *http.Request {
			value := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
			value.Header.Set("Authorization", "Bearer graph-secret")
			value.Header.Set("Content-Type", "application/json")
			return value
		}
		embedded.Handler().ServeHTTP(left, request())
		standalone.Handler().ServeHTTP(right, request())
		if left.Code != http.StatusOK {
			t.Fatalf("%s status = %d body=%q", path, left.Code, left.Body.String())
		}
		if left.Code != right.Code || left.Body.String() != right.Body.String() ||
			left.Header().Get("Content-Type") != right.Header().Get("Content-Type") {
			t.Fatalf("%s differs: embedded=%d %q standalone=%d %q", path, left.Code, left.Body.String(), right.Code, right.Body.String())
		}
	}
}

type emptySource struct{}

func (emptySource) GraphManifests(context.Context) ([]graphartifact.Manifest, error) {
	return nil, nil
}

func (emptySource) GraphArtifact(context.Context, int64, int64) (graphartifact.Artifact, error) {
	return graphartifact.Artifact{}, errors.New("unexpected artifact request")
}
