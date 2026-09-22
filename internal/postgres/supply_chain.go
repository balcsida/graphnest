package postgres

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"time"

	"github.com/balcsida/graphnest/internal/scipgraph"
	"github.com/balcsida/graphnest/internal/supplychain"
	"github.com/jackc/pgx/v5"
)

// ErrSupplyChainFenced reports that a publication or completion lost its
// lease: another worker holds a newer lease on the job, or the job was
// cancelled. Nothing was written.
var ErrSupplyChainFenced = errors.New("supply chain job lease lost")

const supplyChainLease = 2 * time.Minute

// SupplyChainPublication is the atomic input of a successful collection: the
// original document bytes, the normalized content, and the attempt metadata.
type SupplyChainPublication struct {
	RepositoryID     int64
	JobID            *int64
	JobOwner         string
	JobFence         int64
	Producer         supplychain.Producer
	Subject          supplychain.Subject
	StreamKey        string
	Format           supplychain.Format
	MediaType        string
	Document         []byte
	Normalized       supplychain.Normalized
	CollectedAt      time.Time
	StartedAt        time.Time
	HTTPStatus       *int
	SubjectRevision  string
	SubjectAssurance supplychain.Assurance
}

// SupplyChainFailure records an attempt that produced no new snapshot.
type SupplyChainFailure struct {
	RepositoryID      int64
	JobID             *int64
	JobOwner          string
	JobFence          int64
	Producer          supplychain.Producer
	Subject           supplychain.Subject
	StreamKey         string
	StartedAt         time.Time
	FinishedAt        time.Time
	Outcome           supplychain.Outcome
	HTTPStatus        *int
	RetryAfterSeconds *int
	ErrorCode         string
	Message           string
}

// EnqueueSupplyChainJob queues one refresh for a repository stream. A queued
// job already covering the stream is returned unchanged (created=false); a
// manual request raises its priority so it runs before scheduled work.
func (s *Store) EnqueueSupplyChainJob(ctx context.Context, repositoryID int64, streamKey, reason, requestedBy string, priority int, runAfter time.Time) (supplychain.Job, bool, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return supplychain.Job{}, false, err
	}
	defer tx.Rollback(ctx)
	if err := tx.QueryRow(ctx, `select id from repositories where id=$1 for update`, repositoryID).Scan(&repositoryID); err != nil {
		return supplychain.Job{}, false, err
	}
	var job supplychain.Job
	err = tx.QueryRow(ctx, `update supply_chain_jobs set priority=greatest(priority, $3), run_after=least(run_after, $4), updated_at=now()
		where repository_id=$1 and stream_key=$2 and state='queued' returning `+supplyChainJobColumns, repositoryID, streamKey, priority, runAfter).Scan(supplyChainJobFields(&job)...)
	if err == nil {
		return job, false, tx.Commit(ctx)
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return supplychain.Job{}, false, err
	}
	err = tx.QueryRow(ctx, `insert into supply_chain_jobs (repository_id, stream_key, reason, state, priority, run_after, requested_by)
		values ($1, $2, $3, 'queued', $4, $5, $6) returning `+supplyChainJobColumns, repositoryID, streamKey, reason, priority, runAfter, requestedBy).Scan(supplyChainJobFields(&job)...)
	if err != nil {
		return supplychain.Job{}, false, err
	}
	return job, true, tx.Commit(ctx)
}

// ClaimSupplyChainJob leases the next runnable job. Repositories that are
// disabled, archived, in inactive installations, or opted out are skipped:
// their queued jobs are superseded instead of run.
func (s *Store) ClaimSupplyChainJob(ctx context.Context, owner string) (supplychain.Job, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return supplychain.Job{}, err
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `update supply_chain_jobs set state='superseded', error_code='repository_unavailable', updated_at=now()
		where state='queued' and run_after<=now() and id in (
			select j.id from supply_chain_jobs j
			join repositories r on r.id=j.repository_id
			join installations i on i.id=r.installation_id
			left join supply_chain_streams st on st.repository_id=r.id and st.stream_key=j.stream_key
			where j.state='queued' and j.run_after<=now()
			and (not r.enabled or r.archived or i.status<>'active' or coalesce(st.opt_out, false)))`); err != nil {
		return supplychain.Job{}, err
	}
	var job supplychain.Job
	err = tx.QueryRow(ctx, `with next as (
			select j.id from supply_chain_jobs j
			join repositories r on r.id=j.repository_id
			join installations i on i.id=r.installation_id
			where j.state='queued' and j.run_after<=now() and r.enabled and not r.archived and i.status='active'
			and not exists (select 1 from supply_chain_jobs running where running.repository_id=j.repository_id and running.stream_key=j.stream_key and running.state='running')
			order by j.priority desc, j.run_after, j.id
			for update of j skip locked limit 1
		)
		update supply_chain_jobs set state='running', attempt=attempt+1, lease_owner=$1, fence=fence+1,
			lease_expires_at=now()+$2::interval, updated_at=now()
		where id=(select id from next) returning `+supplyChainJobColumns, owner, supplyChainLease).Scan(supplyChainJobFields(&job)...)
	if errors.Is(err, pgx.ErrNoRows) {
		return supplychain.Job{}, ErrNoJob
	}
	if err != nil {
		return supplychain.Job{}, err
	}
	return job, tx.Commit(ctx)
}

func (s *Store) RenewSupplyChainLease(ctx context.Context, id int64, owner string, fence int64) error {
	result, err := s.pool.Exec(ctx, `update supply_chain_jobs set lease_expires_at=now()+$4::interval, updated_at=now()
		where id=$1 and state='running' and lease_owner=$2 and fence=$3 and lease_expires_at>now()`, id, owner, fence, supplyChainLease)
	if err != nil {
		return err
	}
	if result.RowsAffected() != 1 {
		return ErrSupplyChainFenced
	}
	return nil
}

// CancelSupplyChainJob cancels a queued or running job. A running job's
// worker learns of it when it next renews or publishes.
func (s *Store) CancelSupplyChainJob(ctx context.Context, id int64) (bool, error) {
	result, err := s.pool.Exec(ctx, `update supply_chain_jobs set state='cancelled', lease_owner=null, lease_expires_at=null, updated_at=now()
		where id=$1 and state in ('queued','running')`, id)
	if err != nil {
		return false, err
	}
	return result.RowsAffected() == 1, nil
}

// ReapExpiredSupplyChainJobs requeues or fails jobs whose lease expired
// without completion (worker crash).
func (s *Store) ReapExpiredSupplyChainJobs(ctx context.Context, limit int) (int64, error) {
	result, err := s.pool.Exec(ctx, `update supply_chain_jobs set
			state=case when attempt<max_attempts then 'queued' else 'failed' end,
			error_code='lease_expired', lease_owner=null, lease_expires_at=null,
			run_after=case when attempt<max_attempts then now()+interval '1 second'*least(60*power(4::double precision, attempt-1), 3600)*(0.5+random()/2) else run_after end,
			updated_at=now()
		where id in (select id from supply_chain_jobs where state='running' and lease_expires_at<=now() order by lease_expires_at, id for update skip locked limit $1)`, limit)
	if err != nil {
		return 0, err
	}
	return result.RowsAffected(), nil
}

// PublishSupplyChainSnapshot atomically stores the document (reusing identical
// bytes), inserts the immutable snapshot with its components and edges,
// records the collection, advances the stream pointer, and completes the job.
// When the document is byte-identical to the stream's current snapshot the
// attempt is recorded as unchanged and no snapshot is created. The write is
// fenced by the job's lease so a stale worker cannot publish over a newer one.
func (s *Store) PublishSupplyChainSnapshot(ctx context.Context, publication SupplyChainPublication) (supplychain.Collection, error) {
	if len(publication.Normalized.Components) > supplychain.DefaultMaxComponents*2 {
		return supplychain.Collection{}, supplychain.ErrTooLarge
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return supplychain.Collection{}, err
	}
	defer tx.Rollback(ctx)
	if err := fenceSupplyChainJob(ctx, tx, publication.JobID, publication.JobOwner, publication.JobFence); err != nil {
		return supplychain.Collection{}, err
	}
	if err := tx.QueryRow(ctx, `select id from repositories where id=$1 for update`, publication.RepositoryID).Scan(&publication.RepositoryID); err != nil {
		return supplychain.Collection{}, err
	}
	digest := sha256.Sum256(publication.Document)
	var documentID int64
	if err := tx.QueryRow(ctx, `insert into supply_chain_documents (sha256, format, media_type, byte_size, body) values ($1, $2, $3, $4, $5)
		on conflict (sha256) do update set sha256=excluded.sha256 returning id`, digest[:], string(publication.Format), publication.MediaType, len(publication.Document), publication.Document).Scan(&documentID); err != nil {
		return supplychain.Collection{}, err
	}
	finished := time.Now().UTC()
	var currentDocumentID *int64
	err = tx.QueryRow(ctx, `select snap.document_id from supply_chain_streams st join supply_chain_snapshots snap on snap.id=st.latest_snapshot_id
		where st.repository_id=$1 and st.stream_key=$2`, publication.RepositoryID, publication.StreamKey).Scan(&currentDocumentID)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return supplychain.Collection{}, err
	}
	collection := supplychain.Collection{RepositoryID: publication.RepositoryID, JobID: publication.JobID, Producer: publication.Producer, StreamKey: publication.StreamKey,
		StartedAt: publication.StartedAt, FinishedAt: finished, HTTPStatus: publication.HTTPStatus, DocumentID: &documentID}
	if currentDocumentID != nil && *currentDocumentID == documentID {
		collection.Outcome = supplychain.OutcomeUnchanged
		var latest int64
		if err := tx.QueryRow(ctx, `select latest_snapshot_id from supply_chain_streams where repository_id=$1 and stream_key=$2`, publication.RepositoryID, publication.StreamKey).Scan(&latest); err != nil {
			return supplychain.Collection{}, err
		}
		collection.SnapshotID = &latest
	} else {
		collection.Outcome = supplychain.OutcomePublished
		snapshotID, err := insertSupplyChainSnapshot(ctx, tx, publication, documentID)
		if err != nil {
			return supplychain.Collection{}, err
		}
		collection.SnapshotID = &snapshotID
	}
	if err := tx.QueryRow(ctx, `insert into supply_chain_collections (repository_id, job_id, producer, stream_key, started_at, finished_at, outcome, http_status, snapshot_id, document_id)
		values ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10) returning id`, collection.RepositoryID, collection.JobID, string(collection.Producer), collection.StreamKey,
		collection.StartedAt, collection.FinishedAt, string(collection.Outcome), collection.HTTPStatus, collection.SnapshotID, collection.DocumentID).Scan(&collection.ID); err != nil {
		return supplychain.Collection{}, err
	}
	if _, err := tx.Exec(ctx, `insert into supply_chain_streams (repository_id, stream_key, producer, subject, latest_snapshot_id, latest_collection_id, last_success_at, last_attempt_at, last_outcome)
		values ($1, $2, $3, $4, $5, $6, $7, $7, $8)
		on conflict (repository_id, stream_key) do update set latest_snapshot_id=excluded.latest_snapshot_id, latest_collection_id=excluded.latest_collection_id,
			last_success_at=excluded.last_success_at, last_attempt_at=excluded.last_attempt_at, last_outcome=excluded.last_outcome, updated_at=now()`,
		collection.RepositoryID, collection.StreamKey, string(publication.Producer), string(publication.Subject), collection.SnapshotID, collection.ID, finished, string(collection.Outcome)); err != nil {
		return supplychain.Collection{}, err
	}
	if err := completeSupplyChainJob(ctx, tx, publication.JobID, "succeeded", ""); err != nil {
		return supplychain.Collection{}, err
	}
	return collection, tx.Commit(ctx)
}

func insertSupplyChainSnapshot(ctx context.Context, tx pgx.Tx, publication SupplyChainPublication, documentID int64) (int64, error) {
	normalized := publication.Normalized
	warnings, err := json.Marshal(nonNilWarnings(normalized.Warnings))
	if err != nil {
		return 0, err
	}
	var revision *string
	if publication.SubjectRevision != "" {
		revision = &publication.SubjectRevision
	}
	assurance := publication.SubjectAssurance
	if assurance == "" {
		assurance = supplychain.AssuranceUnknown
	}
	roots := normalized.RootElementIDs
	if roots == nil {
		roots = []string{}
	}
	var snapshotID int64
	if err := tx.QueryRow(ctx, `insert into supply_chain_snapshots (repository_id, document_id, producer, subject, stream_key, collected_at, created_at_claimed,
			producer_tool, document_namespace, document_name, spdx_version, data_license, subject_revision, subject_assurance, root_element_ids,
			parser_version, component_count, edge_count, warning_count, warnings)
		values ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18, $19, $20) returning id`,
		publication.RepositoryID, documentID, string(publication.Producer), string(publication.Subject), publication.StreamKey, publication.CollectedAt.UTC(), normalized.CreatedAtClaimed,
		normalized.ProducerTool, normalized.DocumentNamespace, normalized.DocumentName, normalized.SPDXVersion, normalized.DataLicense, revision, string(assurance), roots,
		supplychain.ParserVersion, len(normalized.Components), len(normalized.Relationships), normalized.WarningCount, warnings).Scan(&snapshotID); err != nil {
		return 0, err
	}
	if _, err := tx.CopyFrom(ctx, pgx.Identifier{"supply_chain_components"},
		[]string{"snapshot_id", "ordinal", "element_id", "name", "version", "purl", "ecosystem", "purl_namespace", "purl_name", "purl_version", "qualifiers",
			"license_declared_raw", "license_concluded_raw", "download_location", "supplier", "checksums", "is_root"},
		pgx.CopyFromSlice(len(normalized.Components), func(index int) ([]any, error) {
			component := normalized.Components[index]
			qualifiers, err := json.Marshal(nonNilMap(component.Qualifiers))
			if err != nil {
				return nil, err
			}
			checksums, err := json.Marshal(nonNilChecksums(component.Checksums))
			if err != nil {
				return nil, err
			}
			return []any{snapshotID, component.Ordinal, component.ElementID, component.Name, component.Version, component.PURL, nullable(component.Ecosystem), nullable(component.PURLNamespace),
				nullable(component.PURLName), nullable(component.PURLVersion), qualifiers, component.LicenseDeclaredRaw, component.LicenseConcludedRaw, component.DownloadLocation,
				component.Supplier, checksums, component.IsRoot}, nil
		})); err != nil {
		return 0, err
	}
	if _, err := tx.CopyFrom(ctx, pgx.Identifier{"supply_chain_relationships"}, []string{"snapshot_id", "from_element", "relationship", "to_element", "resolved"},
		pgx.CopyFromSlice(len(normalized.Relationships), func(index int) ([]any, error) {
			edge := normalized.Relationships[index]
			return []any{snapshotID, edge.FromElement, edge.Type, edge.ToElement, edge.Resolved}, nil
		})); err != nil {
		return 0, err
	}
	return snapshotID, nil
}

// RecordSupplyChainFailure records a failed attempt, updates the stream's
// last-attempt fields without touching its latest snapshot, and moves the job
// to queued (with backoff), failed, or cancelled.
func (s *Store) RecordSupplyChainFailure(ctx context.Context, failure SupplyChainFailure) (supplychain.Collection, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return supplychain.Collection{}, err
	}
	defer tx.Rollback(ctx)
	if failure.Outcome != supplychain.OutcomeCancelled {
		if err := fenceSupplyChainJob(ctx, tx, failure.JobID, failure.JobOwner, failure.JobFence); err != nil {
			return supplychain.Collection{}, err
		}
	}
	collection := supplychain.Collection{RepositoryID: failure.RepositoryID, JobID: failure.JobID, Producer: failure.Producer, StreamKey: failure.StreamKey, StartedAt: failure.StartedAt,
		FinishedAt: failure.FinishedAt, Outcome: failure.Outcome, HTTPStatus: failure.HTTPStatus, RetryAfterSeconds: failure.RetryAfterSeconds, ErrorCode: failure.ErrorCode, Message: failure.Message}
	if collection.FinishedAt.IsZero() {
		collection.FinishedAt = time.Now().UTC()
	}
	if err := tx.QueryRow(ctx, `insert into supply_chain_collections (repository_id, job_id, producer, stream_key, started_at, finished_at, outcome, http_status, retry_after_seconds, error_code, message)
		values ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11) returning id`, collection.RepositoryID, collection.JobID, string(collection.Producer), collection.StreamKey, collection.StartedAt,
		collection.FinishedAt, string(collection.Outcome), collection.HTTPStatus, collection.RetryAfterSeconds, collection.ErrorCode, collection.Message).Scan(&collection.ID); err != nil {
		return supplychain.Collection{}, err
	}
	if _, err := tx.Exec(ctx, `insert into supply_chain_streams (repository_id, stream_key, producer, subject, latest_collection_id, last_attempt_at, last_outcome)
		values ($1, $2, $3, $4, $5, $6, $7)
		on conflict (repository_id, stream_key) do update set latest_collection_id=excluded.latest_collection_id, last_attempt_at=excluded.last_attempt_at,
			last_outcome=excluded.last_outcome, updated_at=now()`,
		collection.RepositoryID, collection.StreamKey, string(failure.Producer), string(failure.Subject), collection.ID, collection.FinishedAt, string(collection.Outcome)); err != nil {
		return supplychain.Collection{}, err
	}
	if failure.JobID != nil {
		var retryAfter time.Duration
		if failure.RetryAfterSeconds != nil {
			retryAfter = time.Duration(*failure.RetryAfterSeconds) * time.Second
		}
		if err := finishSupplyChainJobFailure(ctx, tx, *failure.JobID, failure.Outcome, failure.ErrorCode, retryAfter); err != nil {
			return supplychain.Collection{}, err
		}
	}
	return collection, tx.Commit(ctx)
}

// RecordSupplyChainProjectionError marks a collection whose snapshot published
// but whose compatibility projection failed, so the gap is visible and
// recoverable rather than silent.
func (s *Store) RecordSupplyChainProjectionError(ctx context.Context, collectionID int64, code string) error {
	_, err := s.pool.Exec(ctx, `update supply_chain_collections set projection_error=$2 where id=$1`, collectionID, code)
	return err
}

func fenceSupplyChainJob(ctx context.Context, tx pgx.Tx, jobID *int64, owner string, fence int64) error {
	if jobID == nil {
		return nil
	}
	var ok bool
	if err := tx.QueryRow(ctx, `select exists(select 1 from supply_chain_jobs where id=$1 and state='running' and lease_owner=$2 and fence=$3 and lease_expires_at>now() for update)`,
		*jobID, owner, fence).Scan(&ok); err != nil {
		return err
	}
	if !ok {
		return ErrSupplyChainFenced
	}
	return nil
}

func completeSupplyChainJob(ctx context.Context, tx pgx.Tx, jobID *int64, state, errorCode string) error {
	if jobID == nil {
		return nil
	}
	_, err := tx.Exec(ctx, `update supply_chain_jobs set state=$2, error_code=$3, lease_owner=null, lease_expires_at=null, updated_at=now() where id=$1`, *jobID, state, errorCode)
	return err
}

func finishSupplyChainJobFailure(ctx context.Context, tx pgx.Tx, jobID int64, outcome supplychain.Outcome, errorCode string, retryAfter time.Duration) error {
	if outcome == supplychain.OutcomeCancelled {
		return completeSupplyChainJob(ctx, tx, &jobID, "cancelled", errorCode)
	}
	var attempt, maxAttempts int
	if err := tx.QueryRow(ctx, `select attempt, max_attempts from supply_chain_jobs where id=$1`, jobID).Scan(&attempt, &maxAttempts); err != nil {
		return err
	}
	if !outcome.Retryable() || attempt >= maxAttempts {
		return completeSupplyChainJob(ctx, tx, &jobID, "failed", errorCode)
	}
	// Exponential backoff (1m, 4m, 16m, ...) capped at an hour with jitter;
	// a Retry-After hint that is longer wins, bounded by the reader to a day.
	_, err := tx.Exec(ctx, `update supply_chain_jobs set state='queued', error_code=$2, lease_owner=null, lease_expires_at=null,
		run_after=now()+greatest($3::interval, interval '1 second'*least(60*power(4::double precision, attempt-1), 3600)*(0.5+random()/2)), updated_at=now() where id=$1`,
		jobID, errorCode, retryAfter)
	return err
}

// DueSupplyChainStreams lists enabled repositories in active installations
// whose GitHub source stream has never been collected or was last attempted
// before the cutoff and is not opted out. The result is bounded.
func (s *Store) DueSupplyChainStreams(ctx context.Context, streamKey string, before time.Time, limit int) ([]int64, error) {
	rows, err := s.pool.Query(ctx, `select r.id from repositories r join installations i on i.id=r.installation_id
		left join supply_chain_streams st on st.repository_id=r.id and st.stream_key=$1
		where r.enabled and not r.archived and i.status='active' and not coalesce(st.opt_out, false)
		and (st.last_attempt_at is null or st.last_attempt_at<$2)
		and not exists (select 1 from supply_chain_jobs j where j.repository_id=r.id and j.stream_key=$1 and j.state in ('queued','running'))
		order by st.last_attempt_at nulls first, r.id limit $3`, streamKey, before, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

func (s *Store) SetSupplyChainOptOut(ctx context.Context, repositoryID int64, streamKey string, producer supplychain.Producer, subject supplychain.Subject, optOut bool) error {
	_, err := s.pool.Exec(ctx, `insert into supply_chain_streams (repository_id, stream_key, producer, subject, opt_out) values ($1, $2, $3, $4, $5)
		on conflict (repository_id, stream_key) do update set opt_out=excluded.opt_out, updated_at=now()`, repositoryID, streamKey, string(producer), string(subject), optOut)
	return err
}

// SupplyChainStream returns the stream pointer for a repository. It is a
// repository-internal read; callers authorize the repository first.
func (s *Store) SupplyChainStream(ctx context.Context, repositoryID int64, streamKey string) (supplychain.Stream, error) {
	var stream supplychain.Stream
	var outcome string
	err := s.pool.QueryRow(ctx, `select repository_id, stream_key, producer, subject, latest_snapshot_id, latest_collection_id, last_success_at, last_attempt_at, last_outcome, opt_out
		from supply_chain_streams where repository_id=$1 and stream_key=$2`, repositoryID, streamKey).
		Scan(&stream.RepositoryID, &stream.StreamKey, &stream.Producer, &stream.Subject, &stream.LatestSnapshotID, &stream.LatestCollectionID, &stream.LastSuccessAt, &stream.LastAttemptAt, &outcome, &stream.OptOut)
	stream.LastOutcome = supplychain.Outcome(outcome)
	return stream, err
}

const supplyChainSnapshotColumns = `snap.id, snap.repository_id, snap.document_id, snap.producer, snap.subject, snap.stream_key, snap.collected_at, snap.created_at_claimed,
	snap.producer_tool, snap.document_namespace, snap.document_name, snap.spdx_version, snap.data_license, coalesce(snap.subject_revision, ''), snap.subject_assurance,
	snap.root_element_ids, snap.parser_version, snap.component_count, snap.edge_count, snap.warning_count, snap.warnings, snap.published_at, doc.sha256, doc.format, doc.byte_size`

func scanSupplyChainSnapshot(row interface{ Scan(...any) error }) (supplychain.Snapshot, error) {
	var snapshot supplychain.Snapshot
	var warnings []byte
	var producer, subject, assurance, format string
	err := row.Scan(&snapshot.ID, &snapshot.RepositoryID, &snapshot.DocumentID, &producer, &subject, &snapshot.StreamKey, &snapshot.CollectedAt, &snapshot.CreatedAtClaimed,
		&snapshot.ProducerTool, &snapshot.DocumentNamespace, &snapshot.DocumentName, &snapshot.SPDXVersion, &snapshot.DataLicense, &snapshot.SubjectRevision, &assurance,
		&snapshot.RootElementIDs, &snapshot.ParserVersion, &snapshot.ComponentCount, &snapshot.EdgeCount, &snapshot.WarningCount, &warnings, &snapshot.PublishedAt,
		&snapshot.DocumentSHA256, &format, &snapshot.DocumentBytes)
	if err != nil {
		return supplychain.Snapshot{}, err
	}
	snapshot.Producer, snapshot.Subject, snapshot.SubjectAssurance, snapshot.DocumentFormat = supplychain.Producer(producer), supplychain.Subject(subject), supplychain.Assurance(assurance), supplychain.Format(format)
	if err := json.Unmarshal(warnings, &snapshot.Warnings); err != nil {
		return supplychain.Snapshot{}, err
	}
	if snapshot.Warnings == nil {
		snapshot.Warnings = []supplychain.Warning{}
	}
	return snapshot, nil
}

// SupplyChainSnapshot reads one snapshot, but only when it belongs to one of
// the given repositories: the caller passes its authorized set so an
// unauthorized snapshot ID is indistinguishable from a missing one.
func (s *Store) SupplyChainSnapshot(ctx context.Context, snapshotID int64, repositoryIDs []int64) (supplychain.Snapshot, error) {
	return scanSupplyChainSnapshot(s.pool.QueryRow(ctx, `select `+supplyChainSnapshotColumns+` from supply_chain_snapshots snap join supply_chain_documents doc on doc.id=snap.document_id
		where snap.id=$1 and snap.repository_id=any($2)`, snapshotID, repositoryIDs))
}

// SupplyChainSnapshots lists a repository stream's snapshots, newest first.
func (s *Store) SupplyChainSnapshots(ctx context.Context, repositoryID int64, streamKey string, beforeID int64, limit int) ([]supplychain.Snapshot, error) {
	rows, err := s.pool.Query(ctx, `select `+supplyChainSnapshotColumns+` from supply_chain_snapshots snap join supply_chain_documents doc on doc.id=snap.document_id
		where snap.repository_id=$1 and snap.stream_key=$2 and ($3=0 or snap.id<$3) order by snap.id desc limit $4`, repositoryID, streamKey, beforeID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	snapshots := []supplychain.Snapshot{}
	for rows.Next() {
		snapshot, err := scanSupplyChainSnapshot(rows)
		if err != nil {
			return nil, err
		}
		snapshots = append(snapshots, snapshot)
	}
	return snapshots, rows.Err()
}

// SupplyChainDocument returns the original bytes of a snapshot's document,
// again scoped to the authorized repositories.
func (s *Store) SupplyChainDocument(ctx context.Context, snapshotID int64, repositoryIDs []int64) ([]byte, string, []byte, error) {
	var body, digest []byte
	var mediaType string
	err := s.pool.QueryRow(ctx, `select doc.body, doc.media_type, doc.sha256 from supply_chain_snapshots snap join supply_chain_documents doc on doc.id=snap.document_id
		where snap.id=$1 and snap.repository_id=any($2)`, snapshotID, repositoryIDs).Scan(&body, &mediaType, &digest)
	return body, mediaType, digest, err
}

// SupplyChainComponents pages a snapshot's occurrences in document order
// after the given ordinal. Authorization is the caller's: it resolves the
// snapshot through SupplyChainSnapshot first.
func (s *Store) SupplyChainComponents(ctx context.Context, snapshotID int64, afterOrdinal int, limit int, search string) ([]supplychain.Component, error) {
	rows, err := s.pool.Query(ctx, `select id, snapshot_id, ordinal, element_id, name, version, purl, coalesce(ecosystem, ''), coalesce(purl_namespace, ''), coalesce(purl_name, ''),
			coalesce(purl_version, ''), qualifiers, license_declared_raw, license_concluded_raw, download_location, supplier, checksums, is_root
		from supply_chain_components where snapshot_id=$1 and ordinal>$2 and ($4='' or lower(name) like '%'||lower($4)||'%' or lower(coalesce(purl, '')) like '%'||lower($4)||'%')
		order by ordinal limit $3`, snapshotID, afterOrdinal, limit, escapeLike(search))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	components := []supplychain.Component{}
	for rows.Next() {
		var component supplychain.Component
		var qualifiers, checksums []byte
		if err := rows.Scan(&component.ID, &component.SnapshotID, &component.Ordinal, &component.ElementID, &component.Name, &component.Version, &component.PURL, &component.Ecosystem,
			&component.PURLNamespace, &component.PURLName, &component.PURLVersion, &qualifiers, &component.LicenseDeclaredRaw, &component.LicenseConcludedRaw, &component.DownloadLocation,
			&component.Supplier, &checksums, &component.IsRoot); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(qualifiers, &component.Qualifiers); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(checksums, &component.Checksums); err != nil {
			return nil, err
		}
		components = append(components, component)
	}
	return components, rows.Err()
}

// SupplyChainRelationships returns a snapshot's edges touching the given
// element (both directions), bounded.
func (s *Store) SupplyChainRelationships(ctx context.Context, snapshotID int64, element string, limit int) ([]supplychain.Relationship, error) {
	rows, err := s.pool.Query(ctx, `select from_element, relationship, to_element, resolved from supply_chain_relationships
		where snapshot_id=$1 and ($2='' or from_element=$2 or to_element=$2) order by id limit $3`, snapshotID, element, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	edges := []supplychain.Relationship{}
	for rows.Next() {
		var edge supplychain.Relationship
		if err := rows.Scan(&edge.FromElement, &edge.Type, &edge.ToElement, &edge.Resolved); err != nil {
			return nil, err
		}
		edges = append(edges, edge)
	}
	return edges, rows.Err()
}

func (s *Store) SupplyChainCollections(ctx context.Context, repositoryID int64, streamKey string, beforeID int64, limit int) ([]supplychain.Collection, error) {
	rows, err := s.pool.Query(ctx, `select id, repository_id, job_id, producer, stream_key, started_at, finished_at, outcome, http_status, retry_after_seconds, snapshot_id, document_id, error_code, message, projection_error
		from supply_chain_collections where repository_id=$1 and stream_key=$2 and ($3=0 or id<$3) order by id desc limit $4`, repositoryID, streamKey, beforeID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	collections := []supplychain.Collection{}
	for rows.Next() {
		var collection supplychain.Collection
		var producer, outcome string
		if err := rows.Scan(&collection.ID, &collection.RepositoryID, &collection.JobID, &producer, &collection.StreamKey, &collection.StartedAt, &collection.FinishedAt, &outcome,
			&collection.HTTPStatus, &collection.RetryAfterSeconds, &collection.SnapshotID, &collection.DocumentID, &collection.ErrorCode, &collection.Message, &collection.ProjectionError); err != nil {
			return nil, err
		}
		collection.Producer, collection.Outcome = supplychain.Producer(producer), supplychain.Outcome(outcome)
		collections = append(collections, collection)
	}
	return collections, rows.Err()
}

// SupplyChainJob reads a job scoped to the authorized repositories.
func (s *Store) SupplyChainJob(ctx context.Context, jobID int64, repositoryIDs []int64) (supplychain.Job, error) {
	var job supplychain.Job
	err := s.pool.QueryRow(ctx, `select `+supplyChainJobColumns+` from supply_chain_jobs where id=$1 and repository_id=any($2)`, jobID, repositoryIDs).Scan(supplyChainJobFields(&job)...)
	return job, err
}

// SupplyChainActiveJob returns the queued or running job of a stream, if any.
func (s *Store) SupplyChainActiveJob(ctx context.Context, repositoryID int64, streamKey string) (supplychain.Job, bool, error) {
	var job supplychain.Job
	err := s.pool.QueryRow(ctx, `select `+supplyChainJobColumns+` from supply_chain_jobs where repository_id=$1 and stream_key=$2 and state in ('queued','running') order by state desc, id limit 1`,
		repositoryID, streamKey).Scan(supplyChainJobFields(&job)...)
	if errors.Is(err, pgx.ErrNoRows) {
		return supplychain.Job{}, false, nil
	}
	return job, err == nil, err
}

func (s *Store) SupplyChainQueueDepths(ctx context.Context) (map[string]int64, error) {
	rows, err := s.pool.Query(ctx, `select state, count(*) from supply_chain_jobs where state in ('queued','running') group by state`)
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

// ProjectSupplyChainPackages rebuilds the GitHub-sourced rows of
// repository_packages from a published snapshot so SCIP cross-repository
// navigation keeps working. Manual rows are never touched, and the flattened
// projection is never read back as inventory.
func (s *Store) ProjectSupplyChainPackages(ctx context.Context, repositoryID, snapshotID int64) (int, error) {
	components, err := s.SupplyChainComponents(ctx, snapshotID, -1, supplychain.DefaultMaxComponents*2, "")
	if err != nil {
		return 0, err
	}
	edges, err := s.SupplyChainRelationships(ctx, snapshotID, "", supplychain.DefaultMaxRelationships)
	if err != nil {
		return 0, err
	}
	byElement := make(map[string]scipgraph.Package, len(components))
	var roots []string
	for _, component := range components {
		if component.PURL == nil {
			continue
		}
		pkg, err := scipgraph.ParsePackageURL(*component.PURL)
		if err != nil {
			continue
		}
		byElement[component.ElementID] = pkg
		if component.IsRoot {
			roots = append(roots, component.ElementID)
		}
	}
	seen := map[string]struct{}{}
	mappings := make([]scipgraph.PackageMapping, 0, len(byElement))
	add := func(element, relation string) {
		pkg, ok := byElement[element]
		if !ok {
			return
		}
		key := relation + "\x00" + pkg.PURL
		if _, duplicate := seen[key]; duplicate {
			return
		}
		seen[key] = struct{}{}
		mappings = append(mappings, scipgraph.PackageMapping{Package: pkg, Relation: relation, Source: "github"})
	}
	for _, root := range roots {
		add(root, "provides")
	}
	for _, edge := range edges {
		if edge.Type == "DEPENDS_ON" && edge.Resolved {
			add(edge.ToElement, "depends_on")
		}
	}
	if err := s.ReplacePackages(ctx, repositoryID, "github", mappings); err != nil {
		return 0, err
	}
	return len(mappings), nil
}

const supplyChainJobColumns = `id, repository_id, stream_key, reason, state, priority, attempt, max_attempts, run_after, coalesce(lease_owner, ''), lease_expires_at, fence, requested_by, error_code, created_at, updated_at`

func supplyChainJobFields(job *supplychain.Job) []any {
	return []any{&job.ID, &job.RepositoryID, &job.StreamKey, &job.Reason, &job.State, &job.Priority, &job.Attempt, &job.MaxAttempts, &job.RunAfter, &job.LeaseOwner, &job.LeaseExpiresAt, &job.Fence, &job.RequestedBy, &job.ErrorCode, &job.CreatedAt, &job.UpdatedAt}
}

func nullable(value string) *string {
	if value == "" {
		return nil
	}
	return &value
}

func nonNilMap(values map[string]string) map[string]string {
	if values == nil {
		return map[string]string{}
	}
	return values
}

func nonNilChecksums(values []supplychain.Checksum) []supplychain.Checksum {
	if values == nil {
		return []supplychain.Checksum{}
	}
	return values
}

func nonNilWarnings(values []supplychain.Warning) []supplychain.Warning {
	if values == nil {
		return []supplychain.Warning{}
	}
	return values
}

// escapeLike neutralizes LIKE metacharacters in a user search term so it
// matches literally.
func escapeLike(value string) string {
	out := make([]byte, 0, len(value))
	for index := 0; index < len(value); index++ {
		switch value[index] {
		case '%', '_', '\\':
			out = append(out, '\\')
		}
		out = append(out, value[index])
	}
	return string(out)
}
