//go:build e2e && unix

package e2e

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/x509"
	"encoding/pem"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/grepnest/grepnest/internal/githubapp"
	"github.com/grepnest/grepnest/internal/indexer"
	"github.com/grepnest/grepnest/internal/repository"
	"github.com/grepnest/grepnest/internal/search"
	"github.com/grepnest/grepnest/internal/zoekt"
)

func TestSnapshotProvidersProduceEquivalentDirectoryIndexes(t *testing.T) {
	zoektIndex := requiredExecutable(t, "ZOEKT_INDEX")
	_, zoektWebserver := requiredExecutables(t)
	gitBinary, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 60*time.Second)
	defer cancel()
	root := t.TempDir()
	work := filepath.Join(root, "work")
	if err := os.Mkdir(work, 0o700); err != nil {
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
		"empty.txt":      {nil, 0o644},
		"executable.sh":  {[]byte("#!/bin/sh\necho ExecutableNeedle\n"), 0o755},
		"order/first.go": {[]byte("// OrderNeedle\n"), 0o644},
		"order/last.go":  {[]byte("// OrderNeedle\n"), 0o644},
	}
	for name, fixture := range fixtures {
		path := filepath.Join(work, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, fixture.data, fixture.mode); err != nil {
			t.Fatal(err)
		}
	}
	run(t, ctx, gitBinary, "init", "--initial-branch=main", work)
	run(t, ctx, gitBinary, "-C", work, "config", "user.name", "GrepNest Test")
	run(t, ctx, gitBinary, "-C", work, "config", "user.email", "test@grepnest.invalid")
	run(t, ctx, gitBinary, "-C", work, "add", ".")
	run(t, ctx, gitBinary, "-C", work, "-c", "commit.gpgsign=false", "commit", "-m", "fixture")
	sha := strings.TrimSpace(run(t, ctx, gitBinary, "-C", work, "rev-parse", "HEAD"))

	origins := filepath.Join(root, "origins")
	bare := filepath.Join(origins, "acme", "repo.git")
	if err := os.MkdirAll(filepath.Dir(bare), 0o700); err != nil {
		t.Fatal(err)
	}
	run(t, ctx, gitBinary, "clone", "--bare", work, bare)
	run(t, ctx, gitBinary, "--git-dir", bare, "update-server-info")
	archive := providerArchive(t, "repo-"+sha, fixtures)
	const token = "provider-parity-token"
	fileServer := http.FileServer(http.Dir(origins))
	server := httptest.NewTLSServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		switch {
		case request.URL.Path == "/api/v3/repos/acme/repo/tarball/"+sha:
			if request.Header.Get("Authorization") != "Bearer "+token {
				http.Error(response, "unauthorized", http.StatusUnauthorized)
				return
			}
			response.Header().Set("Content-Type", "application/gzip")
			_, _ = response.Write(archive)
		case strings.HasPrefix(request.URL.Path, "/acme/repo.git/"):
			username, password, ok := request.BasicAuth()
			if !ok || username != "x-access-token" || password != token {
				response.Header().Set("WWW-Authenticate", `Basic realm="git"`)
				http.Error(response, "unauthorized", http.StatusUnauthorized)
				return
			}
			fileServer.ServeHTTP(response, request)
		default:
			http.NotFound(response, request)
		}
	}))
	defer server.Close()
	certificate := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw})
	caFile := filepath.Join(root, "server-ca.pem")
	if err := os.WriteFile(caFile, certificate, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := x509.ParseCertificate(server.Certificate().Raw); err != nil {
		t.Fatal(err)
	}
	askPass := filepath.Join(root, "askpass")
	if err := os.WriteFile(askPass, []byte("#!/bin/sh\ncase \"$1\" in *Username*) printf '%s\\n' x-access-token;; *Password*) printf '%s\\n' \"$GREPNEST_GIT_TOKEN\";; *) exit 1;; esac\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	base, err := url.Parse(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	api := *base
	api.Path = "/api/v3"
	endpoints := githubapp.Endpoints{Web: base, API: &api, Upload: base, Git: base, Archive: base}
	httpClient, err := githubapp.NewHTTPClient(certificate, endpoints, 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	client := githubapp.NewClient(endpoints, httpClient, nil, "2022-11-28", 4<<20, nil)
	archives := filepath.Join(root, "provider-data", "archives")
	gitProvider := indexer.GitSnapshotProvider{Git: &indexer.Git{
		Binary: gitBinary, BaseURL: server.URL, AskPass: askPass, CABundle: caFile,
		MirrorsDir: filepath.Join(root, "provider-data", "git", "mirrors"), WorktreesDir: filepath.Join(root, "provider-data", "git", "worktrees"),
		Runner: indexer.Runner{MaxOutput: 64 << 10, KillGrace: 100 * time.Millisecond}, CommandTimeout: 15 * time.Second,
	}}
	archiveProvider := indexer.ArchiveSnapshotProvider{Client: client, WorkspacesDir: archives, Limits: indexer.ArchiveLimits{
		MaxDownloadBytes: 4 << 20, MaxExtractedBytes: 4 << 20, MaxFileBytes: 1 << 20, MaxFiles: 100, MaxPathBytes: 1024,
	}}
	repo := repository.Repository{ID: 41, ZoektID: 7041, Name: "acme/repo", Branch: "main", DesiredSHA: sha, WebURL: server.URL + "/acme/repo"}
	request := indexer.SnapshotRequest{RepositoryID: repo.ID, Repository: repo, JobID: 101, CommitSHA: sha, AccessToken: token}
	gitSnapshot, err := gitProvider.Prepare(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	request.JobID = 102
	archiveSnapshot, err := archiveProvider.Prepare(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := snapshotTree(t, archiveSnapshot.Root), snapshotTree(t, gitSnapshot.Root); !reflect.DeepEqual(got, want) {
		t.Fatalf("provider trees differ\narchive: %#v\ngit: %#v", got, want)
	}

	clients := make(map[string]*zoekt.Client, 2)
	processes := make([]*managedProcess, 0, 2)
	for name, snapshot := range map[string]indexer.Snapshot{"git": gitSnapshot, "archive": archiveSnapshot} {
		indexDir := filepath.Join(root, name+"-index")
		if err := os.Mkdir(indexDir, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := (&indexer.ZoektIndexer{Binary: zoektIndex, IndexDir: indexDir, Runner: indexer.Runner{MaxOutput: 64 << 10, KillGrace: 100 * time.Millisecond}}).Index(ctx, repo, snapshot.Root); err != nil {
			t.Fatal(err)
		}
		client, process, _ := parityZoekt(t, ctx, zoektWebserver, indexDir)
		clients[name] = client
		processes = append(processes, process)
	}
	t.Cleanup(func() {
		for _, process := range processes {
			process.stop(t)
		}
	})
	for _, query := range []string{"LiteralNeedle", "Regex[0-9]+Needle", "árvíztűrő", "BinaryNeedle", "file:empty.txt", "ExecutableNeedle", "OrderNeedle"} {
		gitResult, err := clients["git"].Search(ctx, search.BackendRequest{Query: query, RepositoryIDs: []uint32{repo.ZoektID}, Limit: 100, Timeout: time.Second})
		if err != nil {
			t.Fatal(err)
		}
		archiveResult, err := clients["archive"].Search(ctx, search.BackendRequest{Query: query, RepositoryIDs: []uint32{repo.ZoektID}, Limit: 100, Timeout: time.Second})
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(archiveResult, gitResult) {
			t.Fatalf("query %q differs\ngit: %#v\narchive: %#v", query, gitResult, archiveResult)
		}
	}
	for name, client := range clients {
		metadata, err := client.List(ctx, repo.ZoektID)
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(metadata, []zoekt.IndexedRepository{{RepoID: repo.ZoektID, Branch: repo.Branch, Version: sha}}) {
			t.Fatalf("%s metadata = %#v", name, metadata)
		}
	}
	if err := archiveProvider.Cleanup(ctx, archiveSnapshot); err != nil {
		t.Fatal(err)
	}
	assertArchiveProviderEmpty(t, archives)
	if err := gitProvider.Cleanup(ctx, gitSnapshot); err != nil {
		t.Fatal(err)
	}
}

type snapshotEntry struct {
	Mode os.FileMode
	Data string
}

func snapshotTree(t *testing.T, root string) map[string]snapshotEntry {
	t.Helper()
	result := make(map[string]snapshotEntry)
	if err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		if relative == ".git" {
			if entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if relative == "." || entry.IsDir() {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		result[filepath.ToSlash(relative)] = snapshotEntry{Mode: info.Mode().Perm(), Data: string(data)}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return result
}

func assertArchiveProviderEmpty(t *testing.T, root string) {
	t.Helper()
	if err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if path == root || entry.IsDir() {
			return nil
		}
		t.Fatalf("archive provider residue: %s", path)
		return nil
	}); err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
}

func providerArchive(t *testing.T, root string, fixtures map[string]struct {
	data []byte
	mode os.FileMode
}) []byte {
	t.Helper()
	var output bytes.Buffer
	gzipWriter := gzip.NewWriter(&output)
	tarWriter := tar.NewWriter(gzipWriter)
	if err := tarWriter.WriteHeader(&tar.Header{Name: root + "/", Typeflag: tar.TypeDir, Mode: 0o755}); err != nil {
		t.Fatal(err)
	}
	if err := tarWriter.WriteHeader(&tar.Header{Name: root + "/order/", Typeflag: tar.TypeDir, Mode: 0o755}); err != nil {
		t.Fatal(err)
	}
	names := make([]string, 0, len(fixtures))
	for name := range fixtures {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		fixture := fixtures[name]
		header := &tar.Header{Name: root + "/" + name, Typeflag: tar.TypeReg, Mode: int64(fixture.mode.Perm()), Size: int64(len(fixture.data))}
		if err := tarWriter.WriteHeader(header); err != nil {
			t.Fatal(err)
		}
		if _, err := tarWriter.Write(fixture.data); err != nil {
			t.Fatal(err)
		}
	}
	if err := tarWriter.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gzipWriter.Close(); err != nil {
		t.Fatal(err)
	}
	return output.Bytes()
}
