package graphscanner

import (
	"context"
	"errors"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/grepnest/grepnest/internal/githubapp"
	"github.com/grepnest/grepnest/internal/graphartifact"
	"github.com/grepnest/grepnest/internal/graphscan"
	"github.com/grepnest/grepnest/internal/postgres"
	"github.com/grepnest/grepnest/internal/repository"
)

const testSHA = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

type fakeQueue struct {
	mu              sync.Mutex
	job             postgres.GraphJob
	claimErr        error
	renewErr        error
	completeErr     error
	failErr         error
	events          []string
	completed       int64
	failedCode      string
	failedRetry     bool
	store           *fakeStore
	externalCurrent bool
}

func (queue *fakeQueue) record(event string) {
	queue.mu.Lock()
	defer queue.mu.Unlock()
	queue.events = append(queue.events, event)
}

func (queue *fakeQueue) ClaimGraph(context.Context, string) (postgres.GraphJob, error) {
	queue.record("claim")
	return queue.job, queue.claimErr
}

func (queue *fakeQueue) RenewGraphLease(context.Context, int64, string) error {
	queue.record("renew")
	return queue.renewErr
}

func (queue *fakeQueue) CompleteGraph(_ context.Context, id int64, _ string, artifact graphartifact.Artifact) error {
	queue.record("complete")
	if queue.completeErr != nil {
		return queue.completeErr
	}
	queue.completed = id
	if !queue.externalCurrent {
		queue.store.publishedRepositoryID = artifact.RepositoryID
		queue.store.publishedCommit = artifact.Commit
		queue.store.publishedAnalyzer = artifact.Analyzer.Name
	}
	return nil
}

func (queue *fakeQueue) FailGraph(_ context.Context, _ int64, _ string, code string, retry bool) error {
	queue.record("fail")
	queue.failedCode, queue.failedRetry = code, retry
	return queue.failErr
}

type fakeStore struct {
	queue                 *fakeQueue
	repository            repository.Repository
	indexedSHAs           []string
	err                   error
	reads                 int
	publishedRepositoryID int64
	publishedCommit       string
	publishedAnalyzer     string
}

func (store *fakeStore) RepositoryForIndex(context.Context, int64) (repository.Repository, error) {
	store.queue.record("repository")
	if store.err != nil {
		return repository.Repository{}, store.err
	}
	result := store.repository
	if store.reads < len(store.indexedSHAs) {
		result.IndexedSHA = store.indexedSHAs[store.reads]
	}
	store.reads++
	return result, nil
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
	err          error
	root         string
	cleaned      bool
	cleanupErr   error
	cleanupRoot  error
	cleanupBound bool
}

func (git *fakeGit) PrepareCommit(context.Context, repository.Repository, int64, string, string) (string, string, error) {
	git.queue.record("prepare")
	return "/mirror", git.root, git.err
}

func (git *fakeGit) Cleanup(ctx context.Context, _, _ int64) error {
	git.queue.record("cleanup")
	git.cleaned = true
	git.cleanupRoot = ctx.Err()
	_, git.cleanupBound = ctx.Deadline()
	return git.cleanupErr
}

type fakeAnalyzer struct {
	queue    *fakeQueue
	err      error
	request  graphscan.Request
	block    bool
	started  chan struct{}
	artifact graphartifact.Artifact
	exact    bool
}

func (analyzer *fakeAnalyzer) Scan(ctx context.Context, request graphscan.Request) (graphartifact.Artifact, error) {
	analyzer.queue.record("scan")
	analyzer.request = request
	if analyzer.started != nil {
		close(analyzer.started)
	}
	if analyzer.block {
		<-ctx.Done()
		return graphartifact.Artifact{}, ctx.Err()
	}
	if analyzer.err != nil {
		return graphartifact.Artifact{}, analyzer.err
	}
	artifact := analyzer.artifact
	if !analyzer.exact {
		artifact.RepositoryID = request.RepositoryID
		artifact.Commit = request.Commit
	}
	if artifact.Analyzer.Name == "" {
		artifact.Analyzer = graphartifact.Analyzer{Name: "managed", Version: "1"}
	}
	return artifact, nil
}

func workerFixture() (*Worker, *fakeQueue, *fakeStore, *fakeGit, *fakeAnalyzer) {
	queue := &fakeQueue{job: postgres.GraphJob{ID: 9, RepositoryID: 101, TargetSHA: testSHA}}
	store := &fakeStore{
		queue: queue,
		repository: repository.Repository{
			ID: 101, InstallationID: 11, GitHubID: 202, ZoektID: 303,
			Name: "acme/repo", Branch: "main", WebURL: "https://example.test/acme/repo",
			IndexedSHA: testSHA,
		},
	}
	queue.store = store
	git := &fakeGit{queue: queue, root: "/work"}
	analyzer := &fakeAnalyzer{queue: queue}
	worker := &Worker{
		ID: "scanner-1", Queue: queue, Store: store, Tokens: &fakeTokens{queue: queue},
		Git: git, Analyzer: analyzer, RenewEvery: time.Hour,
	}
	return worker, queue, store, git, analyzer
}

func TestRunOnePublishesExactCommit(t *testing.T) {
	worker, queue, store, git, analyzer := workerFixture()

	worked, err := worker.RunOne(t.Context())

	if err != nil || !worked {
		t.Fatalf("RunOne() = %v, %v", worked, err)
	}
	if queue.completed != 9 || store.publishedRepositoryID != 101 || store.publishedCommit != testSHA {
		t.Fatalf("completed=%d repository=%d commit=%q", queue.completed, store.publishedRepositoryID, store.publishedCommit)
	}
	if analyzer.request != (graphscan.Request{RepositoryID: 101, Commit: testSHA, Root: "/work"}) {
		t.Fatalf("scan request = %#v", analyzer.request)
	}
	want := []string{"claim", "repository", "token", "prepare", "scan", "repository", "complete", "cleanup"}
	if !slices.Equal(queue.events, want) {
		t.Fatalf("events = %v, want %v", queue.events, want)
	}
	if !git.cleaned {
		t.Fatal("worktree was not cleaned")
	}
}

func TestRunOneDoesNotPublishChangedIndexedSHA(t *testing.T) {
	worker, queue, store, _, _ := workerFixture()
	store.indexedSHAs = []string{testSHA, strings.Repeat("b", 40)}

	worked, err := worker.RunOne(t.Context())

	if err != nil || !worked {
		t.Fatalf("RunOne() = %v, %v", worked, err)
	}
	if store.publishedCommit != "" || queue.completed != 0 {
		t.Fatalf("published=%q completed=%d", store.publishedCommit, queue.completed)
	}
	if queue.failedCode != "superseded" || queue.failedRetry {
		t.Fatalf("failure=%q retry=%v", queue.failedCode, queue.failedRetry)
	}
}

func TestRunOnePreservesExternalArtifact(t *testing.T) {
	worker, queue, store, _, _ := workerFixture()
	queue.externalCurrent = true
	store.publishedCommit = testSHA
	store.publishedAnalyzer = "external"

	worked, err := worker.RunOne(t.Context())

	if err != nil || !worked {
		t.Fatalf("RunOne() = %v, %v", worked, err)
	}
	if queue.completed != 9 || store.publishedCommit != testSHA || store.publishedAnalyzer != "external" {
		t.Fatalf("completed=%d commit=%q analyzer=%q", queue.completed, store.publishedCommit, store.publishedAnalyzer)
	}
}

func TestRunOneRejectsArtifactForDifferentCommit(t *testing.T) {
	worker, queue, store, _, analyzer := workerFixture()
	analyzer.exact = true
	analyzer.artifact = graphartifact.Artifact{
		RepositoryID: 101,
		Commit:       strings.Repeat("b", 40),
		Analyzer:     graphartifact.Analyzer{Name: "managed", Version: "1"},
	}

	worked, err := worker.RunOne(t.Context())

	if err != nil || !worked {
		t.Fatalf("RunOne() = %v, %v", worked, err)
	}
	if store.publishedCommit != "" || queue.completed != 0 {
		t.Fatalf("published=%q completed=%d", store.publishedCommit, queue.completed)
	}
	if queue.failedCode != "scan_failed" || !queue.failedRetry {
		t.Fatalf("failure=%q retry=%v", queue.failedCode, queue.failedRetry)
	}
}

func TestRunOneStopsOnLeaseLossWithoutPublishing(t *testing.T) {
	worker, queue, store, git, analyzer := workerFixture()
	queue.renewErr = postgres.ErrLeaseLost
	worker.RenewEvery = time.Millisecond
	analyzer.block = true

	worked, err := worker.RunOne(t.Context())

	if !worked || !errors.Is(err, postgres.ErrLeaseLost) {
		t.Fatalf("RunOne() = %v, %v", worked, err)
	}
	if store.publishedCommit != "" || queue.completed != 0 || queue.failedCode != "" {
		t.Fatalf("published=%q completed=%d failure=%q", store.publishedCommit, queue.completed, queue.failedCode)
	}
	if !git.cleaned {
		t.Fatal("worktree was not cleaned")
	}
}

func TestRunOneClassifiesFailures(t *testing.T) {
	databaseErr := errors.New("database unavailable")
	for _, test := range []struct {
		name      string
		configure func(*Worker, *fakeQueue, *fakeStore, *fakeGit, *fakeAnalyzer)
		code      string
		retry     bool
		wantErr   error
	}{
		{
			name: "token", code: "token_failed", retry: true,
			configure: func(worker *Worker, queue *fakeQueue, _ *fakeStore, _ *fakeGit, _ *fakeAnalyzer) {
				worker.Tokens = &fakeTokens{queue: queue, err: errors.New("token failed")}
			},
		},
		{
			name: "checkout", code: "git_failed", retry: true,
			configure: func(_ *Worker, _ *fakeQueue, _ *fakeStore, git *fakeGit, _ *fakeAnalyzer) {
				git.err = errors.New("checkout failed")
			},
		},
		{
			name: "scan limit", code: "scan_limit", retry: false,
			configure: func(_ *Worker, _ *fakeQueue, _ *fakeStore, _ *fakeGit, analyzer *fakeAnalyzer) {
				analyzer.err = graphscan.ErrLimitExceeded
			},
		},
		{
			name: "scan", code: "scan_failed", retry: true,
			configure: func(_ *Worker, _ *fakeQueue, _ *fakeStore, _ *fakeGit, analyzer *fakeAnalyzer) {
				analyzer.err = errors.New("scan failed")
			},
		},
		{
			name: "repository", code: "repository_failed", retry: true,
			configure: func(_ *Worker, _ *fakeQueue, store *fakeStore, _ *fakeGit, _ *fakeAnalyzer) {
				store.err = databaseErr
			},
		},
		{
			name: "complete", code: "publish_failed", retry: true,
			configure: func(_ *Worker, queue *fakeQueue, _ *fakeStore, _ *fakeGit, _ *fakeAnalyzer) {
				queue.completeErr = databaseErr
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			worker, queue, store, git, analyzer := workerFixture()
			test.configure(worker, queue, store, git, analyzer)

			worked, err := worker.RunOne(t.Context())

			if !worked || !errors.Is(err, test.wantErr) {
				t.Fatalf("RunOne() = %v, %v", worked, err)
			}
			if queue.failedCode != test.code || queue.failedRetry != test.retry {
				t.Fatalf("failure=%q retry=%v", queue.failedCode, queue.failedRetry)
			}
			if store.publishedCommit != "" || queue.completed != 0 {
				t.Fatalf("published=%q completed=%d", store.publishedCommit, queue.completed)
			}
		})
	}
}

func TestRunOneCleansUpAfterCancellation(t *testing.T) {
	worker, queue, store, git, analyzer := workerFixture()
	analyzer.block = true
	analyzer.started = make(chan struct{})
	ctx, cancel := context.WithCancel(t.Context())
	result := make(chan error, 1)
	go func() {
		_, err := worker.RunOne(ctx)
		result <- err
	}()
	<-analyzer.started
	cancel()

	if err := <-result; !errors.Is(err, context.Canceled) {
		t.Fatalf("RunOne() error = %v", err)
	}
	if !git.cleaned || git.cleanupRoot != nil || !git.cleanupBound {
		t.Fatalf("cleaned=%v root=%v bound=%v", git.cleaned, git.cleanupRoot, git.cleanupBound)
	}
	if store.publishedCommit != "" || queue.completed != 0 || queue.failedCode != "" {
		t.Fatalf("published=%q completed=%d failure=%q", store.publishedCommit, queue.completed, queue.failedCode)
	}
}

func TestRunReturnsContextCancellationWhileIdle(t *testing.T) {
	worker, queue, _, _, _ := workerFixture()
	queue.claimErr = postgres.ErrNoJob
	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	if err := worker.Run(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("Run() error = %v", err)
	}
}
