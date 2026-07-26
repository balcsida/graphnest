package postgres

import (
	"context"
	"time"

	"github.com/grepnest/grepnest/internal/authn"
)

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
	_, err := s.pool.Exec(ctx, `insert into auth_sessions (token_hash, provider, principal_subject, display_name, method, administrator, installation_id, repository_ids, created_at, expires_at) values ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)`, session.TokenHash[:], session.Provider, session.Principal.Subject, session.DisplayName, session.Principal.Method, session.Principal.Administrator, session.Principal.InstallationID, append([]int64(nil), session.Principal.RepositoryIDs...), session.CreatedAt, session.ExpiresAt)
	return err
}

func (s *Store) Session(ctx context.Context, tokenHash [32]byte, now time.Time) (authn.SessionRecord, error) {
	session := authn.SessionRecord{TokenHash: tokenHash}
	err := s.pool.QueryRow(ctx, `select provider, principal_subject, display_name, method, administrator, installation_id, repository_ids, created_at, expires_at from auth_sessions where token_hash=$1 and expires_at>$2`, tokenHash[:], now).Scan(&session.Provider, &session.Principal.Subject, &session.DisplayName, &session.Principal.Method, &session.Principal.Administrator, &session.Principal.InstallationID, &session.Principal.RepositoryIDs, &session.CreatedAt, &session.ExpiresAt)
	if err == nil {
		session.Principal.RepositoryIDs = append([]int64(nil), session.Principal.RepositoryIDs...)
	}
	return session, err
}

func (s *Store) DeleteSession(ctx context.Context, tokenHash [32]byte) error {
	_, err := s.pool.Exec(ctx, "delete from auth_sessions where token_hash=$1", tokenHash[:])
	return err
}

func (s *Store) DeleteExpiredAuth(ctx context.Context, now time.Time) (flows, sessions int64, err error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return 0, 0, err
	}
	defer tx.Rollback(ctx)
	if result, err := tx.Exec(ctx, "delete from auth_login_flows where expires_at <= $1", now); err != nil {
		return 0, 0, err
	} else {
		flows = result.RowsAffected()
	}
	if result, err := tx.Exec(ctx, "delete from auth_sessions where expires_at <= $1", now); err != nil {
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
