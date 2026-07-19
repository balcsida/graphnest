//go:build unix

package indexer

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"time"

	"github.com/grepnest/grepnest/internal/githubapp"
	"github.com/grepnest/grepnest/internal/postgres"
	"github.com/grepnest/grepnest/internal/repository"
	"golang.org/x/sys/unix"
)

type JobQueue interface {
	ClaimIndex(context.Context, string) (postgres.IndexJob, error)
	RenewLease(context.Context, int64, string) error
	CompleteIndex(context.Context, int64, string) error
	FailIndex(context.Context, int64, string, string, bool) error
	ActiveJobIDs(context.Context) (map[int64]struct{}, error)
}

type IndexStore interface {
	RepositoryForIndex(context.Context, int64) (repository.Repository, error)
	DesiredSHA(context.Context, int64) (string, error)
}

type TokenSource interface {
	InstallationToken(context.Context, int64, []int64) (githubapp.Token, error)
}

type GitWorkspace interface {
	Prepare(context.Context, repository.Repository, postgres.IndexJob, string) (string, string, error)
	Cleanup(context.Context, int64, int64) error
	Prune(context.Context, map[int64]struct{}) error
}

type IndexPublisher interface {
	Index(context.Context, repository.Repository, string) error
	WaitVisible(context.Context, uint32, string, string) error
}

type Worker struct {
	ID             string
	Queue          JobQueue
	Store          IndexStore
	Tokens         TokenSource
	Git            GitWorkspace
	Zoekt          IndexPublisher
	MinFreeBytes   uint64
	RenewEvery     time.Duration
	CleanupTimeout time.Duration
}

func (worker *Worker) Run(ctx context.Context) error {
	if err := worker.validate(); err != nil {
		return err
	}
	active, err := worker.Queue.ActiveJobIDs(ctx)
	if err != nil {
		return err
	}
	if err := worker.Git.Prune(ctx, active); err != nil {
		return err
	}
	for {
		worked, err := worker.RunOne(ctx)
		if err != nil {
			return err
		}
		if worked {
			continue
		}
		timer := time.NewTimer(250 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
}

func (worker *Worker) RunOne(ctx context.Context) (worked bool, resultErr error) {
	if err := worker.validate(); err != nil {
		return false, err
	}
	job, err := worker.Queue.ClaimIndex(ctx, worker.ID)
	if errors.Is(err, postgres.ErrNoJob) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	jobCtx, cancel := context.WithCancel(ctx)
	renewErrors := make(chan error, 1)
	renewDone := make(chan struct{})
	go worker.renew(jobCtx, job, cancel, renewErrors, renewDone)
	defer func() {
		cancel()
		<-renewDone
	}()

	fail := func(code string, retry bool) (bool, error) {
		if leaseErr := readError(renewErrors); leaseErr != nil {
			return true, leaseErr
		}
		if ctx.Err() != nil {
			return true, ctx.Err()
		}
		if err := worker.Queue.FailIndex(ctx, job.ID, worker.ID, code, retry); err != nil {
			return true, err
		}
		return true, nil
	}

	repo, err := worker.Store.RepositoryForIndex(jobCtx, job.RepositoryID)
	if err != nil {
		return fail("repository_failed", true)
	}
	defer func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.WithoutCancel(ctx), worker.cleanupTimeout())
		defer cleanupCancel()
		resultErr = errors.Join(resultErr, worker.Git.Cleanup(cleanupCtx, repo.ID, job.ID))
	}()
	token, err := worker.Tokens.InstallationToken(jobCtx, repo.InstallationID, []int64{repo.GitHubID})
	if err != nil {
		return fail("token_failed", true)
	}
	if enough, err := worker.enoughSpace(); err != nil || !enough {
		return fail("insufficient_space", true)
	}
	_, worktree, err := worker.Git.Prepare(jobCtx, repo, job, token.Value)
	if err != nil {
		if errors.Is(err, ErrTargetMissing) {
			return fail("target_missing", false)
		}
		return fail("git_failed", true)
	}
	desired, err := worker.Store.DesiredSHA(jobCtx, repo.ID)
	if err != nil {
		return fail("repository_failed", true)
	}
	if desired != job.TargetSHA {
		return fail("superseded", false)
	}
	if err := worker.Zoekt.Index(jobCtx, repo, worktree); err != nil {
		return fail("index_failed", true)
	}
	if err := worker.Zoekt.WaitVisible(jobCtx, repo.ZoektID, repo.Branch, job.TargetSHA); err != nil {
		return fail("visibility_failed", true)
	}
	if leaseErr := readError(renewErrors); leaseErr != nil {
		return true, leaseErr
	}
	if err := worker.Queue.CompleteIndex(ctx, job.ID, worker.ID); err != nil {
		return true, err
	}
	return true, nil
}

func (worker *Worker) renew(ctx context.Context, job postgres.IndexJob, cancel context.CancelFunc, result chan<- error, done chan<- struct{}) {
	defer close(done)
	interval := worker.RenewEvery
	if interval <= 0 {
		interval = 30 * time.Second
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := worker.Queue.RenewLease(ctx, job.ID, worker.ID); err != nil {
				result <- err
				cancel()
				return
			}
		}
	}
}

func (worker *Worker) validate() error {
	if worker == nil || worker.ID == "" || worker.Queue == nil || worker.Store == nil || worker.Tokens == nil || worker.Git == nil || worker.Zoekt == nil {
		return errors.New("invalid index worker")
	}
	return nil
}

func (worker *Worker) cleanupTimeout() time.Duration {
	if worker.CleanupTimeout > 0 {
		return worker.CleanupTimeout
	}
	return 30 * time.Second
}

func (worker *Worker) enoughSpace() (bool, error) {
	if worker.MinFreeBytes == 0 {
		return true, nil
	}
	path := "."
	if git, ok := worker.Git.(*Git); ok && git.WorktreesDir != "" {
		path = git.WorktreesDir
		for {
			if _, err := os.Stat(path); err == nil {
				break
			}
			parent := filepath.Dir(path)
			if parent == path {
				break
			}
			path = parent
		}
	}
	var stats unix.Statfs_t
	if err := unix.Statfs(path, &stats); err != nil {
		return false, err
	}
	return stats.Bavail*uint64(stats.Bsize) >= worker.MinFreeBytes, nil
}

func readError(errors <-chan error) error {
	select {
	case err := <-errors:
		return err
	default:
		return nil
	}
}
