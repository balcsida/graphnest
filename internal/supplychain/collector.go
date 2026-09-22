package supplychain

import (
	"context"
	"errors"
	"log/slog"
	"math/rand/v2"
	"net/http"
	"strings"
	"time"

	"github.com/balcsida/graphnest/internal/githubapp"
	"github.com/balcsida/graphnest/internal/repository"
)

// SBOMReader is the lossless GitHub SBOM export reader.
type SBOMReader interface {
	DependencySBOMDocument(ctx context.Context, installationID int64, owner, name string, maxBytes int64) (githubapp.SBOMDocument, error)
}

// CollectorStore is what the worker needs from PostgreSQL.
type CollectorStore interface {
	ClaimSupplyChainJob(context.Context, string) (Job, error)
	RenewSupplyChainLease(context.Context, int64, string, int64) error
	ReapExpiredSupplyChainJobs(context.Context, int) (int64, error)
	RepositoryForIndex(context.Context, int64) (repository.Repository, error)
	PublishSupplyChainSnapshot(context.Context, Publication) (Collection, error)
	RecordSupplyChainFailure(context.Context, Failure) (Collection, error)
	RecordSupplyChainProjectionError(context.Context, int64, string) error
	ProjectSupplyChainPackages(context.Context, int64, int64) (int, error)
	DueSupplyChainStreams(context.Context, string, time.Time, int) ([]int64, error)
	EnqueueSupplyChainJob(context.Context, int64, string, string, string, int, time.Time) (Job, bool, error)
	SupplyChainQueueDepths(context.Context) (map[string]int64, error)
}

// Observer receives bounded telemetry: outcomes by class and queue depths,
// never component or repository identifiers.
type Observer interface {
	ObserveSupplyChainCollection(outcome string, duration time.Duration)
	SetSupplyChainQueueDepth(state string, depth int64)
}

// Enricher receives every newly published snapshot so license lookups can
// be queued. It must not block publication on registry availability.
type Enricher interface {
	EnqueueSnapshot(ctx context.Context, snapshotID int64) (int, error)
}

// Collector leases refresh jobs and turns GHES SBOM exports into published
// snapshots. It runs in the server process, independent of the indexer.
type Collector struct {
	Store            CollectorStore
	GitHub           SBOMReader
	Owner            string
	MaxDocumentBytes int64
	Limits           Limits
	Logger           *slog.Logger
	Observer         Observer
	Now              func() time.Time
	// Poll is how long the worker waits when no job is available.
	Poll time.Duration
	// Enricher is optional; nil means no license enrichment is configured.
	Enricher Enricher
}

func (collector *Collector) now() time.Time {
	if collector.Now != nil {
		return collector.Now().UTC()
	}
	return time.Now().UTC()
}

func (collector *Collector) logger() *slog.Logger {
	if collector.Logger == nil {
		return slog.Default()
	}
	return collector.Logger
}

// Run leases and processes jobs until the context ends. Expired leases from
// crashed workers are reaped on start and before each idle wait.
func (collector *Collector) Run(ctx context.Context) error {
	poll := collector.Poll
	if poll <= 0 {
		poll = 15 * time.Second
	}
	for {
		if _, err := collector.Store.ReapExpiredSupplyChainJobs(ctx, 100); err != nil && ctx.Err() == nil {
			collector.logger().Error("supply chain reap failed", "error", err)
		}
		collector.refreshDepths(ctx)
		processed, err := collector.RunOnce(ctx)
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err != nil {
			collector.logger().Error("supply chain collection failed", "error", err)
		}
		if processed && err == nil {
			continue
		}
		timer := time.NewTimer(poll + time.Duration(rand.Int64N(int64(poll)/4+1)))
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
}

// RunOnce claims and processes at most one job. It reports whether a job was
// claimed so callers can drain a queue deterministically in tests.
func (collector *Collector) RunOnce(ctx context.Context) (bool, error) {
	job, err := collector.Store.ClaimSupplyChainJob(ctx, collector.Owner)
	if errors.Is(err, ErrNoJob) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	started := collector.now()
	outcome, err := collector.process(ctx, job, started)
	if collector.Observer != nil {
		collector.Observer.ObserveSupplyChainCollection(string(outcome), collector.now().Sub(started))
	}
	return true, err
}

func (collector *Collector) process(ctx context.Context, job Job, started time.Time) (Outcome, error) {
	repo, err := collector.Store.RepositoryForIndex(ctx, job.RepositoryID)
	if err != nil {
		return OutcomeError, collector.fail(ctx, job, started, OutcomeError, nil, nil, "repository_lookup_failed", "")
	}
	owner, name, ok := strings.Cut(repo.Name, "/")
	if !ok || owner == "" || name == "" {
		return OutcomeError, collector.fail(ctx, job, started, OutcomeError, nil, nil, "repository_name_invalid", "")
	}
	renewCtx, stopRenewal := context.WithCancel(ctx)
	renewErr := make(chan error, 1)
	go collector.renew(renewCtx, job, renewErr)
	document, fetchErr := collector.GitHub.DependencySBOMDocument(ctx, repo.InstallationID, owner, name, collector.MaxDocumentBytes)
	stopRenewal()
	if err := <-renewErr; err != nil {
		// The lease was lost mid-fetch; whatever was fetched belongs to the newer lease holder.
		return OutcomeError, err
	}
	if ctx.Err() != nil {
		return OutcomeCancelled, ctx.Err()
	}
	if fetchErr != nil {
		outcome, status, retryAfter, code := classifyFetchError(fetchErr)
		return outcome, collector.fail(ctx, job, started, outcome, status, retryAfter, code, safeMessage(outcome))
	}
	normalized, err := NormalizeSPDX23(document.Body, collector.Limits)
	if err != nil {
		outcome := OutcomeMalformed
		code := "malformed_document"
		if errors.Is(err, ErrTooLarge) {
			outcome, code = OutcomeTooLarge, "document_too_large"
		} else if errors.Is(err, ErrUnsupportedVersion) {
			code = "unsupported_spdx_version"
		}
		status := document.Status
		return outcome, collector.fail(ctx, job, started, outcome, &status, nil, code, sanitize(err.Error(), 200))
	}
	status := document.Status
	jobID := job.ID
	collection, err := collector.Store.PublishSupplyChainSnapshot(ctx, Publication{
		RepositoryID: repo.ID, JobID: &jobID, JobOwner: job.LeaseOwner, JobFence: job.Fence, Producer: ProducerGitHub, Subject: SubjectSource, StreamKey: job.StreamKey,
		Format: FormatSPDX23JSON, MediaType: document.MediaType, Document: document.Body, Normalized: normalized, CollectedAt: document.FetchedAt, StartedAt: started,
		HTTPStatus: &status, SubjectAssurance: AssuranceUnknown,
	})
	if err != nil {
		if errors.Is(err, ErrFenced) {
			return OutcomeError, nil
		}
		return OutcomeError, err
	}
	if collection.Outcome == OutcomePublished && collection.SnapshotID != nil {
		if _, err := collector.Store.ProjectSupplyChainPackages(ctx, repo.ID, *collection.SnapshotID); err != nil {
			collector.logger().Warn("supply chain projection failed", "repository_id", repo.ID, "error", err)
			if err := collector.Store.RecordSupplyChainProjectionError(ctx, collection.ID, "projection_failed"); err != nil {
				return collection.Outcome, err
			}
		}
		if collector.Enricher != nil {
			if _, err := collector.Enricher.EnqueueSnapshot(ctx, *collection.SnapshotID); err != nil {
				// Enrichment is best-effort background work; the snapshot is
				// published regardless and the next publication re-queues.
				collector.logger().Warn("supply chain enrichment enqueue failed", "repository_id", repo.ID, "error", err)
			}
		}
	}
	return collection.Outcome, nil
}

func (collector *Collector) renew(ctx context.Context, job Job, result chan<- error) {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			result <- nil
			return
		case <-ticker.C:
			if err := collector.Store.RenewSupplyChainLease(ctx, job.ID, job.LeaseOwner, job.Fence); err != nil && ctx.Err() == nil {
				result <- err
				return
			}
		}
	}
}

func (collector *Collector) fail(ctx context.Context, job Job, started time.Time, outcome Outcome, status *int, retryAfter *int, code, message string) error {
	jobID := job.ID
	if ctx.Err() != nil {
		outcome = OutcomeCancelled
	}
	_, err := collector.Store.RecordSupplyChainFailure(context.WithoutCancel(ctx), Failure{
		RepositoryID: job.RepositoryID, JobID: &jobID, JobOwner: job.LeaseOwner, JobFence: job.Fence, Producer: ProducerGitHub, Subject: SubjectSource, StreamKey: job.StreamKey,
		StartedAt: started, FinishedAt: collector.now(), Outcome: outcome, HTTPStatus: status, RetryAfterSeconds: retryAfter, ErrorCode: code, Message: message,
	})
	if errors.Is(err, ErrFenced) {
		return nil
	}
	return err
}

// classifyFetchError maps a GitHub failure to a typed outcome. A 403 is
// rate limiting only when the headers say so; otherwise it is forbidden, and a
// 404 is not found. Neither proves the dependency graph is disabled.
func classifyFetchError(err error) (Outcome, *int, *int, string) {
	var sbomError githubapp.SBOMError
	if errors.As(err, &sbomError) {
		status := sbomError.Status
		var retryAfter *int
		if sbomError.RetryAfter > 0 {
			seconds := int(sbomError.RetryAfter / time.Second)
			retryAfter = &seconds
		} else if !sbomError.RateLimit.ResetAt.IsZero() && sbomError.RateLimit.Limited {
			seconds := int(time.Until(sbomError.RateLimit.ResetAt)/time.Second) + 1
			if seconds > 0 {
				retryAfter = &seconds
			}
		}
		switch {
		case sbomError.IsRateLimited():
			return OutcomeRateLimited, &status, retryAfter, "rate_limited"
		case status == http.StatusForbidden:
			return OutcomeForbidden, &status, nil, "github_forbidden"
		case status == http.StatusNotFound:
			return OutcomeNotFound, &status, nil, "github_not_found"
		case status == http.StatusUnauthorized:
			return OutcomeTransient, &status, nil, "github_unauthorized"
		case status >= 500 || status == http.StatusRequestTimeout:
			return OutcomeTransient, &status, retryAfter, "github_unavailable"
		default:
			return OutcomeUnavailable, &status, nil, "github_status"
		}
	}
	if errors.Is(err, githubapp.ErrSBOMTooLarge) {
		return OutcomeTooLarge, nil, nil, "document_too_large"
	}
	if errors.Is(err, githubapp.ErrSBOMMalformed) {
		return OutcomeMalformed, nil, nil, "envelope_malformed"
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return OutcomeTransient, nil, nil, "github_timeout"
	}
	return OutcomeTransient, nil, nil, "github_request_failed"
}

// safeMessage is the operator-facing explanation. It never echoes GitHub's
// response body.
func safeMessage(outcome Outcome) string {
	switch outcome {
	case OutcomeForbidden:
		return "GitHub returned 403 for the SBOM export: the dependency graph may be disabled, the installation may lack Contents read access, or the endpoint may be unsupported on this GitHub version."
	case OutcomeNotFound:
		return "GitHub returned 404 for the SBOM export: the repository may be inaccessible to the installation or the dependency graph may be unavailable."
	case OutcomeRateLimited:
		return "GitHub rate limited the SBOM export; the refresh will retry after the advertised wait."
	case OutcomeTransient:
		return "GitHub was unavailable or timed out; the refresh will retry."
	case OutcomeTooLarge:
		return "The SBOM export exceeds the configured document limit."
	case OutcomeMalformed:
		return "The SBOM export could not be parsed as SPDX 2.3 JSON."
	}
	return ""
}

func (collector *Collector) refreshDepths(ctx context.Context) {
	if collector.Observer == nil {
		return
	}
	depths, err := collector.Store.SupplyChainQueueDepths(ctx)
	if err != nil {
		return
	}
	for state, depth := range depths {
		collector.Observer.SetSupplyChainQueueDepth(state, depth)
	}
}

// Scheduler enqueues scheduled refreshes for due streams with jitter so a
// fleet of repositories does not refresh in lockstep.
type Scheduler struct {
	Store    CollectorStore
	Interval time.Duration
	Batch    int
	Now      func() time.Time
	Rand     *rand.Rand
}

// Tick enqueues jobs for streams last attempted before now-interval. Returns
// how many jobs were created.
func (scheduler *Scheduler) Tick(ctx context.Context) (int, error) {
	now := time.Now().UTC()
	if scheduler.Now != nil {
		now = scheduler.Now().UTC()
	}
	interval := scheduler.Interval
	if interval <= 0 {
		interval = 24 * time.Hour
	}
	batch := scheduler.Batch
	if batch <= 0 {
		batch = 200
	}
	due, err := scheduler.Store.DueSupplyChainStreams(ctx, StreamGitHubSource, now.Add(-interval), batch)
	if err != nil {
		return 0, err
	}
	created := 0
	for _, repositoryID := range due {
		jitter := time.Duration(scheduler.random().Int64N(int64(interval / 10)))
		if _, ok, err := scheduler.Store.EnqueueSupplyChainJob(ctx, repositoryID, StreamGitHubSource, "scheduled", "", 0, now.Add(jitter)); err != nil {
			return created, err
		} else if ok {
			created++
		}
	}
	return created, nil
}

func (scheduler *Scheduler) random() *rand.Rand {
	if scheduler.Rand != nil {
		return scheduler.Rand
	}
	return rand.New(rand.NewPCG(uint64(time.Now().UnixNano()), 0))
}
