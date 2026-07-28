//go:build unix

package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/grepnest/grepnest/internal/config"
	"github.com/grepnest/grepnest/internal/githubapp"
	"github.com/grepnest/grepnest/internal/graphartifact"
	"github.com/grepnest/grepnest/internal/graphscan"
	"github.com/grepnest/grepnest/internal/graphscan/golang"
	"github.com/grepnest/grepnest/internal/graphscan/java"
	"github.com/grepnest/grepnest/internal/graphscan/javascript"
	"github.com/grepnest/grepnest/internal/graphscan/kotlin"
	"github.com/grepnest/grepnest/internal/graphscan/rust"
	"github.com/grepnest/grepnest/internal/graphscanner"
	"github.com/grepnest/grepnest/internal/indexer"
	"github.com/grepnest/grepnest/internal/observability"
	"github.com/grepnest/grepnest/internal/postgres"
)

const (
	askPassModeEnv    = "GREPNEST_ASKPASS_MODE"
	askPassOriginEnv  = "GREPNEST_ASKPASS_ORIGIN"
	gitTokenEnv       = "GREPNEST_GIT_TOKEN"
	maxPrivateKeySize = 64 << 10
	maxCABytes        = 1 << 20
	maxBackendBytes   = 256 << 10
)

type scannerRuntime struct {
	ping, migrate, runWorker, runMetrics func(context.Context) error
	close                                func()
}

type scannerAnalyzer struct {
	parsers map[string]graphscan.Parser
	limits  graphscan.Limits
}

func (analyzer scannerAnalyzer) Scan(ctx context.Context, request graphscan.Request) (graphartifact.Artifact, error) {
	return graphscan.Scan(ctx, request, analyzer.parsers, analyzer.limits)
}

func main() {
	if mode := os.Getenv(askPassModeEnv); mode == "1" {
		os.Exit(runAskPass(os.Args[1:], os.Getenv, os.Stdout))
	} else if mode != "" {
		os.Exit(1)
	}

	logger := slog.New(slog.NewJSONHandler(os.Stderr, nil))
	settings, err := config.LoadScanner()
	if err != nil {
		logScannerError(logger)
		os.Exit(1)
	}
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()
	runtime, err := newScannerRuntime(ctx, settings)
	if err == nil {
		err = runtime.run(ctx)
	}
	if err != nil {
		logScannerError(logger)
		os.Exit(1)
	}
}

func logScannerError(logger *slog.Logger) {
	logger.Error("scanner stopped")
}

func (runtime scannerRuntime) run(ctx context.Context) error {
	if runtime.close != nil {
		defer runtime.close()
	}
	for _, initialize := range []func(context.Context) error{runtime.ping, runtime.migrate} {
		if err := initialize(ctx); err != nil {
			return err
		}
	}
	if runtime.runMetrics == nil {
		if err := runtime.runWorker(ctx); err != nil && err != ctx.Err() {
			return err
		}
		return nil
	}
	runCtx, cancel := context.WithCancel(ctx)
	type result struct {
		name string
		err  error
	}
	results := make(chan result, 2)
	go func() { results <- result{"worker", runtime.runWorker(runCtx)} }()
	go func() { results <- result{"metrics", runtime.runMetrics(runCtx)} }()
	first := <-results
	cancel()
	second := <-results
	var failures []error
	for _, result := range []result{first, second} {
		if result.err != nil && result.err != context.Canceled && result.err != ctx.Err() {
			failures = append(failures, fmt.Errorf("%s: %w", result.name, result.err))
		}
	}
	return errors.Join(failures...)
}

func newScannerRuntime(ctx context.Context, settings config.Scanner) (scannerRuntime, error) {
	privateKey, err := readBoundedFile(settings.GitHub.PrivateKeyFile, maxPrivateKeySize)
	if err != nil {
		return scannerRuntime{}, errors.New("GitHub private key could not be read")
	}
	var caPEM []byte
	if settings.GitHub.CAFile != "" {
		if caPEM, err = readBoundedFile(settings.GitHub.CAFile, maxCABytes); err != nil {
			return scannerRuntime{}, errors.New("GitHub CA could not be read")
		}
	}
	endpoints, err := parseGitHubEndpoints(settings.GitHub)
	if err != nil {
		return scannerRuntime{}, err
	}
	httpClient, err := githubapp.NewHTTPClient(caPEM, endpoints, 30*time.Second)
	if err != nil {
		return scannerRuntime{}, errors.New("GitHub HTTP client could not be created")
	}
	signer, err := githubapp.NewSigner(settings.GitHub.AppID, privateKey, nil)
	if err != nil {
		return scannerRuntime{}, errors.New("GitHub signer could not be created")
	}
	pool, err := postgres.Open(ctx, settings.DatabaseURL)
	if err != nil {
		return scannerRuntime{}, errors.New("PostgreSQL could not be opened")
	}
	executable, err := os.Executable()
	if err != nil {
		pool.Close()
		return scannerRuntime{}, errors.New("scanner executable could not be resolved")
	}
	listener, err := net.Listen("tcp", settings.MetricsListenAddress)
	if err != nil {
		pool.Close()
		return scannerRuntime{}, errors.New("metrics listener could not be opened")
	}
	metrics := observability.New()
	store := postgres.New(pool)
	githubClient := githubapp.NewClient(endpoints, httpClient, signer, settings.GitHub.APIVersion, maxBackendBytes, nil, metrics)
	git := scannerGit(settings, executable)
	limits := graphscan.Limits{
		MaxFileBytes: settings.Limits.MaxFileBytes, MaxTotalBytes: settings.Limits.MaxTotalBytes,
		MaxFiles: settings.Limits.MaxFiles, MaxNodes: settings.Limits.MaxNodes, MaxEdges: settings.Limits.MaxEdges,
		ParseTimeout: settings.Limits.ParseTimeout, SkipDirectories: settings.Limits.SkipDirectories,
	}
	worker := &graphscanner.Worker{
		ID: settings.WorkerID, Queue: store, Store: store, Tokens: githubClient, Git: git,
		Analyzer: scannerAnalyzer{parsers: scannerParsers(), limits: limits}, Metrics: metrics,
	}
	mux := http.NewServeMux()
	mux.Handle("GET /metrics", metrics.Handler())
	return scannerRuntime{
		ping: pool.Ping, migrate: func(ctx context.Context) error { return postgres.Migrate(ctx, pool) },
		runWorker: worker.Run, runMetrics: func(ctx context.Context) error { return serveMetrics(ctx, listener, mux) },
		close: func() { _ = listener.Close(); pool.Close() },
	}, nil
}

func scannerGit(settings config.Scanner, executable string) *indexer.Git {
	return &indexer.Git{
		Binary: settings.GitPath, BaseURL: settings.GitHub.GitURL, AskPass: executable,
		CABundle: settings.GitHub.CAFile, MirrorsDir: filepath.Join(settings.DataDir, "mirrors"),
		WorktreesDir:   filepath.Join(settings.DataDir, "graph-worktrees"),
		Runner:         indexer.Runner{MaxOutput: 64 << 10, KillGrace: 5 * time.Second},
		CommandTimeout: 2 * time.Minute,
	}
}

func scannerParsers() map[string]graphscan.Parser {
	return map[string]graphscan.Parser{
		".go": golang.Parse, ".js": javascript.Parse, ".ts": javascript.Parse, ".tsx": javascript.Parse,
		".java": java.Parse, ".kt": kotlin.Parse, ".rs": rust.Parse,
	}
}

func serveMetrics(ctx context.Context, listener net.Listener, handler http.Handler) error {
	server := &http.Server{
		Handler: handler, ReadHeaderTimeout: 5 * time.Second, WriteTimeout: 10 * time.Second, IdleTimeout: time.Minute,
	}
	done := make(chan error, 1)
	go func() { done <- server.Serve(listener) }()
	select {
	case err := <-done:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		shutdownErr := server.Shutdown(shutdownCtx)
		if shutdownErr != nil {
			_ = server.Close()
		}
		serveErr := <-done
		if errors.Is(serveErr, http.ErrServerClosed) {
			serveErr = nil
		}
		return errors.Join(shutdownErr, serveErr)
	}
}

func parseGitHubEndpoints(settings config.GitHub) (githubapp.Endpoints, error) {
	values := []string{settings.WebURL, settings.APIURL, settings.UploadURL, settings.GitURL}
	parsed := make([]*url.URL, len(values))
	for index, value := range values {
		var err error
		if parsed[index], err = url.Parse(value); err != nil {
			return githubapp.Endpoints{}, errors.New("invalid GitHub endpoint")
		}
	}
	return githubapp.Endpoints{Web: parsed[0], API: parsed[1], Upload: parsed[2], Git: parsed[3]}, nil
}

func readBoundedFile(path string, maxBytes int64) ([]byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, maxBytes+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) == 0 || int64(len(data)) > maxBytes {
		return nil, errors.New("secret file size is invalid")
	}
	return data, nil
}

func runAskPass(args []string, getenv func(string) string, output io.Writer) int {
	if getenv(askPassModeEnv) != "1" || len(args) != 1 {
		return 1
	}
	response := askPass(args[0], getenv(gitTokenEnv), getenv(askPassOriginEnv))
	if response == "" {
		return 1
	}
	if _, err := io.WriteString(output, response); err != nil {
		return 1
	}
	return 0
}

func askPass(prompt, token, origin string) string {
	if token == "" {
		return ""
	}
	parsed, err := url.Parse(origin)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil ||
		parsed.Path != "" || parsed.RawQuery != "" || parsed.Fragment != "" || parsed.String() != origin {
		return ""
	}
	if prompt == "Username for '"+origin+"': " {
		return "x-access-token\n"
	}
	passwordURL := *parsed
	passwordURL.User = url.User("x-access-token")
	if prompt == "Password for '"+passwordURL.String()+"': " {
		return token + "\n"
	}
	return ""
}
