//go:build unix

package indexer

import (
	"context"
	"errors"
	"math"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/grepnest/grepnest/internal/githubapp"
	"github.com/grepnest/grepnest/internal/observability"
	"github.com/grepnest/grepnest/internal/postgres"
	"github.com/grepnest/grepnest/internal/repository"
)

type fakeQueue struct {
	mu          sync.Mutex
	job         postgres.IndexJob
	claimErr    error
	events      []string
	renewErr    error
	renewed     chan struct{}
	completed   bool
	failedCode  string
	failedRetry bool
	active      map[int64]struct{}
}

func (queue *fakeQueue) record(event string) {
	queue.mu.Lock()
	queue.events = append(queue.events, event)
	queue.mu.Unlock()
}

func (queue *fakeQueue) ClaimIndex(context.Context, string) (postgres.IndexJob, error) {
	queue.record("claim")
	return queue.job, queue.claimErr
}
func (queue *fakeQueue) RenewLease(context.Context, int64, string) error {
	queue.record("renew")
	if queue.renewed != nil {
		select {
		case queue.renewed <- struct{}{}:
		default:
		}
	}
	return queue.renewErr
}
func (queue *fakeQueue) CompleteIndex(context.Context, int64, string) error {
	queue.record("complete")
	queue.completed = true
	return nil
}
func (queue *fakeQueue) FailIndex(_ context.Context, _ int64, _ string, code string, retry bool) error {
	queue.record("fail")
	queue.failedCode, queue.failedRetry = code, retry
	return nil
}
func (queue *fakeQueue) ActiveJobIDs(context.Context) (map[int64]struct{}, error) {
	queue.record("active")
	return queue.active, nil
}
func (queue *fakeQueue) QueueDepths(context.Context) (map[string]int64, error) {
	return map[string]int64{"queued": 1, "running": 2, "succeeded": 3, "failed": 4, "superseded": 5}, nil
}

type fakeStore struct {
	repo       repository.Repository
	desired    string
	desiredErr error
}

func (store *fakeStore) RepositoryForIndex(context.Context, int64) (repository.Repository, error) {
	return store.repo, nil
}
func (store *fakeStore) DesiredSHA(context.Context, int64) (string, error) {
	return store.desired, store.desiredErr
}

type fakeTokens struct {
	queue *fakeQueue
	err   error
}

func (tokens *fakeTokens) InstallationToken(context.Context, int64, []int64) (githubapp.Token, error) {
	tokens.queue.record("token")
	return githubapp.Token{Value: "token"}, tokens.err
}

type fakeGit struct {
	queue        *fakeQueue
	prepareErr   error
	prepared     bool
	cleaned      bool
	cleanupErr   error
	cleanupWait  bool
	cleanupRoot  error
	cleanupBound bool
	pruned       map[int64]struct{}
	prepareDone  chan struct{}
}

func (git *fakeGit) Prepare(context.Context, repository.Repository, postgres.IndexJob, string) (string, string, error) {
	git.queue.record("prepare")
	git.prepared = true
	if git.prepareDone != nil {
		close(git.prepareDone)
	}
	return "/mirror", "/worktree", git.prepareErr
}
func (git *fakeGit) Cleanup(ctx context.Context, _ int64, _ int64) error {
	git.queue.record("cleanup")
	git.cleaned = true
	git.cleanupRoot = ctx.Err()
	_, git.cleanupBound = ctx.Deadline()
	if git.cleanupWait {
		if !git.cleanupBound {
			return errors.New("cleanup context has no deadline")
		}
		<-ctx.Done()
		return ctx.Err()
	}
	return git.cleanupErr
}
func (git *fakeGit) Prune(_ context.Context, active map[int64]struct{}) error {
	git.queue.record("prune")
	git.pruned = active
	return nil
}

type fakePublisher struct {
	queue      *fakeQueue
	indexErr   error
	waitErr    error
	blockIndex bool
	started    chan struct{}
	cancelled  chan struct{}
	indexed    bool
}

func (publisher *fakePublisher) Index(ctx context.Context, _ repository.Repository, _ string) error {
	publisher.queue.record("index")
	publisher.indexed = true
	if publisher.started != nil {
		close(publisher.started)
	}
	if publisher.blockIndex {
		<-ctx.Done()
		close(publisher.cancelled)
		return ctx.Err()
	}
	return publisher.indexErr
}
func (publisher *fakePublisher) WaitVisible(context.Context, uint32, string, string) error {
	publisher.queue.record("visible")
	return publisher.waitErr
}

func workerFixture() (*Worker, *fakeQueue, *fakeStore, *fakeGit, *fakePublisher) {
	job := postgres.IndexJob{ID: 11, RepositoryID: 4, TargetSHA: gitTargetSHA}
	queue := &fakeQueue{job: job}
	store := &fakeStore{repo: repository.Repository{ID: 4, InstallationID: 5, GitHubID: 6, ZoektID: 7, Name: "acme/repo", Branch: "main", WebURL: "https://ghe.example/acme/repo"}, desired: gitTargetSHA}
	git := &fakeGit{queue: queue}
	publisher := &fakePublisher{queue: queue}
	worker := &Worker{ID: "worker-1", Queue: queue, Store: store, Tokens: &fakeTokens{queue: queue}, Git: git, Zoekt: publisher, RenewEvery: time.Hour}
	return worker, queue, store, git, publisher
}

func TestWorkerRunOneCompletesOnlyAfterExactVisibility(t *testing.T) {
	worker, queue, _, git, _ := workerFixture()
	worked, err := worker.RunOne(t.Context())
	if err != nil || !worked {
		t.Fatalf("worked = %v, error = %v", worked, err)
	}
	want := []string{"claim", "token", "prepare", "index", "visible", "complete", "cleanup"}
	if !slices.Equal(queue.events, want) {
		t.Fatalf("events = %v, want %v", queue.events, want)
	}
	if !queue.completed || !git.cleaned {
		t.Fatalf("completed=%v cleaned=%v", queue.completed, git.cleaned)
	}
}

func TestWorkerRecordsQueueAndPhaseMetrics(t *testing.T) {
	worker, _, _, _, _ := workerFixture()
	metrics := observability.New()
	worker.Metrics = metrics
	if worked, err := worker.RunOne(t.Context()); err != nil || !worked {
		t.Fatalf("worked = %v, error = %v", worked, err)
	}

	recorder := httptest.NewRecorder()
	metrics.Handler().ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	body := recorder.Body.String()
	for _, want := range []string{
		`grepnest_index_queue_depth{state="running"} 2`,
		`grepnest_index_phase_total{phase="fetch",result="success"} 1`,
		`grepnest_index_phase_total{phase="index",result="success"} 1`,
		`grepnest_index_phase_total{phase="visibility",result="success"} 1`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("metrics missing %q:\n%s", want, body)
		}
	}
}

func TestWorkerRunOneChecksSpaceBeforeGit(t *testing.T) {
	worker, queue, _, git, publisher := workerFixture()
	worker.MinFreeBytes = math.MaxUint64
	worked, err := worker.RunOne(t.Context())
	if err != nil || !worked {
		t.Fatalf("worked = %v, error = %v", worked, err)
	}
	if git.prepared || publisher.indexed || queue.failedCode != "insufficient_space" || !queue.failedRetry {
		t.Fatalf("prepared=%v indexed=%v failure=%q retry=%v", git.prepared, publisher.indexed, queue.failedCode, queue.failedRetry)
	}
}

func TestWorkerRunOneSupersedesChangedDesiredSHABeforeZoekt(t *testing.T) {
	worker, queue, store, git, publisher := workerFixture()
	store.desired = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	worked, err := worker.RunOne(t.Context())
	if err != nil || !worked {
		t.Fatalf("worked = %v, error = %v", worked, err)
	}
	if publisher.indexed || queue.failedCode != "superseded" || queue.failedRetry || !git.cleaned {
		t.Fatalf("indexed=%v failure=%q retry=%v cleaned=%v", publisher.indexed, queue.failedCode, queue.failedRetry, git.cleaned)
	}
}

func TestWorkerRunOneClassifiesPermanentAndRetryableFailures(t *testing.T) {
	for _, test := range []struct {
		name, code string
		err        error
		retry      bool
	}{
		{name: "missing target", code: "target_missing", err: ErrTargetMissing},
		{name: "index failure", code: "index_failed", err: errors.New("zoekt failed"), retry: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			worker, queue, _, git, publisher := workerFixture()
			if errors.Is(test.err, ErrTargetMissing) {
				git.prepareErr = test.err
			} else {
				publisher.indexErr = test.err
			}
			worked, err := worker.RunOne(t.Context())
			if err != nil || !worked {
				t.Fatalf("worked = %v, error = %v", worked, err)
			}
			if queue.failedCode != test.code || queue.failedRetry != test.retry || (git.prepared && !git.cleaned) {
				t.Fatalf("failure=%q retry=%v cleaned=%v", queue.failedCode, queue.failedRetry, git.cleaned)
			}
		})
	}
}

func TestWorkerLeaseLossCancelsIndexAndSkipsTransition(t *testing.T) {
	worker, queue, _, git, publisher := workerFixture()
	queue.renewErr = postgres.ErrLeaseLost
	queue.renewed = make(chan struct{}, 1)
	worker.RenewEvery = time.Millisecond
	publisher.blockIndex = true
	publisher.cancelled = make(chan struct{})
	worked, err := worker.RunOne(t.Context())
	if !worked || !errors.Is(err, postgres.ErrLeaseLost) {
		t.Fatalf("worked = %v, error = %v", worked, err)
	}
	select {
	case <-publisher.cancelled:
	default:
		t.Fatal("index context was not cancelled")
	}
	if queue.failedCode != "" || queue.completed || !git.cleaned {
		t.Fatalf("failure=%q completed=%v cleaned=%v", queue.failedCode, queue.completed, git.cleaned)
	}
}

func TestWorkerCleanupRunsAfterCancellationWithIndependentDeadline(t *testing.T) {
	worker, _, _, git, publisher := workerFixture()
	worker.CleanupTimeout = 5 * time.Millisecond
	git.cleanupWait = true
	publisher.blockIndex = true
	publisher.started = make(chan struct{})
	publisher.cancelled = make(chan struct{})
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct {
		worked bool
		err    error
	}, 1)
	go func() {
		worked, err := worker.RunOne(ctx)
		done <- struct {
			worked bool
			err    error
		}{worked, err}
	}()
	<-publisher.started
	cancel()
	result := <-done
	if !result.worked || !errors.Is(result.err, context.Canceled) || !errors.Is(result.err, context.DeadlineExceeded) {
		t.Fatalf("worked = %v, error = %v", result.worked, result.err)
	}
	if !git.cleaned || !git.cleanupBound || git.cleanupRoot != nil {
		t.Fatalf("cleaned=%v bounded=%v initial error=%v", git.cleaned, git.cleanupBound, git.cleanupRoot)
	}
}

func TestWorkerCleanupFailureIsJoinedWithPrimaryError(t *testing.T) {
	worker, queue, _, git, publisher := workerFixture()
	cleanupErr := errors.New("cleanup failed")
	git.cleanupErr = cleanupErr
	queue.renewErr = postgres.ErrLeaseLost
	worker.RenewEvery = time.Millisecond
	publisher.blockIndex = true
	publisher.cancelled = make(chan struct{})
	worked, err := worker.RunOne(t.Context())
	if !worked || !errors.Is(err, postgres.ErrLeaseLost) || !errors.Is(err, cleanupErr) {
		t.Fatalf("worked = %v, error = %v", worked, err)
	}
}

func TestWorkerRunPrunesActiveLeasesAndStopsIdlePollOnCancel(t *testing.T) {
	worker, queue, _, git, _ := workerFixture()
	queue.active = map[int64]struct{}{11: {}}
	queue.claimErr = postgres.ErrNoJob
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- worker.Run(ctx) }()
	for {
		queue.mu.Lock()
		claimed := slices.Contains(queue.events, "claim")
		queue.mu.Unlock()
		if claimed {
			break
		}
		time.Sleep(time.Millisecond)
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("error = %v", err)
		}
	case <-time.After(100 * time.Millisecond):
		t.Fatal("idle worker did not stop")
	}
	if _, ok := git.pruned[11]; !ok {
		t.Fatalf("pruned active IDs = %v", git.pruned)
	}
}
