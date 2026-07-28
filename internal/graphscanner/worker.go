package graphscanner

import (
	"context"
	"errors"
	"time"

	"github.com/grepnest/grepnest/internal/githubapp"
	"github.com/grepnest/grepnest/internal/graphartifact"
	"github.com/grepnest/grepnest/internal/graphscan"
	"github.com/grepnest/grepnest/internal/observability"
	"github.com/grepnest/grepnest/internal/postgres"
	"github.com/grepnest/grepnest/internal/repository"
)

type Queue interface {
	ClaimGraph(context.Context, string) (postgres.GraphJob, error)
	RenewGraphLease(context.Context, int64, string) error
	CompleteGraph(context.Context, int64, string, graphartifact.Artifact) error
	FailGraph(context.Context, int64, string, string, bool) error
}

type Store interface {
	RepositoryForIndex(context.Context, int64) (repository.Repository, error)
}

type TokenSource interface {
	InstallationToken(context.Context, int64, []int64) (githubapp.Token, error)
}

type GitWorkspace interface {
	PrepareCommit(context.Context, repository.Repository, int64, string, string) (string, string, error)
	Cleanup(context.Context, int64, int64) error
}

type Analyzer interface {
	Scan(context.Context, graphscan.Request) (graphartifact.Artifact, error)
}

type Worker struct {
	ID             string
	Queue          Queue
	Store          Store
	Tokens         TokenSource
	Git            GitWorkspace
	Analyzer       Analyzer
	RenewEvery     time.Duration
	ReapEvery      time.Duration
	CleanupTimeout time.Duration
	Metrics        *observability.Metrics
	lastReap       time.Time
}

func (worker *Worker) Run(ctx context.Context) error {
	if err := worker.validate(); err != nil {
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
	if err := worker.reapExpired(ctx); err != nil {
		return false, err
	}
	worker.refreshQueueDepths(ctx)
	job, err := worker.Queue.ClaimGraph(ctx, worker.ID)
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
		if leaseErr := receive(renewErrors); leaseErr != nil {
			return true, leaseErr
		}
		if err := ctx.Err(); err != nil {
			return true, err
		}
		if err := worker.Queue.FailGraph(ctx, job.ID, worker.ID, code, retry); err != nil {
			return true, err
		}
		return true, nil
	}

	repo, err := worker.Store.RepositoryForIndex(jobCtx, job.RepositoryID)
	if err != nil || repo.ID != job.RepositoryID {
		return fail("repository_failed", true)
	}
	if repo.IndexedSHA != job.TargetSHA {
		return fail("superseded", false)
	}
	defer func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.WithoutCancel(ctx), worker.cleanupTimeout())
		defer cleanupCancel()
		resultErr = errors.Join(resultErr, worker.Git.Cleanup(cleanupCtx, repo.ID, job.ID))
	}()

	started := time.Now()
	token, err := worker.Tokens.InstallationToken(jobCtx, repo.InstallationID, []int64{repo.GitHubID})
	worker.observePhase("token", started, err)
	if err != nil {
		return fail("token_failed", true)
	}
	started = time.Now()
	_, root, err := worker.Git.PrepareCommit(jobCtx, repo, job.ID, job.TargetSHA, token.Value)
	worker.observePhase("checkout", started, err)
	if err != nil {
		return fail("git_failed", true)
	}
	started = time.Now()
	artifact, err := worker.Analyzer.Scan(jobCtx, graphscan.Request{
		RepositoryID: job.RepositoryID,
		Commit:       job.TargetSHA,
		Root:         root,
	})
	worker.observePhase("scan", started, err)
	if errors.Is(err, graphscan.ErrLimitExceeded) {
		return fail("scan_limit", false)
	}
	if err != nil || artifact.RepositoryID != job.RepositoryID || artifact.Commit != job.TargetSHA {
		return fail("scan_failed", true)
	}
	current, err := worker.Store.RepositoryForIndex(jobCtx, job.RepositoryID)
	if err != nil || current.ID != job.RepositoryID {
		return fail("repository_failed", true)
	}
	if current.IndexedSHA != job.TargetSHA {
		return fail("superseded", false)
	}
	if leaseErr := receive(renewErrors); leaseErr != nil {
		return true, leaseErr
	}
	started = time.Now()
	err = worker.Queue.CompleteGraph(ctx, job.ID, worker.ID, artifact)
	worker.observePhase("publish", started, err)
	if err != nil {
		if errors.Is(err, postgres.ErrLeaseLost) {
			return true, err
		}
		return fail("publish_failed", true)
	}
	return true, nil
}

func (worker *Worker) refreshQueueDepths(ctx context.Context) {
	queue, ok := worker.Queue.(interface {
		GraphQueueDepths(context.Context) (map[string]int64, error)
	})
	if worker.Metrics == nil || !ok {
		return
	}
	depths, err := queue.GraphQueueDepths(ctx)
	if err != nil {
		return
	}
	for _, state := range []string{"queued", "running", "succeeded", "failed", "superseded"} {
		worker.Metrics.SetGraphQueueDepth(state, depths[state])
	}
}

func (worker *Worker) observePhase(phase string, started time.Time, err error) {
	if worker.Metrics == nil {
		return
	}
	result := "success"
	if err != nil {
		result = "error"
	}
	worker.Metrics.ObserveGraphPhase(phase, result, time.Since(started))
}

func (worker *Worker) reapExpired(ctx context.Context) error {
	reaper, ok := worker.Queue.(interface {
		ReapExpiredGraph(context.Context, int) (int64, error)
	})
	if !ok {
		return nil
	}
	interval := worker.ReapEvery
	if interval <= 0 {
		interval = 30 * time.Second
	}
	if !worker.lastReap.IsZero() && time.Since(worker.lastReap) < interval {
		return nil
	}
	if _, err := reaper.ReapExpiredGraph(ctx, 1000); err != nil {
		return err
	}
	worker.lastReap = time.Now()
	return nil
}

func (worker *Worker) renew(ctx context.Context, job postgres.GraphJob, cancel context.CancelFunc, result chan<- error, done chan<- struct{}) {
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
			if err := worker.Queue.RenewGraphLease(ctx, job.ID, worker.ID); err != nil {
				result <- err
				cancel()
				return
			}
		}
	}
}

func (worker *Worker) validate() error {
	if worker == nil || worker.ID == "" || worker.Queue == nil || worker.Store == nil || worker.Tokens == nil || worker.Git == nil || worker.Analyzer == nil {
		return errors.New("invalid graph scanner worker")
	}
	return nil
}

func (worker *Worker) cleanupTimeout() time.Duration {
	if worker.CleanupTimeout > 0 {
		return worker.CleanupTimeout
	}
	return 30 * time.Second
}

func receive(errors <-chan error) error {
	select {
	case err := <-errors:
		return err
	default:
		return nil
	}
}
