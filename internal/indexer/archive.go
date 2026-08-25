//go:build unix

package indexer

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/balcsida/graphnest/internal/githubapp"
	"github.com/balcsida/graphnest/internal/observability"
)

type ArchiveDownloader interface {
	DownloadArchive(context.Context, string, string, string, string) (io.ReadCloser, error)
}

type ArchiveLimits struct {
	MaxDownloadBytes, MaxExtractedBytes, MaxFileBytes int64
	MaxFiles, MaxPathBytes                            int
}

type ArchiveSnapshotProvider struct {
	Client        ArchiveDownloader
	WorkspacesDir string
	Limits        ArchiveLimits
	Metrics       *observability.Metrics
}

func (provider ArchiveSnapshotProvider) Prepare(ctx context.Context, request SnapshotRequest) (snapshot Snapshot, err error) {
	snapshot = Snapshot{RepositoryID: request.RepositoryID, JobID: request.JobID, CommitSHA: request.CommitSHA}
	if err := provider.validate(request); err != nil {
		return snapshot, err
	}
	owner, name, _ := strings.Cut(request.Repository.Name, "/")
	started := time.Now()
	body, err := provider.Client.DownloadArchive(ctx, owner, name, request.CommitSHA, request.AccessToken)
	if err != nil {
		provider.observe("download", err, started)
		var status githubapp.HTTPStatusError
		if errors.As(err, &status) && status.StatusCode == 404 {
			return snapshot, errors.Join(ErrTargetMissing, err)
		}
		return snapshot, err
	}
	defer body.Close()
	downloadErr := errors.New("archive body was not consumed")
	defer func() { provider.observe("download", downloadErr, started) }()
	repositoryDir, err := numericPath(provider.WorkspacesDir, strconv.FormatInt(request.RepositoryID, 10))
	if err != nil {
		return snapshot, err
	}
	if err := ensureDirectory(repositoryDir); err != nil {
		return snapshot, err
	}
	workspace, err := numericPath(repositoryDir, strconv.FormatInt(request.JobID, 10))
	if err != nil {
		return snapshot, err
	}
	if _, err := os.Lstat(workspace); err == nil {
		return snapshot, errors.New("archive workspace already exists")
	} else if !errors.Is(err, os.ErrNotExist) {
		return snapshot, err
	}
	staging, err := os.MkdirTemp(repositoryDir, "."+strconv.FormatInt(request.JobID, 10)+"-")
	if err != nil {
		return snapshot, err
	}
	defer func() {
		if err != nil {
			cleanupStarted := time.Now()
			cleanupErr := os.RemoveAll(staging)
			provider.observe("cleanup", cleanupErr, cleanupStarted)
			if cleanupErr != nil {
				err = errors.Join(err, fmt.Errorf("cleanup partial archive workspace: %w", cleanupErr))
			}
		}
	}()
	extractStarted := time.Now()
	err = extractArchive(ctx, body, staging, provider.Limits)
	downloadErr = err
	provider.observe("extract", err, extractStarted)
	if err != nil {
		return snapshot, err
	}
	if err = os.Rename(staging, workspace); err != nil {
		return snapshot, err
	}
	snapshot.Root = workspace
	return snapshot, nil
}

func (provider ArchiveSnapshotProvider) Cleanup(_ context.Context, snapshot Snapshot) error {
	started := time.Now()
	var err error
	defer func() { provider.observe("cleanup", err, started) }()
	if snapshot.RepositoryID <= 0 || snapshot.JobID <= 0 {
		return nil
	}
	workspace, err := numericPath(provider.WorkspacesDir, strconv.FormatInt(snapshot.RepositoryID, 10), strconv.FormatInt(snapshot.JobID, 10))
	if err != nil {
		return err
	}
	err = os.RemoveAll(workspace)
	return err
}

func (provider ArchiveSnapshotProvider) CleanupStale(ctx context.Context, active ActiveJobs) error {
	started := time.Now()
	var observedErr error
	defer func() { provider.observe("stale_cleanup", observedErr, started) }()
	repositories, err := os.ReadDir(provider.WorkspacesDir)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		observedErr = err
		return err
	}
	for _, repositoryEntry := range repositories {
		if err := ctx.Err(); err != nil {
			observedErr = err
			return err
		}
		repositoryID, ok := numericID(repositoryEntry.Name())
		if !ok || !repositoryEntry.IsDir() {
			continue
		}
		repositoryPath, _ := numericPath(provider.WorkspacesDir, repositoryEntry.Name())
		jobs, err := os.ReadDir(repositoryPath)
		if err != nil {
			observedErr = err
			return err
		}
		for _, jobEntry := range jobs {
			jobID, ok := numericID(jobEntry.Name())
			staging := false
			if !ok {
				jobID, ok = archiveStagingID(jobEntry.Name())
				staging = ok
			}
			if !ok || !jobEntry.IsDir() {
				continue
			}
			if _, keep := active[jobID]; keep {
				continue
			}
			var err error
			if staging {
				err = os.RemoveAll(filepath.Join(repositoryPath, jobEntry.Name()))
			} else {
				err = provider.Cleanup(ctx, Snapshot{RepositoryID: repositoryID, JobID: jobID})
			}
			if err != nil {
				observedErr = err
				return err
			}
		}
	}
	return nil
}

func archiveStagingID(name string) (int64, bool) {
	id, suffix, found := strings.Cut(strings.TrimPrefix(name, "."), "-")
	if !strings.HasPrefix(name, ".") || !found || suffix == "" {
		return 0, false
	}
	return numericID(id)
}

func (provider ArchiveSnapshotProvider) FreeSpacePath() string { return provider.WorkspacesDir }

func (provider ArchiveSnapshotProvider) observe(operation string, err error, started time.Time) {
	if provider.Metrics == nil {
		return
	}
	result := "success"
	if err != nil {
		result = "error"
	}
	provider.Metrics.ObserveArchive(operation, result, time.Since(started))
}

func (provider ArchiveSnapshotProvider) validate(request SnapshotRequest) error {
	limits := provider.Limits
	owner, name, found := strings.Cut(request.Repository.Name, "/")
	if provider.Client == nil || provider.WorkspacesDir == "" || request.RepositoryID <= 0 || request.JobID <= 0 || request.RepositoryID != request.Repository.ID || request.AccessToken == "" || !validSHA(request.CommitSHA) || !found || owner == "" || name == "" || strings.Contains(name, "/") || limits.MaxDownloadBytes <= 0 || limits.MaxExtractedBytes <= 0 || limits.MaxFileBytes <= 0 || limits.MaxFiles <= 0 || limits.MaxPathBytes <= 0 {
		return errors.New("invalid archive repository job")
	}
	return nil
}

func extractArchive(ctx context.Context, input io.Reader, destination string, limits ArchiveLimits) error {
	compressed := &io.LimitedReader{R: contextReader{ctx: ctx, reader: input}, N: limits.MaxDownloadBytes + 1}
	gz, err := gzip.NewReader(compressed)
	if err != nil {
		return fmt.Errorf("read archive gzip: %w", err)
	}
	defer gz.Close()
	reader := tar.NewReader(gz)
	root, err := os.OpenRoot(destination)
	if err != nil {
		return err
	}
	defer root.Close()
	var prefix string
	var extracted int64
	seen := make(map[string]struct{})
	entries := 0
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		header, err := reader.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return fmt.Errorf("read archive tar: %w", err)
		}
		if header.Typeflag == tar.TypeXGlobalHeader {
			continue
		}
		if !archivePathSafe(header.Name, limits.MaxPathBytes) {
			return errors.New("unsafe archive path")
		}
		entries++
		if entries > limits.MaxFiles {
			return errors.New("archive entry limit exceeded")
		}
		clean := path.Clean(header.Name)
		parts := strings.Split(clean, "/")
		if prefix == "" {
			prefix = parts[0]
		}
		if parts[0] != prefix {
			return errors.New("archive has multiple top-level paths")
		}
		relative := strings.Join(parts[1:], "/")
		if relative == "" {
			if header.Typeflag != tar.TypeDir {
				return errors.New("archive top-level entry is not a directory")
			}
			continue
		}
		if _, exists := seen[relative]; exists {
			return errors.New("duplicate archive path")
		}
		seen[relative] = struct{}{}
		target := filepath.FromSlash(relative)
		switch header.Typeflag {
		case tar.TypeDir:
			if err := root.MkdirAll(target, 0o700); err != nil {
				return err
			}
		case tar.TypeReg:
			if header.Size < 0 || header.Size > limits.MaxFileBytes || extracted > limits.MaxExtractedBytes-header.Size {
				return errors.New("archive content limit exceeded")
			}
			extracted += header.Size
			if parent := filepath.Dir(target); parent != "." {
				if err := root.MkdirAll(parent, 0o700); err != nil {
					return err
				}
			}
			mode := os.FileMode(0o644)
			if header.Mode&0o111 != 0 {
				mode = 0o755
			}
			file, err := root.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
			if err != nil {
				return err
			}
			copyErr := copyContextN(ctx, file, reader, header.Size)
			closeErr := file.Close()
			if copyErr != nil || closeErr != nil {
				return errors.Join(copyErr, closeErr)
			}
		default:
			return errors.New("unsupported archive entry type")
		}
	}
	if compressed.N <= 0 {
		return errors.New("archive download limit exceeded")
	}
	if prefix == "" {
		return errors.New("archive is empty")
	}
	return nil
}

func archivePathSafe(name string, maxBytes int) bool {
	clean := path.Clean(name)
	return len(name) <= maxBytes && !strings.ContainsAny(name, "\x00\\") && clean != "." && !path.IsAbs(clean) && clean != ".." && !strings.HasPrefix(clean, "../") && clean == strings.TrimSuffix(name, "/")
}

type contextReader struct {
	ctx    context.Context
	reader io.Reader
}

func (reader contextReader) Read(buffer []byte) (int, error) {
	if err := reader.ctx.Err(); err != nil {
		return 0, err
	}
	read, err := reader.reader.Read(buffer)
	if contextErr := reader.ctx.Err(); contextErr != nil {
		return read, contextErr
	}
	return read, err
}

func copyContextN(ctx context.Context, destination io.Writer, source io.Reader, remaining int64) error {
	buffer := make([]byte, 32<<10)
	for remaining > 0 {
		if err := ctx.Err(); err != nil {
			return err
		}
		chunk := int64(len(buffer))
		if remaining < chunk {
			chunk = remaining
		}
		read, err := io.ReadFull(source, buffer[:chunk])
		if err != nil {
			return err
		}
		written, err := destination.Write(buffer[:read])
		if err != nil {
			return err
		}
		if written != read {
			return io.ErrShortWrite
		}
		remaining -= int64(written)
	}
	return nil
}
