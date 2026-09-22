//go:build integration

package postgres

import (
	"errors"
	"testing"
	"time"

	"github.com/balcsida/graphnest/internal/supplychain"
	"github.com/balcsida/graphnest/internal/supplychain/license"
	"github.com/balcsida/graphnest/internal/supplychain/spdxexpr"
)

func resolvedEvidence(coordinates license.Coordinates, route, raw string) license.Evidence {
	evidence := license.Evidence{Source: license.SourceRegistryNPM, Route: route, Coordinates: coordinates, Outcome: license.OutcomeResolved, FetchedAt: time.Now().UTC(), ContentSHA256: make([]byte, 32),
		RawValue: raw, RawKind: license.RawExpression, ResolverVersion: license.ResolverVersion, LicenseListVersion: spdxexpr.ListVersion, Detail: map[string]any{"integrity": "sha512-x"}}
	parsed := spdxexpr.Parse(raw)
	evidence.ParseStatus, evidence.NormalizedExpression, evidence.ExpressionTree, evidence.UnknownTerms = parsed.Status, parsed.Normalized, parsed.Expression, parsed.UnknownTerms
	return evidence
}

func TestLicenseEvidenceIsImmutableAndRouteScoped(t *testing.T) {
	store := migratedStore(t)
	coordinates := license.Coordinates{Ecosystem: "npm", Namespace: "@scope", Name: "left-pad", Version: "1.3.0"}
	first, duplicate, err := store.InsertLicenseEvidence(t.Context(), resolvedEvidence(coordinates, "npm:public", "MIT"))
	if err != nil || duplicate || first == 0 {
		t.Fatalf("first insert = %d %v %v", first, duplicate, err)
	}
	second, duplicate, err := store.InsertLicenseEvidence(t.Context(), resolvedEvidence(coordinates, "npm:public", "MIT"))
	if err != nil || !duplicate || second == first {
		t.Fatalf("identical re-fetch must be a new row flagged duplicate: %d %v %v", second, duplicate, err)
	}
	changed, duplicate, err := store.InsertLicenseEvidence(t.Context(), resolvedEvidence(coordinates, "npm:public", "ISC"))
	if err != nil || duplicate {
		t.Fatalf("changed metadata = %d %v %v", changed, duplicate, err)
	}
	private, duplicate, err := store.InsertLicenseEvidence(t.Context(), resolvedEvidence(coordinates, "npm:private", "LicenseRef-Acme-Internal"))
	if err != nil || duplicate {
		t.Fatalf("private route = %d %v %v", private, duplicate, err)
	}
	latest, err := store.LatestLicenseEvidence(t.Context(), coordinates)
	if err != nil || len(latest) != 2 {
		t.Fatalf("latest = %+v, %v", latest, err)
	}
	byRoute := map[string]license.Evidence{}
	for _, evidence := range latest {
		byRoute[evidence.Route] = evidence
	}
	if byRoute["npm:public"].NormalizedExpression != "ISC" || byRoute["npm:public"].ID != changed || byRoute["npm:private"].NormalizedExpression != "LicenseRef-Acme-Internal" {
		t.Fatalf("latest per route = %+v", byRoute)
	}
	if byRoute["npm:public"].ExpressionTree == nil || byRoute["npm:public"].ExpressionTree.ID != "ISC" || byRoute["npm:public"].Detail["integrity"] != "sha512-x" {
		t.Fatalf("round trip lost tree or detail: %+v", byRoute["npm:public"])
	}
	// An outage after resolved evidence keeps the resolved row alongside the negative one.
	outage := license.Evidence{Source: license.SourceRegistryNPM, Route: "npm:public", Coordinates: coordinates, Outcome: license.OutcomeUnavailable, FetchedAt: time.Now().UTC(), RawKind: license.RawMissing,
		ParseStatus: license.NotApplicable, ResolverVersion: 1, LicenseListVersion: spdxexpr.ListVersion, Message: "registry unavailable"}
	if _, _, err := store.InsertLicenseEvidence(t.Context(), outage); err != nil {
		t.Fatal(err)
	}
	latest, err = store.LatestLicenseEvidence(t.Context(), coordinates)
	if err != nil || len(latest) != 3 {
		t.Fatalf("latest with outage = %d rows, %v", len(latest), err)
	}
	outcomes := map[license.Outcome]int{}
	for _, evidence := range latest {
		outcomes[evidence.Outcome]++
		if evidence.Route == "npm:public" && evidence.Outcome == license.OutcomeResolved && evidence.NormalizedExpression != "ISC" {
			t.Fatalf("earlier resolved evidence must be the newest resolved row: %+v", evidence)
		}
	}
	if outcomes[license.OutcomeResolved] != 2 || outcomes[license.OutcomeUnavailable] != 1 {
		t.Fatalf("outcomes = %v", outcomes)
	}
	history, err := store.LicenseEvidenceHistory(t.Context(), coordinates, 10)
	if err != nil || len(history) != 5 || history[1].ID != private || history[4].ID != first {
		t.Fatalf("history = %d rows, %v", len(history), err)
	}
	// A same-name package at a different version shares nothing.
	other, err := store.LatestLicenseEvidence(t.Context(), license.Coordinates{Ecosystem: "npm", Namespace: "@scope", Name: "left-pad", Version: "1.4.0"})
	if err != nil || len(other) != 0 {
		t.Fatalf("other version = %+v, %v", other, err)
	}
	// Evidence rows cannot be edited through the store API; verify the DB has no update path used here by checking counts are additive.
	var count int
	if err := store.pool.QueryRow(t.Context(), `select count(*) from supply_chain_license_evidence`).Scan(&count); err != nil || count != 5 {
		t.Fatalf("rows = %d, %v", count, err)
	}
}

func TestEnrichmentQueueFreshnessAndLeases(t *testing.T) {
	store := migratedStore(t)
	coordinates := license.Coordinates{Ecosystem: "maven", Namespace: "org.example", Name: "core", Version: "2.1.0"}
	created, err := store.EnqueueEnrichment(t.Context(), coordinates, "maven:test")
	if err != nil || !created {
		t.Fatalf("enqueue = %v %v", created, err)
	}
	if created, err := store.EnqueueEnrichment(t.Context(), coordinates, "maven:test"); err != nil || created {
		t.Fatalf("duplicate enqueue = %v %v", created, err)
	}
	job, err := store.ClaimEnrichment(t.Context(), "worker-a")
	if err != nil || job.Coordinates != coordinates || job.Route != "maven:test" || job.Fence != 1 || job.Attempt != 1 {
		t.Fatalf("claim = %+v %v", job, err)
	}
	if _, err := store.ClaimEnrichment(t.Context(), "worker-b"); !errors.Is(err, license.ErrNoJob) {
		t.Fatalf("second claim = %v", err)
	}
	// Unavailable retries with backoff; the stale fence cannot complete.
	if err := store.CompleteEnrichment(t.Context(), job.ID, "worker-a", job.Fence+1, license.OutcomeResolved, ""); !errors.Is(err, license.ErrFenced) {
		t.Fatalf("stale fence completed: %v", err)
	}
	if err := store.CompleteEnrichment(t.Context(), job.ID, job.LeaseOwner, job.Fence, license.OutcomeUnavailable, "registry_down"); err != nil {
		t.Fatal(err)
	}
	var state string
	var runAfter time.Time
	if err := store.pool.QueryRow(t.Context(), `select state, run_after from supply_chain_enrichment_jobs where id=$1`, job.ID).Scan(&state, &runAfter); err != nil || state != "queued" || !runAfter.After(time.Now()) {
		t.Fatalf("after unavailable: state=%s run_after=%v err=%v", state, runAfter, err)
	}
	// Negative evidence with an unexpired TTL suppresses re-enqueue; expired does not.
	negative := license.Evidence{Source: license.SourceRegistryMaven, Route: "maven:test", Coordinates: coordinates, Outcome: license.OutcomeNotFound, FetchedAt: time.Now().UTC(), RawKind: license.RawMissing,
		ParseStatus: license.NotApplicable, ResolverVersion: 1, LicenseListVersion: spdxexpr.ListVersion}
	expires := time.Now().Add(time.Hour)
	negative.ExpiresAt = &expires
	if _, err := store.pool.Exec(t.Context(), `delete from supply_chain_enrichment_jobs`); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.InsertLicenseEvidence(t.Context(), negative); err != nil {
		t.Fatal(err)
	}
	if created, err := store.EnqueueEnrichment(t.Context(), coordinates, "maven:test"); err != nil || created {
		t.Fatalf("fresh negative evidence must suppress lookups: %v %v", created, err)
	}
	expired := time.Now().Add(-time.Minute)
	negative.ExpiresAt = &expired
	if _, _, err := store.InsertLicenseEvidence(t.Context(), negative); err != nil {
		t.Fatal(err)
	}
	if created, err := store.EnqueueEnrichment(t.Context(), coordinates, "maven:test"); err != nil || !created {
		t.Fatalf("expired negative evidence must allow a lookup: %v %v", created, err)
	}
	// Another route is independent.
	if created, err := store.EnqueueEnrichment(t.Context(), coordinates, "maven:other"); err != nil || !created {
		t.Fatalf("other route = %v %v", created, err)
	}
	// Reaping requeues an expired running lease.
	job, err = store.ClaimEnrichment(t.Context(), "worker-a")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.pool.Exec(t.Context(), `update supply_chain_enrichment_jobs set lease_expires_at=now()-interval '1 second' where id=$1`, job.ID); err != nil {
		t.Fatal(err)
	}
	if reaped, err := store.ReapExpiredEnrichment(t.Context(), 10); err != nil || reaped != 1 {
		t.Fatalf("reaped = %d %v", reaped, err)
	}
	if err := store.RenewEnrichment(t.Context(), job.ID, job.LeaseOwner, job.Fence); !errors.Is(err, license.ErrFenced) {
		t.Fatalf("renew after reap = %v", err)
	}
	depths, err := store.EnrichmentQueueDepths(t.Context())
	if err != nil || depths["queued"] != 2 || depths["running"] != 0 {
		t.Fatalf("depths = %v %v", depths, err)
	}
}

func TestSnapshotCoordinatesAndAssessments(t *testing.T) {
	store := migratedStore(t)
	repositoryID := supplyChainRepository(t, store, 101, "widgets")
	collection, err := store.PublishSupplyChainSnapshot(t.Context(), publication(t, repositoryID, claimSupplyChain(t, store, repositoryID, "worker"), supplyChainFixture(t)))
	if err != nil {
		t.Fatal(err)
	}
	coordinates, err := store.SnapshotCoordinates(t.Context(), *collection.SnapshotID)
	if err != nil {
		t.Fatal(err)
	}
	// github root (no version), vendored (no purl) are excluded; npm, maven, nuget, githubactions, golang remain.
	if len(coordinates) != 5 {
		t.Fatalf("coordinates = %+v", coordinates)
	}
	npm := license.Coordinates{Ecosystem: "npm", Namespace: "@scope", Name: "left-pad", Version: "1.3.0"}
	occurrences, err := store.ComponentsForCoordinates(t.Context(), npm, 10)
	if err != nil || len(occurrences) != 1 || occurrences[0][1] != *collection.SnapshotID {
		t.Fatalf("occurrences = %v %v", occurrences, err)
	}
	declared, concluded, err := store.ComponentDeclarations(t.Context(), occurrences[0][0])
	if err != nil || declared == nil || *declared != "NOASSERTION" || concluded == nil || *concluded != "NOASSERTION" {
		t.Fatalf("declarations = %v %v %v", declared, concluded, err)
	}
	id, _, err := store.InsertLicenseEvidence(t.Context(), resolvedEvidence(npm, "npm:test", "MIT"))
	if err != nil {
		t.Fatal(err)
	}
	evidence, err := store.LatestLicenseEvidence(t.Context(), npm)
	if err != nil {
		t.Fatal(err)
	}
	assessment := license.Assess(occurrences[0][0], occurrences[0][1], declared, concluded, evidence, time.Now())
	if assessment.Status != license.AssessmentResolved || assessment.NormalizedExpression != "MIT" || len(assessment.EvidenceIDs) != 1 || assessment.EvidenceIDs[0] != id {
		t.Fatalf("assessment = %+v", assessment)
	}
	if err := store.UpsertAssessment(t.Context(), assessment); err != nil {
		t.Fatal(err)
	}
	stored, err := store.SupplyChainAssessments(t.Context(), *collection.SnapshotID, []int64{occurrences[0][0]})
	if err != nil || len(stored) != 1 || stored[occurrences[0][0]].Status != license.AssessmentResolved || string(stored[occurrences[0][0]].EvidenceFingerprint) != string(assessment.EvidenceFingerprint) {
		t.Fatalf("stored = %+v %v", stored, err)
	}
	counts, err := store.SupplyChainAssessmentCounts(t.Context(), *collection.SnapshotID)
	if err != nil || counts["resolved"] != 1 {
		t.Fatalf("counts = %v %v", counts, err)
	}
	// The assessment is rebuilt in place (one row per component) and cascades with the snapshot.
	assessment.Status = license.AssessmentConflict
	if err := store.UpsertAssessment(t.Context(), assessment); err != nil {
		t.Fatal(err)
	}
	if counts, _ := store.SupplyChainAssessmentCounts(t.Context(), *collection.SnapshotID); counts["conflict"] != 1 || counts["resolved"] != 0 {
		t.Fatalf("counts after upsert = %v", counts)
	}
	component, err := store.SupplyChainComponentByElement(t.Context(), *collection.SnapshotID, "SPDXRef-npm-scope-left-pad-1.3.0")
	if err != nil || component.PURLName != "left-pad" || component.ID != occurrences[0][0] {
		t.Fatalf("component by element = %+v %v", component, err)
	}
	if _, err := store.pool.Exec(t.Context(), `delete from repositories where id=$1`, repositoryID); err != nil {
		t.Fatal(err)
	}
	var remaining int
	if err := store.pool.QueryRow(t.Context(), `select count(*) from supply_chain_component_assessments`).Scan(&remaining); err != nil || remaining != 0 {
		t.Fatalf("assessments after repository removal = %d %v", remaining, err)
	}
	// Evidence is shared registry knowledge, not repository data: it survives.
	if history, err := store.LicenseEvidenceHistory(t.Context(), npm, 10); err != nil || len(history) != 1 {
		t.Fatalf("evidence after repository removal = %d %v", len(history), err)
	}
	_ = supplychain.StreamGitHubSource
}
