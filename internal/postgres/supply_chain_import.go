package postgres

import (
	"context"
	"errors"
	"time"

	"github.com/balcsida/graphnest/internal/supplychain"
	"github.com/jackc/pgx/v5"
)

// SupplyChainUploadAllowed reports whether a non-administrator subject holds
// an upload grant for the repository.
func (s *Store) SupplyChainUploadAllowed(ctx context.Context, repositoryID int64, subject string) (bool, error) {
	var allowed bool
	err := s.pool.QueryRow(ctx, `select exists(select 1 from supply_chain_upload_grants where repository_id=$1 and subject=$2)`, repositoryID, subject).Scan(&allowed)
	return allowed, err
}

// SetSupplyChainUploadGrant adds or removes a repository-scoped upload grant.
func (s *Store) SetSupplyChainUploadGrant(ctx context.Context, repositoryID int64, subject, grantedBy string, allow bool) error {
	if !allow {
		_, err := s.pool.Exec(ctx, `delete from supply_chain_upload_grants where repository_id=$1 and subject=$2`, repositoryID, subject)
		return err
	}
	_, err := s.pool.Exec(ctx, `insert into supply_chain_upload_grants (repository_id, subject, granted_by) values ($1, $2, $3) on conflict do nothing`, repositoryID, subject, grantedBy)
	return err
}

func (s *Store) SupplyChainImportCount(ctx context.Context, repositoryID int64, since time.Time) (int, error) {
	var count int
	err := s.pool.QueryRow(ctx, `select count(*) from supply_chain_imports where repository_id=$1 and created_at>=$2`, repositoryID, since).Scan(&count)
	return count, err
}

func (s *Store) SupplyChainImportByDigest(ctx context.Context, repositoryID int64, streamKey string, digest []byte) (supplychain.ImportRecord, bool, error) {
	var record supplychain.ImportRecord
	var format string
	err := s.pool.QueryRow(ctx, `select id, repository_id, stream_key, document_sha256, format, uploaded_by, upload_label, byte_size, outcome, snapshot_id, error_code, message, created_at
		from supply_chain_imports where repository_id=$1 and stream_key=$2 and document_sha256=$3 order by id desc limit 1`, repositoryID, streamKey, digest).
		Scan(&record.ID, &record.RepositoryID, &record.StreamKey, &record.DocumentSHA256, &format, &record.UploadedBy, &record.UploadLabel, &record.ByteSize, &record.Outcome, &record.SnapshotID, &record.ErrorCode, &record.Message, &record.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return supplychain.ImportRecord{}, false, nil
	}
	record.Format = supplychain.Format(format)
	return record, err == nil, err
}

func (s *Store) RecordSupplyChainImport(ctx context.Context, record supplychain.ImportRecord) (int64, error) {
	var id int64
	err := s.pool.QueryRow(ctx, `insert into supply_chain_imports (repository_id, stream_key, document_sha256, format, uploaded_by, upload_label, byte_size, outcome, snapshot_id, error_code, message)
		values ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11) returning id`, record.RepositoryID, record.StreamKey, record.DocumentSHA256, string(record.Format), record.UploadedBy, record.UploadLabel,
		record.ByteSize, record.Outcome, record.SnapshotID, record.ErrorCode, record.Message).Scan(&id)
	return id, err
}

// SupplyChainStreams lists the streams that exist for a repository so the UI
// can offer producer/subject selection.
func (s *Store) SupplyChainStreams(ctx context.Context, repositoryID int64) ([]supplychain.Stream, error) {
	rows, err := s.pool.Query(ctx, `select repository_id, stream_key, producer, subject, latest_snapshot_id, latest_collection_id, last_success_at, last_attempt_at, last_outcome, opt_out
		from supply_chain_streams where repository_id=$1 order by stream_key`, repositoryID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	streams := []supplychain.Stream{}
	for rows.Next() {
		var stream supplychain.Stream
		var outcome string
		if err := rows.Scan(&stream.RepositoryID, &stream.StreamKey, &stream.Producer, &stream.Subject, &stream.LatestSnapshotID, &stream.LatestCollectionID, &stream.LastSuccessAt, &stream.LastAttemptAt, &outcome, &stream.OptOut); err != nil {
			return nil, err
		}
		stream.LastOutcome = supplychain.Outcome(outcome)
		streams = append(streams, stream)
	}
	return streams, rows.Err()
}
