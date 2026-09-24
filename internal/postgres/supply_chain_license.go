package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/balcsida/graphnest/internal/supplychain"
	"github.com/balcsida/graphnest/internal/supplychain/license"
	"github.com/balcsida/graphnest/internal/supplychain/spdxexpr"
	"github.com/jackc/pgx/v5"
)

const enrichmentLease = 2 * time.Minute

// InsertLicenseEvidence appends an immutable evidence row. duplicate reports
// that the newest existing row for the same coordinates, source, and route
// has the same material fingerprint.
func (s *Store) InsertLicenseEvidence(ctx context.Context, evidence license.Evidence) (int64, bool, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return 0, false, err
	}
	defer tx.Rollback(ctx)
	duplicate := false
	previous, err := s.latestEvidenceRow(ctx, tx, evidence.Coordinates, string(evidence.Source), evidence.Route)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return 0, false, err
	}
	if err == nil && string(previous.Fingerprint()) == string(evidence.Fingerprint()) {
		duplicate = true
	}
	var tree []byte
	if evidence.ExpressionTree != nil {
		if tree, err = json.Marshal(evidence.ExpressionTree); err != nil {
			return 0, false, err
		}
	}
	detail, err := json.Marshal(nonNilAny(evidence.Detail))
	if err != nil {
		return 0, false, err
	}
	unknown := evidence.UnknownTerms
	if unknown == nil {
		unknown = []string{}
	}
	var id int64
	err = tx.QueryRow(ctx, `insert into supply_chain_license_evidence (source, route, ecosystem, namespace, name, version, artifact_sha256, raw_value, raw_kind, parse_status,
			normalized_expression, expression_tree, unknown_terms, license_url, license_file_name, detail, resolver_version, license_list_version, content_sha256, fetched_at, expires_at, outcome, http_status, message)
		values ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18, $19, $20, $21, $22, $23, $24) returning id`,
		string(evidence.Source), evidence.Route, evidence.Coordinates.Ecosystem, evidence.Coordinates.Namespace, evidence.Coordinates.Name, evidence.Coordinates.Version, evidence.ArtifactSHA256,
		evidence.RawValue, string(evidence.RawKind), string(evidence.ParseStatus), evidence.NormalizedExpression, nullableJSON(tree), unknown, evidence.LicenseURL, evidence.LicenseFileName, detail,
		evidence.ResolverVersion, evidence.LicenseListVersion, evidence.ContentSHA256, evidence.FetchedAt.UTC(), evidence.ExpiresAt, string(evidence.Outcome), evidence.HTTPStatus, evidence.Message).Scan(&id)
	if err != nil {
		return 0, false, err
	}
	return id, duplicate, tx.Commit(ctx)
}

func nullableJSON(data []byte) any {
	if len(data) == 0 {
		return nil
	}
	return data
}

func nonNilAny(values map[string]any) map[string]any {
	if values == nil {
		return map[string]any{}
	}
	return values
}

const licenseEvidenceColumns = `id, source, route, ecosystem, namespace, name, version, artifact_sha256, raw_value, raw_kind, parse_status, normalized_expression, expression_tree, unknown_terms,
	license_url, license_file_name, detail, resolver_version, license_list_version, content_sha256, fetched_at, expires_at, outcome, http_status, message`

func scanLicenseEvidence(row interface{ Scan(...any) error }) (license.Evidence, error) {
	var evidence license.Evidence
	var source, rawKind, parseStatus, outcome string
	var tree, detail []byte
	if err := row.Scan(&evidence.ID, &source, &evidence.Route, &evidence.Coordinates.Ecosystem, &evidence.Coordinates.Namespace, &evidence.Coordinates.Name, &evidence.Coordinates.Version, &evidence.ArtifactSHA256,
		&evidence.RawValue, &rawKind, &parseStatus, &evidence.NormalizedExpression, &tree, &evidence.UnknownTerms, &evidence.LicenseURL, &evidence.LicenseFileName, &detail, &evidence.ResolverVersion,
		&evidence.LicenseListVersion, &evidence.ContentSHA256, &evidence.FetchedAt, &evidence.ExpiresAt, &outcome, &evidence.HTTPStatus, &evidence.Message); err != nil {
		return license.Evidence{}, err
	}
	evidence.Source, evidence.RawKind, evidence.ParseStatus, evidence.Outcome = license.Source(source), license.RawKind(rawKind), spdxexpr.Status(parseStatus), license.Outcome(outcome)
	if len(tree) > 0 {
		evidence.ExpressionTree = &spdxexpr.Node{}
		if err := json.Unmarshal(tree, evidence.ExpressionTree); err != nil {
			return license.Evidence{}, err
		}
	}
	if err := json.Unmarshal(detail, &evidence.Detail); err != nil {
		return license.Evidence{}, err
	}
	return evidence, nil
}

func (s *Store) latestEvidenceRow(ctx context.Context, tx pgx.Tx, coordinates license.Coordinates, source, route string) (license.Evidence, error) {
	return scanLicenseEvidence(tx.QueryRow(ctx, `select `+licenseEvidenceColumns+` from supply_chain_license_evidence
		where ecosystem=$1 and namespace=$2 and name=$3 and version=$4 and source=$5 and route=$6 order by id desc limit 1`,
		coordinates.Ecosystem, coordinates.Namespace, coordinates.Name, coordinates.Version, source, route))
}

// LatestLicenseEvidence returns, per (source, route), the newest row and,
// when that row is a negative outcome, also the newest resolved row, so an
// outage keeps earlier evidence visible with its age. Ordered by ID.
func (s *Store) LatestLicenseEvidence(ctx context.Context, coordinates license.Coordinates) ([]license.Evidence, error) {
	rows, err := s.pool.Query(ctx, `with scoped as (
			select `+licenseEvidenceColumns+` from supply_chain_license_evidence where ecosystem=$1 and namespace=$2 and name=$3 and version=$4
		), newest as (
			select distinct on (source, route) * from scoped order by source, route, id desc
		), newest_resolved as (
			select distinct on (source, route) * from scoped where outcome='resolved' order by source, route, id desc
		)
		select * from newest
		union
		select r.* from newest_resolved r join newest n on n.source=r.source and n.route=r.route and n.outcome<>'resolved'
		order by id`, coordinates.Ecosystem, coordinates.Namespace, coordinates.Name, coordinates.Version)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []license.Evidence
	for rows.Next() {
		evidence, err := scanLicenseEvidence(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, evidence)
	}
	return result, rows.Err()
}

// LicenseEvidenceHistory lists every evidence row for the coordinates, newest
// first, bounded. Callers authorize the coordinates through an authorized
// occurrence first.
func (s *Store) LicenseEvidenceHistory(ctx context.Context, coordinates license.Coordinates, limit int) ([]license.Evidence, error) {
	rows, err := s.pool.Query(ctx, `select `+licenseEvidenceColumns+` from supply_chain_license_evidence
		where ecosystem=$1 and namespace=$2 and name=$3 and version=$4 order by id desc limit $5`, coordinates.Ecosystem, coordinates.Namespace, coordinates.Name, coordinates.Version, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []license.Evidence{}
	for rows.Next() {
		evidence, err := scanLicenseEvidence(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, evidence)
	}
	return result, rows.Err()
}

// EnqueueEnrichment queues a lookup unless one is active or fresh evidence
// exists for the route: a resolved row (any age; metadata changes are picked
// up by explicit re-enrichment), or an unexpired negative row.
func (s *Store) EnqueueEnrichment(ctx context.Context, coordinates license.Coordinates, route string) (bool, error) {
	var fresh bool
	if err := s.pool.QueryRow(ctx, `select exists(select 1 from supply_chain_license_evidence where ecosystem=$1 and namespace=$2 and name=$3 and version=$4 and route=$5
		and (outcome='resolved' or expires_at is null or expires_at>now()) order by id desc limit 1)`, coordinates.Ecosystem, coordinates.Namespace, coordinates.Name, coordinates.Version, route).Scan(&fresh); err != nil {
		return false, err
	}
	if fresh {
		// Only the newest row decides; check it explicitly.
		var outcome string
		var expires *time.Time
		err := s.pool.QueryRow(ctx, `select outcome, expires_at from supply_chain_license_evidence where ecosystem=$1 and namespace=$2 and name=$3 and version=$4 and route=$5 order by id desc limit 1`,
			coordinates.Ecosystem, coordinates.Namespace, coordinates.Name, coordinates.Version, route).Scan(&outcome, &expires)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return false, err
		}
		if err == nil && (outcome == "resolved" || expires == nil || expires.After(time.Now())) {
			return false, nil
		}
	}
	result, err := s.pool.Exec(ctx, `insert into supply_chain_enrichment_jobs (ecosystem, namespace, name, version, route, state) values ($1, $2, $3, $4, $5, 'queued')
		on conflict do nothing`, coordinates.Ecosystem, coordinates.Namespace, coordinates.Name, coordinates.Version, route)
	if err != nil {
		return false, err
	}
	return result.RowsAffected() == 1, nil
}

const enrichmentJobColumns = `id, ecosystem, namespace, name, version, route, state, attempt, max_attempts, run_after, coalesce(lease_owner, ''), lease_expires_at, fence, error_code`

func scanEnrichmentJob(row interface{ Scan(...any) error }) (license.Job, error) {
	var job license.Job
	err := row.Scan(&job.ID, &job.Coordinates.Ecosystem, &job.Coordinates.Namespace, &job.Coordinates.Name, &job.Coordinates.Version, &job.Route, &job.State, &job.Attempt, &job.MaxAttempts, &job.RunAfter, &job.LeaseOwner, &job.LeaseExpiresAt, &job.Fence, &job.ErrorCode)
	return job, err
}

func (s *Store) ClaimEnrichment(ctx context.Context, owner string) (license.Job, error) {
	job, err := scanEnrichmentJob(s.pool.QueryRow(ctx, `with next as (
			select id from supply_chain_enrichment_jobs where state='queued' and run_after<=now() order by run_after, id for update skip locked limit 1
		)
		update supply_chain_enrichment_jobs set state='running', attempt=attempt+1, lease_owner=$1, fence=fence+1, lease_expires_at=now()+$2::interval, updated_at=now()
		where id=(select id from next) returning `+enrichmentJobColumns, owner, enrichmentLease))
	if errors.Is(err, pgx.ErrNoRows) {
		return license.Job{}, license.ErrNoJob
	}
	return job, err
}

func (s *Store) RenewEnrichment(ctx context.Context, id int64, owner string, fence int64) error {
	result, err := s.pool.Exec(ctx, `update supply_chain_enrichment_jobs set lease_expires_at=now()+$4::interval, updated_at=now()
		where id=$1 and state='running' and lease_owner=$2 and fence=$3 and lease_expires_at>now()`, id, owner, fence, enrichmentLease)
	if err != nil {
		return err
	}
	if result.RowsAffected() != 1 {
		return license.ErrFenced
	}
	return nil
}

// CompleteEnrichment finishes a leased job. Unavailable outcomes retry with
// backoff up to max_attempts; everything else is terminal (the evidence row
// records what happened).
func (s *Store) CompleteEnrichment(ctx context.Context, id int64, owner string, fence int64, outcome license.Outcome, errorCode string) error {
	state := "succeeded"
	switch outcome {
	case license.OutcomeUnavailable:
		state = "retry"
	case license.OutcomeRejected:
		state = "skipped"
	}
	result, err := s.pool.Exec(ctx, `update supply_chain_enrichment_jobs set
			state=case when $4='retry' and attempt<max_attempts then 'queued' when $4='retry' then 'failed' else $4 end,
			run_after=case when $4='retry' and attempt<max_attempts then now()+interval '1 second'*least(60*power(4::double precision, attempt-1), 3600)*(0.5+random()/2) else run_after end,
			error_code=$5, lease_owner=null, lease_expires_at=null, updated_at=now()
		where id=$1 and state='running' and lease_owner=$2 and fence=$3`, id, owner, fence, state, errorCode)
	if err != nil {
		return err
	}
	if result.RowsAffected() != 1 {
		return license.ErrFenced
	}
	return nil
}

func (s *Store) ReapExpiredEnrichment(ctx context.Context, limit int) (int64, error) {
	result, err := s.pool.Exec(ctx, `update supply_chain_enrichment_jobs set state=case when attempt<max_attempts then 'queued' else 'failed' end, error_code='lease_expired',
			lease_owner=null, lease_expires_at=null, run_after=now()+interval '30 seconds', updated_at=now()
		where id in (select id from supply_chain_enrichment_jobs where state='running' and lease_expires_at<=now() order by lease_expires_at, id for update skip locked limit $1)`, limit)
	if err != nil {
		return 0, err
	}
	return result.RowsAffected(), nil
}

// EnrichmentQueueDepths reports queued/running enrichment jobs.
func (s *Store) EnrichmentQueueDepths(ctx context.Context) (map[string]int64, error) {
	rows, err := s.pool.Query(ctx, `select state, count(*) from supply_chain_enrichment_jobs where state in ('queued','running') group by state`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	depths := map[string]int64{"queued": 0, "running": 0}
	for rows.Next() {
		var state string
		var count int64
		if err := rows.Scan(&state, &count); err != nil {
			return nil, err
		}
		depths[state] = count
	}
	return depths, rows.Err()
}

// ComponentsForCoordinates returns (component_id, snapshot_id) of occurrences
// in every stream's latest snapshot that carry the exact coordinates.
func (s *Store) ComponentsForCoordinates(ctx context.Context, coordinates license.Coordinates, limit int) ([][2]int64, error) {
	rows, err := s.pool.Query(ctx, `select c.id, c.snapshot_id from supply_chain_components c
		join supply_chain_streams st on st.latest_snapshot_id=c.snapshot_id
		where c.ecosystem=$1 and coalesce(c.purl_namespace, '')=$2 and c.purl_name=$3 and coalesce(c.purl_version, c.version, '')=$4 order by c.id limit $5`,
		coordinates.Ecosystem, coordinates.Namespace, coordinates.Name, coordinates.Version, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result [][2]int64
	for rows.Next() {
		var pair [2]int64
		if err := rows.Scan(&pair[0], &pair[1]); err != nil {
			return nil, err
		}
		result = append(result, pair)
	}
	return result, rows.Err()
}

// ComponentDeclarations returns the producer's raw declared and concluded
// values for one occurrence.
func (s *Store) ComponentDeclarations(ctx context.Context, componentID int64) (*string, *string, error) {
	var declared, concluded *string
	err := s.pool.QueryRow(ctx, `select license_declared_raw, license_concluded_raw from supply_chain_components where id=$1`, componentID).Scan(&declared, &concluded)
	return declared, concluded, err
}

func (s *Store) UpsertAssessment(ctx context.Context, assessment license.Assessment) error {
	ids := assessment.EvidenceIDs
	if ids == nil {
		ids = []int64{}
	}
	_, err := s.pool.Exec(ctx, `insert into supply_chain_component_assessments (component_id, snapshot_id, status, normalized_expression, evidence_ids, conflict_detail, assessed_at, evidence_fingerprint)
		values ($1, $2, $3, $4, $5, $6, $7, $8)
		on conflict (component_id) do update set status=excluded.status, normalized_expression=excluded.normalized_expression, evidence_ids=excluded.evidence_ids,
			conflict_detail=excluded.conflict_detail, assessed_at=excluded.assessed_at, evidence_fingerprint=excluded.evidence_fingerprint`,
		assessment.ComponentID, assessment.SnapshotID, string(assessment.Status), assessment.NormalizedExpression, ids, assessment.ConflictDetail, assessment.AssessedAt.UTC(), assessment.EvidenceFingerprint)
	return err
}

// LatestSnapshotIDs lists every stream's current snapshot.
func (s *Store) LatestSnapshotIDs(ctx context.Context) ([]int64, error) {
	rows, err := s.pool.Query(ctx, `select latest_snapshot_id from supply_chain_streams where latest_snapshot_id is not null order by latest_snapshot_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		result = append(result, id)
	}
	return result, rows.Err()
}

// SnapshotCoordinates lists distinct exact coordinates of a snapshot's
// components that have a purl name and a version.
func (s *Store) SnapshotCoordinates(ctx context.Context, snapshotID int64) ([]license.Coordinates, error) {
	rows, err := s.pool.Query(ctx, `select distinct ecosystem, coalesce(purl_namespace, ''), purl_name, coalesce(purl_version, version) from supply_chain_components
		where snapshot_id=$1 and ecosystem is not null and purl_name is not null and coalesce(purl_version, version, '')<>'' order by 1, 2, 3, 4`, snapshotID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []license.Coordinates
	for rows.Next() {
		var item license.Coordinates
		if err := rows.Scan(&item.Ecosystem, &item.Namespace, &item.Name, &item.Version); err != nil {
			return nil, err
		}
		result = append(result, item)
	}
	return result, rows.Err()
}

// SupplyChainAssessments returns assessments for a snapshot's components,
// keyed by component ID. Callers authorize the snapshot first.
func (s *Store) SupplyChainAssessments(ctx context.Context, snapshotID int64, componentIDs []int64) (map[int64]license.Assessment, error) {
	rows, err := s.pool.Query(ctx, `select component_id, snapshot_id, status, normalized_expression, evidence_ids, conflict_detail, assessed_at, evidence_fingerprint
		from supply_chain_component_assessments where snapshot_id=$1 and component_id=any($2)`, snapshotID, componentIDs)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := map[int64]license.Assessment{}
	for rows.Next() {
		var assessment license.Assessment
		var status string
		if err := rows.Scan(&assessment.ComponentID, &assessment.SnapshotID, &status, &assessment.NormalizedExpression, &assessment.EvidenceIDs, &assessment.ConflictDetail, &assessment.AssessedAt, &assessment.EvidenceFingerprint); err != nil {
			return nil, err
		}
		assessment.Status = license.AssessmentStatus(status)
		result[assessment.ComponentID] = assessment
	}
	return result, rows.Err()
}

// SupplyChainAssessmentCounts summarizes assessment statuses for a snapshot.
func (s *Store) SupplyChainAssessmentCounts(ctx context.Context, snapshotID int64) (map[string]int, error) {
	rows, err := s.pool.Query(ctx, `select status, count(*) from supply_chain_component_assessments where snapshot_id=$1 group by status`, snapshotID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	counts := map[string]int{}
	for rows.Next() {
		var status string
		var count int
		if err := rows.Scan(&status, &count); err != nil {
			return nil, err
		}
		counts[status] = count
	}
	return counts, rows.Err()
}

// SupplyChainComponentByElement resolves one occurrence in an authorized
// snapshot by its document element ID.
func (s *Store) SupplyChainComponentByElement(ctx context.Context, snapshotID int64, elementID string) (supplychain.Component, error) {
	var component supplychain.Component
	var qualifiers, checksums []byte
	err := s.pool.QueryRow(ctx, `select id, snapshot_id, ordinal, element_id, name, version, purl, coalesce(ecosystem, ''), coalesce(purl_namespace, ''), coalesce(purl_name, ''),
			coalesce(purl_version, ''), qualifiers, license_declared_raw, license_concluded_raw, download_location, supplier, checksums, is_root
		from supply_chain_components where snapshot_id=$1 and element_id=$2`, snapshotID, elementID).Scan(&component.ID, &component.SnapshotID, &component.Ordinal, &component.ElementID, &component.Name,
		&component.Version, &component.PURL, &component.Ecosystem, &component.PURLNamespace, &component.PURLName, &component.PURLVersion, &qualifiers, &component.LicenseDeclaredRaw, &component.LicenseConcludedRaw,
		&component.DownloadLocation, &component.Supplier, &checksums, &component.IsRoot)
	if err != nil {
		return supplychain.Component{}, err
	}
	if err := json.Unmarshal(qualifiers, &component.Qualifiers); err != nil {
		return supplychain.Component{}, err
	}
	if err := json.Unmarshal(checksums, &component.Checksums); err != nil {
		return supplychain.Component{}, err
	}
	return component, nil
}
