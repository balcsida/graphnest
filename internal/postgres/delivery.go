package postgres

import (
	"context"

	"github.com/grepnest/grepnest/internal/admin"
	"github.com/grepnest/grepnest/internal/repository"
	"github.com/jackc/pgx/v5"
)

func (s *Store) AdminDeliveries(ctx context.Context, installationID int64, repositoryIDs []int64, limit int) ([]admin.Delivery, bool, error) {
	rows, err := s.pool.Query(ctx, `select deliveries.id,deliveries.delivery_id,deliveries.event_name,
		installations.github_id,deliveries.received_at,deliveries.processed_at,deliveries.state,
		coalesce(deliveries.error_code,'') from webhook_deliveries deliveries
		join installations on installations.id=deliveries.installation_id
		join repositories on repositories.id=deliveries.repository_id
			and repositories.installation_id=deliveries.installation_id
		where installations.github_id=$1 and repositories.github_id=any($2)
		order by deliveries.received_at desc,deliveries.id desc limit $3`, installationID, repositoryIDs, limit+1)
	if err != nil {
		return nil, false, err
	}
	defer rows.Close()
	items := make([]admin.Delivery, 0, limit+1)
	for rows.Next() {
		var x admin.Delivery
		if err := rows.Scan(&x.ID, &x.DeliveryID, &x.Event, &x.InstallationID, &x.ReceivedAt, &x.ProcessedAt, &x.State, &x.ErrorCode); err != nil {
			return nil, false, err
		}
		items = append(items, x)
	}
	if err := rows.Err(); err != nil {
		return nil, false, err
	}
	more := len(items) > limit
	if more {
		items = items[:limit]
	}
	return items, more, nil
}

type Delivery struct {
	ID, Event, State, ErrorCode  string
	InstallationID, RepositoryID *int64
}

type DeliveryTx struct{ tx pgx.Tx }

func (s *Store) ApplyDelivery(ctx context.Context, delivery Delivery, callback func(context.Context, *DeliveryTx) error) (bool, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer tx.Rollback(ctx)

	result, err := tx.Exec(ctx, `
		insert into webhook_deliveries (delivery_id, event_name, installation_id, repository_id, processed_at, state, error_code)
		values ($1, $2, (select id from installations where github_id=$3),
			(select repositories.id from repositories join installations on installations.id=repositories.installation_id
				where installations.github_id=$3 and repositories.github_id=$4),
			now(), $5, nullif($6, ''))
		on conflict (delivery_id) do nothing`, delivery.ID, delivery.Event, delivery.InstallationID,
		delivery.RepositoryID, delivery.State, delivery.ErrorCode)
	if err != nil {
		return false, err
	}
	if result.RowsAffected() == 0 {
		return false, tx.Commit(ctx)
	}
	if callback != nil {
		if err := callback(ctx, &DeliveryTx{tx: tx}); err != nil {
			return false, err
		}
	}
	return true, tx.Commit(ctx)
}

func (tx *DeliveryTx) EnqueueIndex(ctx context.Context, request IndexRequest) error {
	return enqueueIndex(ctx, tx.tx, request)
}

func (tx *DeliveryTx) RepositoryForPush(ctx context.Context, installationID, githubID int64) (repository.Repository, error) {
	return scanRepository(tx.tx.QueryRow(ctx, `select `+repositoryColumns+`
		from installations join repositories on repositories.installation_id=installations.id
		where installations.github_id=$1 and repositories.github_id=$2
		and installations.status='active' and repositories.enabled and not repositories.archived
		for share of installations for update of repositories`, installationID, githubID))
}

func (tx *DeliveryTx) UpdateRepositorySize(ctx context.Context, repositoryID, sizeBytes int64) error {
	_, err := tx.tx.Exec(ctx, "update repositories set size_bytes=$2, updated_at=now() where id=$1", repositoryID, sizeBytes)
	return err
}

func (tx *DeliveryTx) RenameRepository(ctx context.Context, githubID int64, owner, name, cloneURL, webURL string) error {
	_, err := tx.tx.Exec(ctx, `update repositories set owner=$2, name=$3, clone_url=$4, web_url=$5, updated_at=now() where github_id=$1`, githubID, owner, name, cloneURL, webURL)
	return err
}

func (tx *DeliveryTx) DisableRepository(ctx context.Context, githubID int64, errorCode string) error {
	if _, err := tx.tx.Exec(ctx, `update repositories set enabled=false, status='disabled', error_code=nullif($2, ''), updated_at=now() where github_id=$1`, githubID, errorCode); err != nil {
		return err
	}
	_, err := tx.tx.Exec(ctx, `update index_jobs set state='superseded', error_code='repository_unavailable',
		error_message=null, updated_at=now() where state='queued'
		and repository_id=(select id from repositories where github_id=$1)`, githubID)
	return err
}

func (tx *DeliveryTx) DisableInstallation(ctx context.Context, githubID int64, status string) error {
	if _, err := tx.tx.Exec(ctx, `update installations set status=$2::varchar, suspended_at=case when $2::varchar='suspended' then now() else suspended_at end, updated_at=now() where github_id=$1`, githubID, status); err != nil {
		return err
	}
	if _, err := tx.tx.Exec(ctx, `update repositories set enabled=false, status='disabled', updated_at=now()
		where installation_id=(select id from installations where github_id=$1)`, githubID); err != nil {
		return err
	}
	_, err := tx.tx.Exec(ctx, `update index_jobs set state='superseded', error_code='repository_unavailable',
		error_message=null, updated_at=now() where state='queued' and repository_id in
		(select id from repositories where installation_id=(select id from installations where github_id=$1))`, githubID)
	return err
}
