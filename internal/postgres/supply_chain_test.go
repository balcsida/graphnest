//go:build integration

package postgres

import (
	"bytes"
	"crypto/sha256"
	"errors"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/balcsida/graphnest/internal/scipgraph"
	"github.com/balcsida/graphnest/internal/supplychain"
	"github.com/jackc/pgx/v5"
)

func supplyChainFixture(t *testing.T) []byte {
	t.Helper()
	data, err := os.ReadFile("../../test/fixtures/supplychain/ghes-spdx-2.3.json")
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func supplyChainRepository(t *testing.T, store *Store, githubID int64, name string) int64 {
	t.Helper()
	if err := store.UpsertInstallation(t.Context(), InstallationUpdate{GitHubID: 10, AccountLogin: "acme", AccountType: "Organization", Status: "active"}); err != nil {
		t.Fatal(err)
	}
	repository, err := store.UpsertRepository(t.Context(), RepositoryUpdate{GitHubID: githubID, InstallationID: 10, Owner: "acme", Name: name, CloneURL: "clone", WebURL: "web", DefaultBranch: "main", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	return repository.ID
}

func claimSupplyChain(t *testing.T, store *Store, repositoryID int64, owner string) supplychain.Job {
	t.Helper()
	if _, _, err := store.EnqueueSupplyChainJob(t.Context(), repositoryID, supplychain.StreamGitHubSource, "manual", "tester", 10, time.Now()); err != nil {
		t.Fatal(err)
	}
	job, err := store.ClaimSupplyChainJob(t.Context(), owner)
	if err != nil {
		t.Fatal(err)
	}
	return job
}

func publication(t *testing.T, repositoryID int64, job supplychain.Job, document []byte) SupplyChainPublication {
	t.Helper()
	normalized, err := supplychain.NormalizeSPDX23(document, supplychain.Limits{})
	if err != nil {
		t.Fatal(err)
	}
	status := 200
	jobID := job.ID
	return SupplyChainPublication{
		RepositoryID: repositoryID, JobID: &jobID, JobOwner: job.LeaseOwner, JobFence: job.Fence,
		Producer: supplychain.ProducerGitHub, Subject: supplychain.SubjectSource, StreamKey: supplychain.StreamGitHubSource,
		Format: supplychain.FormatSPDX23JSON, MediaType: "application/json", Document: document, Normalized: normalized,
		CollectedAt: time.Now().UTC(), StartedAt: time.Now().UTC().Add(-time.Second), HTTPStatus: &status, SubjectAssurance: supplychain.AssuranceUnknown,
	}
}

func TestSupplyChainPublishPreservesDocumentAndOccurrences(t *testing.T) {
	store := migratedStore(t)
	repositoryID := supplyChainRepository(t, store, 101, "widgets")
	document := supplyChainFixture(t)
	job := claimSupplyChain(t, store, repositoryID, "worker-a")

	collection, err := store.PublishSupplyChainSnapshot(t.Context(), publication(t, repositoryID, job, document))
	if err != nil {
		t.Fatal(err)
	}
	if collection.Outcome != supplychain.OutcomePublished || collection.SnapshotID == nil || collection.JobID == nil {
		t.Fatalf("collection = %+v", collection)
	}
	snapshot, err := store.SupplyChainSnapshot(t.Context(), *collection.SnapshotID, []int64{repositoryID})
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(document)
	if !bytes.Equal(snapshot.DocumentSHA256, digest[:]) || snapshot.ComponentCount != 7 || snapshot.EdgeCount != 8 || snapshot.ProducerTool != "GitHub.com-Dependency-Graph" ||
		snapshot.SubjectAssurance != supplychain.AssuranceUnknown || snapshot.SubjectRevision != "" || snapshot.ParserVersion != supplychain.ParserVersion || len(snapshot.RootElementIDs) != 1 {
		t.Fatalf("snapshot = %+v", snapshot)
	}
	body, mediaType, storedDigest, err := store.SupplyChainDocument(t.Context(), snapshot.ID, []int64{repositoryID})
	if err != nil || !bytes.Equal(body, document) || mediaType != "application/json" || !bytes.Equal(storedDigest, digest[:]) {
		t.Fatalf("document = %d bytes, %q, %v", len(body), mediaType, err)
	}
	components, err := store.SupplyChainComponents(t.Context(), snapshot.ID, -1, 100, "")
	if err != nil || len(components) != 7 {
		t.Fatalf("components = %d, %v", len(components), err)
	}
	if !components[0].IsRoot || components[6].PURL != nil || components[6].Version != nil || components[1].Qualifiers == nil || components[2].Qualifiers["type"] != "jar" {
		t.Fatalf("components = %+v", components)
	}
	if search, err := store.SupplyChainComponents(t.Context(), snapshot.ID, -1, 100, "left-pad"); err != nil || len(search) != 1 || search[0].PURLName != "left-pad" {
		t.Fatalf("search = %+v, %v", search, err)
	}
	// LIKE metacharacters match literally: "%" matches only the percent-encoded scoped purl, "_" matches nothing.
	if search, err := store.SupplyChainComponents(t.Context(), snapshot.ID, -1, 100, "%"); err != nil || len(search) != 1 || search[0].PURLName != "left-pad" {
		t.Fatalf("percent search = %+v, %v", search, err)
	}
	if search, err := store.SupplyChainComponents(t.Context(), snapshot.ID, -1, 100, "_"); err != nil || len(search) != 0 {
		t.Fatalf("underscore search = %+v, %v", search, err)
	}
	edges, err := store.SupplyChainRelationships(t.Context(), snapshot.ID, "SPDXRef-maven-org.example-core-2.1.0", 10)
	if err != nil || len(edges) != 2 || edges[1].Resolved || edges[1].ToElement != "SPDXRef-missing-transitive" {
		t.Fatalf("edges = %+v, %v", edges, err)
	}
	stream, err := store.SupplyChainStream(t.Context(), repositoryID, supplychain.StreamGitHubSource)
	if err != nil || stream.LatestSnapshotID == nil || *stream.LatestSnapshotID != snapshot.ID || stream.LastOutcome != supplychain.OutcomePublished || stream.LastSuccessAt == nil {
		t.Fatalf("stream = %+v, %v", stream, err)
	}
	finished, err := store.SupplyChainJob(t.Context(), job.ID, []int64{repositoryID})
	if err != nil || finished.State != supplychain.JobSucceeded || finished.LeaseOwner != "" {
		t.Fatalf("job = %+v, %v", finished, err)
	}
	// A repository outside the caller's authorized set cannot see the snapshot or document.
	if _, err := store.SupplyChainSnapshot(t.Context(), snapshot.ID, []int64{repositoryID + 1000}); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("unauthorized snapshot read = %v", err)
	}
	if _, _, _, err := store.SupplyChainDocument(t.Context(), snapshot.ID, nil); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("unauthorized document read = %v", err)
	}
	if _, err := store.SupplyChainJob(t.Context(), job.ID, []int64{}); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("unauthorized job read = %v", err)
	}
}

func TestSupplyChainRepeatCollectionRecordsUnchangedWithoutNewSnapshot(t *testing.T) {
	store := migratedStore(t)
	repositoryID := supplyChainRepository(t, store, 101, "widgets")
	document := supplyChainFixture(t)
	first, err := store.PublishSupplyChainSnapshot(t.Context(), publication(t, repositoryID, claimSupplyChain(t, store, repositoryID, "a"), document))
	if err != nil {
		t.Fatal(err)
	}
	second, err := store.PublishSupplyChainSnapshot(t.Context(), publication(t, repositoryID, claimSupplyChain(t, store, repositoryID, "a"), document))
	if err != nil {
		t.Fatal(err)
	}
	if second.Outcome != supplychain.OutcomeUnchanged || *second.SnapshotID != *first.SnapshotID || *second.DocumentID != *first.DocumentID {
		t.Fatalf("second = %+v, first = %+v", second, first)
	}
	snapshots, err := store.SupplyChainSnapshots(t.Context(), repositoryID, supplychain.StreamGitHubSource, 0, 10)
	if err != nil || len(snapshots) != 1 {
		t.Fatalf("snapshots = %d, %v", len(snapshots), err)
	}
	collections, err := store.SupplyChainCollections(t.Context(), repositoryID, supplychain.StreamGitHubSource, 0, 10)
	if err != nil || len(collections) != 2 || collections[0].Outcome != supplychain.OutcomeUnchanged || collections[1].Outcome != supplychain.OutcomePublished {
		t.Fatalf("collections = %+v, %v", collections, err)
	}
	// A changed document (one extra byte of whitespace is a different document) publishes a second snapshot but shares nothing.
	changed := append(bytes.TrimRight(document, "\n"), ' ', '\n')
	third, err := store.PublishSupplyChainSnapshot(t.Context(), publication(t, repositoryID, claimSupplyChain(t, store, repositoryID, "a"), changed))
	if err != nil || third.Outcome != supplychain.OutcomePublished || *third.SnapshotID == *first.SnapshotID {
		t.Fatalf("third = %+v, %v", third, err)
	}
	if snapshots, err := store.SupplyChainSnapshots(t.Context(), repositoryID, supplychain.StreamGitHubSource, 0, 10); err != nil || len(snapshots) != 2 || snapshots[0].ID != *third.SnapshotID {
		t.Fatalf("snapshots = %+v, %v", snapshots, err)
	}
}

func TestSupplyChainFailureRetainsLastInventory(t *testing.T) {
	store := migratedStore(t)
	repositoryID := supplyChainRepository(t, store, 101, "widgets")
	published, err := store.PublishSupplyChainSnapshot(t.Context(), publication(t, repositoryID, claimSupplyChain(t, store, repositoryID, "a"), supplyChainFixture(t)))
	if err != nil {
		t.Fatal(err)
	}
	job := claimSupplyChain(t, store, repositoryID, "a")
	status, retryAfter := 403, 120
	failure, err := store.RecordSupplyChainFailure(t.Context(), SupplyChainFailure{
		RepositoryID: repositoryID, JobID: &job.ID, JobOwner: job.LeaseOwner, JobFence: job.Fence, Producer: supplychain.ProducerGitHub, Subject: supplychain.SubjectSource,
		StreamKey: supplychain.StreamGitHubSource, StartedAt: time.Now(), Outcome: supplychain.OutcomeRateLimited, HTTPStatus: &status, RetryAfterSeconds: &retryAfter, ErrorCode: "rate_limited",
	})
	if err != nil || failure.Outcome != supplychain.OutcomeRateLimited || failure.SnapshotID != nil {
		t.Fatalf("failure = %+v, %v", failure, err)
	}
	stream, err := store.SupplyChainStream(t.Context(), repositoryID, supplychain.StreamGitHubSource)
	if err != nil || stream.LatestSnapshotID == nil || *stream.LatestSnapshotID != *published.SnapshotID || stream.LastOutcome != supplychain.OutcomeRateLimited || *stream.LatestCollectionID != failure.ID {
		t.Fatalf("stream after failure = %+v, %v", stream, err)
	}
	requeued, err := store.SupplyChainJob(t.Context(), job.ID, []int64{repositoryID})
	if err != nil || requeued.State != supplychain.JobQueued || requeued.ErrorCode != "rate_limited" || requeued.RunAfter.Before(time.Now().Add(110*time.Second)) {
		t.Fatalf("job after rate limit = %+v, %v", requeued, err)
	}
	if _, err := store.ClaimSupplyChainJob(t.Context(), "b"); !errors.Is(err, supplychain.ErrNoJob) {
		t.Fatalf("job ran before its Retry-After: %v", err)
	}
	// A permanent failure is not retried.
	if _, err := store.pool.Exec(t.Context(), `update supply_chain_jobs set run_after=now() where id=$1`, job.ID); err != nil {
		t.Fatal(err)
	}
	job, err = store.ClaimSupplyChainJob(t.Context(), "b")
	if err != nil {
		t.Fatal(err)
	}
	notFound := 404
	if _, err := store.RecordSupplyChainFailure(t.Context(), SupplyChainFailure{RepositoryID: repositoryID, JobID: &job.ID, JobOwner: job.LeaseOwner, JobFence: job.Fence, Producer: supplychain.ProducerGitHub,
		Subject: supplychain.SubjectSource, StreamKey: supplychain.StreamGitHubSource, StartedAt: time.Now(), Outcome: supplychain.OutcomeNotFound, HTTPStatus: &notFound, ErrorCode: "not_found"}); err != nil {
		t.Fatal(err)
	}
	if failed, err := store.SupplyChainJob(t.Context(), job.ID, []int64{repositoryID}); err != nil || failed.State != supplychain.JobFailed {
		t.Fatalf("job after 404 = %+v, %v", failed, err)
	}
}

func TestSupplyChainStaleWorkerCannotPublish(t *testing.T) {
	store := migratedStore(t)
	repositoryID := supplyChainRepository(t, store, 101, "widgets")
	document := supplyChainFixture(t)
	stale := claimSupplyChain(t, store, repositoryID, "worker-a")
	// The lease expires (crash) and is reaped back to queued, then another worker claims it.
	if _, err := store.pool.Exec(t.Context(), `update supply_chain_jobs set lease_expires_at=now()-interval '1 second' where id=$1`, stale.ID); err != nil {
		t.Fatal(err)
	}
	if reaped, err := store.ReapExpiredSupplyChainJobs(t.Context(), 10); err != nil || reaped != 1 {
		t.Fatalf("reaped = %d, %v", reaped, err)
	}
	if _, err := store.pool.Exec(t.Context(), `update supply_chain_jobs set run_after=now() where id=$1`, stale.ID); err != nil {
		t.Fatal(err)
	}
	fresh, err := store.ClaimSupplyChainJob(t.Context(), "worker-b")
	if err != nil || fresh.ID != stale.ID || fresh.Fence != stale.Fence+1 || fresh.Attempt != 2 {
		t.Fatalf("fresh claim = %+v, %v", fresh, err)
	}
	if err := store.RenewSupplyChainLease(t.Context(), stale.ID, stale.LeaseOwner, stale.Fence); !errors.Is(err, ErrSupplyChainFenced) {
		t.Fatalf("stale renew = %v", err)
	}
	if _, err := store.PublishSupplyChainSnapshot(t.Context(), publication(t, repositoryID, stale, document)); !errors.Is(err, ErrSupplyChainFenced) {
		t.Fatalf("stale publish = %v", err)
	}
	if snapshots, err := store.SupplyChainSnapshots(t.Context(), repositoryID, supplychain.StreamGitHubSource, 0, 10); err != nil || len(snapshots) != 0 {
		t.Fatalf("stale publish wrote %d snapshots, %v", len(snapshots), err)
	}
	if _, err := store.RecordSupplyChainFailure(t.Context(), SupplyChainFailure{RepositoryID: repositoryID, JobID: &stale.ID, JobOwner: stale.LeaseOwner, JobFence: stale.Fence,
		Producer: supplychain.ProducerGitHub, Subject: supplychain.SubjectSource, StreamKey: supplychain.StreamGitHubSource, StartedAt: time.Now(), Outcome: supplychain.OutcomeError}); !errors.Is(err, ErrSupplyChainFenced) {
		t.Fatalf("stale failure = %v", err)
	}
	if err := store.RenewSupplyChainLease(t.Context(), fresh.ID, fresh.LeaseOwner, fresh.Fence); err != nil {
		t.Fatalf("fresh renew = %v", err)
	}
	if _, err := store.PublishSupplyChainSnapshot(t.Context(), publication(t, repositoryID, fresh, document)); err != nil {
		t.Fatalf("fresh publish = %v", err)
	}
}

func TestSupplyChainJobsDeduplicateAndClaimOnce(t *testing.T) {
	store := migratedStore(t)
	repositoryID := supplyChainRepository(t, store, 101, "widgets")
	first, created, err := store.EnqueueSupplyChainJob(t.Context(), repositoryID, supplychain.StreamGitHubSource, "scheduled", "", 0, time.Now().Add(time.Hour))
	if err != nil || !created {
		t.Fatalf("first = %+v, %v, %v", first, created, err)
	}
	second, created, err := store.EnqueueSupplyChainJob(t.Context(), repositoryID, supplychain.StreamGitHubSource, "manual", "admin", 10, time.Now())
	if err != nil || created || second.ID != first.ID || second.Priority != 10 || second.RunAfter.After(time.Now().Add(time.Second)) {
		t.Fatalf("second = %+v, %v, %v", second, created, err)
	}
	var group sync.WaitGroup
	results := make(chan error, 4)
	for range 4 {
		group.Add(1)
		go func() {
			defer group.Done()
			_, err := store.ClaimSupplyChainJob(t.Context(), "worker")
			results <- err
		}()
	}
	group.Wait()
	close(results)
	claimed := 0
	for err := range results {
		switch {
		case err == nil:
			claimed++
		case errors.Is(err, supplychain.ErrNoJob):
		default:
			t.Fatal(err)
		}
	}
	if claimed != 1 {
		t.Fatalf("claimed %d times", claimed)
	}
	// While running, a new enqueue creates a separate queued job that cannot be claimed until the running one finishes.
	if _, created, err := store.EnqueueSupplyChainJob(t.Context(), repositoryID, supplychain.StreamGitHubSource, "manual", "admin", 10, time.Now()); err != nil || !created {
		t.Fatalf("enqueue while running = %v, %v", created, err)
	}
	if _, err := store.ClaimSupplyChainJob(t.Context(), "worker"); !errors.Is(err, supplychain.ErrNoJob) {
		t.Fatalf("second running job claimed: %v", err)
	}
	depths, err := store.SupplyChainQueueDepths(t.Context())
	if err != nil || depths["queued"] != 1 || depths["running"] != 1 {
		t.Fatalf("depths = %v, %v", depths, err)
	}
}

func TestSupplyChainCancellationAndOptOut(t *testing.T) {
	store := migratedStore(t)
	repositoryID := supplyChainRepository(t, store, 101, "widgets")
	job := claimSupplyChain(t, store, repositoryID, "worker")
	if cancelled, err := store.CancelSupplyChainJob(t.Context(), job.ID); err != nil || !cancelled {
		t.Fatalf("cancel = %v, %v", cancelled, err)
	}
	if _, err := store.PublishSupplyChainSnapshot(t.Context(), publication(t, repositoryID, job, supplyChainFixture(t))); !errors.Is(err, ErrSupplyChainFenced) {
		t.Fatalf("publish after cancel = %v", err)
	}
	if err := store.SetSupplyChainOptOut(t.Context(), repositoryID, supplychain.StreamGitHubSource, supplychain.ProducerGitHub, supplychain.SubjectSource, true); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.EnqueueSupplyChainJob(t.Context(), repositoryID, supplychain.StreamGitHubSource, "scheduled", "", 0, time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ClaimSupplyChainJob(t.Context(), "worker"); !errors.Is(err, supplychain.ErrNoJob) {
		t.Fatalf("opted-out repository job claimed: %v", err)
	}
	if due, err := store.DueSupplyChainStreams(t.Context(), supplychain.StreamGitHubSource, time.Now(), 10); err != nil || len(due) != 0 {
		t.Fatalf("opted-out repository is due: %v, %v", due, err)
	}
}

func TestSupplyChainDueStreamsAndProjection(t *testing.T) {
	store := migratedStore(t)
	widgets := supplyChainRepository(t, store, 101, "widgets")
	gadgets := supplyChainRepository(t, store, 102, "gadgets")
	due, err := store.DueSupplyChainStreams(t.Context(), supplychain.StreamGitHubSource, time.Now().Add(-time.Hour), 10)
	if err != nil || len(due) != 2 {
		t.Fatalf("due = %v, %v", due, err)
	}
	// Manual mappings must survive the projection; GitHub mappings are rebuilt from the snapshot.
	manual := scipgraph.PackageMapping{Package: scipgraph.Package{PURL: "pkg:npm/manual@1.0.0", Manager: "npm", Name: "manual", Version: "1.0.0"}, Relation: "depends_on", Source: "manual"}
	if err := store.ReplacePackages(t.Context(), widgets, "manual", []scipgraph.PackageMapping{manual}); err != nil {
		t.Fatal(err)
	}
	legacy := scipgraph.PackageMapping{Package: scipgraph.Package{PURL: "pkg:npm/legacy@0.1.0", Manager: "npm", Name: "legacy", Version: "0.1.0"}, Relation: "depends_on", Source: "github"}
	if err := store.ReplacePackages(t.Context(), widgets, "github", []scipgraph.PackageMapping{legacy}); err != nil {
		t.Fatal(err)
	}
	collection, err := store.PublishSupplyChainSnapshot(t.Context(), publication(t, widgets, claimSupplyChain(t, store, widgets, "worker"), supplyChainFixture(t)))
	if err != nil {
		t.Fatal(err)
	}
	count, err := store.ProjectSupplyChainPackages(t.Context(), widgets, *collection.SnapshotID)
	if err != nil {
		t.Fatal(err)
	}
	// The legacy mapping parser is kept exactly: pkg:github is not a SCIP manager and a purl with
	// qualifiers (maven ...?type=jar) is rejected, so only npm, nuget, and golang project.
	if count != 3 {
		t.Fatalf("projected %d mappings", count)
	}
	rows, err := store.pool.Query(t.Context(), `select source, relation, purl from repository_packages where repository_id=$1 order by source, purl`, widgets)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var got []string
	for rows.Next() {
		var source, relation, purl string
		if err := rows.Scan(&source, &relation, &purl); err != nil {
			t.Fatal(err)
		}
		got = append(got, source+":"+relation+":"+purl)
	}
	want := []string{
		"github:depends_on:pkg:golang/golang.org/x/text@0.14.0",
		"github:depends_on:pkg:npm/%40scope/left-pad@1.3.0", "github:depends_on:pkg:nuget/Newtonsoft.Json@13.0.3",
		"manual:depends_on:pkg:npm/manual@1.0.0",
	}
	if len(got) != len(want) {
		t.Fatalf("mappings = %v, want %v", got, want)
	}
	for index := range want {
		if got[index] != want[index] {
			t.Fatalf("mappings = %v, want %v", got, want)
		}
	}
	if due, err := store.DueSupplyChainStreams(t.Context(), supplychain.StreamGitHubSource, time.Now().Add(-time.Hour), 10); err != nil || len(due) != 1 || due[0] != gadgets {
		t.Fatalf("due after collection = %v, %v", due, err)
	}
	if err := store.RecordSupplyChainProjectionError(t.Context(), collection.ID, "projection_failed"); err != nil {
		t.Fatal(err)
	}
	collections, err := store.SupplyChainCollections(t.Context(), widgets, supplychain.StreamGitHubSource, 0, 1)
	if err != nil || len(collections) != 1 || collections[0].ProjectionError != "projection_failed" {
		t.Fatalf("collections = %+v, %v", collections, err)
	}
}

func TestSupplyChainRepositoryRemovalCascades(t *testing.T) {
	store := migratedStore(t)
	repositoryID := supplyChainRepository(t, store, 101, "widgets")
	collection, err := store.PublishSupplyChainSnapshot(t.Context(), publication(t, repositoryID, claimSupplyChain(t, store, repositoryID, "worker"), supplyChainFixture(t)))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.pool.Exec(t.Context(), `delete from repositories where id=$1`, repositoryID); err != nil {
		t.Fatal(err)
	}
	for _, table := range []string{"supply_chain_snapshots", "supply_chain_components", "supply_chain_relationships", "supply_chain_collections", "supply_chain_streams", "supply_chain_jobs"} {
		var count int
		if err := store.pool.QueryRow(t.Context(), `select count(*) from `+table).Scan(&count); err != nil || count != 0 {
			t.Fatalf("%s has %d rows after repository removal (%v)", table, count, err)
		}
	}
	if _, err := store.SupplyChainSnapshot(t.Context(), *collection.SnapshotID, []int64{repositoryID}); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("snapshot survived removal: %v", err)
	}
}
