//go:build e2e && unix

package e2e

import (
	"context"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/grepnest/grepnest/internal/authn"
	"github.com/grepnest/grepnest/internal/authz"
	"github.com/grepnest/grepnest/internal/indexer"
	"github.com/grepnest/grepnest/internal/observability"
	"github.com/grepnest/grepnest/internal/repository"
	"github.com/grepnest/grepnest/internal/search"
	"github.com/grepnest/grepnest/internal/zoekt"
	"github.com/grepnest/grepnest/pkg/api"
)

func TestZoektDirectoryMatchesGitIndex(t *testing.T) {
	zoektIndex := requiredExecutable(t, "ZOEKT_INDEX")
	zoektGitIndex, zoektWebserver := requiredExecutables(t)
	ctx, cancel := context.WithTimeout(t.Context(), 60*time.Second)
	defer cancel()
	root := t.TempDir()
	gitSource := filepath.Join(root, "git-source")
	directorySource := filepath.Join(root, "directory-source")
	if err := os.MkdirAll(gitSource, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(directorySource, 0o700); err != nil {
		t.Fatal(err)
	}
	fixtures := map[string]struct {
		data []byte
		mode os.FileMode
	}{
		"literal.txt":    {[]byte("LiteralNeedle\n"), 0o644},
		"regex.txt":      {[]byte("Regex123Needle\n"), 0o644},
		"unicode.txt":    {[]byte("árvíztűrő tükörfúrógép\n"), 0o644},
		"binary.bin":     {[]byte("\x00BinaryNeedle\x00"), 0o644},
		"empty.txt":      {[]byte{}, 0o644},
		"executable.sh":  {[]byte("#!/bin/sh\necho ExecutableNeedle\n"), 0o755},
		"order/first.go": {[]byte("// OrderNeedle\n"), 0o644},
		"order/last.go":  {[]byte("// OrderNeedle\n"), 0o644},
	}
	for name, fixture := range fixtures {
		for _, source := range []string{gitSource, directorySource} {
			path := filepath.Join(source, filepath.FromSlash(name))
			if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, fixture.data, fixture.mode); err != nil {
				t.Fatal(err)
			}
		}
	}
	run(t, ctx, "git", "init", "--initial-branch=main", gitSource)
	run(t, ctx, "git", "-C", gitSource, "config", "user.name", "GrepNest Test")
	run(t, ctx, "git", "-C", gitSource, "config", "user.email", "test@grepnest.invalid")
	run(t, ctx, "git", "-C", gitSource, "config", "zoekt.repoid", "7001")
	run(t, ctx, "git", "-C", gitSource, "config", "zoekt.name", "fixture/parity")
	run(t, ctx, "git", "-C", gitSource, "config", "zoekt.web-url", "https://example.test/fixture/parity")
	run(t, ctx, "git", "-C", gitSource, "add", ".")
	run(t, ctx, "git", "-C", gitSource, "-c", "commit.gpgsign=false", "commit", "-m", "fixture")
	sha := strings.TrimSpace(run(t, ctx, "git", "-C", gitSource, "rev-parse", "HEAD"))

	gitIndexDir := filepath.Join(root, "git-index")
	directoryIndexDir := filepath.Join(root, "directory-index")
	for _, path := range []string{gitIndexDir, directoryIndexDir} {
		if err := os.Mkdir(path, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	run(t, ctx, zoektGitIndex, "-index", gitIndexDir, "-branches", "main", "-submodules=false", "-incremental=false", "-file_limit", "2097152", "-parallelism", "1", "-disable_ctags", gitSource)
	repo := repository.Repository{ID: 4, ZoektID: 7001, Name: "fixture/parity", Branch: "main", DesiredSHA: sha, WebURL: "https://example.test/fixture/parity"}
	if err := (&indexer.ZoektIndexer{Binary: zoektIndex, IndexDir: directoryIndexDir, Runner: indexer.Runner{MaxOutput: 64 << 10, KillGrace: 100 * time.Millisecond}}).Index(ctx, repo, directorySource); err != nil {
		t.Fatal(err)
	}

	gitClient, gitProcess, _ := parityZoekt(t, ctx, zoektWebserver, gitIndexDir)
	directoryClient, directoryProcess, _ := parityZoekt(t, ctx, zoektWebserver, directoryIndexDir)
	t.Cleanup(func() { gitProcess.stop(t); directoryProcess.stop(t) })
	for _, query := range []string{"LiteralNeedle", "Regex[0-9]+Needle", "árvíztűrő", "BinaryNeedle", "file:empty.txt", "ExecutableNeedle", "OrderNeedle"} {
		gitResult, err := gitClient.Search(ctx, search.BackendRequest{Query: query, RepositoryIDs: []uint32{7001}, Limit: 100, Timeout: time.Second})
		if err != nil {
			t.Fatalf("git query %q: %v", query, err)
		}
		directoryResult, err := directoryClient.Search(ctx, search.BackendRequest{Query: query, RepositoryIDs: []uint32{7001}, Limit: 100, Timeout: time.Second})
		if err != nil {
			t.Fatalf("directory query %q: %v", query, err)
		}
		if !reflect.DeepEqual(directoryResult, gitResult) {
			t.Fatalf("query %q differs\ngit: %#v\ndirectory: %#v", query, gitResult, directoryResult)
		}
	}
	for name, client := range map[string]*zoekt.Client{"git": gitClient, "directory": directoryClient} {
		metadata, err := client.List(ctx, 7001)
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(metadata, []zoekt.IndexedRepository{{RepoID: 7001, Branch: "main", Version: sha}}) {
			t.Fatalf("%s metadata = %#v", name, metadata)
		}
	}
	assertParitySHASuppression(t, ctx, gitClient, directoryClient, repo, sha)
}

func requiredExecutable(t *testing.T, name string) string {
	t.Helper()
	path := os.Getenv(name)
	if path == "" {
		t.Fatalf("missing executable path: %s", name)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("%s path %q: %v", name, path, err)
	}
	return path
}

func parityZoekt(t *testing.T, ctx context.Context, executable, indexDir string) (*zoekt.Client, *managedProcess, string) {
	t.Helper()
	address := freeAddress(t)
	process := startProcess(t, exec.CommandContext(ctx, executable, "-index", indexDir, "-listen", address, "-rpc", "-html=false"))
	client, err := zoekt.New("http://"+address, &http.Client{Timeout: 3 * time.Second}, 256<<10, observability.New())
	if err != nil {
		t.Fatal(err)
	}
	waitReady(t, ctx, client, process)
	return client, process, address
}

func assertParitySHASuppression(t *testing.T, ctx context.Context, gitClient, directoryClient *zoekt.Client, repo repository.Repository, sha string) {
	t.Helper()
	for name, backend := range map[string]search.SearchBackend{"git": gitClient, "directory": directoryClient} {
		candidate := repo
		candidate.Name = "fixture/parity"
		candidate.IndexedSHA = strings.Repeat("f", 40)
		registry, err := repository.NewStatic([]repository.Repository{candidate})
		if err != nil {
			t.Fatal(err)
		}
		service := search.NewService(backend, authz.NewStatic(registry), search.Limits{MaxResults: 100, MaxResponseBytes: 256 << 10})
		result, err := service.Search(ctx, authn.Principal{RepositoryNames: []string{candidate.Name}}, api.SearchRequest{Query: "LiteralNeedle"})
		if err != nil {
			t.Fatal(err)
		}
		if len(result.Matches) != 0 {
			t.Fatalf("%s stale SHA matches = %#v, indexed SHA %s", name, result.Matches, sha)
		}
	}
}
