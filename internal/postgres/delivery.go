package postgres

import (
	"context"

	"github.com/grepnest/grepnest/internal/repository"
	"github.com/jackc/pgx/v5"
)

type Delivery struct {
	ID, Event, State, ErrorCode string
	InstallationID              *int64
}

type DeliveryTx struct{ tx pgx.Tx }

func (s *Store) ApplyDelivery(ctx context.Context, delivery Delivery, callback func(context.Context, *DeliveryTx) error) (bool, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer tx.Rollback(ctx)

	result, err := tx.Exec(ctx, `
		insert into webhook_deliveries (delivery_id, event_name, installation_id, processed_at, state, error_code)
		values ($1, $2, (select id from installations where github_id=$3), now(), $4, nullif($5, ''))
		on conflict (delivery_id) do nothing`, delivery.ID, delivery.Event, delivery.InstallationID, delivery.State, delivery.ErrorCode)
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

func (tx *DeliveryTx) RepositoryForPush(ctx context.Context, githubID int64) (repository.Repository, error) {
	return scanRepository(tx.tx.QueryRow(ctx, `select `+repositoryColumns+`
		from repositories join installations on installations.id=repositories.installation_id
		where repositories.github_id=$1 for update of repositories`, githubID))
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
	_, err := tx.tx.Exec(ctx, `update repositories set enabled=false, status='disabled', error_code=nullif($2, ''), updated_at=now() where github_id=$1`, githubID, errorCode)
	return err
}

func (tx *DeliveryTx) DisableInstallation(ctx context.Context, githubID int64, status string) error {
	if _, err := tx.tx.Exec(ctx, `update installations set status=$2::varchar, suspended_at=case when $2::varchar='suspended' then now() else suspended_at end, updated_at=now() where github_id=$1`, githubID, status); err != nil {
		return err
	}
	_, err := tx.tx.Exec(ctx, `update repositories set enabled=false, status='disabled', updated_at=now()
		where installation_id=(select id from installations where github_id=$1)`, githubID)
	return err
}
