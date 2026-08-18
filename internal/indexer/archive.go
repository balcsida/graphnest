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

	"github.com/grepnest/grepnest/internal/githubapp"
	"github.com/grepnest/grepnest/internal/observability"
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
	provider.observe("download", err, started)
	if err != nil {
		var status githubapp.HTTPStatusError
		if errors.As(err, &status) && status.StatusCode == 404 {
			return snapshot, errors.Join(ErrTargetMissing, err)
		}
		return snapshot, err
	}
	defer body.Close()
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
			_ = os.RemoveAll(staging)
		}
	}()
	started = time.Now()
	err = extractArchive(body, staging, provider.Limits)
	provider.observe("extract", err, started)
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

func extractArchive(input io.Reader, destination string, limits ArchiveLimits) error {
	compressed := &io.LimitedReader{R: input, N: limits.MaxDownloadBytes + 1}
	gz, err := gzip.NewReader(compressed)
	if err != nil {
		return fmt.Errorf("read archive gzip: %w", err)
	}
	defer gz.Close()
	reader := tar.NewReader(gz)
	var prefix string
	var extracted int64
	seen := make(map[string]struct{})
	files := 0
	for {
		header, err := reader.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return fmt.Errorf("read archive tar: %w", err)
		}
		if len(header.Name) > limits.MaxPathBytes || strings.ContainsAny(header.Name, "\x00\\") {
			return errors.New("archive entry limit exceeded")
		}
		clean := path.Clean(header.Name)
		if clean == "." || path.IsAbs(clean) || clean == ".." || strings.HasPrefix(clean, "../") || clean != strings.TrimSuffix(header.Name, "/") {
			return errors.New("unsafe archive path")
		}
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
		target := filepath.Join(destination, filepath.FromSlash(relative))
		switch header.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0o700); err != nil {
				return err
			}
		case tar.TypeReg:
			files++
			if files > limits.MaxFiles {
				return errors.New("archive file limit exceeded")
			}
			if header.Size < 0 || header.Size > limits.MaxFileBytes || extracted > limits.MaxExtractedBytes-header.Size {
				return errors.New("archive content limit exceeded")
			}
			extracted += header.Size
			if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
				return err
			}
			mode := os.FileMode(0o644)
			if header.Mode&0o111 != 0 {
				mode = 0o755
			}
			file, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
			if err != nil {
				return err
			}
			_, copyErr := io.CopyN(file, reader, header.Size)
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
