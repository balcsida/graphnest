//go:build unix

package indexer

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/grepnest/grepnest/internal/githubapp"
	"github.com/grepnest/grepnest/internal/repository"
)

type archiveDownload struct {
	body  []byte
	err   error
	owner string
	repo  string
	sha   string
	token string
}

func (download *archiveDownload) DownloadArchive(_ context.Context, owner, repo, sha, token string) (io.ReadCloser, error) {
	download.owner, download.repo, download.sha, download.token = owner, repo, sha, token
	return io.NopCloser(bytes.NewReader(download.body)), download.err
}

func TestArchiveSnapshotProviderExtractsExactSHAIntoJobWorkspace(t *testing.T) {
	sha := strings.Repeat("a", 40)
	download := &archiveDownload{body: tarGzip(t,
		tarEntry{name: "acme-repo-deadbeef/", mode: 0o755, kind: tar.TypeDir},
		tarEntry{name: "acme-repo-deadbeef/main.go", mode: 0o777, body: "package main\n"},
	)}
	provider := ArchiveSnapshotProvider{Client: download, WorkspacesDir: t.TempDir(), Limits: testArchiveLimits()}
	snapshot, err := provider.Prepare(t.Context(), SnapshotRequest{
		RepositoryID: 7, JobID: 9, CommitSHA: sha, AccessToken: "secret",
		Repository: repository.Repository{ID: 7, Name: "acme/repo"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if download.owner != "acme" || download.repo != "repo" || download.sha != sha || download.token != "secret" {
		t.Fatalf("download = %#v", download)
	}
	if snapshot.RepositoryID != 7 || snapshot.JobID != 9 || snapshot.CommitSHA != sha || snapshot.Root != filepath.Join(provider.WorkspacesDir, "7", "9") {
		t.Fatalf("snapshot = %#v", snapshot)
	}
	data, err := os.ReadFile(filepath.Join(snapshot.Root, "main.go"))
	if err != nil || string(data) != "package main\n" {
		t.Fatalf("file = %q, %v", data, err)
	}
	info, err := os.Stat(filepath.Join(snapshot.Root, "main.go"))
	if err != nil || info.Mode().Perm() != 0o755 {
		t.Fatalf("mode = %v, %v", info.Mode(), err)
	}
}

func TestArchiveSnapshotProviderRejectsUnsafeArchives(t *testing.T) {
	tests := []struct {
		name   string
		limits ArchiveLimits
		entry  tarEntry
	}{
		{name: "traversal", entry: tarEntry{name: "root/../escape", body: "x"}},
		{name: "absolute", entry: tarEntry{name: "/root/file", body: "x"}},
		{name: "symlink", entry: tarEntry{name: "root/link", kind: tar.TypeSymlink, link: "file"}},
		{name: "hardlink", entry: tarEntry{name: "root/link", kind: tar.TypeLink, link: "root/file"}},
		{name: "fifo", entry: tarEntry{name: "root/pipe", kind: tar.TypeFifo}},
		{name: "file too large", limits: ArchiveLimits{MaxDownloadBytes: 1 << 20, MaxExtractedBytes: 1 << 20, MaxFileBytes: 1, MaxFiles: 10, MaxPathBytes: 1024}, entry: tarEntry{name: "root/file", body: "xx"}},
		{name: "total too large", limits: ArchiveLimits{MaxDownloadBytes: 1 << 20, MaxExtractedBytes: 1, MaxFileBytes: 10, MaxFiles: 10, MaxPathBytes: 1024}, entry: tarEntry{name: "root/file", body: "xx"}},
		{name: "path too long", limits: ArchiveLimits{MaxDownloadBytes: 1 << 20, MaxExtractedBytes: 1 << 20, MaxFileBytes: 10, MaxFiles: 10, MaxPathBytes: 8}, entry: tarEntry{name: "root/longname", body: "x"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			limits := test.limits
			if limits == (ArchiveLimits{}) {
				limits = testArchiveLimits()
			}
			download := &archiveDownload{body: tarGzip(t, test.entry)}
			provider := ArchiveSnapshotProvider{Client: download, WorkspacesDir: t.TempDir(), Limits: limits}
			_, err := provider.Prepare(t.Context(), archiveRequest())
			if err == nil {
				t.Fatal("Prepare() succeeded")
			}
			if _, statErr := os.Stat(filepath.Join(provider.WorkspacesDir, "7", "9")); !errors.Is(statErr, os.ErrNotExist) {
				t.Fatalf("partial workspace remains: %v", statErr)
			}
		})
	}
}

func TestArchiveSnapshotProviderEnforcesDownloadAndFileCountLimits(t *testing.T) {
	body := tarGzip(t, tarEntry{name: "root/one", body: "1"}, tarEntry{name: "root/two", body: "2"})
	for _, test := range []struct {
		name   string
		limits ArchiveLimits
	}{
		{"download bytes", ArchiveLimits{MaxDownloadBytes: int64(len(body) - 1), MaxExtractedBytes: 10, MaxFileBytes: 10, MaxFiles: 10, MaxPathBytes: 1024}},
		{"file count", ArchiveLimits{MaxDownloadBytes: 1 << 20, MaxExtractedBytes: 10, MaxFileBytes: 10, MaxFiles: 1, MaxPathBytes: 1024}},
	} {
		t.Run(test.name, func(t *testing.T) {
			provider := ArchiveSnapshotProvider{Client: &archiveDownload{body: body}, WorkspacesDir: t.TempDir(), Limits: test.limits}
			if _, err := provider.Prepare(t.Context(), archiveRequest()); err == nil {
				t.Fatal("Prepare() succeeded")
			}
		})
	}
}

func TestArchiveSnapshotProviderMapsMissingTargetAndCleansWorkspaces(t *testing.T) {
	root := t.TempDir()
	provider := ArchiveSnapshotProvider{Client: &archiveDownload{err: githubapp.HTTPStatusError{StatusCode: 404}}, WorkspacesDir: root, Limits: testArchiveLimits()}
	if _, err := provider.Prepare(t.Context(), archiveRequest()); !errors.Is(err, ErrTargetMissing) {
		t.Fatalf("error = %v", err)
	}
	for _, path := range []string{filepath.Join(root, "7", "9"), filepath.Join(root, "7", "10"), filepath.Join(root, "7", ".10-active"), filepath.Join(root, "7", ".12-stale"), filepath.Join(root, "8", "11")} {
		if err := os.MkdirAll(path, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := provider.CleanupStale(t.Context(), ActiveJobs{10: {}}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "7", "10")); err != nil {
		t.Fatalf("active workspace removed: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "7", ".10-active")); err != nil {
		t.Fatalf("active staging workspace removed: %v", err)
	}
	for _, path := range []string{filepath.Join(root, "7", "9"), filepath.Join(root, "7", ".12-stale"), filepath.Join(root, "8", "11")} {
		if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("stale workspace remains: %s: %v", path, err)
		}
	}
}

func archiveRequest() SnapshotRequest {
	return SnapshotRequest{RepositoryID: 7, JobID: 9, CommitSHA: strings.Repeat("a", 40), AccessToken: "secret", Repository: repository.Repository{ID: 7, Name: "acme/repo"}}
}

func testArchiveLimits() ArchiveLimits {
	return ArchiveLimits{MaxDownloadBytes: 1 << 20, MaxExtractedBytes: 1 << 20, MaxFileBytes: 1 << 20, MaxFiles: 100, MaxPathBytes: 1024}
}

type tarEntry struct {
	name, body, link string
	mode             int64
	kind             byte
}

func tarGzip(t *testing.T, entries ...tarEntry) []byte {
	t.Helper()
	var output bytes.Buffer
	gz := gzip.NewWriter(&output)
	tw := tar.NewWriter(gz)
	for _, entry := range entries {
		kind := entry.kind
		if kind == 0 {
			kind = tar.TypeReg
		}
		if err := tw.WriteHeader(&tar.Header{Name: entry.name, Size: int64(len(entry.body)), Mode: entry.mode, Typeflag: kind, Linkname: entry.link}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write([]byte(entry.body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return output.Bytes()
}
