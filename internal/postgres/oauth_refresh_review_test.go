//go:build integration

package postgres

import (
	"context"
	"errors"
	"strconv"
	"testing"
	"time"

	"github.com/balcsida/graphnest/internal/audit"
	"github.com/balcsida/graphnest/internal/authn"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func testOAuthRefreshAudit(userID, grantID int64) audit.Event {
	return audit.Event{
		ActorType: "user", ActorID: strconv.FormatInt(userID, 10), TargetType: "oauth_grant",
		TargetID: strconv.FormatInt(grantID, 10), AuthenticationMethod: authn.ProviderOAuthToken,
		Operation: audit.OperationOAuthGrantRefreshed, Outcome: "success",
	}
}

func TestOAuthRefreshAuditCommitsWithRotation(t *testing.T) {
	store := migratedStore(t)
	now := time.Now().UTC().Truncate(time.Microsecond)
	userID := insertIdentityUser(t, store, "refresh-audit", "refresh-audit")
	client := seedOAuthClient(t, store, now)
	grantID, err := store.CreateOAuthGrant(t.Context(), authn.OAuthGrant{
		ClientID: client.ID, UserID: userID, AccessHash: [32]byte{1}, AccessExpiresAt: now.Add(time.Hour),
		RefreshHash: [32]byte{2}, CreatedAt: now, ExpiresAt: now.Add(24 * time.Hour),
	})
	if err != nil {
		t.Fatal(err)
	}
	event := testOAuthRefreshAudit(userID, grantID)
	event.RequestID = "request-42"
	rotation := authn.OAuthRotation{
		AccessHash: [32]byte{3}, AccessExpiresAt: now.Add(2 * time.Hour), RefreshHash: [32]byte{4},
		Now: now.Add(time.Minute), Grace: 30 * time.Second, Audit: event,
	}

	failed := rotation
	failed.Audit.CreatedAt = time.Date(1_000_000, 1, 1, 0, 0, 0, 0, time.UTC)
	if _, err := store.RotateOAuthGrant(t.Context(), [32]byte{2}, failed); err == nil {
		t.Fatal("refresh rotation survived audit insert failure")
	}
	current, err := store.OAuthGrantByRefresh(t.Context(), [32]byte{2}, rotation.Now)
	if err != nil || current.AccessHash != [32]byte{1} {
		t.Fatalf("failed audit changed current grant: grant=%+v err=%v", current, err)
	}
	var consumed, events int
	if err := store.pool.QueryRow(t.Context(), `select count(*) from oauth_refresh_tokens`).Scan(&consumed); err != nil || consumed != 0 {
		t.Fatalf("failed audit retained consumed token: count=%d err=%v", consumed, err)
	}
	if err := store.pool.QueryRow(t.Context(), `select count(*) from audit_events`).Scan(&events); err != nil || events != 0 {
		t.Fatalf("failed audit retained event: count=%d err=%v", events, err)
	}

	if _, err := store.RotateOAuthGrant(t.Context(), [32]byte{2}, rotation); err != nil {
		t.Fatalf("retry refresh: %v", err)
	}
	audits, _, err := store.AuditEvents(t.Context(), 10)
	if err != nil || len(audits) != 1 || audits[0].ActorType != event.ActorType || audits[0].ActorID != event.ActorID ||
		audits[0].TargetType != event.TargetType || audits[0].TargetID != event.TargetID ||
		audits[0].AuthenticationMethod != event.AuthenticationMethod || audits[0].Operation != event.Operation ||
		audits[0].Outcome != event.Outcome || audits[0].RequestID != event.RequestID {
		t.Fatalf("refresh audits=%#v err=%v", audits, err)
	}
}

func TestOAuthRefreshSnapshotCommitsWithRotation(t *testing.T) {
	store, grant := seedReplayGrant(t)
	seedReadyRepository(t, store, 101, testSHA('a'))
	seedReadyRepository(t, store, 102, testSHA('a'))
	if err := store.ReplaceGitHubGrants(t.Context(), grant.UserID, []int64{101}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.pool.Exec(t.Context(), `create function reject_refresh_grant() returns trigger language plpgsql as $$
		begin raise exception 'injected repository grant failure'; end $$;
		create trigger reject_refresh_grant before insert on user_github_grants
		for each row execute function reject_refresh_grant()`); err != nil {
		t.Fatal(err)
	}
	rotation := authn.OAuthRotation{
		AccessHash: [32]byte{3}, AccessExpiresAt: grant.AccessExpiresAt, RefreshHash: [32]byte{4},
		Now: grant.CreatedAt.Add(time.Minute), Grace: 30 * time.Second,
		Audit: testOAuthRefreshAudit(grant.UserID, grant.ID), RepositoryIDs: []int64{102}, ReplaceRepositories: true,
	}
	if _, err := store.RotateOAuthGrant(t.Context(), grant.RefreshHash, rotation); err == nil {
		t.Fatal("refresh rotation survived repository grant failure")
	}
	current, err := store.OAuthGrantByRefresh(t.Context(), grant.RefreshHash, rotation.Now)
	if err != nil || current.AccessHash != grant.AccessHash {
		t.Fatalf("failed snapshot changed current grant: grant=%+v err=%v", current, err)
	}
	assertGitHubGrants(t, store, grant.UserID, []int64{101})
	if _, err := store.pool.Exec(t.Context(), `drop trigger reject_refresh_grant on user_github_grants; drop function reject_refresh_grant()`); err != nil {
		t.Fatal(err)
	}
	rotated, err := store.RotateOAuthGrant(t.Context(), grant.RefreshHash, rotation)
	if err != nil {
		t.Fatalf("retry refresh: %v", err)
	}
	assertGitHubGrants(t, store, grant.UserID, []int64{102})
	rotation.AccessHash, rotation.RefreshHash, rotation.Now = [32]byte{5}, [32]byte{6}, rotation.Now.Add(time.Minute)
	rotation.RepositoryIDs = []int64{}
	if _, err := store.RotateOAuthGrant(t.Context(), rotated.RefreshHash, rotation); err != nil {
		t.Fatalf("empty snapshot refresh: %v", err)
	}
	assertGitHubGrants(t, store, grant.UserID, nil)
}

func assertGitHubGrants(t *testing.T, store *Store, userID int64, want []int64) {
	t.Helper()
	rows, err := store.pool.Query(t.Context(), `select repository_id from user_github_grants where user_id=$1 order by repository_id`, userID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var got []int64
	for rows.Next() {
		var repositoryID int64
		if err := rows.Scan(&repositoryID); err != nil {
			t.Fatal(err)
		}
		got = append(got, repositoryID)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if len(got) != len(want) {
		t.Fatalf("GitHub grants=%v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("GitHub grants=%v, want %v", got, want)
		}
	}
}

func TestOAuthRefreshRejectionRevokesOnlyCurrentGrant(t *testing.T) {
	store, grant := seedReplayGrant(t)
	seedReadyRepository(t, store, 101, testSHA('a'))
	if err := store.ReplaceGitHubGrants(t.Context(), grant.UserID, []int64{101}); err != nil {
		t.Fatal(err)
	}
	event := testOAuthRefreshAudit(grant.UserID, grant.ID)
	event.Operation, event.Outcome = audit.OperationOAuthGrantRevoked, "denied"
	rejection := authn.OAuthRotation{Now: grant.CreatedAt.Add(time.Minute), Revoke: true, Audit: event}
	failed := rejection
	failed.Audit.CreatedAt = time.Date(1_000_000, 1, 1, 0, 0, 0, 0, time.UTC)
	if _, err := store.RotateOAuthGrant(t.Context(), grant.RefreshHash, failed); err == nil {
		t.Fatal("refresh rejection survived audit insert failure")
	}
	current, err := store.OAuthGrantByRefresh(t.Context(), grant.RefreshHash, rejection.Now)
	if err != nil || current.RevokedAt != nil || string(current.GitHubTokenCiphertext) != "ciphertext" {
		t.Fatalf("failed rejection changed grant: grant=%+v err=%v", current, err)
	}
	assertGitHubGrants(t, store, grant.UserID, []int64{101})

	revoked, err := store.RotateOAuthGrant(t.Context(), grant.RefreshHash, rejection)
	if err != nil || revoked.RevokedAt == nil || revoked.GitHubTokenCiphertext != nil {
		t.Fatalf("rejected grant=%+v err=%v", revoked, err)
	}
	if _, err := store.OAuthGrantByRefresh(t.Context(), grant.RefreshHash, rejection.Now); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("rejected refresh still current: %v", err)
	}
	assertGitHubGrants(t, store, grant.UserID, []int64{101})
	audits, _, err := store.AuditEvents(t.Context(), 10)
	if err != nil || len(audits) != 1 || audits[0].Operation != audit.OperationOAuthGrantRevoked || audits[0].Outcome != "denied" {
		t.Fatalf("rejection audits=%#v err=%v", audits, err)
	}
}

func TestOAuthRefreshRejectionIgnoresConsumedToken(t *testing.T) {
	store, grant := seedReplayGrant(t)
	rotated, err := store.RotateOAuthGrant(t.Context(), grant.RefreshHash, authn.OAuthRotation{
		AccessHash: [32]byte{3}, AccessExpiresAt: grant.AccessExpiresAt, RefreshHash: [32]byte{4},
		Now: grant.CreatedAt.Add(time.Minute), Grace: 30 * time.Second, Audit: testOAuthRefreshAudit(grant.UserID, grant.ID),
	})
	if err != nil {
		t.Fatal(err)
	}
	event := testOAuthRefreshAudit(grant.UserID, grant.ID)
	event.Operation, event.Outcome = audit.OperationOAuthGrantRevoked, "denied"
	if _, err := store.RotateOAuthGrant(t.Context(), grant.RefreshHash, authn.OAuthRotation{Now: grant.CreatedAt.Add(2 * time.Minute), Revoke: true, Audit: event}); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("consumed-token rejection error=%v, want no rows", err)
	}
	current, err := store.OAuthGrantByRefresh(t.Context(), rotated.RefreshHash, grant.CreatedAt.Add(2*time.Minute))
	if err != nil || current.RevokedAt != nil || string(current.GitHubTokenCiphertext) != "ciphertext" {
		t.Fatalf("stale rejection changed current grant: grant=%+v err=%v", current, err)
	}
}

func TestOAuthRefreshLocksUserBeforeGrant(t *testing.T) {
	store, grant := seedReplayGrant(t)
	seedReadyRepository(t, store, 101, testSHA('a'))
	seedReadyRepository(t, store, 102, testSHA('a'))
	if err := store.ReplaceGitHubGrants(t.Context(), grant.UserID, []int64{101}); err != nil {
		t.Fatal(err)
	}
	applicationName := "oauth-refresh-lock-order-" + strconv.FormatInt(time.Now().UnixNano(), 10)
	config := store.pool.Config()
	config.MaxConns = 1
	config.ConnConfig.RuntimeParams["application_name"] = applicationName
	refreshPool, err := pgxpool.NewWithConfig(t.Context(), config)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(refreshPool.Close)
	adminTx, err := store.pool.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer adminTx.Rollback(t.Context())
	var found int
	if err := adminTx.QueryRow(t.Context(), `select 1 from users where id=$1 for update`, grant.UserID).Scan(&found); err != nil {
		t.Fatal(err)
	}
	refreshCtx, cancelRefresh := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancelRefresh()
	refreshResult := make(chan error, 1)
	go func() {
		_, err := New(refreshPool).RotateOAuthGrant(refreshCtx, grant.RefreshHash, authn.OAuthRotation{
			AccessHash: [32]byte{3}, AccessExpiresAt: grant.AccessExpiresAt, RefreshHash: [32]byte{4},
			Now: grant.CreatedAt.Add(time.Minute), Grace: 30 * time.Second,
			Audit: testOAuthRefreshAudit(grant.UserID, grant.ID), RepositoryIDs: []int64{102}, ReplaceRepositories: true,
		})
		refreshResult <- err
	}()
	waitCtx, cancelWait := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancelWait()
	for {
		var waiting bool
		if err := store.pool.QueryRow(waitCtx, `select exists(
			select 1 from pg_stat_activity
			where application_name=$1 and wait_event_type='Lock')`, applicationName).Scan(&waiting); err != nil {
			t.Fatal(err)
		}
		if waiting {
			break
		}
	}
	if err := adminTx.QueryRow(t.Context(), `select 1 from oauth_grants where id=$1 for update nowait`, grant.ID).Scan(&found); err != nil {
		cancelRefresh()
		_ = adminTx.Rollback(t.Context())
		refreshErr := <-refreshResult
		t.Fatalf("refresh locked grant before user: admin=%v refresh=%v", err, refreshErr)
	}
	if _, err := adminTx.Exec(t.Context(), `update oauth_grants set revoked_at=now(), github_token_ct=null where id=$1`, grant.ID); err != nil {
		t.Fatal(err)
	}
	if err := adminTx.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := <-refreshResult; !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("refresh after administrative revocation=%v, want no rows", err)
	}
	assertGitHubGrants(t, store, grant.UserID, []int64{101})
}
