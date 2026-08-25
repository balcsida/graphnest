//go:build integration && unix

package integration

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"runtime"
	"sync/atomic"
	"testing"
	"time"

	"github.com/balcsida/graphnest/internal/authn"
	"github.com/balcsida/graphnest/internal/authz"
	"github.com/balcsida/graphnest/internal/githubapp"
	"github.com/balcsida/graphnest/internal/indexer"
	"github.com/balcsida/graphnest/internal/postgres"
	"github.com/balcsida/graphnest/internal/repository"
	"github.com/balcsida/graphnest/internal/webhook"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	postgresSHAA = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	postgresSHAB = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	postgresSHAC = "cccccccccccccccccccccccccccccccccccccccc"
)

func TestPostgresConcurrency(t *testing.T) {
	t.Run("delivery dedupe is atomic", testDeliveryDedupe)
	t.Run("workers claim unique jobs", testConcurrentClaims)
	t.Run("pushes coalesce behind running work", testRunningCoalescing)
	t.Run("push size blocks credentials", testPushSizeBlocksCredentials)
	t.Run("lost owners cannot mutate leases", testLeaseOwnerLoss)
	t.Run("worker recovers lease expiring after restart", testLeaseRecoveryAfterRestart)
	t.Run("reapers partition expired work", testConcurrentReapers)
	t.Run("completion wins repository lock", func(t *testing.T) { testCompletionPushOrder(t, true) })
	t.Run("push wins repository lock", func(t *testing.T) { testCompletionPushOrder(t, false) })
	t.Run("installation disable wins completion lock", testDisableCompletionOrder)
	t.Run("installation disable wins claim lock", testDisableClaimOrder)
	t.Run("rename preserves numeric authorization", testRenameAuthorization)
	t.Run("disabled push and queue are inert", testDisabledPushAndQueue)
	t.Run("unavailable pushes are inert", testUnavailablePushes)
	t.Run("disabled state blocks authorization", testDisabledAuthorization)
	t.Run("re-enabled unchanged desired remains claimable", testReenabledRepository)
	t.Run("retention is bounded", testRetention)
}

func testLeaseRecoveryAfterRestart(t *testing.T) {
	h := newPostgresHarness(t)
	repositoryID := h.seedRepository(t, 10, 101)
	if err := h.store.EnqueueIndex(t.Context(), postgres.IndexRequest{RepositoryID: repositoryID, TargetSHA: postgresSHAA}); err != nil {
		t.Fatal(err)
	}
	job, err := h.store.ClaimIndex(t.Context(), "old-owner")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.pool.Exec(t.Context(), "update index_jobs set lease_expires_at=now()+interval '100 milliseconds' where id=$1", job.ID); err != nil {
		t.Fatal(err)
	}
	queue := &controlledQueue{Store: h.store, blockClaim: true}
	worker := &indexer.Worker{
		ID: "new-owner", Queue: queue, Store: h.store, Tokens: integrationTokens{}, Snapshots: integrationGit{}, Zoekt: integrationPublisher{},
		ReapEvery: 10 * time.Millisecond,
	}
	if worked, err := worker.RunOne(t.Context()); err != nil || worked {
		t.Fatalf("startup claim worked=%v err=%v", worked, err)
	}

	deadline := time.Now().Add(2 * time.Second)
	for {
		var expired bool
		if err := h.pool.QueryRow(t.Context(), "select lease_expires_at<=now() from index_jobs where id=$1", job.ID).Scan(&expired); err != nil {
			t.Fatal(err)
		}
		if expired {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("lease did not expire")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if worked, err := worker.RunOne(t.Context()); err != nil || worked {
		t.Fatalf("reap claim worked=%v err=%v", worked, err)
	}
	var state, code string
	if err := h.pool.QueryRow(t.Context(), "select state,error_code from index_jobs where id=$1", job.ID).Scan(&state, &code); err != nil || state != "queued" || code != "lease_expired" {
		t.Fatalf("state=%q code=%q err=%v", state, code, err)
	}
	if _, err := h.pool.Exec(t.Context(), "update index_jobs set run_after=now() where id=$1", job.ID); err != nil {
		t.Fatal(err)
	}
	queue.blockClaim = false
	if worked, err := worker.RunOne(t.Context()); err != nil || !worked {
		t.Fatalf("recovery worked=%v err=%v", worked, err)
	}
	if err := h.pool.QueryRow(t.Context(), "select state,attempt from index_jobs where id=$1", job.ID).Scan(&state, &job.Attempt); err != nil || state != "succeeded" || job.Attempt != 2 {
		t.Fatalf("state=%q attempt=%d err=%v", state, job.Attempt, err)
	}
}

type controlledQueue struct {
	*postgres.Store
	blockClaim bool
}

func (queue *controlledQueue) ClaimIndex(ctx context.Context, owner string) (postgres.IndexJob, error) {
	if queue.blockClaim {
		return postgres.IndexJob{}, postgres.ErrNoJob
	}
	return queue.Store.ClaimIndex(ctx, owner)
}

type integrationTokens struct{ calls *atomic.Int64 }

func (tokens integrationTokens) InstallationToken(context.Context, int64, []int64) (githubapp.Token, error) {
	if tokens.calls != nil {
		tokens.calls.Add(1)
	}
	return githubapp.Token{Value: "token"}, nil
}

type integrationGit struct{ prepares *atomic.Int64 }

func (git integrationGit) Prepare(_ context.Context, request indexer.SnapshotRequest) (indexer.Snapshot, error) {
	if git.prepares != nil {
		git.prepares.Add(1)
	}
	return indexer.Snapshot{Root: "/worktree", RepositoryID: request.RepositoryID, JobID: request.JobID, CommitSHA: request.CommitSHA}, nil
}
func (integrationGit) Cleanup(context.Context, indexer.Snapshot) error        { return nil }
func (integrationGit) CleanupStale(context.Context, indexer.ActiveJobs) error { return nil }
func (integrationGit) FreeSpacePath() string                                  { return "/worktree" }

type integrationPublisher struct{}

func (integrationPublisher) Index(context.Context, repository.Repository, string) error { return nil }
func (integrationPublisher) WaitVisible(context.Context, uint32, string, string) error  { return nil }

func testDeliveryDedupe(t *testing.T) {
	h := newPostgresHarness(t)
	repositoryID := h.seedRepository(t, 10, 101)
	processor := webhook.NewGitHubProcessor(h.store, nil)
	start := make(chan struct{})
	results := make(chan error, 20)
	var inserted atomic.Int64
	for range 20 {
		go func() {
			<-start
			ok, err := processor.Process(t.Context(), pushDelivery("same-delivery", 10, 101, postgresSHAA))
			if ok {
				inserted.Add(1)
			}
			results <- err
		}()
	}
	close(start)
	for range 20 {
		if err := <-results; err != nil {
			t.Fatal(err)
		}
	}
	var deliveries, jobs int
	var desired string
	if err := h.pool.QueryRow(t.Context(), "select count(*) from webhook_deliveries").Scan(&deliveries); err != nil {
		t.Fatal(err)
	}
	if err := h.pool.QueryRow(t.Context(), "select count(*) from index_jobs").Scan(&jobs); err != nil {
		t.Fatal(err)
	}
	if err := h.pool.QueryRow(t.Context(), "select desired_sha from repositories where id=$1", repositoryID).Scan(&desired); err != nil {
		t.Fatal(err)
	}
	if inserted.Load() != 1 || deliveries != 1 || jobs != 1 || desired != postgresSHAA {
		t.Fatalf("inserted=%d deliveries=%d jobs=%d desired=%q", inserted.Load(), deliveries, jobs, desired)
	}
}

func testPushSizeBlocksCredentials(t *testing.T) {
	h := newPostgresHarness(t)
	h.seedRepository(t, 10, 101)
	processor := webhook.NewGitHubProcessor(h.store, nil)
	if inserted, err := processor.Process(t.Context(), pushDeliveryWithSize("oversized", 10, 101, postgresSHAA, 6)); err != nil || !inserted {
		t.Fatalf("push inserted=%v err=%v", inserted, err)
	}

	var tokenCalls, fetchCalls atomic.Int64
	worker := &indexer.Worker{
		ID: "size-worker", Queue: h.store, Store: h.store,
		Tokens: integrationTokens{calls: &tokenCalls}, Snapshots: integrationGit{prepares: &fetchCalls}, Zoekt: integrationPublisher{},
		MaxRepositoryBytes: 5 * 1024, RenewEvery: time.Hour,
	}
	worked, err := worker.RunOne(t.Context())
	if err != nil || !worked {
		t.Fatalf("worker worked=%v err=%v", worked, err)
	}
	var sizeBytes int64
	var state, code string
	if err := h.pool.QueryRow(t.Context(), `select repositories.size_bytes, index_jobs.state, coalesce(index_jobs.error_code, '')
		from repositories join index_jobs on index_jobs.repository_id=repositories.id where repositories.github_id=101`).Scan(&sizeBytes, &state, &code); err != nil {
		t.Fatal(err)
	}
	if sizeBytes != 6*1024 || state != "failed" || code != "repository_too_large" || tokenCalls.Load() != 0 || fetchCalls.Load() != 0 {
		t.Fatalf("size=%d state=%q code=%q tokens=%d fetches=%d", sizeBytes, state, code, tokenCalls.Load(), fetchCalls.Load())
	}
}

func testConcurrentClaims(t *testing.T) {
	h := newPostgresHarness(t)
	for i := range 20 {
		repositoryID := h.seedRepository(t, 10, int64(100+i))
		if err := h.store.EnqueueIndex(t.Context(), postgres.IndexRequest{RepositoryID: repositoryID, TargetSHA: fmt.Sprintf("%040x", i+1)}); err != nil {
			t.Fatal(err)
		}
	}
	start := make(chan struct{})
	results := make(chan claimResult, 20)
	for i := range 20 {
		go func() {
			<-start
			job, err := h.store.ClaimIndex(t.Context(), fmt.Sprintf("worker-%d", i))
			results <- claimResult{job: job, err: err}
		}()
	}
	close(start)
	ids := make(map[int64]struct{}, 20)
	for range 20 {
		result := <-results
		if result.err != nil {
			t.Fatal(result.err)
		}
		ids[result.job.ID] = struct{}{}
	}
	var queued, running int
	if err := h.pool.QueryRow(t.Context(), "select count(*) filter (where state='queued'), count(*) filter (where state='running') from index_jobs").Scan(&queued, &running); err != nil {
		t.Fatal(err)
	}
	if len(ids) != 20 || queued != 0 || running != 20 {
		t.Fatalf("unique=%d queued=%d running=%d", len(ids), queued, running)
	}
}

func testRunningCoalescing(t *testing.T) {
	h := newPostgresHarness(t)
	repositoryID := h.seedRepository(t, 10, 101)
	processor := webhook.NewGitHubProcessor(h.store, nil)
	processPush(t, processor, "a", 10, 101, postgresSHAA)
	jobA, err := h.store.ClaimIndex(t.Context(), "owner-a")
	if err != nil {
		t.Fatal(err)
	}
	processPush(t, processor, "b", 10, 101, postgresSHAB)
	processPush(t, processor, "c", 10, 101, postgresSHAC)
	if err := h.store.CompleteIndex(t.Context(), jobA.ID, "owner-a"); err != nil {
		t.Fatal(err)
	}
	var desired, queuedSHA, state string
	if err := h.pool.QueryRow(t.Context(), "select desired_sha from repositories where id=$1", repositoryID).Scan(&desired); err != nil {
		t.Fatal(err)
	}
	if err := h.pool.QueryRow(t.Context(), "select target_sha from index_jobs where repository_id=$1 and state='queued'", repositoryID).Scan(&queuedSHA); err != nil {
		t.Fatal(err)
	}
	if err := h.pool.QueryRow(t.Context(), "select state from index_jobs where id=$1", jobA.ID).Scan(&state); err != nil {
		t.Fatal(err)
	}
	jobC, err := h.store.ClaimIndex(t.Context(), "owner-c")
	if err != nil {
		t.Fatal(err)
	}
	if desired != postgresSHAC || queuedSHA != postgresSHAC || state != "superseded" || jobC.TargetSHA != postgresSHAC {
		t.Fatalf("desired=%q queued=%q old=%q new=%#v", desired, queuedSHA, state, jobC)
	}
}

func testLeaseOwnerLoss(t *testing.T) {
	h := newPostgresHarness(t)
	repositoryID := h.seedRepository(t, 10, 101)
	if err := h.store.EnqueueIndex(t.Context(), postgres.IndexRequest{RepositoryID: repositoryID, TargetSHA: postgresSHAA}); err != nil {
		t.Fatal(err)
	}
	job, err := h.store.ClaimIndex(t.Context(), "owner")
	if err != nil {
		t.Fatal(err)
	}
	for _, operation := range []func() error{
		func() error { return h.store.RenewLease(t.Context(), job.ID, "wrong") },
		func() error { return h.store.CompleteIndex(t.Context(), job.ID, "wrong") },
		func() error { return h.store.FailIndex(t.Context(), job.ID, "wrong", "failure", true) },
	} {
		if err := operation(); !errors.Is(err, postgres.ErrLeaseLost) {
			t.Fatalf("wrong owner: %v", err)
		}
	}
	if _, err := h.pool.Exec(t.Context(), "update index_jobs set lease_expires_at=now()-interval '1 second' where id=$1", job.ID); err != nil {
		t.Fatal(err)
	}
	if err := h.store.CompleteIndex(t.Context(), job.ID, "owner"); !errors.Is(err, postgres.ErrLeaseLost) {
		t.Fatalf("expired owner: %v", err)
	}
	if count, err := h.store.ReapExpired(t.Context(), 1); err != nil || count != 1 {
		t.Fatalf("reaped=%d err=%v", count, err)
	}
	if _, err := h.pool.Exec(t.Context(), "update index_jobs set run_after=now()-interval '1 second' where id=$1", job.ID); err != nil {
		t.Fatal(err)
	}
	recovered, err := h.store.ClaimIndex(t.Context(), "new-owner")
	if err != nil || recovered.ID != job.ID || recovered.Attempt != 2 {
		t.Fatalf("recovered=%#v err=%v", recovered, err)
	}
}

func testConcurrentReapers(t *testing.T) {
	h := newPostgresHarness(t)
	for i := range 20 {
		repositoryID := h.seedRepository(t, 10, int64(100+i))
		if err := h.store.EnqueueIndex(t.Context(), postgres.IndexRequest{RepositoryID: repositoryID, TargetSHA: fmt.Sprintf("%040x", i+1)}); err != nil {
			t.Fatal(err)
		}
		if _, err := h.store.ClaimIndex(t.Context(), fmt.Sprintf("owner-%d", i)); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := h.pool.Exec(t.Context(), "update index_jobs set lease_expires_at=now()-interval '1 second' where state='running'"); err != nil {
		t.Fatal(err)
	}
	start := make(chan struct{})
	results := make(chan reapResult, 10)
	for range 10 {
		go func() {
			<-start
			count, err := h.store.ReapExpired(t.Context(), 20)
			results <- reapResult{count: count, err: err}
		}()
	}
	close(start)
	var total int64
	for range 10 {
		result := <-results
		if result.err != nil {
			t.Fatal(result.err)
		}
		total += result.count
	}
	var queued, attempts int
	if err := h.pool.QueryRow(t.Context(), "select count(*) filter (where state='queued'), sum(attempt) from index_jobs").Scan(&queued, &attempts); err != nil {
		t.Fatal(err)
	}
	if total != 20 || queued != 20 || attempts != 20 {
		t.Fatalf("reaped=%d queued=%d attempts=%d", total, queued, attempts)
	}
}

func testCompletionPushOrder(t *testing.T, completionFirst bool) {
	h := newPostgresHarness(t)
	repositoryID := h.seedRepository(t, 10, 101)
	if _, err := h.pool.Exec(t.Context(), "update repositories set indexed_sha=$2 where id=$1", repositoryID, postgresSHAC); err != nil {
		t.Fatal(err)
	}
	if err := h.store.EnqueueIndex(t.Context(), postgres.IndexRequest{RepositoryID: repositoryID, TargetSHA: postgresSHAA}); err != nil {
		t.Fatal(err)
	}
	job, err := h.store.ClaimIndex(t.Context(), "owner")
	if err != nil {
		t.Fatal(err)
	}
	barrier, err := h.pool.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = barrier.Rollback(context.Background()) })
	if _, err := barrier.Exec(t.Context(), "select id from repositories where id=$1 for update", repositoryID); err != nil {
		t.Fatal(err)
	}
	processor := webhook.NewGitHubProcessor(h.store, nil)
	complete := func() error { return h.store.CompleteIndex(t.Context(), job.ID, "owner") }
	push := func() error {
		_, err := processor.Process(t.Context(), pushDelivery("new-push", 10, 101, postgresSHAB))
		return err
	}
	first, second := complete, push
	if !completionFirst {
		first, second = push, complete
	}
	results := make(chan error, 2)
	go func() { results <- first() }()
	h.waitForLockWaiters(t, 1)
	go func() { results <- second() }()
	h.waitForLockWaiters(t, 2)
	if err := barrier.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := <-results; err != nil {
			t.Fatal(err)
		}
	}
	var desired, indexed, state string
	var queued int
	if err := h.pool.QueryRow(t.Context(), "select desired_sha, indexed_sha from repositories where id=$1", repositoryID).Scan(&desired, &indexed); err != nil {
		t.Fatal(err)
	}
	if err := h.pool.QueryRow(t.Context(), "select state from index_jobs where id=$1", job.ID).Scan(&state); err != nil {
		t.Fatal(err)
	}
	if err := h.pool.QueryRow(t.Context(), "select count(*) from index_jobs where repository_id=$1 and state='queued' and target_sha=$2", repositoryID, postgresSHAB).Scan(&queued); err != nil {
		t.Fatal(err)
	}
	validCompletion := state == "succeeded" && indexed == postgresSHAA
	validSuperseded := state == "superseded" && (indexed == postgresSHAA || indexed == postgresSHAC)
	if desired != postgresSHAB || (!validCompletion && !validSuperseded) || queued != 1 {
		t.Fatalf("desired=%q indexed=%q state=%q queued=%d", desired, indexed, state, queued)
	}
}

func testDisableCompletionOrder(t *testing.T) {
	h := newPostgresHarness(t)
	repositoryID := h.seedRepository(t, 10, 101)
	if err := h.store.EnqueueIndex(t.Context(), postgres.IndexRequest{RepositoryID: repositoryID, TargetSHA: postgresSHAA}); err != nil {
		t.Fatal(err)
	}
	job, err := h.store.ClaimIndex(t.Context(), "owner")
	if err != nil {
		t.Fatal(err)
	}
	barrier, err := h.pool.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = barrier.Rollback(context.Background()) })
	if _, err := barrier.Exec(t.Context(), "select id from installations where github_id=10 for update"); err != nil {
		t.Fatal(err)
	}
	results := make(chan error, 2)
	go func() { results <- h.store.DisableInstallation(t.Context(), 10, "deleted") }()
	h.waitForLockWaiters(t, 1)
	go func() { results <- h.store.CompleteIndex(t.Context(), job.ID, "owner") }()
	h.waitForLockWaiters(t, 2)
	if err := barrier.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := <-results; err != nil {
			t.Fatal(err)
		}
	}
	var indexedSHA, jobState, errorCode string
	if err := h.pool.QueryRow(t.Context(), "select coalesce(indexed_sha, '') from repositories where id=$1", repositoryID).Scan(&indexedSHA); err != nil {
		t.Fatal(err)
	}
	if err := h.pool.QueryRow(t.Context(), "select state, coalesce(error_code, '') from index_jobs where id=$1", job.ID).Scan(&jobState, &errorCode); err != nil {
		t.Fatal(err)
	}
	if indexedSHA != "" || jobState != "superseded" || errorCode != "repository_unavailable" {
		t.Fatalf("indexed=%q job_state=%q code=%q", indexedSHA, jobState, errorCode)
	}
}

func testDisableClaimOrder(t *testing.T) {
	h := newPostgresHarness(t)
	repositoryID := h.seedRepository(t, 10, 101)
	if err := h.store.EnqueueIndex(t.Context(), postgres.IndexRequest{RepositoryID: repositoryID, TargetSHA: postgresSHAA}); err != nil {
		t.Fatal(err)
	}
	barrier, err := h.pool.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = barrier.Rollback(context.Background()) })
	if _, err := barrier.Exec(t.Context(), "select id from installations where github_id=10 for update"); err != nil {
		t.Fatal(err)
	}
	disabled := make(chan error, 1)
	go func() { disabled <- h.store.DisableInstallation(t.Context(), 10, "deleted") }()
	h.waitForLockWaiters(t, 1)
	var tokenCalls, fetchCalls atomic.Int64
	worker := &indexer.Worker{
		ID: "owner", Queue: h.store, Store: h.store,
		Tokens: integrationTokens{calls: &tokenCalls}, Snapshots: integrationGit{prepares: &fetchCalls}, Zoekt: integrationPublisher{},
		RenewEvery: time.Hour,
	}
	type workerResult struct {
		worked bool
		err    error
	}
	result := make(chan workerResult, 1)
	go func() {
		worked, err := worker.RunOne(t.Context())
		result <- workerResult{worked: worked, err: err}
	}()
	h.waitForLockWaiters(t, 2)
	if err := barrier.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := <-disabled; err != nil {
		t.Fatal(err)
	}
	outcome := <-result
	if outcome.err != nil || outcome.worked || tokenCalls.Load() != 0 || fetchCalls.Load() != 0 {
		t.Fatalf("worked=%v err=%v tokens=%d fetches=%d", outcome.worked, outcome.err, tokenCalls.Load(), fetchCalls.Load())
	}
}

func testRenameAuthorization(t *testing.T) {
	h := newPostgresHarness(t)
	installation := githubapp.Installation{ID: 10, AccountLogin: "acme", AccountType: "Organization", Status: "active"}
	repository := githubRepository(101, "acme", "old", postgresSHAA)
	if err := h.store.ReconcileInstallation(t.Context(), installation, []githubapp.Repository{repository}); err != nil {
		t.Fatal(err)
	}
	var internalID, zoektID int64
	if err := h.pool.QueryRow(t.Context(), "select id, zoekt_repo_id from repositories where github_id=101").Scan(&internalID, &zoektID); err != nil {
		t.Fatal(err)
	}
	repository.Owner, repository.Name = "renamed", "new"
	replacement := githubRepository(102, "acme", "old", postgresSHAB)
	if err := h.store.ReconcileInstallation(t.Context(), installation, []githubapp.Repository{repository, replacement}); err != nil {
		t.Fatal(err)
	}
	var currentInternal, currentZoekt int64
	if err := h.pool.QueryRow(t.Context(), "select id, zoekt_repo_id from repositories where github_id=101").Scan(&currentInternal, &currentZoekt); err != nil {
		t.Fatal(err)
	}
	principal := authn.Principal{InstallationID: 10, RepositoryIDs: []int64{101}}
	authorizer := authz.NewPostgres(h.store)
	old, err := authorizer.AuthorizedRepositories(t.Context(), principal, authz.RepositorySelection{Names: []string{"acme/old"}})
	if err != nil {
		t.Fatal(err)
	}
	current, err := authorizer.AuthorizedRepositories(t.Context(), principal, authz.RepositorySelection{Names: []string{"renamed/new"}})
	if err != nil {
		t.Fatal(err)
	}
	if internalID != currentInternal || zoektID != currentZoekt || len(old) != 0 || len(current) != 1 || current[0].GitHubID != 101 {
		t.Fatalf("ids=(%d,%d)->(%d,%d) old=%#v current=%#v", internalID, zoektID, currentInternal, currentZoekt, old, current)
	}
}

func testDisabledPushAndQueue(t *testing.T) {
	h := newPostgresHarness(t)
	repositoryID := h.seedRepository(t, 10, 101)
	if err := h.store.EnqueueIndex(t.Context(), postgres.IndexRequest{RepositoryID: repositoryID, TargetSHA: postgresSHAA}); err != nil {
		t.Fatal(err)
	}
	var jobID int64
	if err := h.pool.QueryRow(t.Context(), "select id from index_jobs where repository_id=$1", repositoryID).Scan(&jobID); err != nil {
		t.Fatal(err)
	}
	processor := webhook.NewGitHubProcessor(h.store, nil)
	body := []byte(`{"action":"deleted","installation":{"id":10},"repository":{"id":101}}`)
	if inserted, err := processor.Process(t.Context(), webhook.Delivery{ID: "disable-before-claim", Event: "repository", Body: body}); err != nil || !inserted {
		t.Fatalf("disable inserted=%v err=%v", inserted, err)
	}
	var state, code string
	if err := h.pool.QueryRow(t.Context(), "select state, coalesce(error_code, '') from index_jobs where id=$1", jobID).Scan(&state, &code); err != nil {
		t.Fatal(err)
	}
	if state != "superseded" || code != "repository_unavailable" {
		t.Errorf("disabled queued job state=%q code=%q", state, code)
	}
	if inserted, err := processor.Process(t.Context(), pushDeliveryWithSize("disabled-push", 10, 101, postgresSHAB, 7)); err != nil || !inserted {
		t.Fatalf("push inserted=%v err=%v", inserted, err)
	}
	var sizeBytes int64
	var desiredSHA string
	if err := h.pool.QueryRow(t.Context(), "select size_bytes, desired_sha from repositories where id=$1", repositoryID).Scan(&sizeBytes, &desiredSHA); err != nil {
		t.Fatal(err)
	}
	var activeJobs int
	if err := h.pool.QueryRow(t.Context(), "select count(*) from index_jobs where repository_id=$1 and state in ('queued','running')", repositoryID).Scan(&activeJobs); err != nil {
		t.Fatal(err)
	}
	if sizeBytes != 0 || desiredSHA != postgresSHAA || activeJobs != 0 {
		t.Errorf("size=%d desired=%q active_jobs=%d", sizeBytes, desiredSHA, activeJobs)
	}
	var tokenCalls, fetchCalls atomic.Int64
	worker := &indexer.Worker{
		ID: "disabled-worker", Queue: h.store, Store: h.store,
		Tokens: integrationTokens{calls: &tokenCalls}, Snapshots: integrationGit{prepares: &fetchCalls}, Zoekt: integrationPublisher{},
		RenewEvery: time.Hour,
	}
	worked, err := worker.RunOne(t.Context())
	if err != nil || worked || tokenCalls.Load() != 0 || fetchCalls.Load() != 0 {
		t.Errorf("worker worked=%v err=%v tokens=%d fetches=%d", worked, err, tokenCalls.Load(), fetchCalls.Load())
	}
	if _, err := h.store.ClaimIndex(t.Context(), "disabled-claim"); !errors.Is(err, postgres.ErrNoJob) {
		t.Errorf("disabled claim err=%v", err)
	}
}

func testUnavailablePushes(t *testing.T) {
	for _, variant := range []string{"archived repository", "inactive installation"} {
		t.Run(variant, func(t *testing.T) {
			h := newPostgresHarness(t)
			repositoryID := h.seedRepository(t, 10, 101)
			if _, err := h.pool.Exec(t.Context(), "update repositories set desired_sha=$2 where id=$1", repositoryID, postgresSHAA); err != nil {
				t.Fatal(err)
			}
			if variant == "archived repository" {
				if _, err := h.pool.Exec(t.Context(), "update repositories set archived=true where id=$1", repositoryID); err != nil {
					t.Fatal(err)
				}
			} else if _, err := h.pool.Exec(t.Context(), "update installations set status='suspended' where github_id=10"); err != nil {
				t.Fatal(err)
			}
			processor := webhook.NewGitHubProcessor(h.store, nil)
			if inserted, err := processor.Process(t.Context(), pushDeliveryWithSize("unavailable-push", 10, 101, postgresSHAB, 7)); err != nil || !inserted {
				t.Fatalf("push inserted=%v err=%v", inserted, err)
			}
			var sizeBytes int64
			var desiredSHA string
			var jobs, deliveries int
			if err := h.pool.QueryRow(t.Context(), "select size_bytes, desired_sha from repositories where id=$1", repositoryID).Scan(&sizeBytes, &desiredSHA); err != nil {
				t.Fatal(err)
			}
			if err := h.pool.QueryRow(t.Context(), "select count(*) from index_jobs").Scan(&jobs); err != nil {
				t.Fatal(err)
			}
			if err := h.pool.QueryRow(t.Context(), "select count(*) from webhook_deliveries where delivery_id='unavailable-push'").Scan(&deliveries); err != nil {
				t.Fatal(err)
			}
			if sizeBytes != 0 || desiredSHA != postgresSHAA || jobs != 0 || deliveries != 1 {
				t.Fatalf("size=%d desired=%q jobs=%d deliveries=%d", sizeBytes, desiredSHA, jobs, deliveries)
			}
		})
	}
}

func testDisabledAuthorization(t *testing.T) {
	h := newPostgresHarness(t)
	repositoryID := h.seedRepository(t, 10, 101)
	if err := h.store.EnqueueIndex(t.Context(), postgres.IndexRequest{RepositoryID: repositoryID, TargetSHA: postgresSHAA}); err != nil {
		t.Fatal(err)
	}
	job, err := h.store.ClaimIndex(t.Context(), "owner")
	if err != nil {
		t.Fatal(err)
	}
	processor := webhook.NewGitHubProcessor(h.store, nil)
	body := []byte(`{"action":"deleted","installation":{"id":10},"repository":{"id":101}}`)
	if inserted, err := processor.Process(t.Context(), webhook.Delivery{ID: "delete-repository", Event: "repository", Body: body}); err != nil || !inserted {
		t.Fatalf("delete inserted=%v err=%v", inserted, err)
	}
	principal := authn.Principal{InstallationID: 10, RepositoryIDs: []int64{101}}
	authorizer := authz.NewPostgres(h.store)
	list, err := authorizer.AuthorizedRepositories(t.Context(), principal, authz.RepositorySelection{})
	if err != nil || len(list) != 0 {
		t.Fatalf("list=%#v err=%v", list, err)
	}
	if _, err := authorizer.AuthorizedRepository(t.Context(), principal, 101); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("single repository err=%v", err)
	}
	if err := h.store.CompleteIndex(t.Context(), job.ID, "owner"); err != nil {
		t.Fatal(err)
	}
	var enabled bool
	var status, indexedSHA, jobState, errorCode string
	if err := h.pool.QueryRow(t.Context(), "select enabled, status, coalesce(indexed_sha, '') from repositories where id=$1", repositoryID).Scan(&enabled, &status, &indexedSHA); err != nil {
		t.Fatal(err)
	}
	if err := h.pool.QueryRow(t.Context(), "select state, coalesce(error_code, '') from index_jobs where id=$1", job.ID).Scan(&jobState, &errorCode); err != nil {
		t.Fatal(err)
	}
	if enabled || status != "disabled" || indexedSHA != "" || jobState != "superseded" || errorCode != "repository_unavailable" {
		t.Fatalf("enabled=%v status=%q indexed=%q job_state=%q code=%q", enabled, status, indexedSHA, jobState, errorCode)
	}
	installationBody := []byte(`{"action":"deleted","installation":{"id":10}}`)
	if inserted, err := processor.Process(t.Context(), webhook.Delivery{ID: "delete-installation", Event: "installation", Body: installationBody}); err != nil || !inserted {
		t.Fatalf("installation inserted=%v err=%v", inserted, err)
	}
	if list, err := authorizer.AuthorizedRepositories(t.Context(), principal, authz.RepositorySelection{}); err != nil || len(list) != 0 {
		t.Fatalf("installation list=%#v err=%v", list, err)
	}
}

func testReenabledRepository(t *testing.T) {
	for _, variant := range []string{"complete", "fail", "reap"} {
		t.Run(variant, func(t *testing.T) {
			h := newPostgresHarness(t)
			repositoryID := h.seedRepository(t, 10, 101)
			if err := h.store.EnqueueIndex(t.Context(), postgres.IndexRequest{RepositoryID: repositoryID, TargetSHA: postgresSHAA}); err != nil {
				t.Fatal(err)
			}
			job, err := h.store.ClaimIndex(t.Context(), "owner")
			if err != nil {
				t.Fatal(err)
			}
			processor := webhook.NewGitHubProcessor(h.store, nil)
			body := []byte(`{"action":"deleted","installation":{"id":10},"repository":{"id":101}}`)
			event := "repository"
			if variant == "fail" {
				body = []byte(`{"action":"deleted","installation":{"id":10}}`)
				event = "installation"
			}
			if inserted, err := processor.Process(t.Context(), webhook.Delivery{ID: "disable", Event: event, Body: body}); err != nil || !inserted {
				t.Fatalf("disable inserted=%v err=%v", inserted, err)
			}
			switch variant {
			case "complete":
				err = h.store.CompleteIndex(t.Context(), job.ID, "owner")
			case "fail":
				err = h.store.FailIndex(t.Context(), job.ID, "owner", "temporary", true)
			case "reap":
				if _, err = h.pool.Exec(t.Context(), "update index_jobs set lease_expires_at=now()-interval '1 second' where id=$1", job.ID); err == nil {
					var count int64
					count, err = h.store.ReapExpired(t.Context(), 1)
					if err == nil && count != 1 {
						err = fmt.Errorf("reaped=%d", count)
					}
				}
			}
			if err != nil {
				t.Fatal(err)
			}
			var state, errorCode string
			if err := h.pool.QueryRow(t.Context(), "select state, coalesce(error_code, '') from index_jobs where id=$1", job.ID).Scan(&state, &errorCode); err != nil {
				t.Fatal(err)
			}
			if state != "superseded" || errorCode != "repository_unavailable" {
				t.Fatalf("disabled job state=%q code=%q", state, errorCode)
			}

			installation := githubapp.Installation{ID: 10, AccountLogin: "acme", AccountType: "Organization", Status: "active"}
			repository := githubRepository(101, "acme", "repo-101", postgresSHAA)
			if err := h.store.ReconcileInstallation(t.Context(), installation, []githubapp.Repository{repository}); err != nil {
				t.Fatal(err)
			}
			if err := h.store.ReconcileInstallation(t.Context(), installation, []githubapp.Repository{repository}); err != nil {
				t.Fatal(err)
			}
			var queued int
			if err := h.pool.QueryRow(t.Context(), "select count(*) from index_jobs where repository_id=$1 and state='queued'", repositoryID).Scan(&queued); err != nil {
				t.Fatal(err)
			}
			retry, err := h.store.ClaimIndex(t.Context(), "new-owner")
			if err != nil || retry.TargetSHA != postgresSHAA || queued != 1 {
				t.Fatalf("queued=%d retry=%#v err=%v", queued, retry, err)
			}
			if err := h.store.CompleteIndex(t.Context(), retry.ID, "new-owner"); err != nil {
				t.Fatal(err)
			}
			if err := h.store.ReconcileInstallation(t.Context(), installation, []githubapp.Repository{repository}); err != nil {
				t.Fatal(err)
			}
			if _, err := h.store.ClaimIndex(t.Context(), "unwanted"); !errors.Is(err, postgres.ErrNoJob) {
				t.Fatalf("ready same-SHA claim err=%v", err)
			}
		})
	}
}

func testRetention(t *testing.T) {
	h := newPostgresHarness(t)
	repositoryID := h.seedRepository(t, 10, 101)
	installationID := int64(10)
	for i := range 2 {
		if _, err := h.store.ApplyDelivery(t.Context(), postgres.Delivery{ID: fmt.Sprintf("delivery-%d", i), Event: "push", State: "accepted", InstallationID: &installationID}, nil); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := h.pool.Exec(t.Context(), "update webhook_deliveries set received_at=now()-interval '31 days' where delivery_id='delivery-0'"); err != nil {
		t.Fatal(err)
	}
	for i := range 102 {
		if _, err := h.pool.Exec(t.Context(), "insert into index_jobs(repository_id,target_sha,state,updated_at) values($1,$2,'failed',now()+$3*interval '1 second')", repositoryID, fmt.Sprintf("%040x", i+1), i); err != nil {
			t.Fatal(err)
		}
	}
	deliveries, jobs, err := h.store.Prune(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	var remainingDeliveries, remainingJobs int
	if err := h.pool.QueryRow(t.Context(), "select count(*) from webhook_deliveries").Scan(&remainingDeliveries); err != nil {
		t.Fatal(err)
	}
	if err := h.pool.QueryRow(t.Context(), "select count(*) from index_jobs").Scan(&remainingJobs); err != nil {
		t.Fatal(err)
	}
	if deliveries != 1 || jobs != 2 || remainingDeliveries != 1 || remainingJobs != 100 {
		t.Fatalf("pruned=(%d,%d) remaining=(%d,%d)", deliveries, jobs, remainingDeliveries, remainingJobs)
	}
}

type postgresHarness struct {
	pool   *pgxpool.Pool
	store  *postgres.Store
	schema string
}

func newPostgresHarness(t *testing.T) *postgresHarness {
	t.Helper()
	dsn := os.Getenv("GREPNEST_TEST_POSTGRES_DSN")
	if dsn == "" {
		dsn = os.Getenv("GREPNEST_TEST_DATABASE_URL")
	}
	if dsn == "" {
		if os.Getenv("GREPNEST_REQUIRE_POSTGRES") == "1" {
			t.Fatal("GREPNEST_TEST_POSTGRES_DSN is not set")
		}
		t.Skip("GREPNEST_TEST_POSTGRES_DSN is not set")
	}
	bytes := make([]byte, 8)
	if _, err := rand.Read(bytes); err != nil {
		t.Fatal(err)
	}
	schema := "grepnest_cross_" + hex.EncodeToString(bytes)
	admin, err := pgxpool.New(t.Context(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(admin.Close)
	identifier := pgx.Identifier{schema}.Sanitize()
	if _, err := admin.Exec(t.Context(), "create schema "+identifier); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if _, err := admin.Exec(ctx, "drop schema "+identifier+" cascade"); err != nil {
			t.Errorf("drop schema %s: %v", schema, err)
		}
	})
	config, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	config.MaxConns = 64
	config.ConnConfig.RuntimeParams["application_name"] = schema
	config.AfterConnect = func(ctx context.Context, connection *pgx.Conn) error {
		_, err := connection.Exec(ctx, "set search_path to "+identifier)
		return err
	}
	pool, err := pgxpool.NewWithConfig(t.Context(), config)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	if err := postgres.Migrate(t.Context(), pool); err != nil {
		t.Fatal(err)
	}
	return &postgresHarness{pool: pool, store: postgres.New(pool), schema: schema}
}

func (h *postgresHarness) seedRepository(t *testing.T, installationID, repositoryID int64) int64 {
	t.Helper()
	if err := h.store.UpsertInstallation(t.Context(), postgres.InstallationUpdate{GitHubID: installationID, AccountLogin: "acme", AccountType: "Organization", Status: "active"}); err != nil {
		t.Fatal(err)
	}
	repository, err := h.store.UpsertRepository(t.Context(), postgres.RepositoryUpdate{GitHubID: repositoryID, InstallationID: installationID, Owner: "acme", Name: fmt.Sprintf("repo-%d", repositoryID), CloneURL: "https://example.invalid/repo.git", WebURL: "https://example.invalid/repo", DefaultBranch: "main", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	return repository.ID
}

func (h *postgresHarness) waitForLockWaiters(t *testing.T, want int) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	for ctx.Err() == nil {
		var count int
		if err := h.pool.QueryRow(ctx, "select count(*) from pg_stat_activity where application_name=$1 and wait_event_type='Lock'", h.schema).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count >= want {
			return
		}
		runtime.Gosched()
	}
	t.Fatalf("wanted %d PostgreSQL lock waiters", want)
}

func pushDelivery(id string, installationID, repositoryID int64, sha string) webhook.Delivery {
	return pushDeliveryWithSize(id, installationID, repositoryID, sha, 1)
}

func pushDeliveryWithSize(id string, installationID, repositoryID int64, sha string, sizeKB int64) webhook.Delivery {
	return webhook.Delivery{ID: id, Event: "push", Body: []byte(fmt.Sprintf(`{"installation":{"id":%d},"repository":{"id":%d,"size":%d},"ref":"refs/heads/main","after":%q}`, installationID, repositoryID, sizeKB, sha))}
}

func processPush(t *testing.T, processor *webhook.GitHubProcessor, id string, installationID, repositoryID int64, sha string) {
	t.Helper()
	if inserted, err := processor.Process(t.Context(), pushDelivery(id, installationID, repositoryID, sha)); err != nil || !inserted {
		t.Fatalf("push %s inserted=%v err=%v", id, inserted, err)
	}
}

func githubRepository(id int64, owner, name, sha string) githubapp.Repository {
	return githubapp.Repository{ID: id, InstallationID: 10, Owner: owner, Name: name, CloneURL: "https://example.invalid/repo.git", HTMLURL: "https://example.invalid/repo", DefaultBranch: "main", DefaultSHA: sha}
}

type claimResult struct {
	job postgres.IndexJob
	err error
}

type reapResult struct {
	count int64
	err   error
}
