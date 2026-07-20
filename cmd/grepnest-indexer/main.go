//go:build unix

package main

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/grepnest/grepnest/internal/config"
	"github.com/grepnest/grepnest/internal/githubapp"
	"github.com/grepnest/grepnest/internal/indexer"
	"github.com/grepnest/grepnest/internal/observability"
	"github.com/grepnest/grepnest/internal/postgres"
	"github.com/grepnest/grepnest/internal/zoekt"
)

const (
	askPassModeEnv    = "GREPNEST_ASKPASS_MODE"
	askPassOriginEnv  = "GREPNEST_ASKPASS_ORIGIN"
	gitTokenEnv       = "GREPNEST_GIT_TOKEN"
	searchNodeID      = "primary"
	maxPrivateKeySize = 64 << 10
	maxCABytes        = 1 << 20
	maxBackendBytes   = 256 << 10
)

type indexRuntime struct {
	ping, migrate, upsertNode, reapExpired, pruneHistory, runWorker func(context.Context) error
	close                                                           func()
}

func main() {
	if mode := os.Getenv(askPassModeEnv); mode == "1" {
		os.Exit(runAskPass(os.Args[1:], os.Getenv, os.Stdout))
	} else if mode != "" {
		os.Exit(1)
	}

	logger := slog.New(slog.NewJSONHandler(os.Stderr, nil))
	settings, err := config.LoadIndexer()
	if err != nil {
		logger.Error("configuration failed", "error", err)
		os.Exit(1)
	}
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()
	runtime, err := newIndexRuntime(ctx, settings)
	if err == nil {
		err = runtime.run(ctx)
	}
	if err != nil {
		logger.Error("indexer stopped", "error", err)
		os.Exit(1)
	}
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
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil || parsed.Path != "" || parsed.RawQuery != "" || parsed.Fragment != "" || parsed.String() != origin {
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

func (runtime indexRuntime) run(ctx context.Context) error {
	if runtime.close != nil {
		defer runtime.close()
	}
	for _, initialize := range []func(context.Context) error{runtime.ping, runtime.migrate, runtime.upsertNode, runtime.reapExpired, runtime.pruneHistory} {
		if err := initialize(ctx); err != nil {
			return err
		}
	}
	if err := runtime.runWorker(ctx); err != nil && err != ctx.Err() {
		return err
	}
	return nil
}

func newIndexRuntime(ctx context.Context, settings config.Indexer) (indexRuntime, error) {
	privateKey, err := readBoundedFile(settings.GitHub.PrivateKeyFile, maxPrivateKeySize)
	if err != nil {
		return indexRuntime{}, err
	}
	var caPEM []byte
	if settings.GitHub.CAFile != "" {
		if caPEM, err = readBoundedFile(settings.GitHub.CAFile, maxCABytes); err != nil {
			return indexRuntime{}, err
		}
	}
	endpoints, err := parseGitHubEndpoints(settings.GitHub)
	if err != nil {
		return indexRuntime{}, err
	}
	httpClient, err := githubapp.NewHTTPClient(caPEM, endpoints, 30*time.Second)
	if err != nil {
		return indexRuntime{}, err
	}
	signer, err := githubapp.NewSigner(settings.GitHub.AppID, privateKey, nil)
	if err != nil {
		return indexRuntime{}, err
	}
	pool, err := postgres.Open(ctx, settings.DatabaseURL)
	if err != nil {
		return indexRuntime{}, err
	}
	metrics := observability.New()
	store := postgres.New(pool)
	githubClient := githubapp.NewClient(endpoints, httpClient, signer, settings.GitHub.APIVersion, maxBackendBytes, nil, metrics)
	zoektClient, err := zoekt.New(settings.ZoektURL, &http.Client{Timeout: 10 * time.Second}, maxBackendBytes, metrics)
	if err != nil {
		pool.Close()
		return indexRuntime{}, err
	}
	executable, err := os.Executable()
	if err != nil {
		pool.Close()
		return indexRuntime{}, err
	}
	runner := indexer.Runner{MaxOutput: 64 << 10, KillGrace: 5 * time.Second}
	git := &indexer.Git{
		Binary: settings.GitPath, BaseURL: settings.GitHub.GitURL, AskPass: executable,
		CABundle: settings.GitHub.CAFile, MirrorsDir: filepath.Join(settings.DataDir, "mirrors"),
		WorktreesDir: filepath.Join(settings.DataDir, "worktrees"), Runner: runner, CommandTimeout: 2 * time.Minute,
	}
	worker := &indexer.Worker{
		ID: settings.WorkerID, Queue: store, Store: store, Tokens: githubClient, Git: git,
		Zoekt:        &indexer.ZoektIndexer{Binary: settings.ZoektGitIndex, IndexDir: settings.IndexDir, Runner: runner, Client: zoektClient, IndexTimeout: 10 * time.Minute, VisibilityTimeout: 2 * time.Minute},
		MinFreeBytes: uint64(settings.MinFreeBytes), Metrics: metrics,
	}
	return indexRuntime{
		ping:         pool.Ping,
		migrate:      func(ctx context.Context) error { return postgres.Migrate(ctx, pool) },
		upsertNode:   func(ctx context.Context) error { return store.UpsertSearchNode(ctx, searchNodeID, settings.ZoektURL) },
		reapExpired:  func(ctx context.Context) error { _, err := store.ReapExpired(ctx, 1000); return err },
		pruneHistory: func(ctx context.Context) error { _, _, err := store.Prune(ctx); return err },
		runWorker:    worker.Run,
		close:        pool.Close,
	}, nil
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
