package supplychain

import (
	"context"
	"errors"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/balcsida/graphnest/internal/githubapp"
	"github.com/balcsida/graphnest/internal/repository"
)

// collectorFakeStore is an in-memory CollectorStore with one repository and a
// single job. It records publications and failures for assertions and can
// simulate a lost lease.
type collectorFakeStore struct {
	mu           sync.Mutex
	job          *Job
	fenced       bool
	published    []Publication
	failed       []Failure
	projections  int
	projectError error
	projectFails []int64
	renewals     int
	reaped       int
	depths       map[string]int64
}

func newCollectorFakeStore() *collectorFakeStore {
	return &collectorFakeStore{job: &Job{ID: 1, RepositoryID: 5, StreamKey: StreamGitHubSource, Reason: "manual", State: JobQueued, MaxAttempts: 5}, depths: map[string]int64{"queued": 1, "running": 0}}
}

func (store *collectorFakeStore) ClaimSupplyChainJob(context.Context, string) (Job, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	if store.job == nil || store.job.State != JobQueued {
		return Job{}, ErrNoJob
	}
	store.job.State, store.job.LeaseOwner, store.job.Fence, store.job.Attempt = JobRunning, "worker", store.job.Fence+1, store.job.Attempt+1
	return *store.job, nil
}

func (store *collectorFakeStore) RenewSupplyChainLease(context.Context, int64, string, int64) error {
	store.mu.Lock()
	defer store.mu.Unlock()
	store.renewals++
	if store.fenced {
		return ErrFenced
	}
	return nil
}

func (store *collectorFakeStore) ReapExpiredSupplyChainJobs(context.Context, int) (int64, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	store.reaped++
	return 0, nil
}

func (store *collectorFakeStore) RepositoryForIndex(_ context.Context, id int64) (repository.Repository, error) {
	if id != 5 {
		return repository.Repository{}, errors.New("missing")
	}
	return repository.Repository{ID: 5, InstallationID: 10, GitHubID: 105, Name: "acme/widgets"}, nil
}

func (store *collectorFakeStore) PublishSupplyChainSnapshot(_ context.Context, publication Publication) (Collection, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	if store.fenced {
		return Collection{}, ErrFenced
	}
	store.published = append(store.published, publication)
	store.job.State = JobSucceeded
	snapshotID := int64(len(store.published))
	return Collection{ID: snapshotID * 100, RepositoryID: publication.RepositoryID, Outcome: OutcomePublished, SnapshotID: &snapshotID}, nil
}

func (store *collectorFakeStore) RecordSupplyChainFailure(_ context.Context, failure Failure) (Collection, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	if store.fenced && failure.Outcome != OutcomeCancelled {
		return Collection{}, ErrFenced
	}
	store.failed = append(store.failed, failure)
	if failure.Outcome.Retryable() && store.job.Attempt < store.job.MaxAttempts {
		store.job.State = JobQueued
	} else {
		store.job.State = JobFailed
	}
	return Collection{Outcome: failure.Outcome}, nil
}

func (store *collectorFakeStore) RecordSupplyChainProjectionError(_ context.Context, collectionID int64, _ string) error {
	store.mu.Lock()
	defer store.mu.Unlock()
	store.projectFails = append(store.projectFails, collectionID)
	return nil
}

func (store *collectorFakeStore) ProjectSupplyChainPackages(context.Context, int64, int64) (int, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	store.projections++
	return 3, store.projectError
}

func (store *collectorFakeStore) DueSupplyChainStreams(context.Context, string, time.Time, int) ([]int64, error) {
	return []int64{5, 6}, nil
}

func (store *collectorFakeStore) EnqueueSupplyChainJob(_ context.Context, repositoryID int64, _, reason, _ string, _ int, runAfter time.Time) (Job, bool, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	store.depths["queued"]++
	return Job{ID: repositoryID, RepositoryID: repositoryID, Reason: reason, RunAfter: runAfter}, repositoryID == 6, nil
}

func (store *collectorFakeStore) SupplyChainQueueDepths(context.Context) (map[string]int64, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	return map[string]int64{"queued": store.depths["queued"], "running": store.depths["running"]}, nil
}

type fakeSBOMReader struct {
	document githubapp.SBOMDocument
	err      error
	calls    atomic.Int32
	block    chan struct{}
}

func (reader *fakeSBOMReader) DependencySBOMDocument(ctx context.Context, _ int64, _, _ string, _ int64) (githubapp.SBOMDocument, error) {
	reader.calls.Add(1)
	if reader.block != nil {
		select {
		case <-reader.block:
		case <-ctx.Done():
			return githubapp.SBOMDocument{}, ctx.Err()
		}
	}
	return reader.document, reader.err
}

type fakeObserver struct {
	outcomes []string
	depths   map[string]int64
}

func (observer *fakeObserver) ObserveSupplyChainCollection(outcome string, _ time.Duration) {
	observer.outcomes = append(observer.outcomes, outcome)
}

func (observer *fakeObserver) SetSupplyChainQueueDepth(state string, depth int64) {
	if observer.depths == nil {
		observer.depths = map[string]int64{}
	}
	observer.depths[state] = depth
}

func successDocument(t *testing.T) githubapp.SBOMDocument {
	return githubapp.SBOMDocument{Body: fixture(t), MediaType: "application/json", Status: 200, FetchedAt: time.Date(2026, 9, 22, 9, 0, 0, 0, time.UTC)}
}

func TestCollectorPublishesAndProjects(t *testing.T) {
	store := newCollectorFakeStore()
	reader := &fakeSBOMReader{document: successDocument(t)}
	observer := &fakeObserver{}
	collector := &Collector{Store: store, GitHub: reader, Owner: "worker", MaxDocumentBytes: 1 << 20, Observer: observer}
	processed, err := collector.RunOnce(t.Context())
	if err != nil || !processed {
		t.Fatalf("processed=%v err=%v", processed, err)
	}
	if len(store.published) != 1 || len(store.failed) != 0 || store.projections != 1 || len(store.projectFails) != 0 {
		t.Fatalf("published=%d failed=%d projections=%d", len(store.published), len(store.failed), store.projections)
	}
	publication := store.published[0]
	if publication.RepositoryID != 5 || *publication.JobID != 1 || publication.JobOwner != "worker" || publication.JobFence != 1 || publication.Producer != ProducerGitHub || publication.Subject != SubjectSource ||
		publication.Format != FormatSPDX23JSON || string(publication.Document) != string(fixture(t)) || !publication.CollectedAt.Equal(reader.document.FetchedAt) || publication.SubjectAssurance != AssuranceUnknown || publication.SubjectRevision != "" {
		t.Fatalf("publication = %+v", publication)
	}
	if len(publication.Normalized.Components) != 7 || *publication.HTTPStatus != 200 {
		t.Fatalf("normalized = %d components", len(publication.Normalized.Components))
	}
	if len(observer.outcomes) != 1 || observer.outcomes[0] != "published" {
		t.Fatalf("outcomes = %v", observer.outcomes)
	}
	if processed, err := collector.RunOnce(t.Context()); err != nil || processed {
		t.Fatalf("second run processed=%v err=%v", processed, err)
	}
}

func TestCollectorProjectionFailureIsRecordedNotFatal(t *testing.T) {
	store := newCollectorFakeStore()
	store.projectError = errors.New("boom")
	collector := &Collector{Store: store, GitHub: &fakeSBOMReader{document: successDocument(t)}, Owner: "worker"}
	if _, err := collector.RunOnce(t.Context()); err != nil {
		t.Fatal(err)
	}
	if len(store.published) != 1 || len(store.projectFails) != 1 || store.projectFails[0] != 100 {
		t.Fatalf("published=%d projectFails=%v", len(store.published), store.projectFails)
	}
}

func TestCollectorClassifiesFailures(t *testing.T) {
	cases := map[string]struct {
		err        error
		document   githubapp.SBOMDocument
		outcome    Outcome
		code       string
		status     int
		retryAfter int
		retried    bool
	}{
		"forbidden":         {err: githubapp.SBOMError{Status: 403}, outcome: OutcomeForbidden, code: "github_forbidden", status: 403},
		"rate limited 403":  {err: githubapp.SBOMError{Status: 403, RateLimit: githubapp.RateLimit{Limited: true}, RetryAfter: 90 * time.Second}, outcome: OutcomeRateLimited, code: "rate_limited", status: 403, retryAfter: 90, retried: true},
		"too many requests": {err: githubapp.SBOMError{Status: 429, RetryAfter: time.Minute}, outcome: OutcomeRateLimited, code: "rate_limited", status: 429, retryAfter: 60, retried: true},
		"not found":         {err: githubapp.SBOMError{Status: 404}, outcome: OutcomeNotFound, code: "github_not_found", status: 404},
		"server error":      {err: githubapp.SBOMError{Status: 502}, outcome: OutcomeTransient, code: "github_unavailable", status: 502, retried: true},
		"unauthorized":      {err: githubapp.SBOMError{Status: 401}, outcome: OutcomeTransient, code: "github_unauthorized", status: 401, retried: true},
		"teapot":            {err: githubapp.SBOMError{Status: 418}, outcome: OutcomeUnavailable, code: "github_status", status: 418},
		"too large":         {err: githubapp.ErrSBOMTooLarge, outcome: OutcomeTooLarge, code: "document_too_large"},
		"envelope":          {err: githubapp.ErrSBOMMalformed, outcome: OutcomeMalformed, code: "envelope_malformed"},
		"network":           {err: errors.New("dial tcp: connection refused"), outcome: OutcomeTransient, code: "github_request_failed", retried: true},
		"timeout":           {err: context.DeadlineExceeded, outcome: OutcomeTransient, code: "github_timeout", retried: true},
		"malformed spdx":    {document: githubapp.SBOMDocument{Body: []byte(`{"spdxVersion":"SPDX-2.3","packages":"x"}`), Status: 200}, outcome: OutcomeMalformed, code: "malformed_document", status: 200},
		"unsupported spdx":  {document: githubapp.SBOMDocument{Body: []byte(`{"spdxVersion":"SPDX-2.2","packages":[]}`), Status: 200}, outcome: OutcomeMalformed, code: "unsupported_spdx_version", status: 200},
		"too many packages": {document: githubapp.SBOMDocument{Body: []byte(`{"spdxVersion":"SPDX-2.3","packages":[{"SPDXID":"a"},{"SPDXID":"b"}]}`), Status: 200}, outcome: OutcomeTooLarge, code: "document_too_large", status: 200},
	}
	for name, test := range cases {
		t.Run(name, func(t *testing.T) {
			store := newCollectorFakeStore()
			collector := &Collector{Store: store, GitHub: &fakeSBOMReader{document: test.document, err: test.err}, Owner: "worker", Limits: Limits{MaxComponents: 1}}
			if _, err := collector.RunOnce(t.Context()); err != nil {
				t.Fatal(err)
			}
			if len(store.published) != 0 || len(store.failed) != 1 {
				t.Fatalf("published=%d failed=%d", len(store.published), len(store.failed))
			}
			failure := store.failed[0]
			if failure.Outcome != test.outcome || failure.ErrorCode != test.code || failure.JobOwner != "worker" || failure.JobFence != 1 {
				t.Fatalf("failure = %+v", failure)
			}
			if test.status != 0 && (failure.HTTPStatus == nil || *failure.HTTPStatus != test.status) {
				t.Fatalf("status = %v, want %d", failure.HTTPStatus, test.status)
			}
			if test.retryAfter != 0 && (failure.RetryAfterSeconds == nil || *failure.RetryAfterSeconds != test.retryAfter) {
				t.Fatalf("retry after = %v, want %d", failure.RetryAfterSeconds, test.retryAfter)
			}
			if (store.job.State == JobQueued) != test.retried {
				t.Fatalf("job state = %s, retried should be %v", store.job.State, test.retried)
			}
			if failure.Message == "" && test.outcome != OutcomeUnavailable {
				t.Fatalf("outcome %s has no operator message", test.outcome)
			}
			if test.outcome == OutcomeForbidden && !contains(failure.Message, "dependency graph may be disabled") {
				t.Fatalf("forbidden message must not assert the dependency graph is disabled: %q", failure.Message)
			}
		})
	}
}

func contains(value, substring string) bool {
	return len(value) >= len(substring) && (value == substring || len(substring) == 0 || indexOf(value, substring) >= 0)
}

func indexOf(value, substring string) int {
	for index := 0; index+len(substring) <= len(value); index++ {
		if value[index:index+len(substring)] == substring {
			return index
		}
	}
	return -1
}

func TestCollectorStaleLeaseDoesNotPublish(t *testing.T) {
	store := newCollectorFakeStore()
	store.fenced = true
	collector := &Collector{Store: store, GitHub: &fakeSBOMReader{document: successDocument(t)}, Owner: "worker"}
	processed, err := collector.RunOnce(t.Context())
	if err != nil || !processed {
		t.Fatalf("processed=%v err=%v", processed, err)
	}
	if len(store.published) != 0 || len(store.failed) != 0 || store.projections != 0 {
		t.Fatalf("stale worker wrote: published=%d failed=%d projections=%d", len(store.published), len(store.failed), store.projections)
	}
}

func TestCollectorCancellationRecordsCancelledWithoutPublishing(t *testing.T) {
	store := newCollectorFakeStore()
	reader := &fakeSBOMReader{document: successDocument(t), block: make(chan struct{})}
	collector := &Collector{Store: store, GitHub: reader, Owner: "worker"}
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() {
		_, err := collector.RunOnce(ctx)
		done <- err
	}()
	for reader.calls.Load() == 0 {
		time.Sleep(time.Millisecond)
	}
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v", err)
	}
	if len(store.published) != 0 {
		t.Fatal("cancelled collection published")
	}
}

func TestCollectorRunLoopReapsAndStops(t *testing.T) {
	store := newCollectorFakeStore()
	observer := &fakeObserver{}
	collector := &Collector{Store: store, GitHub: &fakeSBOMReader{document: successDocument(t)}, Owner: "worker", Observer: observer, Poll: 5 * time.Millisecond}
	ctx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
	defer cancel()
	err := collector.Run(ctx)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Run = %v", err)
	}
	if store.reaped == 0 || len(store.published) != 1 || observer.depths["queued"] != 1 {
		t.Fatalf("reaped=%d published=%d depths=%v", store.reaped, len(store.published), observer.depths)
	}
}

func TestSchedulerEnqueuesDueStreamsWithJitter(t *testing.T) {
	store := newCollectorFakeStore()
	now := time.Date(2026, 9, 22, 12, 0, 0, 0, time.UTC)
	scheduler := &Scheduler{Store: store, Interval: 10 * time.Hour, Now: func() time.Time { return now }}
	created, err := scheduler.Tick(t.Context())
	if err != nil || created != 1 {
		t.Fatalf("created=%d err=%v", created, err)
	}
	if store.depths["queued"] != 3 {
		t.Fatalf("enqueue calls = %d, want one per due stream", store.depths["queued"]-1)
	}
}

func TestClassifyFetchErrorRateLimitResetDrivesRetryAfter(t *testing.T) {
	reset := time.Now().Add(10 * time.Minute)
	outcome, status, retryAfter, code := classifyFetchError(githubapp.SBOMError{Status: http.StatusForbidden, RateLimit: githubapp.RateLimit{Limited: true, ResetAt: reset}})
	if outcome != OutcomeRateLimited || *status != 403 || retryAfter == nil || *retryAfter < 590 || *retryAfter > 601 || code != "rate_limited" {
		t.Fatalf("classify = %s %v %v %s", outcome, status, retryAfter, code)
	}
}
