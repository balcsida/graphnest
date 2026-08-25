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

	"github.com/balcsida/graphnest/internal/githubapp"
	"github.com/balcsida/graphnest/internal/graphartifact"
	"github.com/balcsida/graphnest/internal/observability"
	"github.com/balcsida/graphnest/internal/postgres"
	"github.com/balcsida/graphnest/internal/repository"
)

type fakeQueue struct {
	mu          sync.Mutex
	job         postgres.IndexJob
	claimErr    error
	events      []string
	renewErr    error
	renewed     chan struct{}
	completed   bool
	published   bool
	enrichment  postgres.EnrichmentStatus
	failedCode  string
	failedRetry bool
	active      map[int64]struct{}
}

type reapingQueue struct {
	*fakeQueue
	reaped  int
	reapErr error
}

func (queue *reapingQueue) ReapExpired(context.Context, int) (int64, error) {
	queue.reaped++
	return 0, queue.reapErr
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

func (queue *fakeQueue) PublishIndex(context.Context, int64, string) error {
	queue.record("publish")
	queue.published = true
	return nil
}
func (queue *fakeQueue) CompleteIndex(_ context.Context, _ int64, _ string, enrichment ...postgres.EnrichmentStatus) error {
	queue.record("complete")
	queue.completed = true
	if len(enrichment) > 0 {
		queue.enrichment = enrichment[0]
	}
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
	desiredSeq []string
	desiredN   int
	desiredErr error
}

func (store *fakeStore) RepositoryForIndex(context.Context, int64) (repository.Repository, error) {
	return store.repo, nil
}
func (store *fakeStore) DesiredSHA(context.Context, int64) (string, error) {
	store.desiredN++
	if len(store.desiredSeq) >= store.desiredN {
		return store.desiredSeq[store.desiredN-1], store.desiredErr
	}
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

type fakeSnapshots struct {
	queue        *fakeQueue
	prepareErr   error
	prepared     bool
	request      SnapshotRequest
	snapshot     Snapshot
	zeroSnapshot bool
	cleaned      bool
	cleanedValue Snapshot
	cleanupErr   error
	cleanupWait  bool
	cleanupRoot  error
	cleanupBound bool
	pruned       ActiveJobs
	freeSpace    string
	spaceAsked   bool
	prepareDone  chan struct{}
}

func (provider *fakeSnapshots) Prepare(_ context.Context, request SnapshotRequest) (Snapshot, error) {
	provider.queue.record("prepare")
	provider.prepared = true
	provider.request = request
	if provider.snapshot.RepositoryID == 0 && !provider.zeroSnapshot {
		provider.snapshot = Snapshot{Root: "/snapshot", RepositoryID: request.Repository.ID, JobID: request.JobID, CommitSHA: request.CommitSHA}
	}
	if provider.prepareDone != nil {
		close(provider.prepareDone)
	}
	return provider.snapshot, provider.prepareErr
}
func (provider *fakeSnapshots) Cleanup(ctx context.Context, snapshot Snapshot) error {
	provider.queue.record("cleanup")
	provider.cleaned = true
	provider.cleanedValue = snapshot
	provider.cleanupRoot = ctx.Err()
	_, provider.cleanupBound = ctx.Deadline()
	if provider.cleanupWait {
		if !provider.cleanupBound {
			return errors.New("cleanup context has no deadline")
		}
		<-ctx.Done()
		return ctx.Err()
	}
	return provider.cleanupErr
}
func (provider *fakeSnapshots) CleanupStale(_ context.Context, active ActiveJobs) error {
	provider.queue.record("prune")
	provider.pruned = active
	return nil
}
func (provider *fakeSnapshots) FreeSpacePath() string {
	provider.spaceAsked = true
	return provider.freeSpace
}

type fakePublisher struct {
	queue      *fakeQueue
	indexErr   error
	waitErr    error
	blockIndex bool
	started    chan struct{}
	cancelled  chan struct{}
	indexed    bool
	root       string
}

type fakeEnricher struct {
	queue    *fakeQueue
	status   EnrichmentStatus
	err      error
	snapshot Snapshot
	started  chan struct{}
	release  chan struct{}
}

func (enricher *fakeEnricher) Enrich(_ context.Context, snapshot Snapshot, _ repository.Repository, _ string) (EnrichmentStatus, error) {
	enricher.queue.record("enrich")
	enricher.snapshot = snapshot
	if enricher.started != nil {
		close(enricher.started)
		<-enricher.release
	}
	return enricher.status, enricher.err
}

func (publisher *fakePublisher) Index(ctx context.Context, _ repository.Repository, root string) error {
	publisher.queue.record("index")
	publisher.indexed = true
	publisher.root = root
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

func workerFixture() (*Worker, *fakeQueue, *fakeStore, *fakeSnapshots, *fakePublisher) {
	job := postgres.IndexJob{ID: 11, RepositoryID: 4, TargetSHA: gitTargetSHA}
	queue := &fakeQueue{job: job}
	store := &fakeStore{repo: repository.Repository{ID: 4, InstallationID: 5, GitHubID: 6, ZoektID: 7, Name: "acme/repo", Branch: "main", WebURL: "https://ghe.example/acme/repo"}, desired: gitTargetSHA}
	provider := &fakeSnapshots{queue: queue, freeSpace: "."}
	publisher := &fakePublisher{queue: queue}
	worker := &Worker{ID: "worker-1", Queue: queue, Store: store, Tokens: &fakeTokens{queue: queue}, Snapshots: provider, Zoekt: publisher, RenewEvery: time.Hour}
	return worker, queue, store, provider, publisher
}

func TestWorkerRunOnePropagatesExactSnapshotAndCompletesAfterVisibility(t *testing.T) {
	worker, queue, store, provider, publisher := workerFixture()
	worked, err := worker.RunOne(t.Context())
	if err != nil || !worked {
		t.Fatalf("worked = %v, error = %v", worked, err)
	}
	want := []string{"claim", "token", "prepare", "index", "visible", "publish", "complete", "cleanup"}
	if !slices.Equal(queue.events, want) {
		t.Fatalf("events = %v, want %v", queue.events, want)
	}
	request := SnapshotRequest{RepositoryID: queue.job.RepositoryID, Repository: store.repo, JobID: queue.job.ID, CommitSHA: queue.job.TargetSHA, AccessToken: "token"}
	if provider.request != request || publisher.root != "/snapshot" || provider.cleanedValue != provider.snapshot {
		t.Fatalf("request=%+v root=%q cleanup=%+v", provider.request, publisher.root, provider.cleanedValue)
	}
	if !queue.published || !queue.completed || !provider.cleaned {
		t.Fatalf("published=%v completed=%v cleaned=%v", queue.published, queue.completed, provider.cleaned)
	}
}

func TestWorkerEnrichesPublishedIndexBeforeSnapshotCleanup(t *testing.T) {
	worker, queue, _, provider, _ := workerFixture()
	artifact := graphartifact.Artifact{RepositoryID: queue.job.RepositoryID, Commit: queue.job.TargetSHA}
	enricher := &fakeEnricher{queue: queue, status: EnrichmentStatus{Artifact: &artifact}}
	worker.Enricher = enricher
	worked, err := worker.RunOne(t.Context())
	if err != nil || !worked {
		t.Fatalf("RunOne() = %v, %v", worked, err)
	}
	want := []string{"claim", "token", "prepare", "index", "visible", "publish", "enrich", "complete", "cleanup"}
	if !slices.Equal(queue.events, want) || enricher.snapshot != provider.snapshot || queue.enrichment.Artifact != &artifact {
		t.Fatalf("events=%v snapshot=%+v status=%+v", queue.events, enricher.snapshot, queue.enrichment)
	}
}

func TestWorkerRecordsEnrichmentFailureWithoutFailingSearch(t *testing.T) {
	worker, queue, _, _, _ := workerFixture()
	worker.Enricher = &fakeEnricher{queue: queue, err: context.DeadlineExceeded}
	worked, err := worker.RunOne(t.Context())
	if err != nil || !worked || queue.failedCode != "" || queue.enrichment.ErrorCode != "enrichment_timeout" {
		t.Fatalf("RunOne()=%v,%v failed=%q enrichment=%+v", worked, err, queue.failedCode, queue.enrichment)
	}
}

func TestWorkerRenewsIndexLeaseDuringEnrichment(t *testing.T) {
	worker, queue, _, _, _ := workerFixture()
	queue.renewed = make(chan struct{}, 1)
	started, release := make(chan struct{}), make(chan struct{})
	worker.Enricher = &fakeEnricher{queue: queue, started: started, release: release}
	worker.RenewEvery = time.Millisecond
	done := make(chan error, 1)
	go func() { _, err := worker.RunOne(t.Context()); done <- err }()
	<-started
	select {
	case <-queue.renewed:
	case <-time.After(time.Second):
		t.Fatal("lease was not renewed during enrichment")
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestWorkerReapsExpiredLeasesBeforeClaimAtBoundedInterval(t *testing.T) {
	worker, queue, _, _, _ := workerFixture()
	reaper := &reapingQueue{fakeQueue: queue}
	worker.Queue = reaper
	worker.ReapEvery = time.Hour
	queue.claimErr = postgres.ErrNoJob

	for range 2 {
		if worked, err := worker.RunOne(t.Context()); err != nil || worked {
			t.Fatalf("worked=%v err=%v", worked, err)
		}
	}
	if reaper.reaped != 1 {
		t.Fatalf("reaps=%d", reaper.reaped)
	}

	want := errors.New("reaper failed")
	reaper.reapErr = want
	worker.lastReap = time.Time{}
	if worked, err := worker.RunOne(t.Context()); worked || !errors.Is(err, want) {
		t.Fatalf("worked=%v err=%v", worked, err)
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

func TestWorkerRecordsEachPhaseTerminalResultOnce(t *testing.T) {
	for _, test := range []struct {
		name      string
		configure func(*fakeSnapshots, *fakePublisher)
		want      []string
		absent    []string
		failed    string
		retry     bool
		completed bool
	}{
		{name: "success", want: []string{`phase="fetch",result="success"`, `phase="index",result="success"`, `phase="visibility",result="success"`}, completed: true},
		{name: "fetch error", configure: func(provider *fakeSnapshots, _ *fakePublisher) { provider.prepareErr = ErrTargetMissing }, want: []string{`phase="fetch",result="error"`}, absent: []string{`phase="index"`, `phase="visibility"`}, failed: "target_missing"},
		{name: "index retry", configure: func(_ *fakeSnapshots, publisher *fakePublisher) { publisher.indexErr = errors.New("index failed") }, want: []string{`phase="fetch",result="success"`, `phase="index",result="error"`}, absent: []string{`phase="visibility"`}, failed: "index_failed", retry: true},
		{name: "visibility retry", configure: func(_ *fakeSnapshots, publisher *fakePublisher) { publisher.waitErr = errors.New("visibility failed") }, want: []string{`phase="fetch",result="success"`, `phase="index",result="success"`, `phase="visibility",result="error"`}, failed: "visibility_failed", retry: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			worker, queue, _, git, publisher := workerFixture()
			metrics := observability.New()
			worker.Metrics = metrics
			if test.configure != nil {
				test.configure(git, publisher)
			}
			if worked, err := worker.RunOne(t.Context()); err != nil || !worked {
				t.Fatalf("worked = %v, error = %v", worked, err)
			}

			body := scrapeWorkerMetrics(t, metrics)
			for _, labels := range test.want {
				want := "grepnest_index_phase_total{" + labels + "} 1"
				if strings.Count(body, want) != 1 {
					t.Errorf("metric %q not recorded exactly once:\n%s", want, body)
				}
			}
			for _, labels := range test.absent {
				if strings.Contains(body, labels) {
					t.Errorf("unexpected phase %q:\n%s", labels, body)
				}
			}
			if queue.failedCode != test.failed || queue.failedRetry != test.retry || queue.completed != test.completed {
				t.Errorf("failed=%q retry=%v completed=%v", queue.failedCode, queue.failedRetry, queue.completed)
			}
		})
	}
}

func scrapeWorkerMetrics(t *testing.T, metrics *observability.Metrics) string {
	t.Helper()
	recorder := httptest.NewRecorder()
	metrics.Handler().ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	return recorder.Body.String()
}

func TestWorkerRunOneChecksSpaceBeforeGit(t *testing.T) {
	worker, queue, _, provider, publisher := workerFixture()
	worker.MinFreeBytes = math.MaxUint64
	worked, err := worker.RunOne(t.Context())
	if err != nil || !worked {
		t.Fatalf("worked = %v, error = %v", worked, err)
	}
	if !provider.spaceAsked || provider.prepared || publisher.indexed || queue.failedCode != "insufficient_space" || !queue.failedRetry {
		t.Fatalf("spaceAsked=%v prepared=%v indexed=%v failure=%q retry=%v", provider.spaceAsked, provider.prepared, publisher.indexed, queue.failedCode, queue.failedRetry)
	}
}

func TestWorkerSupersedesChangedDesiredSHABeforeExpensiveWork(t *testing.T) {
	worker, queue, store, provider, publisher := workerFixture()
	store.desired = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	worked, err := worker.RunOne(t.Context())
	if err != nil || !worked {
		t.Fatalf("worked = %v, error = %v", worked, err)
	}
	if store.desiredN != 1 || slices.Contains(queue.events, "token") || provider.prepared || publisher.indexed || queue.failedCode != "superseded" || queue.failedRetry {
		t.Fatalf("desired=%d events=%v prepared=%v indexed=%v failure=%q retry=%v", store.desiredN, queue.events, provider.prepared, publisher.indexed, queue.failedCode, queue.failedRetry)
	}
}

func TestWorkerRejectsOversizedRepositoryBeforeCredentials(t *testing.T) {
	worker, queue, store, git, publisher := workerFixture()
	store.repo.SizeBytes = 101
	worker.MaxRepositoryBytes = 100
	worked, err := worker.RunOne(t.Context())
	if err != nil || !worked {
		t.Fatalf("worked = %v, error = %v", worked, err)
	}
	if slices.Contains(queue.events, "token") || git.prepared || publisher.indexed || queue.failedCode != "repository_too_large" || queue.failedRetry {
		t.Fatalf("events=%v prepared=%v indexed=%v failure=%q retry=%v", queue.events, git.prepared, publisher.indexed, queue.failedCode, queue.failedRetry)
	}
}

func TestWorkerRunOneRechecksDesiredSHABeforePublication(t *testing.T) {
	worker, queue, store, provider, publisher := workerFixture()
	store.desiredSeq = []string{gitTargetSHA, "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"}
	worked, err := worker.RunOne(t.Context())
	if err != nil || !worked {
		t.Fatalf("worked = %v, error = %v", worked, err)
	}
	if store.desiredN != 2 || publisher.indexed || queue.failedCode != "superseded" || queue.failedRetry || !provider.cleaned {
		t.Fatalf("desired=%d indexed=%v failure=%q retry=%v cleaned=%v", store.desiredN, publisher.indexed, queue.failedCode, queue.failedRetry, provider.cleaned)
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
	metrics := observability.New()
	worker.Metrics = metrics
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
	body := scrapeWorkerMetrics(t, metrics)
	for _, want := range []string{
		`grepnest_index_phase_total{phase="fetch",result="success"} 1`,
		`grepnest_index_phase_total{phase="index",result="error"} 1`,
	} {
		if strings.Count(body, want) != 1 {
			t.Errorf("metric %q not recorded exactly once:\n%s", want, body)
		}
	}
	if strings.Contains(body, `phase="visibility"`) || strings.Contains(body, `phase="index",result="success"`) {
		t.Fatalf("lease loss double-counted phases:\n%s", body)
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

func TestWorkerSuccessfulCleanupPreservesCancellationIdentity(t *testing.T) {
	worker, _, _, git, publisher := workerFixture()
	publisher.blockIndex = true
	publisher.started = make(chan struct{})
	publisher.cancelled = make(chan struct{})
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { _, err := worker.RunOne(ctx); done <- err }()
	<-publisher.started
	cancel()
	if err := <-done; err != context.Canceled {
		t.Fatalf("error identity = %T %v", err, err)
	}
	if !git.cleaned {
		t.Fatal("worktree was not cleaned")
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

func TestWorkerCleansZeroSnapshotAndJoinsErrorAfterPrepareFailure(t *testing.T) {
	worker, _, _, provider, _ := workerFixture()
	prepareErr := errors.New("prepare failed")
	cleanupErr := errors.New("cleanup failed")
	provider.prepareErr = prepareErr
	provider.cleanupErr = cleanupErr
	provider.zeroSnapshot = true
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	worked, err := worker.RunOne(ctx)
	if !worked || !errors.Is(err, context.Canceled) || !errors.Is(err, cleanupErr) {
		t.Fatalf("worked=%v error=%v", worked, err)
	}
	if !provider.cleaned || provider.cleanedValue != (Snapshot{}) {
		t.Fatalf("cleaned=%v snapshot=%+v", provider.cleaned, provider.cleanedValue)
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
