//go:build integration

package postgres

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

const (
	shaA = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	shaB = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	shaC = "cccccccccccccccccccccccccccccccccccccccc"
)

func queueRepository(t *testing.T, store *Store) int64 {
	t.Helper()
	if err := store.UpsertInstallation(t.Context(), InstallationUpdate{GitHubID: 10, AccountLogin: "acme", AccountType: "Organization", Status: "active"}); err != nil {
		t.Fatal(err)
	}
	repository, err := store.UpsertRepository(t.Context(), RepositoryUpdate{GitHubID: 101, InstallationID: 10, Owner: "acme", Name: "repo", CloneURL: "clone", WebURL: "web", DefaultBranch: "main", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	return repository.ID
}

func runningIndexJob(t *testing.T) (*Store, IndexJob) {
	t.Helper()
	store := migratedStore(t)
	repositoryID := queueRepository(t, store)
	if _, err := store.pool.Exec(t.Context(), "update repositories set indexed_sha=$2 where id=$1", repositoryID, shaC); err != nil {
		t.Fatal(err)
	}
	if err := store.EnqueueIndex(t.Context(), IndexRequest{RepositoryID: repositoryID, TargetSHA: shaA}); err != nil {
		t.Fatal(err)
	}
	job, err := store.ClaimIndex(t.Context(), "indexer-1")
	if err != nil {
		t.Fatal(err)
	}
	return store, job
}

func TestDeliveryDeduplicatesWithMutation(t *testing.T) {
	store := migratedStore(t)
	repositoryID := queueRepository(t, store)
	installationID := int64(10)
	called := 0
	apply := func() (bool, error) {
		return store.ApplyDelivery(t.Context(), Delivery{ID: "delivery-1", Event: "push", State: "accepted", InstallationID: &installationID}, func(ctx context.Context, tx *DeliveryTx) error {
			called++
			return tx.EnqueueIndex(ctx, IndexRequest{RepositoryID: repositoryID, TargetSHA: shaA, TargetRef: "refs/heads/main", Reason: "push"})
		})
	}
	if inserted, err := apply(); err != nil || !inserted {
		t.Fatalf("first delivery: inserted=%v err=%v", inserted, err)
	}
	if inserted, err := apply(); err != nil || inserted || called != 1 {
		t.Fatalf("duplicate: inserted=%v called=%d err=%v", inserted, called, err)
	}
	var deliveries, jobs int
	var desired string
	if err := store.pool.QueryRow(t.Context(), "select count(*) from webhook_deliveries").Scan(&deliveries); err != nil {
		t.Fatal(err)
	}
	if err := store.pool.QueryRow(t.Context(), "select count(*) from index_jobs").Scan(&jobs); err != nil {
		t.Fatal(err)
	}
	if err := store.pool.QueryRow(t.Context(), "select desired_sha from repositories where id=$1", repositoryID).Scan(&desired); err != nil || deliveries != 1 || jobs != 1 || desired != shaA {
		t.Fatalf("deliveries=%d jobs=%d desired=%q err=%v", deliveries, jobs, desired, err)
	}
}

func TestDeliveryMutationsStayAtomic(t *testing.T) {
	store := migratedStore(t)
	repositoryID := queueRepository(t, store)
	installationID := int64(10)
	want := errors.New("rollback")
	inserted, err := store.ApplyDelivery(t.Context(), Delivery{ID: "delivery-rollback", Event: "push", State: "accepted", InstallationID: &installationID}, func(ctx context.Context, tx *DeliveryTx) error {
		if err := tx.EnqueueIndex(ctx, IndexRequest{RepositoryID: repositoryID, TargetSHA: shaA}); err != nil {
			return err
		}
		return want
	})
	if inserted || !errors.Is(err, want) {
		t.Fatalf("inserted=%v err=%v", inserted, err)
	}
	var deliveries, jobs int
	if err := store.pool.QueryRow(t.Context(), "select count(*) from webhook_deliveries").Scan(&deliveries); err != nil {
		t.Fatal(err)
	}
	if err := store.pool.QueryRow(t.Context(), "select count(*) from index_jobs").Scan(&jobs); err != nil || deliveries != 0 || jobs != 0 {
		t.Fatalf("deliveries=%d jobs=%d err=%v", deliveries, jobs, err)
	}
}

func TestQueueCoalescesNewestDesiredSHA(t *testing.T) {
	store := migratedStore(t)
	repositoryID := queueRepository(t, store)
	for _, sha := range []string{shaA, shaA, shaB} {
		if err := store.EnqueueIndex(t.Context(), IndexRequest{RepositoryID: repositoryID, TargetSHA: sha}); err != nil {
			t.Fatal(err)
		}
	}
	var count int
	var target, desired string
	if err := store.pool.QueryRow(t.Context(), "select count(*), min(target_sha) from index_jobs where state='queued'").Scan(&count, &target); err != nil {
		t.Fatal(err)
	}
	if err := store.pool.QueryRow(t.Context(), "select desired_sha from repositories where id=$1", repositoryID).Scan(&desired); err != nil || count != 1 || target != shaB || desired != shaB {
		t.Fatalf("count=%d target=%q desired=%q err=%v", count, target, desired, err)
	}
}

func TestQueuePersistsOperationalMetadata(t *testing.T) {
	store := migratedStore(t)
	repositoryID := queueRepository(t, store)
	request := IndexRequest{
		RepositoryID: repositoryID, TargetRef: "refs/heads/main", TargetSHA: shaA,
		Reason: "push", Priority: 7, MaxAttempts: 2,
	}
	if err := store.EnqueueIndex(t.Context(), request); err != nil {
		t.Fatal(err)
	}
	job, err := store.ClaimIndex(t.Context(), "owner")
	if err != nil {
		t.Fatal(err)
	}
	if job.TargetRef != request.TargetRef || job.Reason != request.Reason || job.Priority != request.Priority || job.MaxAttempts != request.MaxAttempts {
		t.Fatalf("job metadata = %#v, want request %#v", job, request)
	}
}

func TestQueueClaimsHigherPriorityFirst(t *testing.T) {
	store := migratedStore(t)
	lowRepositoryID := queueRepository(t, store)
	highRepository, err := store.UpsertRepository(t.Context(), RepositoryUpdate{
		GitHubID: 102, InstallationID: 10, Owner: "acme", Name: "priority", CloneURL: "clone", WebURL: "web", DefaultBranch: "main", Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.EnqueueIndex(t.Context(), IndexRequest{RepositoryID: lowRepositoryID, TargetSHA: shaA, Priority: 1}); err != nil {
		t.Fatal(err)
	}
	if err := store.EnqueueIndex(t.Context(), IndexRequest{RepositoryID: highRepository.ID, TargetSHA: shaB, Priority: 9}); err != nil {
		t.Fatal(err)
	}
	job, err := store.ClaimIndex(t.Context(), "owner")
	if err != nil {
		t.Fatal(err)
	}
	if job.RepositoryID != highRepository.ID {
		t.Fatalf("claimed repository = %d, want high-priority repository %d", job.RepositoryID, highRepository.ID)
	}
}

func TestQueueStopsAtConfiguredMaxAttempts(t *testing.T) {
	store := migratedStore(t)
	repositoryID := queueRepository(t, store)
	if err := store.EnqueueIndex(t.Context(), IndexRequest{RepositoryID: repositoryID, TargetSHA: shaA, MaxAttempts: 1}); err != nil {
		t.Fatal(err)
	}
	job, err := store.ClaimIndex(t.Context(), "owner")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.FailIndex(t.Context(), job.ID, "owner", "temporary", true); err != nil {
		t.Fatal(err)
	}
	var state string
	if err := store.pool.QueryRow(t.Context(), "select state from index_jobs where id=$1", job.ID).Scan(&state); err != nil || state != "failed" {
		t.Fatalf("state = %q, err = %v", state, err)
	}
}

func TestAdminRetryOnlyRequeuesCurrentFailedWork(t *testing.T) {
	store := migratedStore(t)
	repositoryID := queueRepository(t, store)
	if err := store.EnqueueIndex(t.Context(), IndexRequest{RepositoryID: repositoryID, TargetSHA: shaA}); err != nil {
		t.Fatal(err)
	}
	job, err := store.ClaimIndex(t.Context(), "owner")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.FailIndex(t.Context(), job.ID, "owner", "permanent", false); err != nil {
		t.Fatal(err)
	}
	if err := store.RetryAdminJob(t.Context(), 10, []int64{101}, job.ID); err != nil {
		t.Fatal(err)
	}
	var state string
	var attempt int
	if err := store.pool.QueryRow(t.Context(), "select state,attempt from index_jobs where id=$1", job.ID).Scan(&state, &attempt); err != nil || state != "queued" || attempt != 0 {
		t.Fatalf("state=%q attempt=%d err=%v", state, attempt, err)
	}
	if err := store.RetryAdminJob(t.Context(), 20, []int64{202}, job.ID); err == nil {
		t.Fatal("cross-scope job was retried")
	}
	if err := store.RetryAdminJob(t.Context(), 10, []int64{101}, job.ID); err == nil {
		t.Fatal("queued job was retried")
	}
	if _, err := store.pool.Exec(t.Context(), "update repositories set enabled=false where id=$1", repositoryID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.pool.Exec(t.Context(), "update index_jobs set state='failed' where id=$1", job.ID); err != nil {
		t.Fatal(err)
	}
	if err := store.RetryAdminJob(t.Context(), 10, []int64{101}, job.ID); err == nil {
		t.Fatal("disabled repository job was retried")
	}
}

func TestQueueClaimsOnceWithSkipLocked(t *testing.T) {
	store := migratedStore(t)
	repositoryID := queueRepository(t, store)
	if err := store.EnqueueIndex(t.Context(), IndexRequest{RepositoryID: repositoryID, TargetSHA: shaA}); err != nil {
		t.Fatal(err)
	}
	var successes atomic.Int32
	errorsFound := make(chan error, 20)
	var group sync.WaitGroup
	for i := range 20 {
		group.Add(1)
		go func() {
			defer group.Done()
			job, err := store.ClaimIndex(t.Context(), fmt.Sprintf("worker-%d", i))
			if err == nil {
				if job.RepositoryID != repositoryID || job.Attempt != 1 || job.State != "running" {
					errorsFound <- fmt.Errorf("bad job: %#v", job)
					return
				}
				successes.Add(1)
				return
			}
			if !errors.Is(err, ErrNoJob) {
				errorsFound <- err
			}
		}()
	}
	group.Wait()
	close(errorsFound)
	for err := range errorsFound {
		t.Error(err)
	}
	if successes.Load() != 1 {
		t.Fatalf("successful claims=%d", successes.Load())
	}
}

func TestQueueRequiresLiveLeaseOwner(t *testing.T) {
	store := migratedStore(t)
	repositoryID := queueRepository(t, store)
	if err := store.EnqueueIndex(t.Context(), IndexRequest{RepositoryID: repositoryID, TargetSHA: shaA}); err != nil {
		t.Fatal(err)
	}
	job, err := store.ClaimIndex(t.Context(), "owner")
	if err != nil {
		t.Fatal(err)
	}
	for _, operation := range []func() error{
		func() error { return store.RenewLease(t.Context(), job.ID, "other") },
		func() error { return store.CompleteIndex(t.Context(), job.ID, "other") },
		func() error { return store.FailIndex(t.Context(), job.ID, "other", "failure", true) },
	} {
		if err := operation(); !errors.Is(err, ErrLeaseLost) {
			t.Fatalf("wrong owner err=%v", err)
		}
	}
	if _, err := store.pool.Exec(t.Context(), "update index_jobs set lease_expires_at=now() where id=$1", job.ID); err != nil {
		t.Fatal(err)
	}
	if err := store.RenewLease(t.Context(), job.ID, "owner"); !errors.Is(err, ErrLeaseLost) {
		t.Fatalf("expired lease err=%v", err)
	}
}

func TestQueuePublishesOnlyCurrentDesiredSHA(t *testing.T) {
	for i := range 20 {
		store := migratedStore(t)
		repositoryID := queueRepository(t, store)
		if _, err := store.pool.Exec(t.Context(), "update repositories set indexed_sha=$2 where id=$1", repositoryID, shaC); err != nil {
			t.Fatal(err)
		}
		if err := store.EnqueueIndex(t.Context(), IndexRequest{RepositoryID: repositoryID, TargetSHA: shaA}); err != nil {
			t.Fatal(err)
		}
		job, err := store.ClaimIndex(t.Context(), "owner")
		if err != nil {
			t.Fatal(err)
		}
		start := make(chan struct{})
		errs := make(chan error, 2)
		go func() { <-start; errs <- store.CompleteIndex(t.Context(), job.ID, "owner") }()
		go func() {
			<-start
			errs <- store.EnqueueIndex(t.Context(), IndexRequest{RepositoryID: repositoryID, TargetSHA: shaB})
		}()
		close(start)
		for range 2 {
			if err := <-errs; err != nil {
				t.Fatal(err)
			}
		}
		var desired, indexed, state string
		var queued int
		if err := store.pool.QueryRow(t.Context(), "select desired_sha, indexed_sha from repositories where id=$1", repositoryID).Scan(&desired, &indexed); err != nil {
			t.Fatal(err)
		}
		if err := store.pool.QueryRow(t.Context(), "select state from index_jobs where id=$1", job.ID).Scan(&state); err != nil {
			t.Fatal(err)
		}
		if err := store.pool.QueryRow(t.Context(), "select count(*) from index_jobs where repository_id=$1 and state='queued' and target_sha=$2", repositoryID, shaB).Scan(&queued); err != nil {
			t.Fatal(err)
		}
		validSucceeded := desired == shaB && indexed == shaA && state == "succeeded" && queued == 1
		validSuperseded := desired == shaB && (indexed == shaA || indexed == shaC) && state == "superseded" && queued == 1
		if !validSucceeded && !validSuperseded {
			t.Fatalf("iteration=%d desired=%q indexed=%q state=%q queued=%d", i, desired, indexed, state, queued)
		}
	}
}

func TestQueueRetriesAndReapsExpiredLeases(t *testing.T) {
	store := migratedStore(t)
	repositoryID := queueRepository(t, store)
	assertScheduled := func(jobID int64, cap string, attempt int) {
		t.Helper()
		var future, bounded bool
		var retainedAttempt int
		if err := store.pool.QueryRow(t.Context(), `select run_after>updated_at, run_after<=updated_at+$2::interval, attempt from index_jobs where id=$1`, jobID, cap).Scan(&future, &bounded, &retainedAttempt); err != nil {
			t.Fatal(err)
		}
		if !future || !bounded || retainedAttempt != attempt {
			t.Fatalf("scheduled future=%v bounded=%v attempt=%d want=%d cap=%s", future, bounded, retainedAttempt, attempt, cap)
		}
		if _, err := store.pool.Exec(t.Context(), "update index_jobs set run_after=now()+interval '1 minute' where id=$1", jobID); err != nil {
			t.Fatal(err)
		}
		if _, err := store.ClaimIndex(t.Context(), "early"); !errors.Is(err, ErrNoJob) {
			t.Fatalf("early claim err=%v", err)
		}
	}
	makeRunnable := func(jobID int64) {
		t.Helper()
		if _, err := store.pool.Exec(t.Context(), "update index_jobs set run_after=now()-interval '1 second' where id=$1", jobID); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.EnqueueIndex(t.Context(), IndexRequest{RepositoryID: repositoryID, TargetSHA: shaA}); err != nil {
		t.Fatal(err)
	}
	job, err := store.ClaimIndex(t.Context(), "owner")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.FailIndex(t.Context(), job.ID, "owner", "temporary", true); err != nil {
		t.Fatal(err)
	}
	assertScheduled(job.ID, "5 seconds", 1)
	makeRunnable(job.ID)
	job, err = store.ClaimIndex(t.Context(), "owner")
	if err != nil || job.Attempt != 2 {
		t.Fatalf("retry job=%#v err=%v", job, err)
	}
	if _, err := store.pool.Exec(t.Context(), "update index_jobs set lease_expires_at=now() where id=$1", job.ID); err != nil {
		t.Fatal(err)
	}
	var reaped atomic.Int64
	var group sync.WaitGroup
	for range 10 {
		group.Add(1)
		go func() {
			defer group.Done()
			count, err := store.ReapExpired(t.Context(), 1)
			if err != nil {
				t.Error(err)
			}
			reaped.Add(count)
		}()
	}
	group.Wait()
	if reaped.Load() != 1 {
		t.Fatalf("reaped=%d", reaped.Load())
	}
	assertScheduled(job.ID, "10 seconds", 2)
	makeRunnable(job.ID)
	job, err = store.ClaimIndex(t.Context(), "owner")
	if err != nil || job.Attempt != 3 {
		t.Fatalf("reaped retry job=%#v err=%v", job, err)
	}
	for _, cap := range []string{"20 seconds", "40 seconds"} {
		if err := store.FailIndex(t.Context(), job.ID, "owner", "temporary", true); err != nil {
			t.Fatal(err)
		}
		assertScheduled(job.ID, cap, job.Attempt)
		makeRunnable(job.ID)
		job, err = store.ClaimIndex(t.Context(), "owner")
		if err != nil || job.Attempt > 5 {
			t.Fatalf("retry job=%#v err=%v", job, err)
		}
	}
	if err := store.FailIndex(t.Context(), job.ID, "owner", "temporary", true); err != nil {
		t.Fatal(err)
	}
	var state string
	if err := store.pool.QueryRow(t.Context(), "select state from index_jobs where id=$1", job.ID).Scan(&state); err != nil || state != "failed" {
		t.Fatalf("fifth attempt state=%q err=%v", state, err)
	}

	if err := store.EnqueueIndex(t.Context(), IndexRequest{RepositoryID: repositoryID, TargetSHA: shaB}); err != nil {
		t.Fatal(err)
	}
	permanent, err := store.ClaimIndex(t.Context(), "owner")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.FailIndex(t.Context(), permanent.ID, "owner", "permanent", false); err != nil {
		t.Fatal(err)
	}
	if err := store.pool.QueryRow(t.Context(), "select state from index_jobs where id=$1", permanent.ID).Scan(&state); err != nil || state != "failed" {
		t.Fatalf("permanent state=%q err=%v", state, err)
	}

	if err := store.EnqueueIndex(t.Context(), IndexRequest{RepositoryID: repositoryID, TargetSHA: shaA}); err != nil {
		t.Fatal(err)
	}
	superseded, err := store.ClaimIndex(t.Context(), "owner")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.EnqueueIndex(t.Context(), IndexRequest{RepositoryID: repositoryID, TargetSHA: shaB}); err != nil {
		t.Fatal(err)
	}
	if err := store.FailIndex(t.Context(), superseded.ID, "owner", "temporary", true); err != nil {
		t.Fatal(err)
	}
	if err := store.pool.QueryRow(t.Context(), "select state from index_jobs where id=$1", superseded.ID).Scan(&state); err != nil || state != "superseded" {
		t.Fatalf("superseded state=%q err=%v", state, err)
	}
}

func TestQueuePrunesBoundedHistory(t *testing.T) {
	store := migratedStore(t)
	repositoryID := queueRepository(t, store)
	installationID := int64(10)
	for i := range 2 {
		if _, err := store.ApplyDelivery(t.Context(), Delivery{ID: fmt.Sprintf("old-%d", i), Event: "push", State: "accepted", InstallationID: &installationID}, nil); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := store.pool.Exec(t.Context(), "update webhook_deliveries set received_at=now()-interval '31 days' where delivery_id='old-0'"); err != nil {
		t.Fatal(err)
	}
	for i := range 102 {
		sha := fmt.Sprintf("%040x", i+1)
		if _, err := store.pool.Exec(t.Context(), "insert into index_jobs(repository_id,target_ref,target_sha,reason,state) values($1,'refs/heads/main',$2,'test','failed')", repositoryID, sha); err != nil {
			t.Fatal(err)
		}
	}
	deliveries, jobs, err := store.Prune(t.Context())
	if err != nil || deliveries != 1 || jobs != 2 {
		t.Fatalf("deliveries=%d jobs=%d err=%v", deliveries, jobs, err)
	}
}

func TestQueueDepthsUseFixedStates(t *testing.T) {
	store := migratedStore(t)
	repositoryID := queueRepository(t, store)
	if err := store.EnqueueIndex(t.Context(), IndexRequest{RepositoryID: repositoryID, TargetSHA: shaA}); err != nil {
		t.Fatal(err)
	}
	depths, err := store.QueueDepths(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]int64{"queued": 1, "running": 0, "succeeded": 0, "failed": 0, "superseded": 0}
	if len(depths) != len(want) {
		t.Fatalf("depths=%v want=%v", depths, want)
	}
	for state, count := range want {
		if depths[state] != count {
			t.Fatalf("depths=%v want=%v", depths, want)
		}
	}
	active, err := store.ActiveJobIDs(t.Context())
	if err != nil || len(active) != 0 {
		t.Fatalf("active=%v err=%v", active, err)
	}
}

func TestPublishIndexIsIdempotentAndRetainsRunningLease(t *testing.T) {
	store, job := runningIndexJob(t)
	for range 2 {
		if err := store.PublishIndex(t.Context(), job.ID, job.LeaseOwner); err != nil {
			t.Fatal(err)
		}
	}
	var indexedSHA, state, owner string
	var graphJobs int
	if err := store.pool.QueryRow(t.Context(), `select repositories.indexed_sha,index_jobs.state,index_jobs.lease_owner,
		(select count(*) from graph_jobs where graph_jobs.repository_id=repositories.id and graph_jobs.target_sha=index_jobs.target_sha)
		from repositories join index_jobs on index_jobs.repository_id=repositories.id where index_jobs.id=$1`, job.ID).
		Scan(&indexedSHA, &state, &owner, &graphJobs); err != nil {
		t.Fatal(err)
	}
	if indexedSHA != job.TargetSHA || state != "running" || owner != job.LeaseOwner || graphJobs != 1 {
		t.Fatalf("indexed_sha=%q state=%q owner=%q graph_jobs=%d", indexedSHA, state, owner, graphJobs)
	}
	if err := store.CompleteIndex(t.Context(), job.ID, job.LeaseOwner); err != nil {
		t.Fatal(err)
	}
}

func TestCompleteIndexRecordsInlineEnrichment(t *testing.T) {
	store, job := runningIndexJob(t)
	if err := store.PublishIndex(t.Context(), job.ID, job.LeaseOwner); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ClaimGraph(t.Context(), "scanner-1"); !errors.Is(err, ErrNoJob) {
		t.Fatalf("ClaimGraph() error = %v", err)
	}
	artifact := artifactFor(job.RepositoryID, job.TargetSHA, "inline")
	if err := store.CompleteIndex(t.Context(), job.ID, job.LeaseOwner, EnrichmentStatus{Artifact: &artifact}); err != nil {
		t.Fatal(err)
	}
	var indexState, graphState string
	var uploads int
	if err := store.pool.QueryRow(t.Context(), `select index_jobs.state,graph_jobs.state,
		(select count(*) from graph_uploads where repository_id=index_jobs.repository_id and commit=index_jobs.target_sha)
		from index_jobs join graph_jobs on graph_jobs.repository_id=index_jobs.repository_id and graph_jobs.target_sha=index_jobs.target_sha
		where index_jobs.id=$1`, job.ID).Scan(&indexState, &graphState, &uploads); err != nil {
		t.Fatal(err)
	}
	if indexState != "succeeded" || graphState != "succeeded" || uploads != 1 {
		t.Fatalf("index=%q graph=%q uploads=%d", indexState, graphState, uploads)
	}
}

func TestPublishedIndexCrashReusesEnrichmentRecord(t *testing.T) {
	store, job := runningIndexJob(t)
	if err := store.PublishIndex(t.Context(), job.ID, job.LeaseOwner); err != nil {
		t.Fatal(err)
	}
	if _, err := store.pool.Exec(t.Context(), "update index_jobs set lease_expires_at=now() where id=$1", job.ID); err != nil {
		t.Fatal(err)
	}
	if reaped, err := store.ReapExpired(t.Context(), 1); err != nil || reaped != 1 {
		t.Fatalf("ReapExpired() = %d, %v", reaped, err)
	}
	if _, err := store.pool.Exec(t.Context(), "update index_jobs set run_after=now() where id=$1", job.ID); err != nil {
		t.Fatal(err)
	}
	retry, err := store.ClaimIndex(t.Context(), "indexer-2")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.PublishIndex(t.Context(), retry.ID, retry.LeaseOwner); err != nil {
		t.Fatal(err)
	}
	var count int
	var owner string
	if err := store.pool.QueryRow(t.Context(), `select count(*),min(lease_owner) from graph_jobs
		where repository_id=$1 and target_sha=$2 and state='running'`, retry.RepositoryID, retry.TargetSHA).Scan(&count, &owner); err != nil {
		t.Fatal(err)
	}
	if count != 1 || owner != retry.LeaseOwner {
		t.Fatalf("running=%d owner=%q", count, owner)
	}
}

func TestCompleteIndexDoesNotLeaveClaimableGraphJob(t *testing.T) {
	store, job := runningIndexJob(t)
	if err := store.CompleteIndex(t.Context(), job.ID, job.LeaseOwner); err != nil {
		t.Fatal(err)
	}
	if graph, err := store.ClaimGraph(t.Context(), "scanner-1"); !errors.Is(err, ErrNoJob) {
		t.Fatalf("ClaimGraph() = %#v, %v", graph, err)
	}
}

func TestCompleteIndexDoesNotQueueGraphAlreadyRunningForSHA(t *testing.T) {
	store, first := runningIndexJob(t)
	if err := store.CompleteIndex(t.Context(), first.ID, first.LeaseOwner); err != nil {
		t.Fatal(err)
	}
	if _, err := store.pool.Exec(t.Context(), `update graph_jobs set state='queued',lease_owner=null,lease_expires_at=null where repository_id=$1`, first.RepositoryID); err != nil {
		t.Fatal(err)
	}
	graph, err := store.ClaimGraph(t.Context(), "scanner-1")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.EnqueueIndex(t.Context(), IndexRequest{RepositoryID: first.RepositoryID, TargetSHA: first.TargetSHA}); err != nil {
		t.Fatal(err)
	}
	repeated, err := store.ClaimIndex(t.Context(), "indexer-1")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.CompleteIndex(t.Context(), repeated.ID, repeated.LeaseOwner); err != nil {
		t.Fatal(err)
	}
	var running, queued int
	if err := store.pool.QueryRow(t.Context(), `select
		count(*) filter (where state='running' and target_sha=$2),
		count(*) filter (where state='queued' and target_sha=$2)
		from graph_jobs where repository_id=$1`, graph.RepositoryID, graph.TargetSHA).Scan(&running, &queued); err != nil {
		t.Fatal(err)
	}
	if running != 0 || queued != 0 {
		t.Fatalf("running=%d queued=%d", running, queued)
	}
}

func TestCompleteIndexRollsBackWhenGraphEnqueueFails(t *testing.T) {
	store, job := runningIndexJob(t)
	if _, err := store.pool.Exec(t.Context(), `create function reject_graph_enqueue() returns trigger language plpgsql as $$
		begin raise exception 'reject graph enqueue'; end $$;
		create trigger reject_graph_enqueue before insert on graph_jobs
		for each row execute function reject_graph_enqueue()`); err != nil {
		t.Fatal(err)
	}
	if err := store.CompleteIndex(t.Context(), job.ID, job.LeaseOwner); err == nil {
		t.Fatal("CompleteIndex() succeeded")
	}
	var indexedSHA, state string
	if err := store.pool.QueryRow(t.Context(), "select indexed_sha from repositories where id=$1", job.RepositoryID).Scan(&indexedSHA); err != nil {
		t.Fatal(err)
	}
	if err := store.pool.QueryRow(t.Context(), "select state from index_jobs where id=$1", job.ID).Scan(&state); err != nil {
		t.Fatal(err)
	}
	if indexedSHA != shaC || state != "running" {
		t.Fatalf("indexed_sha=%q state=%q", indexedSHA, state)
	}
}

func TestGraphQueueCoalescesNewestIndexedSHA(t *testing.T) {
	store, first := runningIndexJob(t)
	if err := store.CompleteIndex(t.Context(), first.ID, first.LeaseOwner); err != nil {
		t.Fatal(err)
	}
	queueLegacyGraph(t, store, first.RepositoryID)
	for range 2 {
		if err := store.EnqueueIndex(t.Context(), IndexRequest{RepositoryID: first.RepositoryID, TargetSHA: shaB}); err != nil {
			t.Fatal(err)
		}
		job, err := store.ClaimIndex(t.Context(), "indexer-1")
		if err != nil {
			t.Fatal(err)
		}
		if err := store.CompleteIndex(t.Context(), job.ID, job.LeaseOwner); err != nil {
			t.Fatal(err)
		}
		queueLegacyGraph(t, store, job.RepositoryID)
	}
	var queued, superseded int
	if err := store.pool.QueryRow(t.Context(), `select
		count(*) filter (where state='queued' and target_sha=$2),
		count(*) filter (where state='superseded' and target_sha=$3)
		from graph_jobs where repository_id=$1`, first.RepositoryID, shaB, shaA).Scan(&queued, &superseded); err != nil {
		t.Fatal(err)
	}
	if queued != 1 || superseded != 1 {
		t.Fatalf("queued=%d superseded=%d", queued, superseded)
	}
}

func TestGraphQueueClaimsOnceAndRequiresLiveLease(t *testing.T) {
	store, index := runningIndexJob(t)
	if err := store.CompleteIndex(t.Context(), index.ID, index.LeaseOwner); err != nil {
		t.Fatal(err)
	}
	queueLegacyGraph(t, store, index.RepositoryID)
	var successes atomic.Int32
	errs := make(chan error, 20)
	var group sync.WaitGroup
	for i := range 20 {
		group.Add(1)
		go func() {
			defer group.Done()
			job, err := store.ClaimGraph(t.Context(), fmt.Sprintf("scanner-%d", i))
			if err == nil {
				if job.RepositoryID != index.RepositoryID || job.Attempt != 1 || job.State != "running" {
					errs <- fmt.Errorf("bad job: %#v", job)
					return
				}
				successes.Add(1)
			} else if !errors.Is(err, ErrNoJob) {
				errs <- err
			}
		}()
	}
	group.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
	if successes.Load() != 1 {
		t.Fatalf("successful claims=%d", successes.Load())
	}
	var job GraphJob
	if err := store.pool.QueryRow(t.Context(), `select id, repository_id, target_sha, state, lease_owner,
		attempt, max_attempts, lease_expires_at from graph_jobs where state='running'`).Scan(
		&job.ID, &job.RepositoryID, &job.TargetSHA, &job.State, &job.LeaseOwner,
		&job.Attempt, &job.MaxAttempts, &job.LeaseExpiresAt); err != nil {
		t.Fatal(err)
	}
	if remaining := time.Until(job.LeaseExpiresAt); remaining < 115*time.Second || remaining > 125*time.Second {
		t.Fatalf("claim lease remaining=%s", remaining)
	}
	if err := store.RenewGraphLease(t.Context(), job.ID, "other"); !errors.Is(err, ErrLeaseLost) {
		t.Fatalf("wrong owner error=%v", err)
	}
	if _, err := store.pool.Exec(t.Context(), "update graph_jobs set lease_expires_at=now()+interval '1 second' where id=$1", job.ID); err != nil {
		t.Fatal(err)
	}
	if err := store.RenewGraphLease(t.Context(), job.ID, job.LeaseOwner); err != nil {
		t.Fatal(err)
	}
	if err := store.pool.QueryRow(t.Context(), "select lease_expires_at from graph_jobs where id=$1", job.ID).Scan(&job.LeaseExpiresAt); err != nil {
		t.Fatal(err)
	}
	if remaining := time.Until(job.LeaseExpiresAt); remaining < 115*time.Second || remaining > 125*time.Second {
		t.Fatalf("renewed lease remaining=%s", remaining)
	}
	if _, err := store.pool.Exec(t.Context(), "update graph_jobs set lease_expires_at=now() where id=$1", job.ID); err != nil {
		t.Fatal(err)
	}
	if err := store.RenewGraphLease(t.Context(), job.ID, job.LeaseOwner); !errors.Is(err, ErrLeaseLost) {
		t.Fatalf("expired lease error=%v", err)
	}
}

func TestGraphQueueRetriesFiveAttemptsAndReapsOnce(t *testing.T) {
	store, index := runningIndexJob(t)
	if err := store.CompleteIndex(t.Context(), index.ID, index.LeaseOwner); err != nil {
		t.Fatal(err)
	}
	queueLegacyGraph(t, store, index.RepositoryID)
	job, err := store.ClaimGraph(t.Context(), "scanner-1")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.FailGraph(t.Context(), job.ID, job.LeaseOwner, "temporary", true); err != nil {
		t.Fatal(err)
	}
	for attempt := 2; attempt <= 5; attempt++ {
		if _, err := store.pool.Exec(t.Context(), "update graph_jobs set run_after=now()-interval '1 second' where id=$1", job.ID); err != nil {
			t.Fatal(err)
		}
		job, err = store.ClaimGraph(t.Context(), "scanner-1")
		if err != nil || job.Attempt != attempt {
			t.Fatalf("attempt=%d job=%#v err=%v", attempt, job, err)
		}
		if attempt == 2 {
			if _, err := store.pool.Exec(t.Context(), "update graph_jobs set lease_expires_at=now() where id=$1", job.ID); err != nil {
				t.Fatal(err)
			}
			var reaped atomic.Int64
			var group sync.WaitGroup
			for range 10 {
				group.Add(1)
				go func() {
					defer group.Done()
					count, err := store.ReapExpiredGraph(t.Context(), 1)
					if err != nil {
						t.Error(err)
					}
					reaped.Add(count)
				}()
			}
			group.Wait()
			if reaped.Load() != 1 {
				t.Fatalf("reaped=%d", reaped.Load())
			}
			continue
		}
		if err := store.FailGraph(t.Context(), job.ID, job.LeaseOwner, "temporary", true); err != nil {
			t.Fatal(err)
		}
	}
	var state string
	if err := store.pool.QueryRow(t.Context(), "select state from graph_jobs where id=$1", job.ID).Scan(&state); err != nil || state != "failed" {
		t.Fatalf("state=%q err=%v", state, err)
	}
}

func TestGraphQueueSupersedesStaleFailureAndReportsState(t *testing.T) {
	store, index := runningIndexJob(t)
	if err := store.CompleteIndex(t.Context(), index.ID, index.LeaseOwner); err != nil {
		t.Fatal(err)
	}
	queueLegacyGraph(t, store, index.RepositoryID)
	job, err := store.ClaimGraph(t.Context(), "scanner-1")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.pool.Exec(t.Context(), "update repositories set indexed_sha=$2 where id=$1", job.RepositoryID, shaB); err != nil {
		t.Fatal(err)
	}
	if err := store.FailGraph(t.Context(), job.ID, job.LeaseOwner, "temporary", true); err != nil {
		t.Fatal(err)
	}
	depths, err := store.GraphQueueDepths(t.Context())
	if err != nil || depths["superseded"] != 1 {
		t.Fatalf("depths=%v err=%v", depths, err)
	}
	active, err := store.ActiveGraphJobIDs(t.Context())
	if err != nil || len(active) != 0 {
		t.Fatalf("active=%v err=%v", active, err)
	}
}
