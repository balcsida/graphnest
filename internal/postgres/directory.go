package postgres

import (
	"context"
	"strconv"
	"time"

	"github.com/grepnest/grepnest/internal/authn"
	"github.com/jackc/pgx/v5"
)

func (s *Store) UserPrincipal(ctx context.Context, userID int64, ceiling []int64) (authn.Principal, error) {
	return userPrincipal(ctx, s.pool, userID, ceiling)
}

type principalQuerier interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}

func userPrincipal(ctx context.Context, queryer principalQuerier, userID int64, ceiling []int64) (authn.Principal, error) {
	var administrator bool
	var repositoryIDs []int64
	err := queryer.QueryRow(ctx, `with active_groups as (
        select memberships.group_id from group_memberships memberships
        join groups on groups.id=memberships.group_id and groups.deleted_at is null
        where memberships.user_id=$1
    ), grants as (
        select repository_id from user_repository_grants where user_id=$1
        union
        select grants.repository_id from group_repository_grants grants join active_groups on active_groups.group_id=grants.group_id
    )
    select exists(select 1 from user_roles where user_id=users.id)
        or exists(select 1 from group_roles roles join active_groups on active_groups.group_id=roles.group_id),
        coalesce(array(select repository_id from grants where $2::bigint[] is null or repository_id=any($2) order by repository_id), '{}')
    from users where id=$1 and scim_active and suspended_at is null and deleted_at is null`, userID, ceiling).Scan(&administrator, &repositoryIDs)
	if err != nil {
		return authn.Principal{}, err
	}
	return authn.Principal{Subject: strconv.FormatInt(userID, 10), Method: "oidc", Administrator: administrator, RepositoryIDs: repositoryIDs}, nil
}

func (s *Store) APIPrincipal(ctx context.Context, tokenHash [32]byte, now time.Time) (authn.Principal, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return authn.Principal{}, err
	}
	defer tx.Rollback(ctx)
	var userID int64
	var ceiling []int64
	if err := tx.QueryRow(ctx, `update api_tokens token set last_used_at=$2
        from users user_record
        where token.token_hash=$1 and token.user_id=user_record.id
          and token.revoked_at is null and (token.expires_at is null or token.expires_at>$2)
          and user_record.scim_active and user_record.suspended_at is null and user_record.deleted_at is null
        returning token.user_id, token.repository_ids`, tokenHash[:], now).Scan(&userID, &ceiling); err != nil {
		return authn.Principal{}, err
	}
	principal, err := userPrincipal(ctx, tx, userID, ceiling)
	if err != nil {
		return authn.Principal{}, err
	}
	if principal.Administrator && ceiling != nil {
		principal.RepositoryIDs = ceiling
	}
	principal.Method = "api_token"
	if err := tx.Commit(ctx); err != nil {
		return authn.Principal{}, err
	}
	return principal, nil
}

func (s *Store) CreateAPIToken(ctx context.Context, token authn.APITokenRecord) (int64, error) {
	var id int64
	err := s.pool.QueryRow(ctx, `insert into api_tokens (token_hash, prefix, user_id, repository_ids, created_at, expires_at) values ($1,$2,$3,$4,$5,$6) returning id`, token.TokenHash[:], token.Prefix, token.UserID, token.RepositoryIDs, token.CreatedAt, token.ExpiresAt).Scan(&id)
	return id, err
}

func (s *Store) RevokeAPIToken(ctx context.Context, userID, tokenID int64) error {
	_, err := s.pool.Exec(ctx, `update api_tokens set revoked_at=now() where id=$1 and user_id=$2 and revoked_at is null`, tokenID, userID)
	return err
}

func (s *Store) ListAPITokens(ctx context.Context, userID int64) ([]authn.APITokenMetadata, error) {
	rows, err := s.pool.Query(ctx, `select id, prefix, repository_ids, created_at, last_used_at, expires_at from api_tokens where user_id=$1 and revoked_at is null order by id`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []authn.APITokenMetadata
	for rows.Next() {
		var item authn.APITokenMetadata
		if err := rows.Scan(&item.ID, &item.Prefix, &item.RepositoryIDs, &item.CreatedAt, &item.LastUsedAt, &item.ExpiresAt); err != nil {
			return nil, err
		}
		item.RepositoryIDs = append([]int64(nil), item.RepositoryIDs...)
		result = append(result, item)
	}
	return result, rows.Err()
}
