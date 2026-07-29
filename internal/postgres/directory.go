package postgres

import (
	"context"
	"strconv"
	"time"

	"github.com/grepnest/grepnest/internal/authn"
)

func (s *Store) UserPrincipal(ctx context.Context, userID int64, ceiling []int64) (authn.Principal, error) {
	var administrator bool
	var repositoryIDs []int64
	err := s.pool.QueryRow(ctx, `with active_groups as (
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
	var userID int64
	var ceiling []int64
	if err := s.pool.QueryRow(ctx, `select user_id, repository_ids from api_tokens where token_hash=$1 and revoked_at is null and (expires_at is null or expires_at>$2)`, tokenHash[:], now).Scan(&userID, &ceiling); err != nil {
		return authn.Principal{}, err
	}
	principal, err := s.UserPrincipal(ctx, userID, ceiling)
	if err != nil {
		return authn.Principal{}, err
	}
	principal.Method = "api_token"
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
