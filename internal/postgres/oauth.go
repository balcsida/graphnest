package postgres

import (
	"context"
	"errors"
	"time"

	"github.com/balcsida/graphnest/internal/audit"
	"github.com/balcsida/graphnest/internal/authn"
	"github.com/jackc/pgx/v5"
)

// Live-grant predicate shared by lookups: not revoked, within the absolute
// lifetime, and owned by a user who may still sign in.
const liveOAuthGrantSQL = `oauth_grants.revoked_at is null and oauth_grants.expires_at > $2
	and users.id = oauth_grants.user_id and users.scim_active and users.suspended_at is null and users.deleted_at is null`

func (s *Store) CreateOAuthClient(ctx context.Context, client authn.OAuthClient) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `select pg_advisory_xact_lock($1,hashtext('/oauth/register'))`, oauthRequestLockNamespace); err != nil {
		return err
	}
	var clients int
	if err := tx.QueryRow(ctx, `select count(*) from oauth_clients`).Scan(&clients); err != nil {
		return err
	}
	if clients >= 10000 {
		return authn.ErrOAuthClientQuota
	}
	if _, err := tx.Exec(ctx, `insert into oauth_clients (id, client_name, redirect_uris, created_at, last_used_at)
		values ($1,$2,$3,$4,$4)`, client.ID, client.Name, client.RedirectURIs, client.CreatedAt); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// OAuthClient loads a client.
func (s *Store) OAuthClient(ctx context.Context, id string, _ time.Time) (authn.OAuthClient, error) {
	client := authn.OAuthClient{ID: id}
	err := s.pool.QueryRow(ctx, `select client_name, redirect_uris, created_at, last_used_at
		from oauth_clients where id=$1`, id).
		Scan(&client.Name, &client.RedirectURIs, &client.CreatedAt, &client.LastUsedAt)
	return client, err
}

func (s *Store) CreateOAuthAuthorizationRequest(ctx context.Context, request authn.OAuthAuthorizationRequest) error {
	var userID *int64
	if request.UserID != 0 {
		userID = &request.UserID
	}
	_, err := s.pool.Exec(ctx, `with inserted as (
		insert into oauth_authorization_requests
		(id, phase, client_id, user_id, redirect_uri, code_challenge, state, scope, resource, created_at, expires_at)
		values ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)
		returning client_id
	)
	update oauth_clients set last_used_at=greatest(last_used_at, $10)
	from inserted where oauth_clients.id=inserted.client_id and $2='pending'`,
		request.ID[:], request.Phase, request.ClientID, userID, request.RedirectURI, request.CodeChallenge,
		request.State, request.Scope, request.Resource, request.CreatedAt, request.ExpiresAt)
	return err
}

func (s *Store) OAuthAuthorizationRequest(ctx context.Context, id [32]byte, phase string, now time.Time) (authn.OAuthAuthorizationRequest, error) {
	return scanAuthorizationRequest(s.pool.QueryRow(ctx, `select id, phase, client_id, user_id, redirect_uri, code_challenge, state, scope, resource, created_at, expires_at
		from oauth_authorization_requests where id=$1 and phase=$2 and expires_at > $3`, id[:], phase, now))
}

// IssueOAuthCode atomically validates the consent session and re-keys a pending request as a code.
func (s *Store) IssueOAuthCode(ctx context.Context, pendingID, codeID, sessionHash [32]byte, userID int64, expiresAt, now time.Time) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	// Match administrative revocation's user-first lock order. Revalidate the
	// session in a separate statement so revocation committed during this wait is visible.
	var found int
	if err := tx.QueryRow(ctx, `select 1 from users where id=$1 and scim_active
		and suspended_at is null and deleted_at is null for update`, userID).Scan(&found); err != nil {
		return err
	}
	if err := tx.QueryRow(ctx, `select 1 from auth_sessions session join users user_record on user_record.id=session.user_id
		where session.token_hash=$1 and session.user_id=$2 and session.revoked_at is null
		and session.expires_at>$3 and session.idle_expires_at>$3 and not session.force_rotation
		and session.provider in ('oidc','oauth','local')
		and (session.provider<>'local' or (user_record.source='local'
			and exists(select 1 from user_roles where user_id=user_record.id)))
		for update of session`, sessionHash[:], userID, now).Scan(&found); err != nil {
		return err
	}
	result, err := tx.Exec(ctx, `update oauth_authorization_requests
		set id=$2, phase='code', user_id=$3, created_at=$5, expires_at=$4
		where id=$1 and phase='pending' and expires_at > $5`, pendingID[:], codeID[:], userID, expiresAt, now)
	if err != nil {
		return err
	}
	if result.RowsAffected() == 0 {
		return pgx.ErrNoRows
	}
	return tx.Commit(ctx)
}

func (s *Store) DeleteOAuthAuthorizationRequest(ctx context.Context, id [32]byte) error {
	_, err := s.pool.Exec(ctx, `delete from oauth_authorization_requests where id=$1`, id[:])
	return err
}

func (s *Store) ExchangeOAuthCode(ctx context.Context, codeID [32]byte, grant authn.OAuthGrant) (int64, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback(ctx)
	// Lock the user before the code or grant, matching administrator revocation.
	// If revocation wins, its code deletion is visible to the next statement;
	// if exchange wins, revocation will see and revoke the committed grant.
	var userID int64
	if err := tx.QueryRow(ctx, `select id from users where id=$1 and scim_active
		and suspended_at is null and deleted_at is null for update`, grant.UserID).Scan(&userID); err != nil {
		return 0, err
	}
	result, err := tx.Exec(ctx, `delete from oauth_authorization_requests
		where id=$1 and phase='code' and expires_at > $2 and user_id=$3 and client_id=$4 and scope=$5`,
		codeID[:], grant.CreatedAt, grant.UserID, grant.ClientID, grant.Scope)
	if err != nil {
		return 0, err
	}
	if result.RowsAffected() != 1 {
		return 0, pgx.ErrNoRows
	}
	id, err := createOAuthGrant(ctx, tx, grant)
	if err != nil {
		return 0, err
	}
	return id, tx.Commit(ctx)
}

func scanAuthorizationRequest(row pgx.Row) (authn.OAuthAuthorizationRequest, error) {
	var request authn.OAuthAuthorizationRequest
	var id []byte
	var userID *int64
	if err := row.Scan(&id, &request.Phase, &request.ClientID, &userID, &request.RedirectURI, &request.CodeChallenge,
		&request.State, &request.Scope, &request.Resource, &request.CreatedAt, &request.ExpiresAt); err != nil {
		return authn.OAuthAuthorizationRequest{}, err
	}
	copy(request.ID[:], id)
	if userID != nil {
		request.UserID = *userID
	}
	return request, nil
}

func (s *Store) CreateOAuthGrant(ctx context.Context, grant authn.OAuthGrant) (int64, error) {
	return createOAuthGrant(ctx, s.pool, grant)
}

func createOAuthGrant(ctx context.Context, queryer principalQuerier, grant authn.OAuthGrant) (int64, error) {
	var id int64
	err := queryer.QueryRow(ctx, `insert into oauth_grants
		(client_id, user_id, scope, access_hash, access_expires_at, refresh_hash, github_token_ct, created_at, last_used_at, expires_at)
		values ($1,$2,$3,$4,$5,$6,$7,$8,$8,$9) returning id`,
		grant.ClientID, grant.UserID, grant.Scope, grant.AccessHash[:], grant.AccessExpiresAt, grant.RefreshHash[:],
		grant.GitHubTokenCiphertext, grant.CreatedAt, grant.ExpiresAt).Scan(&id)
	return id, err
}

// OAuthPrincipal resolves a live access token with the user's repository read
// access, without delegating administrative or write privileges.
func (s *Store) OAuthPrincipal(ctx context.Context, accessHash [32]byte, now time.Time) (authn.Principal, error) {
	var userID int64
	if err := s.pool.QueryRow(ctx, `update oauth_grants set last_used_at=$2
		from users where oauth_grants.access_hash=$1 and oauth_grants.access_expires_at > $2 and `+liveOAuthGrantSQL+`
		returning oauth_grants.user_id`, accessHash[:], now).Scan(&userID); err != nil {
		return authn.Principal{}, err
	}
	principal, err := s.UserPrincipal(ctx, userID, nil)
	if err != nil {
		return authn.Principal{}, err
	}
	if principal.Administrator {
		repositories, err := s.GraphRepositories(ctx, principal)
		if err != nil {
			return authn.Principal{}, err
		}
		for _, repository := range repositories {
			principal.RepositoryIDs = append(principal.RepositoryIDs, repository.GitHubID)
		}
		principal.Administrator = false
	}
	principal.Method = authn.ProviderOAuthToken
	return principal, nil
}

func (s *Store) OAuthGrantByRefresh(ctx context.Context, refreshHash [32]byte, now time.Time) (authn.OAuthGrant, error) {
	return scanGrant(s.pool.QueryRow(ctx, `select `+grantColumns+` from oauth_grants join users on `+liveOAuthGrantSQL+`
		where oauth_grants.refresh_hash=$1`, refreshHash[:], now))
}

// RotateOAuthGrant implements refresh-token rotation with replay detection.
// Presenting any consumed refresh token after its rotation grace period revokes
// the grant and returns ErrOAuthReplay. Access use does not extend that grace.
func (s *Store) RotateOAuthGrant(ctx context.Context, refreshHash [32]byte, rotation authn.OAuthRotation) (authn.OAuthGrant, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return authn.OAuthGrant{}, err
	}
	defer tx.Rollback(ctx)
	grant, err := scanGrant(tx.QueryRow(ctx, `update oauth_grants
		set access_hash=$3, access_expires_at=$4, previous_refresh_hash=refresh_hash, refresh_hash=$5, last_used_at=$2
		from users where oauth_grants.refresh_hash=$1 and `+liveOAuthGrantSQL+`
		returning `+grantColumns, refreshHash[:], rotation.Now, rotation.AccessHash[:], rotation.AccessExpiresAt, rotation.RefreshHash[:]))
	if errors.Is(err, pgx.ErrNoRows) {
		// Not a current token: was it a rotated one? Within the grace window
		// the client most likely lost the rotation response and will retry
		// with whichever token it holds, so the grant is left intact and the
		// request simply fails. Later use is treated as theft and revokes the
		// whole grant.
		result, replayErr := tx.Exec(ctx, `update oauth_grants set revoked_at=coalesce(revoked_at, $2), github_token_ct=null
			from oauth_refresh_tokens
			where oauth_refresh_tokens.grant_id=oauth_grants.id
			and oauth_refresh_tokens.refresh_hash=$1 and oauth_refresh_tokens.consumed_at <= $3`, refreshHash[:], rotation.Now, rotation.Now.Add(-rotation.Grace))
		if replayErr != nil {
			return authn.OAuthGrant{}, replayErr
		}
		if result.RowsAffected() > 0 {
			if err := tx.Commit(ctx); err != nil {
				return authn.OAuthGrant{}, err
			}
			return authn.OAuthGrant{}, authn.ErrOAuthReplay
		}
		return authn.OAuthGrant{}, pgx.ErrNoRows
	}
	if err != nil {
		return authn.OAuthGrant{}, err
	}
	if _, err := tx.Exec(ctx, `insert into oauth_refresh_tokens(refresh_hash, grant_id, consumed_at) values($1,$2,$3)`, refreshHash[:], grant.ID, rotation.Now); err != nil {
		return authn.OAuthGrant{}, err
	}
	if rotation.ReplaceRepositories {
		if err := replaceGitHubGrants(ctx, tx, grant.UserID, rotation.RepositoryIDs); err != nil {
			return authn.OAuthGrant{}, err
		}
	}
	if err := appendAudit(ctx, tx, rotation.Audit); err != nil {
		return authn.OAuthGrant{}, err
	}
	return grant, tx.Commit(ctx)
}

func (s *Store) UpdateOAuthGrantGitHubToken(ctx context.Context, grantID int64, ciphertext []byte) error {
	result, err := s.pool.Exec(ctx, `update oauth_grants set github_token_ct=$2 where id=$1 and revoked_at is null`, grantID, ciphertext)
	if err != nil {
		return err
	}
	if result.RowsAffected() != 1 {
		return pgx.ErrNoRows
	}
	return nil
}

func (s *Store) RevokeOAuthGrant(ctx context.Context, grantID int64) error {
	_, err := s.pool.Exec(ctx, `update oauth_grants set revoked_at=coalesce(revoked_at, now()), github_token_ct=null where id=$1`, grantID)
	return err
}

func (s *Store) RevokeOAuthGrantByToken(ctx context.Context, hash [32]byte, clientID string) (bool, error) {
	result, err := s.pool.Exec(ctx, `update oauth_grants set revoked_at=now(), github_token_ct=null
		where revoked_at is null and client_id=$2 and (access_hash=$1 or refresh_hash=$1
		or exists(select 1 from oauth_refresh_tokens where grant_id=oauth_grants.id and oauth_refresh_tokens.refresh_hash=$1))`, hash[:], clientID)
	if err != nil {
		return false, err
	}
	return result.RowsAffected() > 0, nil
}

func (s *Store) ListOAuthGrants(ctx context.Context, userID, afterID int64, limit int) ([]authn.OAuthGrantMetadata, bool, error) {
	rows, err := s.pool.Query(ctx, `select oauth_grants.id, oauth_clients.client_name, oauth_grants.scope, oauth_grants.created_at, oauth_grants.last_used_at, oauth_grants.expires_at
		from oauth_grants join oauth_clients on oauth_clients.id=oauth_grants.client_id
		where oauth_grants.user_id=$1 and oauth_grants.id>$2 and oauth_grants.revoked_at is null and oauth_grants.expires_at > now()
		order by oauth_grants.id limit $3`, userID, afterID, limit+1)
	if err != nil {
		return nil, false, err
	}
	defer rows.Close()
	var grants []authn.OAuthGrantMetadata
	for rows.Next() {
		var grant authn.OAuthGrantMetadata
		if err := rows.Scan(&grant.ID, &grant.ClientName, &grant.Scope, &grant.CreatedAt, &grant.LastUsedAt, &grant.ExpiresAt); err != nil {
			return nil, false, err
		}
		grants = append(grants, grant)
	}
	if err := rows.Err(); err != nil {
		return nil, false, err
	}
	if len(grants) > limit {
		return grants[:limit], true, nil
	}
	return grants, false, nil
}

func (s *Store) RevokeUserOAuthGrant(ctx context.Context, userID, grantID int64) error {
	result, err := s.pool.Exec(ctx, `update oauth_grants set revoked_at=now(), github_token_ct=null
		where id=$1 and user_id=$2 and revoked_at is null`, grantID, userID)
	if err != nil {
		return err
	}
	if result.RowsAffected() == 0 {
		return pgx.ErrNoRows
	}
	return nil
}

func (s *Store) RevokeUserOAuthGrantAudited(ctx context.Context, userID, grantID int64, event audit.Event) error {
	if err := event.Validate(); err != nil {
		return err
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	result, err := tx.Exec(ctx, `update oauth_grants set revoked_at=now(), github_token_ct=null
		where id=$1 and user_id=$2 and revoked_at is null`, grantID, userID)
	if err != nil {
		return err
	}
	if result.RowsAffected() != 1 {
		return pgx.ErrNoRows
	}
	if err := appendAudit(ctx, tx, event); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// ReplaceGitHubGrants mirrors bindProviderUser's grant replacement for a
// refresh-time sync: unknown repository IDs are dropped by the join.
func (s *Store) ReplaceGitHubGrants(ctx context.Context, userID int64, repositoryIDs []int64) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if err := replaceGitHubGrants(ctx, tx, userID, repositoryIDs); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func replaceGitHubGrants(ctx context.Context, tx pgx.Tx, userID int64, repositoryIDs []int64) error {
	if _, err := tx.Exec(ctx, `delete from user_github_grants where user_id=$1`, userID); err != nil {
		return err
	}
	_, err := tx.Exec(ctx, `insert into user_github_grants (user_id, repository_id)
		select $1, github_id from repositories where github_id=any($2)`, userID, repositoryIDs)
	return err
}

// DeleteExpiredOAuth removes finished browser interactions, grants that have
// been dead for a week (kept briefly for audit correlation), and clients idle
// for ninety days.
func (s *Store) DeleteExpiredOAuth(ctx context.Context, now time.Time) (requests, grants, clients int64, err error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return 0, 0, 0, err
	}
	defer tx.Rollback(ctx)
	result, err := tx.Exec(ctx, `delete from oauth_authorization_requests where expires_at <= $1`, now)
	if err != nil {
		return 0, 0, 0, err
	}
	requests = result.RowsAffected()
	result, err = tx.Exec(ctx, `delete from oauth_grants where least(expires_at, coalesce(revoked_at, expires_at)) <= $1`, now.Add(-7*24*time.Hour))
	if err != nil {
		return 0, 0, 0, err
	}
	grants = result.RowsAffected()
	result, err = tx.Exec(ctx, `delete from oauth_clients where last_used_at <= $1
		and not exists (select 1 from oauth_grants where oauth_grants.client_id=oauth_clients.id)`, now.Add(-90*24*time.Hour))
	if err != nil {
		return 0, 0, 0, err
	}
	clients = result.RowsAffected()
	return requests, grants, clients, tx.Commit(ctx)
}

const grantColumns = `oauth_grants.id, oauth_grants.client_id, oauth_grants.user_id, oauth_grants.scope, oauth_grants.access_hash, oauth_grants.access_expires_at,
	oauth_grants.refresh_hash, oauth_grants.previous_refresh_hash, oauth_grants.github_token_ct, oauth_grants.created_at, oauth_grants.last_used_at, oauth_grants.expires_at, oauth_grants.revoked_at`

func scanGrant(row pgx.Row) (authn.OAuthGrant, error) {
	var grant authn.OAuthGrant
	var access, refresh, previous []byte
	if err := row.Scan(&grant.ID, &grant.ClientID, &grant.UserID, &grant.Scope, &access, &grant.AccessExpiresAt,
		&refresh, &previous, &grant.GitHubTokenCiphertext, &grant.CreatedAt, &grant.LastUsedAt, &grant.ExpiresAt, &grant.RevokedAt); err != nil {
		return authn.OAuthGrant{}, err
	}
	copy(grant.AccessHash[:], access)
	copy(grant.RefreshHash[:], refresh)
	if previous != nil {
		var hash [32]byte
		copy(hash[:], previous)
		grant.PreviousRefreshHash = &hash
	}
	return grant, nil
}

// UserDisplayName renders "Display Name (user_name)" for the consent page.
func (s *Store) UserDisplayName(ctx context.Context, userID int64) (string, error) {
	var displayName, userName string
	if err := s.pool.QueryRow(ctx, `select display_name, user_name from users where id=$1 and deleted_at is null`, userID).Scan(&displayName, &userName); err != nil {
		return "", err
	}
	if displayName == "" || displayName == userName {
		return userName, nil
	}
	return displayName + " (" + userName + ")", nil
}
