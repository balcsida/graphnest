//go:build unix

package indexer

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"time"

	"github.com/balcsida/graphnest/internal/githubapp"
	"github.com/balcsida/graphnest/internal/observability"
	"github.com/balcsida/graphnest/internal/postgres"
	"github.com/balcsida/graphnest/internal/repository"
	"golang.org/x/sys/unix"
)

type EnrichmentStatus = postgres.EnrichmentStatus

type JobQueue interface {
	ClaimIndex(context.Context, string) (postgres.IndexJob, error)
	RenewLease(context.Context, int64, string) error
	PublishIndex(context.Context, int64, string) error
	CompleteIndex(context.Context, int64, string, ...postgres.EnrichmentStatus) error
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

type SnapshotRequest struct {
	RepositoryID int64
	Repository   repository.Repository
	JobID        int64
	CommitSHA    string
	AccessToken  string
}
type Snapshot struct {
	Root         string
	RepositoryID int64
	JobID        int64
	CommitSHA    string
}
type ActiveJobs map[int64]struct{}
type SnapshotProvider interface {
	Prepare(context.Context, SnapshotRequest) (Snapshot, error)
	Cleanup(context.Context, Snapshot) error
	CleanupStale(context.Context, ActiveJobs) error
	FreeSpacePath() string
}

type IndexPublisher interface {
	Index(context.Context, repository.Repository, string) error
	WaitVisible(context.Context, uint32, string, string) error
}

type Enricher interface {
	Enrich(context.Context, Snapshot, repository.Repository, string) (EnrichmentStatus, error)
}

type Worker struct {
	ID                 string
	Queue              JobQueue
	Store              IndexStore
	Tokens             TokenSource
	Snapshots          SnapshotProvider
	Zoekt              IndexPublisher
	Enricher           Enricher
	MinFreeBytes       uint64
	MaxRepositoryBytes int64
	RenewEvery         time.Duration
	ReapEvery          time.Duration
	CleanupTimeout     time.Duration
	Metrics            *observability.Metrics
	lastReap           time.Time
}

func (worker *Worker) Run(ctx context.Context) error {
	if err := worker.validate(); err != nil {
		return err
	}
	active, err := worker.Queue.ActiveJobIDs(ctx)
	if err != nil {
		return err
	}
	if err := worker.Snapshots.CleanupStale(ctx, ActiveJobs(active)); err != nil {
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
	if RepositoryTooLarge(repo.SizeBytes, worker.MaxRepositoryBytes) {
		return fail("repository_too_large", false)
	}
	desired, err := worker.Store.DesiredSHA(jobCtx, repo.ID)
	if err != nil {
		return fail("repository_failed", true)
	}
	if desired != job.TargetSHA {
		return fail("superseded", false)
	}
	token, err := worker.Tokens.InstallationToken(jobCtx, repo.InstallationID, []int64{repo.GitHubID})
	if err != nil {
		return fail("token_failed", true)
	}
	if enough, err := worker.enoughSpace(); err != nil || !enough {
		return fail("insufficient_space", true)
	}
	started := time.Now()
	snapshot, err := worker.Snapshots.Prepare(jobCtx, SnapshotRequest{RepositoryID: job.RepositoryID, Repository: repo, JobID: job.ID, CommitSHA: job.TargetSHA, AccessToken: token.Value})
	defer func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.WithoutCancel(ctx), worker.cleanupTimeout())
		defer cleanupCancel()
		if cleanupErr := worker.Snapshots.Cleanup(cleanupCtx, snapshot); cleanupErr != nil {
			resultErr = errors.Join(resultErr, cleanupErr)
		}
	}()
	worker.observePhase("fetch", started, err)
	if err != nil {
		if errors.Is(err, ErrTargetMissing) {
			return fail("target_missing", false)
		}
		return fail("git_failed", true)
	}
	desired, err = worker.Store.DesiredSHA(jobCtx, repo.ID)
	if err != nil {
		return fail("repository_failed", true)
	}
	if desired != job.TargetSHA {
		return fail("superseded", false)
	}
	started = time.Now()
	if err := worker.Zoekt.Index(jobCtx, repo, snapshot.Root); err != nil {
		worker.observePhase("index", started, err)
		return fail("index_failed", true)
	}
	worker.observePhase("index", started, nil)
	started = time.Now()
	if err := worker.Zoekt.WaitVisible(jobCtx, repo.ZoektID, repo.Branch, job.TargetSHA); err != nil {
		worker.observePhase("visibility", started, err)
		return fail("visibility_failed", true)
	}
	worker.observePhase("visibility", started, nil)
	if leaseErr := readError(renewErrors); leaseErr != nil {
		return true, leaseErr
	}
	if err := worker.Queue.PublishIndex(jobCtx, job.ID, worker.ID); err != nil {
		return fail("publish_failed", true)
	}
	status := EnrichmentStatus{ErrorCode: "enrichment_disabled"}
	if worker.Enricher != nil {
		started := time.Now()
		status, err = worker.Enricher.Enrich(jobCtx, snapshot, repo, job.TargetSHA)
		worker.observePhase("enrichment", started, err)
		if err != nil {
			status.ErrorCode = "enrichment_failed"
			if errors.Is(err, context.DeadlineExceeded) {
				status.ErrorCode = "enrichment_timeout"
			}
		}
	}
	if err := worker.Queue.CompleteIndex(ctx, job.ID, worker.ID, status); err != nil {
		return true, err
	}
	return true, nil
}

func (worker *Worker) reapExpired(ctx context.Context) error {
	reaper, ok := worker.Queue.(interface {
		ReapExpired(context.Context, int) (int64, error)
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
	if _, err := reaper.ReapExpired(ctx, 1000); err != nil {
		return err
	}
	worker.lastReap = time.Now()
	return nil
}

func (worker *Worker) refreshQueueDepths(ctx context.Context) {
	queue, ok := worker.Queue.(interface {
		QueueDepths(context.Context) (map[string]int64, error)
	})
	if worker.Metrics == nil || !ok {
		return
	}
	depths, err := queue.QueueDepths(ctx)
	if err != nil {
		return
	}
	for _, state := range []string{"queued", "running", "succeeded", "failed", "superseded"} {
		worker.Metrics.SetQueueDepth(state, depths[state])
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
	worker.Metrics.ObserveIndexPhase(phase, result, time.Since(started))
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
	if worker == nil || worker.ID == "" || worker.Queue == nil || worker.Store == nil || worker.Tokens == nil || worker.Snapshots == nil || worker.Zoekt == nil {
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
	return EnoughFreeSpace(worker.Snapshots.FreeSpacePath(), worker.MinFreeBytes)
}

func RepositoryTooLarge(sizeBytes, maxBytes int64) bool {
	return maxBytes > 0 && sizeBytes > maxBytes
}

func EnoughFreeSpace(path string, minBytes uint64) (bool, error) {
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
	var stats unix.Statfs_t
	if err := unix.Statfs(path, &stats); err != nil {
		return false, err
	}
	return stats.Bavail*uint64(stats.Bsize) >= minBytes, nil
}

func readError(errors <-chan error) error {
	select {
	case err := <-errors:
		return err
	default:
		return nil
	}
}
