//go:build unix

package main

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/grepnest/grepnest/internal/config"
	"github.com/grepnest/grepnest/internal/graphscan"
)

func TestParserMapContainsSupportedExtensions(t *testing.T) {
	parsers := scannerParsers()
	for _, extension := range []string{".go", ".js", ".ts", ".tsx", ".java", ".kt", ".rs"} {
		if parsers[extension] == nil {
			t.Errorf("missing parser for %s", extension)
		}
	}
	if len(parsers) != 7 {
		t.Fatalf("parser count = %d", len(parsers))
	}
}

func TestScannerGitSharesMirrorsButSeparatesGraphWorktrees(t *testing.T) {
	git := scannerGit(config.Scanner{
		DataDir: "/var/lib/grepnest",
		GitPath: "/usr/bin/git",
		GitHub:  config.GitHub{GitURL: "https://ghe.example"},
	}, "/usr/local/bin/grepnest-scanner")

	if git.MirrorsDir != "/var/lib/grepnest/mirrors" {
		t.Fatalf("mirrors = %q", git.MirrorsDir)
	}
	if git.WorktreesDir != "/var/lib/grepnest/graph-worktrees" {
		t.Fatalf("worktrees = %q", git.WorktreesDir)
	}
	if git.MirrorsDir == git.WorktreesDir {
		t.Fatal("mirror and worktree namespaces overlap")
	}
}

func TestScannerWorkerUsesResourceLimits(t *testing.T) {
	worker := newScannerWorker(config.Scanner{
		WorkerID: "scanner-1", MaxRepositoryBytes: 5 << 30, MinFreeBytes: 1 << 30, ScanTimeout: 20 * time.Minute,
	}, nil, nil, nil, nil, nil, nil)

	if worker.MaxRepositoryBytes != 5<<30 || worker.MinFreeBytes != 1<<30 {
		t.Fatalf("storage limits = %d, %d", worker.MaxRepositoryBytes, worker.MinFreeBytes)
	}
	if worker.ScanTimeout != 20*time.Minute {
		t.Fatalf("scan timeout = %v", worker.ScanTimeout)
	}
}

func TestAskPassMatchesExactGitCredentialPrompts(t *testing.T) {
	const origin = "https://ghe.example"
	for _, test := range []struct{ prompt, token, origin, want string }{
		{"Username for 'https://ghe.example': ", "secret", origin, "x-access-token\n"},
		{"Password for 'https://x-access-token@ghe.example': ", "secret", origin, "secret\n"},
		{"Password for 'https://attacker.example': ", "secret", origin, ""},
		{"Password supplied by repository", "secret", origin, ""},
		{"Username for 'https://ghe.example': ", "secret", "http://ghe.example", ""},
		{"Username for 'https://ghe.example': ", "secret", "https://ghe.example/path", ""},
		{"other", "", origin, ""},
	} {
		if got := askPass(test.prompt, test.token, test.origin); got != test.want {
			t.Fatalf("askPass(%q) = %q, want %q", test.prompt, got, test.want)
		}
	}
}

func TestAnalyzerUsesConfiguredLimits(t *testing.T) {
	root := t.TempDir()
	limits := graphscan.Limits{
		MaxFileBytes: 1, MaxTotalBytes: 1, MaxFiles: 1,
		MaxNodes: 10, MaxEdges: 10, ParseTimeout: time.Second,
	}
	analyzer := scannerAnalyzer{parsers: map[string]graphscan.Parser{".go": func(context.Context, string, []byte) (graphscan.File, error) {
		t.Fatal("oversized file reached parser")
		return graphscan.File{}, nil
	}}, limits: limits}
	if err := os.WriteFile(filepath.Join(root, "main.go"), []byte("xx"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := analyzer.Scan(t.Context(), graphscan.Request{
		RepositoryID: 1, Commit: strings.Repeat("a", 40), Root: root,
	})
	if !errors.Is(err, graphscan.ErrLimitExceeded) {
		t.Fatalf("Scan() error = %v", err)
	}
}

func TestRuntimeInitializesBeforeStartingAndClosesLast(t *testing.T) {
	var events []string
	add := func(event string) func(context.Context) error {
		return func(context.Context) error { events = append(events, event); return nil }
	}
	runtime := scannerRuntime{
		ping: add("ping"), migrate: add("migrate"), runWorker: add("worker"),
		close: func() { events = append(events, "close") },
	}
	if err := runtime.run(t.Context()); err != nil {
		t.Fatal(err)
	}
	if got, want := strings.Join(events, ","), "ping,migrate,worker,close"; got != want {
		t.Fatalf("events = %q, want %q", got, want)
	}
}

func TestRuntimeCoordinatesCancellationBeforeClose(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	workerStarted := make(chan struct{})
	metricsStarted := make(chan struct{})
	var mu sync.Mutex
	stopped := 0
	runtime := scannerRuntime{
		ping: func(context.Context) error { return nil }, migrate: func(context.Context) error { return nil },
		runWorker: func(ctx context.Context) error {
			close(workerStarted)
			<-ctx.Done()
			mu.Lock()
			stopped++
			mu.Unlock()
			return ctx.Err()
		},
		runMetrics: func(ctx context.Context) error {
			close(metricsStarted)
			<-ctx.Done()
			mu.Lock()
			stopped++
			mu.Unlock()
			return ctx.Err()
		},
		close: func() {
			mu.Lock()
			defer mu.Unlock()
			if stopped != 2 {
				t.Errorf("closed with %d routines stopped", stopped)
			}
		},
	}
	done := make(chan error, 1)
	go func() { done <- runtime.run(ctx) }()
	<-workerStarted
	<-metricsStarted
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestRuntimeCancelsWorkerWhenMetricsFail(t *testing.T) {
	want := errors.New("metrics failed")
	workerStopped := make(chan struct{})
	runtime := scannerRuntime{
		ping: func(context.Context) error { return nil }, migrate: func(context.Context) error { return nil },
		runWorker: func(ctx context.Context) error {
			<-ctx.Done()
			close(workerStopped)
			return ctx.Err()
		},
		runMetrics: func(context.Context) error { return want },
		close: func() {
			select {
			case <-workerStopped:
			default:
				t.Error("worker still running")
			}
		},
	}
	if err := runtime.run(t.Context()); !errors.Is(err, want) {
		t.Fatalf("error = %v", err)
	}
}

func TestScannerLoggingHasNoErrorDetails(t *testing.T) {
	var output bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&output, nil))
	logScannerError(logger)
	if got := output.String(); !strings.Contains(got, `"msg":"scanner stopped"`) || strings.Contains(got, `"error"`) {
		t.Fatalf("unexpected log details: %s", got)
	}
}
