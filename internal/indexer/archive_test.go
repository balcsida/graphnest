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
	"time"

	"github.com/balcsida/graphnest/internal/githubapp"
	"github.com/balcsida/graphnest/internal/observability"
	"github.com/balcsida/graphnest/internal/repository"
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
		{name: "backslash", entry: tarEntry{name: `root\escape`, body: "x"}},
		{name: "symlink", entry: tarEntry{name: "root/link", kind: tar.TypeSymlink, link: "file"}},
		{name: "hardlink", entry: tarEntry{name: "root/link", kind: tar.TypeLink, link: "root/file"}},
		{name: "fifo", entry: tarEntry{name: "root/pipe", kind: tar.TypeFifo}},
		{name: "character device", entry: tarEntry{name: "root/device", kind: tar.TypeChar}},
		{name: "block device", entry: tarEntry{name: "root/device", kind: tar.TypeBlock}},
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

func TestArchiveSnapshotProviderCountsDirectoriesAndRejectsInvalidLimits(t *testing.T) {
	body := tarGzip(t, tarEntry{name: "root/empty/", kind: tar.TypeDir}, tarEntry{name: "root/file", body: "x"})
	provider := ArchiveSnapshotProvider{Client: &archiveDownload{body: body}, WorkspacesDir: t.TempDir(), Limits: ArchiveLimits{MaxDownloadBytes: 1 << 20, MaxExtractedBytes: 10, MaxFileBytes: 10, MaxFiles: 1, MaxPathBytes: 1024}}
	if _, err := provider.Prepare(t.Context(), archiveRequest()); err == nil {
		t.Fatal("directory did not consume archive entry limit")
	}
	valid := testArchiveLimits()
	for _, clear := range []func(*ArchiveLimits){
		func(limits *ArchiveLimits) { limits.MaxDownloadBytes = 0 },
		func(limits *ArchiveLimits) { limits.MaxExtractedBytes = 0 },
		func(limits *ArchiveLimits) { limits.MaxFileBytes = 0 },
		func(limits *ArchiveLimits) { limits.MaxFiles = 0 },
		func(limits *ArchiveLimits) { limits.MaxPathBytes = 0 },
	} {
		limits := valid
		clear(&limits)
		provider.Limits = limits
		if _, err := provider.Prepare(t.Context(), archiveRequest()); err == nil {
			t.Fatalf("limits accepted: %#v", limits)
		}
	}
}

func TestExtractArchiveRejectsDuplicateRootsMalformedAndNULPaths(t *testing.T) {
	archives := []struct {
		name string
		body []byte
	}{
		{"duplicate output", tarGzip(t, tarEntry{name: "root/file", body: "a"}, tarEntry{name: "root/file", body: "b"})},
		{"multiple roots", tarGzip(t, tarEntry{name: "root/a", body: "a"}, tarEntry{name: "other/b", body: "b"})},
		{"inconsistent root", tarGzip(t, tarEntry{name: "root/", kind: tar.TypeDir}, tarEntry{name: "other/file", body: "x"})},
		{"malformed gzip", []byte("not gzip")},
		{"malformed tar", gzipBytes(t, []byte("not tar"))},
	}
	for _, test := range archives {
		t.Run(test.name, func(t *testing.T) {
			if err := extractArchive(t.Context(), bytes.NewReader(test.body), t.TempDir(), testArchiveLimits()); err == nil {
				t.Fatal("extractArchive() succeeded")
			}
		})
	}
	if archivePathSafe("root/file\x00name", 1024) {
		t.Fatal("NUL path accepted")
	}
}

func TestExtractArchiveDoesNotFollowHostSymlink(t *testing.T) {
	destination := t.TempDir()
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(destination, "link")); err != nil {
		t.Fatal(err)
	}
	body := tarGzip(t, tarEntry{name: "root/link/escape", body: "owned"})
	if err := extractArchive(t.Context(), bytes.NewReader(body), destination, testArchiveLimits()); err == nil {
		t.Fatal("extractArchive() followed host symlink")
	}
	if _, err := os.Stat(filepath.Join(outside, "escape")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("file escaped extraction root: %v", err)
	}
}

func TestArchiveSnapshotProviderCancellationRemovesPartialWorkspace(t *testing.T) {
	root := t.TempDir()
	ctx, cancel := context.WithCancel(t.Context())
	body := tarGzip(t, tarEntry{name: "root/file", body: strings.Repeat("x", 64<<10)})
	provider := ArchiveSnapshotProvider{Client: &readerDownload{Reader: &cancelAfterReader{Reader: bytes.NewReader(body), cancel: cancel, remaining: 64}}, WorkspacesDir: root, Limits: testArchiveLimits()}
	if _, err := provider.Prepare(ctx, archiveRequest()); !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v", err)
	}
	entries, err := os.ReadDir(filepath.Join(root, "7"))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("partial workspace remains: %v", entries)
	}
}

func TestArchiveSnapshotProviderDeadlineRemovesPartialWorkspace(t *testing.T) {
	root := t.TempDir()
	body := tarGzip(t, tarEntry{name: "root/file", body: strings.Repeat("deadline-data", 4096)})
	ctx, cancel := context.WithTimeout(t.Context(), 25*time.Millisecond)
	defer cancel()
	reader := io.MultiReader(bytes.NewReader(body[:len(body)/2]), deadlineReader{ctx: ctx})
	provider := ArchiveSnapshotProvider{Client: &readerDownload{Reader: reader}, WorkspacesDir: root, Limits: testArchiveLimits()}
	if _, err := provider.Prepare(ctx, archiveRequest()); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("error = %v", err)
	}
	entries, err := os.ReadDir(filepath.Join(root, "7"))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("partial workspace remains: %v", entries)
	}
}

func TestArchiveSnapshotProviderReportsPartialCleanupFailure(t *testing.T) {
	root := t.TempDir()
	metrics := observability.New()
	repositoryDir := filepath.Join(root, "7")
	reader := &chmodReader{Reader: strings.NewReader("not gzip"), path: repositoryDir}
	provider := ArchiveSnapshotProvider{Client: &readerDownload{Reader: reader}, WorkspacesDir: root, Limits: testArchiveLimits(), Metrics: metrics}
	_, err := provider.Prepare(t.Context(), archiveRequest())
	if chmodErr := os.Chmod(repositoryDir, 0o700); chmodErr != nil {
		t.Fatal(chmodErr)
	}
	if err == nil || !strings.Contains(err.Error(), "gzip") || !strings.Contains(err.Error(), "cleanup partial archive workspace") {
		t.Fatalf("error = %v", err)
	}
	if body := scrapeWorkerMetrics(t, metrics); !strings.Contains(body, `graphnest_archive_operations_total{operation="cleanup",result="error"} 1`) {
		t.Fatalf("cleanup failure metric missing:\n%s", body)
	}
}

func TestArchiveSnapshotProviderCountsStreamingFailureAsDownloadError(t *testing.T) {
	metrics := observability.New()
	provider := ArchiveSnapshotProvider{Client: &archiveDownload{body: []byte("not gzip")}, WorkspacesDir: t.TempDir(), Limits: testArchiveLimits(), Metrics: metrics}
	if _, err := provider.Prepare(t.Context(), archiveRequest()); err == nil {
		t.Fatal("Prepare() succeeded")
	}
	body := scrapeWorkerMetrics(t, metrics)
	for _, want := range []string{
		`graphnest_archive_operations_total{operation="download",result="error"} 1`,
		`graphnest_archive_operations_total{operation="extract",result="error"} 1`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("metrics missing %q:\n%s", want, body)
		}
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

func TestArchiveSnapshotProviderLeavesTransientHTTPFailuresRetryable(t *testing.T) {
	for _, status := range []int{429, 500, 503} {
		provider := ArchiveSnapshotProvider{Client: &archiveDownload{err: githubapp.HTTPStatusError{StatusCode: status}}, WorkspacesDir: t.TempDir(), Limits: testArchiveLimits()}
		_, err := provider.Prepare(t.Context(), archiveRequest())
		if err == nil || errors.Is(err, ErrTargetMissing) {
			t.Fatalf("status %d error = %v", status, err)
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

func tarGzip(t testing.TB, entries ...tarEntry) []byte {
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

func gzipBytes(t testing.TB, body []byte) []byte {
	t.Helper()
	var output bytes.Buffer
	writer := gzip.NewWriter(&output)
	if _, err := writer.Write(body); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return output.Bytes()
}

type readerDownload struct{ io.Reader }

func (download *readerDownload) DownloadArchive(context.Context, string, string, string, string) (io.ReadCloser, error) {
	return io.NopCloser(download.Reader), nil
}

type chmodReader struct {
	io.Reader
	path string
	done bool
}

type cancelAfterReader struct {
	io.Reader
	cancel    context.CancelFunc
	remaining int
}

func (reader *cancelAfterReader) Read(buffer []byte) (int, error) {
	if len(buffer) > reader.remaining {
		buffer = buffer[:reader.remaining]
	}
	read, err := reader.Reader.Read(buffer)
	reader.remaining -= read
	if reader.remaining == 0 {
		reader.cancel()
	}
	return read, err
}

func (reader *chmodReader) Read(buffer []byte) (int, error) {
	if !reader.done {
		reader.done = true
		if err := os.Chmod(reader.path, 0o500); err != nil {
			return 0, err
		}
	}
	return reader.Reader.Read(buffer)
}

type deadlineReader struct{ ctx context.Context }

func (reader deadlineReader) Read([]byte) (int, error) {
	<-reader.ctx.Done()
	return 0, reader.ctx.Err()
}

func FuzzArchiveExtraction(f *testing.F) {
	valid := tarGzip(f, tarEntry{name: "root/file", body: "safe"})
	for _, seed := range [][]byte{
		valid,
		[]byte("not gzip"),
		gzipBytes(f, []byte("not tar")),
		tarGzip(f, tarEntry{name: "root/../escape", body: "x"}),
		tarGzip(f, tarEntry{name: `root\escape`, body: "x"}),
		tarGzip(f, tarEntry{name: "root/link", kind: tar.TypeSymlink, link: "file"}),
		tarGzip(f, tarEntry{name: "root/file", body: "a"}, tarEntry{name: "root/file", body: "b"}),
		tarGzip(f, tarEntry{name: "root/a", body: "a"}, tarEntry{name: "other/b", body: "b"}),
	} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, archive []byte) {
		if len(archive) > 64<<10 {
			return
		}
		ctx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
		defer cancel()
		limits := ArchiveLimits{MaxDownloadBytes: 64 << 10, MaxExtractedBytes: 64 << 10, MaxFileBytes: 16 << 10, MaxFiles: 100, MaxPathBytes: 256}
		_ = extractArchive(ctx, bytes.NewReader(archive), t.TempDir(), limits)
	})
}
