//go:build unix

package indexer

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/grepnest/grepnest/internal/observability"
	"github.com/grepnest/grepnest/internal/repository"
	"github.com/grepnest/grepnest/internal/zoekt"
)

func TestZoektIndexUsesPinnedArgumentsAndSafeEnvironment(t *testing.T) {
	directory := t.TempDir()
	argumentsFile := filepath.Join(directory, "arguments")
	environmentFile := filepath.Join(directory, "environment")
	binary := filepath.Join(directory, "zoekt-git-index")
	script := "#!/bin/sh\nprintf '%s\\n' \"$@\" > '" + argumentsFile + "'\nenv > '" + environmentFile + "'\n"
	if err := os.WriteFile(binary, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	indexDirectory := filepath.Join(directory, "index")
	worktree := filepath.Join(directory, "worktree")
	t.Setenv("GREPNEST_GIT_TOKEN", "must-not-leak")
	indexer := ZoektIndexer{Binary: binary, IndexDir: indexDirectory, Runner: Runner{MaxOutput: 1024, KillGrace: time.Millisecond}}
	repo := repository.Repository{ID: 4, ZoektID: 7, Name: "acme/repo", Branch: "main", WebURL: "https://ghe.example/acme/repo"}
	if err := indexer.Index(t.Context(), repo, worktree); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(argumentsFile)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"-index", indexDirectory, "-branches", "main", "-submodules=false", "-incremental=true", "-file_limit", "2097152", "-parallelism", "1", "-disable_ctags", worktree}
	if got := strings.Fields(string(data)); !slices.Equal(got, want) {
		t.Fatalf("arguments = %q, want %q", got, want)
	}
	environment, err := os.ReadFile(environmentFile)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(environment), "must-not-leak") || strings.Contains(string(environment), "GREPNEST_GIT_TOKEN") {
		t.Fatalf("credential environment leaked: %s", environment)
	}
}

func TestZoektIndexUsesFixedDeadline(t *testing.T) {
	binary := filepath.Join(t.TempDir(), "slow-indexer")
	if err := os.WriteFile(binary, []byte("#!/bin/sh\n/bin/sleep 5\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	indexer := ZoektIndexer{
		Binary: binary, IndexDir: t.TempDir(), IndexTimeout: 10 * time.Millisecond,
		Runner: Runner{MaxOutput: 1024, KillGrace: time.Millisecond},
	}
	repo := repository.Repository{ZoektID: 7, Branch: "main"}
	started := time.Now()
	err := indexer.Index(t.Context(), repo, "-c")
	if err == nil || time.Since(started) > time.Second {
		t.Fatalf("error=%v duration=%s", err, time.Since(started))
	}
}

func TestZoektWaitVisibleRequiresExactRepositoryBranchAndVersion(t *testing.T) {
	responses := []string{
		`{"List":{"ReposMap":{"8":{"Branches":[{"Name":"main","Version":"target"}]}}}}`,
		`{"List":{"ReposMap":{"7":{"Branches":[{"Name":"other","Version":"target"},{"Name":"main","Version":"old"}]}}}}`,
		`{"List":{"ReposMap":{"7":{"Branches":[{"Name":"main","Version":"target"}]}}}}`,
	}
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		index := calls
		calls++
		if index >= len(responses) {
			index = len(responses) - 1
		}
		_, _ = writer.Write([]byte(responses[index]))
	}))
	defer server.Close()
	client, err := zoekt.New(server.URL, server.Client(), 1024, observability.New())
	if err != nil {
		t.Fatal(err)
	}
	indexer := ZoektIndexer{Client: client}
	if err := indexer.WaitVisible(t.Context(), 7, "main", "target"); err != nil {
		t.Fatal(err)
	}
	if calls != 3 {
		t.Fatalf("calls = %d, want 3", calls)
	}
}

func TestZoektWaitVisibleStopsWithContext(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		_, _ = writer.Write([]byte(`{"List":{"ReposMap":{}}}`))
	}))
	defer server.Close()
	client, err := zoekt.New(server.URL, server.Client(), 1024, observability.New())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Millisecond)
	defer cancel()
	err = (&ZoektIndexer{Client: client}).WaitVisible(ctx, 7, "main", "target")
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("error = %v", err)
	}
}
