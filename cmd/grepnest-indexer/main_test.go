//go:build unix

package main

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/grepnest/grepnest/internal/config"
	"github.com/grepnest/grepnest/internal/githubapp"
	"github.com/grepnest/grepnest/internal/graphartifact"
	"github.com/grepnest/grepnest/internal/graphruntime"
	"github.com/grepnest/grepnest/internal/indexer"
	"github.com/grepnest/grepnest/internal/ladybug"
	"github.com/grepnest/grepnest/internal/postgres"
	"github.com/grepnest/grepnest/internal/repository"
)

func TestAskPass(t *testing.T) {
	const origin = "https://ghe.example"
	for _, test := range []struct{ prompt, token, origin, want string }{
		{"Username for 'https://ghe.example': ", "secret", origin, "x-access-token\n"},
		{"Password for 'https://x-access-token@ghe.example': ", "secret", origin, "secret\n"},
		{"Password for 'https://attacker.example': ", "secret", origin, ""},
		{"Password supplied by repository", "secret", origin, ""},
		{"Username for 'https://ghe.example': ", "secret", "http://ghe.example", ""},
		{"Username for 'https://ghe.example': ", "secret", "https://user@ghe.example", ""},
		{"Username for 'https://ghe.example': ", "secret", "https://ghe.example/path", ""},
		{"other", "secret", origin, ""},
		{"Password for 'https://x-access-token@ghe.example': ", "", origin, ""},
	} {
		if got := askPass(test.prompt, test.token, test.origin); got != test.want {
			t.Fatalf("askPass(%q) = %q, want %q", test.prompt, got, test.want)
		}
	}
}

func TestRunAskPassFailsClosed(t *testing.T) {
	for _, test := range []struct {
		name string
		args []string
		env  map[string]string
		want string
		code int
	}{
		{name: "username", args: []string{"Username for 'https://ghe.example': "}, env: map[string]string{askPassModeEnv: "1", gitTokenEnv: "secret", askPassOriginEnv: "https://ghe.example"}, want: "x-access-token\n"},
		{name: "password", args: []string{"Password for 'https://x-access-token@ghe.example': "}, env: map[string]string{askPassModeEnv: "1", gitTokenEnv: "secret", askPassOriginEnv: "https://ghe.example"}, want: "secret\n"},
		{name: "wrong mode", args: []string{"Password for 'https://x-access-token@ghe.example': "}, env: map[string]string{askPassModeEnv: "true", gitTokenEnv: "secret", askPassOriginEnv: "https://ghe.example"}, code: 1},
		{name: "mismatched host", args: []string{"Password for 'https://x-access-token@attacker.example': "}, env: map[string]string{askPassModeEnv: "1", gitTokenEnv: "secret", askPassOriginEnv: "https://ghe.example"}, code: 1},
		{name: "unknown prompt", args: []string{"Other"}, env: map[string]string{askPassModeEnv: "1", gitTokenEnv: "secret", askPassOriginEnv: "https://ghe.example"}, code: 1},
		{name: "missing origin", args: []string{"Password for 'https://x-access-token@ghe.example': "}, env: map[string]string{askPassModeEnv: "1", gitTokenEnv: "secret"}, code: 1},
		{name: "extra argument", args: []string{"Password:", "extra"}, env: map[string]string{askPassModeEnv: "1", gitTokenEnv: "secret", askPassOriginEnv: "https://ghe.example"}, code: 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			var output strings.Builder
			code := runAskPass(test.args, func(name string) string { return test.env[name] }, &output)
			if code != test.code || output.String() != test.want {
				t.Fatalf("code=%d output=%q", code, output.String())
			}
		})
	}
}

func TestRuntimeInitializesBeforeOneWorkerAndClosesLast(t *testing.T) {
	var events []string
	add := func(event string) func(context.Context) error {
		return func(context.Context) error { events = append(events, event); return nil }
	}
	runtime := indexRuntime{
		ping: add("ping"), migrate: add("migrate"), upsertNode: add("upsert primary"),
		reapExpired: add("reap"), pruneHistory: add("prune history"), runWorker: add("prune worktrees and run worker"),
		close: func() { events = append(events, "close") },
	}
	if err := runtime.run(t.Context()); err != nil {
		t.Fatal(err)
	}
	want := "ping,migrate,upsert primary,reap,prune history,prune worktrees and run worker,close"
	if got := strings.Join(events, ","); got != want {
		t.Fatalf("events=%q want=%q", got, want)
	}
}

func TestRuntimeStartsEmbeddedGraphAfterHealthyInitialization(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	var mu sync.Mutex
	var events []string
	add := func(event string) func(context.Context) error {
		return func(ctx context.Context) error {
			mu.Lock()
			events = append(events, event)
			mu.Unlock()
			if event == "worker" || event == "graph" {
				<-ctx.Done()
				return ctx.Err()
			}
			return nil
		}
	}
	runtime := indexRuntime{
		ping: add("ping"), migrate: add("migrate"), upsertNode: add("upsert"),
		reapExpired: add("reap"), pruneHistory: add("prune"),
		runWorker: add("worker"), runGraph: add("graph"),
	}
	done := make(chan error, 1)
	go func() { done <- runtime.run(ctx) }()
	deadline := time.After(time.Second)
	for {
		mu.Lock()
		started := len(events) == 7
		mu.Unlock()
		if started {
			break
		}
		select {
		case <-deadline:
			cancel()
			<-done
			t.Fatal("embedded graph did not start")
		default:
		}
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if got := strings.Join(events[:5], ","); got != "ping,migrate,upsert,reap,prune" {
		t.Fatalf("initialization = %q", got)
	}
}

func TestGraphServiceSkipsSeparateMode(t *testing.T) {
	called := false
	old := startGraphRuntime
	startGraphRuntime = func(context.Context, graphruntime.Config, ladybug.SnapshotSource, *slog.Logger) error {
		called = true
		return nil
	}
	t.Cleanup(func() { startGraphRuntime = old })
	if run := graphService(config.Graph{Mode: "separate"}, nil, nil); run != nil {
		t.Fatal("separate mode returned graph service")
	}
	if called {
		t.Fatal("separate mode opened Ladybug")
	}
}

func TestGraphServicePropagatesEmbeddedLimits(t *testing.T) {
	var got graphruntime.Config
	old := startGraphRuntime
	startGraphRuntime = func(_ context.Context, settings graphruntime.Config, _ ladybug.SnapshotSource, _ *slog.Logger) error {
		got = settings
		return nil
	}
	t.Cleanup(func() { startGraphRuntime = old })
	settings := config.Graph{
		Mode: "embedded", DataDir: "/graph",
		QueryLimits: config.GraphQueryLimits{
			DefaultImpactDepth: 2, MaxDepth: 7, DefaultTraceDepth: 4,
			MaxTraceDepth: 9, MaxRows: 321,
		},
	}
	run := graphService(settings, emptySnapshotSource{}, nil)
	if run == nil {
		t.Fatal("embedded mode omitted graph service")
	}
	if err := run(t.Context()); err != nil {
		t.Fatal(err)
	}
	if got.QueryLimits.DefaultImpactDepth != 2 || got.QueryLimits.MaxDepth != 7 ||
		got.QueryLimits.DefaultTraceDepth != 4 || got.QueryLimits.MaxTraceDepth != 9 ||
		got.QueryLimits.MaxRows != 321 {
		t.Fatalf("runtime limits = %#v", got.QueryLimits)
	}
}

type emptySnapshotSource struct{}

func (emptySnapshotSource) GraphManifests(context.Context) ([]graphartifact.Manifest, error) {
	return nil, nil
}
func (emptySnapshotSource) GraphArtifact(context.Context, int64, int64) (graphartifact.Artifact, error) {
	return graphartifact.Artifact{}, errors.New("unexpected artifact")
}

func TestRuntimeWaitsForCancelledWorkerBeforeClosing(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	started := make(chan struct{})
	var mu sync.Mutex
	closed := false
	runtime := indexRuntime{
		ping:         func(context.Context) error { return nil },
		migrate:      func(context.Context) error { return nil },
		upsertNode:   func(context.Context) error { return nil },
		reapExpired:  func(context.Context) error { return nil },
		pruneHistory: func(context.Context) error { return nil },
		runWorker: func(ctx context.Context) error {
			close(started)
			<-ctx.Done()
			mu.Lock()
			defer mu.Unlock()
			if closed {
				t.Error("resources closed before worker stopped")
			}
			return ctx.Err()
		},
		close: func() { mu.Lock(); closed = true; mu.Unlock() },
	}
	done := make(chan error, 1)
	go func() { done <- runtime.run(ctx) }()
	<-started
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if !closed {
		t.Fatal("resources were not closed")
	}
}

func TestRuntimeStopsMetricsBeforeClosing(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	workerStarted := make(chan struct{})
	metricsStarted := make(chan struct{})
	metricsStopped := make(chan struct{})
	closed := false
	runtime := indexRuntime{
		ping: func(context.Context) error { return nil }, migrate: func(context.Context) error { return nil },
		upsertNode: func(context.Context) error { return nil }, reapExpired: func(context.Context) error { return nil },
		pruneHistory: func(context.Context) error { return nil },
		runWorker:    func(ctx context.Context) error { close(workerStarted); <-ctx.Done(); return ctx.Err() },
		runMetrics: func(ctx context.Context) error {
			close(metricsStarted)
			<-ctx.Done()
			close(metricsStopped)
			return nil
		},
		close: func() {
			select {
			case <-metricsStopped:
			default:
				t.Error("resources closed before metrics stopped")
			}
			closed = true
		},
	}
	done := make(chan error, 1)
	go func() { done <- runtime.run(ctx) }()
	<-workerStarted
	<-metricsStarted
	cancel()
	if err := <-done; err != nil || !closed {
		t.Fatalf("error=%v closed=%v", err, closed)
	}
}

func TestRuntimeCancelsWorkerWhenMetricsFail(t *testing.T) {
	want := errors.New("metrics failed")
	workerStopped := make(chan struct{})
	runtime := indexRuntime{
		ping: func(context.Context) error { return nil }, migrate: func(context.Context) error { return nil },
		upsertNode: func(context.Context) error { return nil }, reapExpired: func(context.Context) error { return nil },
		pruneHistory: func(context.Context) error { return nil },
		runWorker:    func(ctx context.Context) error { <-ctx.Done(); close(workerStopped); return ctx.Err() },
		runMetrics:   func(context.Context) error { return want },
		close: func() {
			select {
			case <-workerStopped:
			default:
				t.Error("resources closed before worker stopped")
			}
		},
	}
	if err := runtime.run(t.Context()); !errors.Is(err, want) {
		t.Fatalf("error=%v", err)
	}
}

func TestRuntimeClosesAfterInitializationFailure(t *testing.T) {
	closed := false
	want := errors.New("migrate")
	runtime := indexRuntime{ping: func(context.Context) error { return nil }, migrate: func(context.Context) error { return want }, close: func() { closed = true }}
	if err := runtime.run(t.Context()); !errors.Is(err, want) || !closed {
		t.Fatalf("error=%v closed=%v", err, closed)
	}
}

func TestRuntimeSurfacesCleanupFailureJoinedWithCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	cleanupErr := errors.New("cleanup failed")
	runtime := indexRuntime{
		ping: func(context.Context) error { return nil }, migrate: func(context.Context) error { return nil },
		upsertNode: func(context.Context) error { return nil }, reapExpired: func(context.Context) error { return nil },
		pruneHistory: func(context.Context) error { return nil },
		runWorker:    func(context.Context) error { return errors.Join(context.Canceled, cleanupErr) },
	}
	if err := runtime.run(ctx); !errors.Is(err, cleanupErr) {
		t.Fatalf("error=%v", err)
	}
}

func TestRuntimeTreatsRealWorkerCancellationAsClean(t *testing.T) {
	job := postgres.IndexJob{ID: 11, RepositoryID: 4, TargetSHA: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}
	queue := &commandQueue{job: job}
	store := commandStore{repo: repository.Repository{ID: 4, InstallationID: 5, GitHubID: 6, ZoektID: 7, Name: "acme/repo", Branch: "main", WebURL: "https://ghe.example/acme/repo"}, desired: job.TargetSHA}
	publisher := &commandPublisher{started: make(chan struct{})}
	worker := &indexer.Worker{ID: "worker", Queue: queue, Store: store, Tokens: commandTokens{}, Snapshots: commandSnapshots{}, Zoekt: publisher}
	runtime := indexRuntime{
		ping: func(context.Context) error { return nil }, migrate: func(context.Context) error { return nil },
		upsertNode: func(context.Context) error { return nil }, reapExpired: func(context.Context) error { return nil },
		pruneHistory: func(context.Context) error { return nil }, runWorker: worker.Run,
	}
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- runtime.run(ctx) }()
	<-publisher.started
	cancel()
	if err := <-done; err != nil {
		t.Fatalf("runtime error = %T %v", err, err)
	}
}

func TestSnapshotProviderSelection(t *testing.T) {
	git := &indexer.Git{}
	client := &githubapp.Client{}
	settings := config.Indexer{SourceProvider: "git"}
	if _, ok := newSnapshotProvider(settings, client, git, nil).(indexer.GitSnapshotProvider); !ok {
		t.Fatal("git provider not selected")
	}
	settings.SourceProvider = "archive"
	settings.DataDir = "/data"
	settings.ArchiveLimits = config.ArchiveLimits{MaxDownloadBytes: 1, MaxExtractedBytes: 2, MaxFileBytes: 3, MaxFiles: 4, MaxPathBytes: 5}
	provider, ok := newSnapshotProvider(settings, client, git, nil).(indexer.ArchiveSnapshotProvider)
	if !ok || provider.WorkspacesDir != "/data/archives" || provider.Limits != (indexer.ArchiveLimits{MaxDownloadBytes: 1, MaxExtractedBytes: 2, MaxFileBytes: 3, MaxFiles: 4, MaxPathBytes: 5}) {
		t.Fatalf("archive provider = %#v", provider)
	}
}

func TestWarnIgnoredGitSettingsInArchiveMode(t *testing.T) {
	var output bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&output, nil))
	warnIgnoredGitSettings(logger, config.Indexer{SourceProvider: "archive", GitPath: "/usr/bin/git"})
	if text := output.String(); !strings.Contains(text, "GREPNEST_GIT_PATH") || !strings.Contains(text, "ignored") {
		t.Fatalf("warning = %q", text)
	}
	output.Reset()
	warnIgnoredGitSettings(logger, config.Indexer{SourceProvider: "git", GitPath: "/usr/bin/git"})
	if output.Len() != 0 {
		t.Fatalf("git mode warning = %q", output.String())
	}
}

type commandQueue struct{ job postgres.IndexJob }

func (queue *commandQueue) ClaimIndex(context.Context, string) (postgres.IndexJob, error) {
	return queue.job, nil
}
func (*commandQueue) RenewLease(context.Context, int64, string) error   { return nil }
func (*commandQueue) PublishIndex(context.Context, int64, string) error { return nil }
func (*commandQueue) CompleteIndex(context.Context, int64, string, ...postgres.EnrichmentStatus) error {
	return nil
}
func (*commandQueue) FailIndex(context.Context, int64, string, string, bool) error { return nil }
func (*commandQueue) ActiveJobIDs(context.Context) (map[int64]struct{}, error) {
	return map[int64]struct{}{}, nil
}

type commandStore struct {
	repo    repository.Repository
	desired string
}

func (store commandStore) RepositoryForIndex(context.Context, int64) (repository.Repository, error) {
	return store.repo, nil
}
func (store commandStore) DesiredSHA(context.Context, int64) (string, error) {
	return store.desired, nil
}

type commandTokens struct{}

func (commandTokens) InstallationToken(context.Context, int64, []int64) (githubapp.Token, error) {
	return githubapp.Token{Value: "token"}, nil
}

type commandSnapshots struct{}

func (commandSnapshots) Prepare(_ context.Context, request indexer.SnapshotRequest) (indexer.Snapshot, error) {
	return indexer.Snapshot{Root: "/snapshot", RepositoryID: request.RepositoryID, JobID: request.JobID, CommitSHA: request.CommitSHA}, nil
}
func (commandSnapshots) Cleanup(context.Context, indexer.Snapshot) error { return nil }
func (commandSnapshots) CleanupStale(context.Context, indexer.ActiveJobs) error {
	return nil
}
func (commandSnapshots) FreeSpacePath() string { return "." }

type commandPublisher struct{ started chan struct{} }

func (publisher *commandPublisher) Index(ctx context.Context, _ repository.Repository, _ string) error {
	close(publisher.started)
	<-ctx.Done()
	return ctx.Err()
}
func (*commandPublisher) WaitVisible(context.Context, uint32, string, string) error { return nil }
