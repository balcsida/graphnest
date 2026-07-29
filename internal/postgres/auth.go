package postgres

import (
	"context"
	"strconv"
	"time"

	"github.com/grepnest/grepnest/internal/authn"
)

func (s *Store) BindOIDCUser(ctx context.Context, issuer, subject, externalID string) (int64, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback(ctx)
	var userID int64
	if err := tx.QueryRow(ctx, `select id from users where external_id=$1 and scim_active and suspended_at is null and deleted_at is null`, externalID).Scan(&userID); err != nil {
		return 0, err
	}
	if _, err := tx.Exec(ctx, `insert into user_identities (user_id, issuer, subject) values ($1, $2, $3)`, userID, issuer, subject); err != nil {
		return 0, err
	}
	return userID, tx.Commit(ctx)
}

func (s *Store) CreateLoginFlow(ctx context.Context, flow authn.LoginFlow) error {
	_, err := s.pool.Exec(ctx, `insert into auth_login_flows (state_hash, browser_hash, provider, nonce, code_verifier, return_to, created_at, expires_at) values ($1,$2,$3,$4,$5,$6,$7,$8)`, flow.StateHash[:], flow.BrowserHash[:], flow.Provider, flow.Nonce, flow.CodeVerifier, flow.ReturnTo, flow.CreatedAt, flow.ExpiresAt)
	return err
}

func (s *Store) ConsumeLoginFlow(ctx context.Context, stateHash, browserHash [32]byte, provider string, now time.Time) (authn.LoginFlow, error) {
	flow := authn.LoginFlow{StateHash: stateHash, BrowserHash: browserHash}
	err := s.pool.QueryRow(ctx, `delete from auth_login_flows where state_hash=$1 and browser_hash=$2 and provider=$3 and expires_at>$4 returning provider, nonce, code_verifier, return_to, created_at, expires_at`, stateHash[:], browserHash[:], provider, now).Scan(&flow.Provider, &flow.Nonce, &flow.CodeVerifier, &flow.ReturnTo, &flow.CreatedAt, &flow.ExpiresAt)
	return flow, err
}

func (s *Store) CreateSession(ctx context.Context, session authn.SessionRecord) error {
	_, err := s.pool.Exec(ctx, `insert into auth_sessions (token_hash, user_id, provider, created_at, last_seen_at, idle_expires_at, expires_at) values ($1,$2,$3,$4,$5,$6,$7)`, session.TokenHash[:], session.UserID, session.Provider, session.CreatedAt, session.LastSeenAt, session.IdleExpiresAt, session.ExpiresAt)
	return err
}

func (s *Store) SessionPrincipal(ctx context.Context, tokenHash [32]byte, now, idleUntil time.Time) (authn.Principal, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return authn.Principal{}, err
	}
	defer tx.Rollback(ctx)
	var userID int64
	var administrator bool
	var repositoryIDs []int64
	err = tx.QueryRow(ctx, `with live_session as (
            update auth_sessions session set last_seen_at=$2, idle_expires_at=$3
            from users user_record
            where session.token_hash=$1 and session.user_id=user_record.id
              and session.revoked_at is null and session.expires_at>$2 and session.idle_expires_at>$2
              and user_record.scim_active and user_record.suspended_at is null and user_record.deleted_at is null
            returning session.user_id
        )
        select live_session.user_id, exists(select 1 from user_roles where user_id=live_session.user_id),
            coalesce(array_agg(user_repository_grants.repository_id) filter (where user_repository_grants.repository_id is not null), '{}')
        from live_session left join user_repository_grants on user_repository_grants.user_id=live_session.user_id
        group by live_session.user_id`, tokenHash[:], now, idleUntil).Scan(&userID, &administrator, &repositoryIDs)
	if err != nil {
		return authn.Principal{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return authn.Principal{}, err
	}
	return authn.Principal{Subject: strconv.FormatInt(userID, 10), Method: "oidc", Administrator: administrator, RepositoryIDs: repositoryIDs}, nil
}

func (s *Store) RevokeSession(ctx context.Context, tokenHash [32]byte) error {
	_, err := s.pool.Exec(ctx, `update auth_sessions set revoked_at=now() where token_hash=$1 and revoked_at is null`, tokenHash[:])
	return err
}

func (s *Store) DeleteExpiredAuth(ctx context.Context, now time.Time) (flows, sessions int64, err error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return 0, 0, err
	}
	defer tx.Rollback(ctx)
	if result, err := tx.Exec(ctx, `delete from auth_login_flows where expires_at <= $1`, now); err != nil {
		return 0, 0, err
	} else {
		flows = result.RowsAffected()
	}
	if result, err := tx.Exec(ctx, `delete from auth_sessions where expires_at <= $1 or revoked_at is not null`, now); err != nil {
		return 0, 0, err
	} else {
		sessions = result.RowsAffected()
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, 0, err
	}
	return flows, sessions, nil
}

var _ authn.SessionStore = (*Store)(nil)
