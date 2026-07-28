package graphruntime

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/grepnest/grepnest/internal/graphartifact"
	"github.com/grepnest/grepnest/internal/ladybug"
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

func TestNewRebuildsIncompatibleDatabaseFromSource(t *testing.T) {
	path := filepath.Join(t.TempDir(), "graph.lbug")
	writeIncompatibleDatabase(t, path)
	artifact := runtimeArtifact()
	manifest := graphartifact.Manifest{
		RepositoryID: artifact.RepositoryID, UploadID: 7, Commit: artifact.Commit,
		SchemaVersion: artifact.SchemaVersion, Source: "managed", ContentHash: artifact.ContentHash,
	}
	runtime, err := New(t.Context(), Config{
		DatabasePath: path, ListenAddress: "127.0.0.1:1",
		InternalSecret: []byte("secret"), SyncInterval: time.Hour,
	}, runtimeSource{manifests: []graphartifact.Manifest{manifest}, artifact: artifact}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := runtime.Close(); err != nil {
		t.Fatal(err)
	}
	db, err := ladybug.Open(ladybug.Options{Path: path})
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	compatible, err := db.Compatible(t.Context())
	manifests, manifestErr := db.Manifests(t.Context())
	if err != nil || !compatible || manifestErr != nil || manifests[artifact.RepositoryID].UploadID != 7 {
		t.Fatalf("compatible=%v err=%v manifests=%#v manifestErr=%v", compatible, err, manifests, manifestErr)
	}
}

func TestNewFailedRebuildPreservesIncompatibleLiveDatabase(t *testing.T) {
	path := filepath.Join(t.TempDir(), "graph.lbug")
	writeIncompatibleDatabase(t, path)
	want := errors.New("source failed")
	_, err := New(t.Context(), Config{
		DatabasePath: path, ListenAddress: "127.0.0.1:1",
		InternalSecret: []byte("secret"), SyncInterval: time.Hour,
	}, runtimeSource{err: want}, nil)
	if !errors.Is(err, want) {
		t.Fatalf("error=%v want=%v", err, want)
	}
	db, openErr := ladybug.Open(ladybug.Options{Path: path})
	if openErr != nil {
		t.Fatal(openErr)
	}
	defer db.Close()
	compatible, compatibleErr := db.Compatible(t.Context())
	if compatibleErr != nil || compatible {
		t.Fatalf("live marker compatible=%v err=%v", compatible, compatibleErr)
	}
}

func writeIncompatibleDatabase(t *testing.T, path string) {
	t.Helper()
	db, err := ladybug.Open(ladybug.Options{Path: path})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.EnsureSchema(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := db.WriteCompatibility(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := db.Update(t.Context(), func(session *ladybug.Session) error {
		_, err := session.Execute(t.Context(), `MATCH (m:GraphMetadata) SET m.schema_version = 0`,
			nil, ladybug.QueryLimits{})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
}

func runtimeArtifact() graphartifact.Artifact {
	return graphartifact.Artifact{
		SchemaVersion: 1, RepositoryID: 101, Commit: strings.Repeat("a", 40),
		ContentHash: bytes.Repeat([]byte{1}, 32), Analyzer: graphartifact.Analyzer{Name: "test", Version: "1"},
		Nodes: []graphartifact.Node{
			{UID: "repository", Kind: graphartifact.NodeRepository},
		},
	}
}

type runtimeSource struct {
	manifests []graphartifact.Manifest
	artifact  graphartifact.Artifact
	err       error
}

func (source runtimeSource) GraphManifests(context.Context) ([]graphartifact.Manifest, error) {
	return source.manifests, source.err
}

func (source runtimeSource) GraphArtifact(context.Context, int64, int64) (graphartifact.Artifact, error) {
	return source.artifact, source.err
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

func TestEmbeddedAndStandaloneHandlersMatchEveryOperation(t *testing.T) {
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
	scope := `{"repositories":[{"id":1,"name":"acme/repo","commit":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}]}`
	requests := map[string]string{
		"/internal/v1/graph/context": `{"scope":` + scope + `,"uid":"symbol"}`,
		"/internal/v1/graph/impact":  `{"scope":` + scope + `,"target_uid":"symbol"}`,
		"/internal/v1/graph/trace":   `{"scope":` + scope + `,"source_uid":"source","target_uid":"target"}`,
		"/internal/v1/graph/cypher":  `{"admin":true,"statement":"RETURN 1 AS value"}`,
	}
	for _, path := range []string{
		"/internal/v1/graph/context", "/internal/v1/graph/impact",
		"/internal/v1/graph/trace", "/internal/v1/graph/cypher",
	} {
		left := httptest.NewRecorder()
		right := httptest.NewRecorder()
		request := func() *http.Request {
			value := httptest.NewRequest(http.MethodPost, path, strings.NewReader(requests[path]))
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
