//go:build unix

package indexer

import (
	"context"
	"errors"
	"os"
	"strconv"
	"time"

	"github.com/grepnest/grepnest/internal/repository"
	"github.com/grepnest/grepnest/internal/search"
	"github.com/grepnest/grepnest/internal/zoekt"
)

type ZoektIndexer struct {
	Binary, IndexDir  string
	Runner            Runner
	Client            *zoekt.Client
	IndexTimeout      time.Duration
	VisibilityTimeout time.Duration
}

func (indexer *ZoektIndexer) Index(ctx context.Context, repo repository.Repository, worktree string) error {
	if indexer == nil || indexer.Binary == "" || indexer.IndexDir == "" || worktree == "" || repo.ZoektID == 0 || repo.Branch == "" {
		return errors.New("invalid Zoekt indexing job")
	}
	arguments := []string{"-index", indexer.IndexDir, "-branches", repo.Branch, "-submodules=false", "-incremental=true", "-file_limit", "2097152", "-parallelism", "1", "-disable_ctags", worktree}
	environment := []string{"LANG=C", "LC_ALL=C", "PATH=" + os.Getenv("PATH"), "TMPDIR=" + os.TempDir()}
	timeout := indexer.IndexTimeout
	if timeout <= 0 {
		timeout = 10 * time.Minute
	}
	indexCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	return indexer.Runner.Run(indexCtx, indexer.Binary, arguments, environment, "")
}

func (indexer *ZoektIndexer) WaitVisible(ctx context.Context, repositoryID uint32, branch, version string) error {
	if indexer == nil || indexer.Client == nil || repositoryID == 0 || branch == "" || version == "" {
		return errors.New("invalid Zoekt visibility check")
	}
	timeout := indexer.VisibilityTimeout
	if timeout <= 0 {
		timeout = 2 * time.Minute
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	delay := 10 * time.Millisecond
	for {
		repositories, err := indexer.Client.List(ctx, repositoryID)
		if err != nil {
			return err
		}
		for _, repo := range repositories {
			if repo.RepoID == repositoryID && repo.Branch == branch && repo.Version == version {
				_, err := indexer.Client.Search(ctx, search.BackendRequest{
					Query: "repoid:" + strconv.FormatUint(uint64(repositoryID), 10), RepositoryIDs: []uint32{repositoryID}, Limit: 1, Timeout: time.Second,
				})
				return err
			}
		}
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
		if delay < 250*time.Millisecond {
			delay *= 2
		}
	}
}
