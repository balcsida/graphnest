package postgres

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

var ErrNoJob = errors.New("no index job available")
var ErrLeaseLost = errors.New("index job lease lost")

type IndexRequest struct {
	RepositoryID                 int64
	TargetSHA, TargetRef, Reason string
}

type IndexJob struct {
	ID, RepositoryID             int64
	TargetSHA, State, LeaseOwner string
	Attempt                      int
	LeaseExpiresAt               time.Time
}

func (s *Store) EnqueueIndex(ctx context.Context, request IndexRequest) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if err := enqueueIndex(ctx, tx, request); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func enqueueIndex(ctx context.Context, tx pgx.Tx, request IndexRequest) error {
	var desiredSHA *string
	if err := tx.QueryRow(ctx, `select desired_sha from repositories where id=$1 for update`, request.RepositoryID).Scan(&desiredSHA); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `update repositories set desired_sha=$2, status='pending', error_code=null, updated_at=now() where id=$1`, request.RepositoryID, request.TargetSHA); err != nil {
		return err
	}
	if desiredSHA != nil && *desiredSHA == request.TargetSHA {
		var exists bool
		if err := tx.QueryRow(ctx, `select exists(select 1 from index_jobs where repository_id=$1 and state in ('queued','running') and target_sha=$2)`, request.RepositoryID, request.TargetSHA).Scan(&exists); err != nil || exists {
			return err
		}
	}
	if _, err := tx.Exec(ctx, `delete from index_jobs where repository_id=$1 and state='queued'`, request.RepositoryID); err != nil {
		return err
	}
	_, err := tx.Exec(ctx, `insert into index_jobs(repository_id, target_sha, state) values($1,$2,'queued')`, request.RepositoryID, request.TargetSHA)
	return err
}

func (s *Store) ClaimIndex(ctx context.Context, owner string) (IndexJob, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return IndexJob{}, err
	}
	defer tx.Rollback(ctx)
	var job IndexJob
	err = tx.QueryRow(ctx, `
		with next as (
			select id from index_jobs j where state='queued' and run_after<=now()
			and not exists(select 1 from index_jobs running where running.repository_id=j.repository_id and running.state='running')
			order by run_after, id for update skip locked limit 1
		)
		update index_jobs set state='running', attempt=attempt+1, lease_owner=$1,
			lease_expires_at=now()+interval '2 minutes', updated_at=now()
		where id=(select id from next)
		returning id, repository_id, target_sha, state, lease_owner, attempt, lease_expires_at`, owner).
		Scan(&job.ID, &job.RepositoryID, &job.TargetSHA, &job.State, &job.LeaseOwner, &job.Attempt, &job.LeaseExpiresAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return IndexJob{}, ErrNoJob
	}
	if err != nil {
		return IndexJob{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return IndexJob{}, err
	}
	return job, nil
}

func (s *Store) RenewLease(ctx context.Context, id int64, owner string) error {
	result, err := s.pool.Exec(ctx, `update index_jobs set lease_expires_at=now()+interval '2 minutes', updated_at=now()
		where id=$1 and state='running' and lease_owner=$2 and lease_expires_at>now()`, id, owner)
	return leaseResult(result, err)
}

func (s *Store) CompleteIndex(ctx context.Context, id int64, owner string) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var repositoryID int64
	var targetSHA string
	if err := tx.QueryRow(ctx, `select repository_id, target_sha from index_jobs where id=$1 and state='running' and lease_owner=$2 and lease_expires_at>now() for update`, id, owner).Scan(&repositoryID, &targetSHA); errors.Is(err, pgx.ErrNoRows) {
		return ErrLeaseLost
	} else if err != nil {
		return err
	}
	var desiredSHA string
	if err := tx.QueryRow(ctx, `select desired_sha from repositories where id=$1 for update`, repositoryID).Scan(&desiredSHA); err != nil {
		return err
	}
	state := "superseded"
	if desiredSHA == targetSHA {
		state = "succeeded"
		if _, err := tx.Exec(ctx, `update repositories set indexed_sha=$2, status='ready', error_code=null, last_indexed_at=now(), updated_at=now() where id=$1`, repositoryID, targetSHA); err != nil {
			return err
		}
	}
	if _, err := tx.Exec(ctx, `update index_jobs set state=$2, lease_owner=null, lease_expires_at=null, error_code=null, error_message=null, updated_at=now() where id=$1`, id, state); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s *Store) FailIndex(ctx context.Context, id int64, owner, errorCode string, retry bool) error {
	return s.finishFailure(ctx, id, owner, errorCode, retry)
}

func (s *Store) finishFailure(ctx context.Context, id int64, owner, errorCode string, retry bool) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var repositoryID int64
	var targetSHA string
	var attempt int
	if err := tx.QueryRow(ctx, `select repository_id, target_sha, attempt from index_jobs
		where id=$1 and state='running' and lease_owner=$2 and lease_expires_at>now() for update`, id, owner).Scan(&repositoryID, &targetSHA, &attempt); errors.Is(err, pgx.ErrNoRows) {
		return ErrLeaseLost
	} else if err != nil {
		return err
	}
	var desiredSHA string
	if err := tx.QueryRow(ctx, `select desired_sha from repositories where id=$1 for update`, repositoryID).Scan(&desiredSHA); err != nil {
		return err
	}
	state := "failed"
	if desiredSHA != targetSHA {
		state = "superseded"
	} else if retry && attempt < 5 {
		state = "queued"
	}
	if _, err := tx.Exec(ctx, `update index_jobs set state=$2, lease_owner=null, lease_expires_at=null,
		error_code=$3, error_message=null, run_after=now(), updated_at=now() where id=$1`, id, state, errorCode); err != nil {
		return err
	}
	if state == "failed" {
		if _, err := tx.Exec(ctx, `update repositories set status='failed', error_code=$2, updated_at=now() where id=$1`, repositoryID, errorCode); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

func (s *Store) ReapExpired(ctx context.Context, limit int) (int64, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback(ctx)
	type expiredJob struct {
		id, repositoryID int64
		target, desired  string
		attempt          int
	}
	rows, err := tx.Query(ctx, `select j.id, j.repository_id, j.target_sha, r.desired_sha, j.attempt
		from index_jobs j join repositories r on r.id=j.repository_id
		where j.state='running' and j.lease_expires_at<=now()
		order by j.lease_expires_at, j.id for update of j, r skip locked limit $1`, limit)
	if err != nil {
		return 0, err
	}
	var jobs []expiredJob
	for rows.Next() {
		var job expiredJob
		if err := rows.Scan(&job.id, &job.repositoryID, &job.target, &job.desired, &job.attempt); err != nil {
			rows.Close()
			return 0, err
		}
		jobs = append(jobs, job)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return 0, err
	}
	rows.Close()
	for _, job := range jobs {
		state := "failed"
		if job.desired != job.target {
			state = "superseded"
		} else if job.attempt < 5 {
			state = "queued"
		}
		if _, err := tx.Exec(ctx, `update index_jobs set state=$2, lease_owner=null, lease_expires_at=null,
			error_code='lease_expired', error_message=null, run_after=now(), updated_at=now() where id=$1`, job.id, state); err != nil {
			return 0, err
		}
		if state == "failed" {
			if _, err := tx.Exec(ctx, `update repositories set status='failed', error_code='lease_expired', updated_at=now() where id=$1`, job.repositoryID); err != nil {
				return 0, err
			}
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, err
	}
	return int64(len(jobs)), nil
}

func (s *Store) ActiveJobIDs(ctx context.Context) (map[int64]struct{}, error) {
	rows, err := s.pool.Query(ctx, `select id from index_jobs where state='running' and lease_expires_at>now()`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := map[int64]struct{}{}
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		result[id] = struct{}{}
	}
	return result, rows.Err()
}

func (s *Store) QueueDepths(ctx context.Context) (map[string]int64, error) {
	result := map[string]int64{"queued": 0, "running": 0, "succeeded": 0, "failed": 0, "superseded": 0}
	rows, err := s.pool.Query(ctx, `select state, count(*) from index_jobs where state in ('queued','running','succeeded','failed','superseded') group by state`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var state string
		var count int64
		if err := rows.Scan(&state, &count); err != nil {
			return nil, err
		}
		result[state] = count
	}
	return result, rows.Err()
}

func (s *Store) Prune(ctx context.Context) (int64, int64, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return 0, 0, err
	}
	defer tx.Rollback(ctx)
	deliveries, err := tx.Exec(ctx, `delete from webhook_deliveries where received_at<now()-interval '30 days'`)
	if err != nil {
		return 0, 0, err
	}
	jobs, err := tx.Exec(ctx, `delete from index_jobs where id in (
		select id from (select id, row_number() over(partition by repository_id order by updated_at desc, id desc) rank
			from index_jobs where state in ('succeeded','failed','superseded')) terminal where rank>100)`)
	if err != nil {
		return 0, 0, err
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, 0, err
	}
	return deliveries.RowsAffected(), jobs.RowsAffected(), nil
}

func leaseResult(result pgconn.CommandTag, err error) error {
	if err != nil {
		return err
	}
	if result.RowsAffected() == 0 {
		return ErrLeaseLost
	}
	return nil
}
