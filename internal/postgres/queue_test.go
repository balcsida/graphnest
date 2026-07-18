//go:build integration

package postgres

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
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
		validSuperseded := desired == shaB && indexed == shaC && state == "superseded" && queued == 1
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
		if _, err := store.pool.Exec(t.Context(), "insert into index_jobs(repository_id,target_sha,state) values($1,$2,'failed')", repositoryID, sha); err != nil {
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
