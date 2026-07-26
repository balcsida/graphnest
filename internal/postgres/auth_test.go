//go:build integration

package postgres

import (
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/grepnest/grepnest/internal/authn"
	"github.com/jackc/pgx/v5"
)

func TestAuthStorePersistsOpaqueRecords(t *testing.T) {
	store := migratedStore(t)
	now := time.Now().UTC().Truncate(time.Microsecond)
	flow := testLoginFlow(now.Add(time.Minute))
	if err := store.CreateLoginFlow(t.Context(), flow); err != nil {
		t.Fatal(err)
	}
	gotFlow, err := store.ConsumeLoginFlow(t.Context(), flow.StateHash, flow.BrowserHash, flow.Provider, now)
	if err != nil || gotFlow.StateHash != flow.StateHash || gotFlow.BrowserHash != flow.BrowserHash || gotFlow.Provider != flow.Provider || gotFlow.Nonce != flow.Nonce || gotFlow.CodeVerifier != flow.CodeVerifier || gotFlow.ReturnTo != flow.ReturnTo || !gotFlow.CreatedAt.Equal(flow.CreatedAt) || !gotFlow.ExpiresAt.Equal(flow.ExpiresAt) {
		t.Fatalf("flow = %#v, err=%v", gotFlow, err)
	}
	if _, err := store.ConsumeLoginFlow(t.Context(), flow.StateHash, flow.BrowserHash, flow.Provider, now); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("reused flow error = %v", err)
	}

	session := testSession(now.Add(time.Minute))
	if err := store.CreateSession(t.Context(), session); err != nil {
		t.Fatal(err)
	}
	session.Principal.RepositoryIDs[0] = 999
	gotSession, err := store.Session(t.Context(), session.TokenHash, now)
	if err != nil || gotSession.Principal.RepositoryIDs[0] != 101 {
		t.Fatalf("session = %#v, err=%v", gotSession, err)
	}
	gotSession.Principal.RepositoryIDs[0] = 999
	gotSession, err = store.Session(t.Context(), session.TokenHash, now)
	if err != nil || gotSession.Principal.RepositoryIDs[0] != 101 {
		t.Fatalf("aliased session = %#v, err=%v", gotSession, err)
	}
	if err := store.DeleteSession(t.Context(), session.TokenHash); err != nil {
		t.Fatal(err)
	}
	if err := store.DeleteSession(t.Context(), session.TokenHash); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Session(t.Context(), session.TokenHash, now); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("deleted session error = %v", err)
	}
}

func TestConsumeLoginFlowRequiresCorrectBindingAndIsAtomic(t *testing.T) {
	store := migratedStore(t)
	now := time.Now().UTC().Truncate(time.Microsecond)
	flow := testLoginFlow(now.Add(time.Minute))
	if err := store.CreateLoginFlow(t.Context(), flow); err != nil {
		t.Fatal(err)
	}
	wrong := flow.BrowserHash
	wrong[0]++
	if _, err := store.ConsumeLoginFlow(t.Context(), flow.StateHash, wrong, flow.Provider, now); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("wrong binding error = %v", err)
	}
	if _, err := store.ConsumeLoginFlow(t.Context(), flow.StateHash, [32]byte{}, flow.Provider, now); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("missing binding error = %v", err)
	}
	if _, err := store.ConsumeLoginFlow(t.Context(), flow.StateHash, flow.BrowserHash, flow.Provider, now); err != nil {
		t.Fatal(err)
	}

	flow = testLoginFlow(now.Add(time.Minute))
	flow.StateHash[0]++
	if err := store.CreateLoginFlow(t.Context(), flow); err != nil {
		t.Fatal(err)
	}
	errs := make(chan error, 2)
	var group sync.WaitGroup
	for range 2 {
		group.Add(1)
		go func() {
			defer group.Done()
			_, err := store.ConsumeLoginFlow(t.Context(), flow.StateHash, flow.BrowserHash, flow.Provider, now)
			errs <- err
		}()
	}
	group.Wait()
	close(errs)
	successes := 0
	for err := range errs {
		if err == nil {
			successes++
		} else if !errors.Is(err, pgx.ErrNoRows) {
			t.Fatal(err)
		}
	}
	if successes != 1 {
		t.Fatalf("successful consumers = %d, want 1", successes)
	}
}

func TestAuthStoreRejectsExpiredRecordsAndCleansUp(t *testing.T) {
	store := migratedStore(t)
	now := time.Now().UTC().Truncate(time.Microsecond)
	expiredFlow := testLoginFlow(now.Add(-time.Minute))
	if _, err := store.pool.Exec(t.Context(), `insert into auth_login_flows
		(state_hash, browser_hash, provider, nonce, code_verifier, return_to, created_at, expires_at)
		values ($1,$2,$3,$4,$5,$6,$7,$8)`, expiredFlow.StateHash[:], expiredFlow.BrowserHash[:], expiredFlow.Provider,
		expiredFlow.Nonce, expiredFlow.CodeVerifier, expiredFlow.ReturnTo, now.Add(-2*time.Minute), expiredFlow.ExpiresAt); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ConsumeLoginFlow(t.Context(), expiredFlow.StateHash, expiredFlow.BrowserHash, expiredFlow.Provider, now); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("expired flow error = %v", err)
	}
	expiredSession := testSession(now.Add(-time.Minute))
	if _, err := store.pool.Exec(t.Context(), `insert into auth_sessions
		(token_hash, provider, principal_subject, display_name, method, administrator, installation_id, repository_ids, created_at, expires_at)
		values ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)`, expiredSession.TokenHash[:], expiredSession.Provider,
		expiredSession.Principal.Subject, expiredSession.DisplayName, expiredSession.Principal.Method, expiredSession.Principal.Administrator,
		expiredSession.Principal.InstallationID, expiredSession.Principal.RepositoryIDs, now.Add(-2*time.Minute), expiredSession.ExpiresAt); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Session(t.Context(), expiredSession.TokenHash, now); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("expired session error = %v", err)
	}
	liveFlow := testLoginFlow(now.Add(time.Minute))
	liveFlow.StateHash[0]++
	liveSession := testSession(now.Add(time.Minute))
	liveSession.TokenHash[0]++
	if err := store.CreateLoginFlow(t.Context(), liveFlow); err != nil {
		t.Fatal(err)
	}
	if err := store.CreateSession(t.Context(), liveSession); err != nil {
		t.Fatal(err)
	}
	flows, sessions, err := store.DeleteExpiredAuth(t.Context(), now)
	if err != nil || flows != 1 || sessions != 1 {
		t.Fatalf("cleanup flows=%d sessions=%d err=%v", flows, sessions, err)
	}
	if _, err := store.Session(t.Context(), liveSession.TokenHash, now); err != nil {
		t.Fatal(err)
	}
}

func TestAuthSchemaEnforcesOpaqueSessionStorage(t *testing.T) {
	store := migratedStore(t)
	for _, table := range []string{"auth_login_flows", "auth_sessions"} {
		rows, err := store.pool.Query(t.Context(), `select column_name from information_schema.columns
			where table_schema=current_schema() and table_name=$1`, table)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		for rows.Next() {
			var name string
			if err := rows.Scan(&name); err != nil {
				t.Fatal(err)
			}
			for _, forbidden := range []string{"access_token", "refresh_token", "id_token", "authorization_code", "issuer", "subject", "state", "browser", "token"} {
				if name == forbidden || name == "raw_"+forbidden {
					t.Fatalf("%s stores forbidden column %q", table, name)
				}
			}
		}
		if err := rows.Err(); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := store.pool.Exec(t.Context(), `insert into auth_login_flows
		(state_hash, browser_hash, provider, nonce, code_verifier, expires_at) values ('bad', 'bad', 'oidc', 'n', 'v', now()+interval '1 minute')`); err == nil {
		t.Fatal("short hashes accepted")
	}
	if _, err := store.pool.Exec(t.Context(), `insert into auth_sessions
		(token_hash, provider, principal_subject, method, installation_id, repository_ids, expires_at)
		values ('bad', 'oidc', 'subject', 'oidc', 1, array[1], now()+interval '1 minute')`); err == nil {
		t.Fatal("short session hash accepted")
	}
	if _, err := store.pool.Exec(t.Context(), `insert into auth_login_flows
		(state_hash, browser_hash, provider, nonce, code_verifier, expires_at) values (decode(repeat('00', 32), 'hex'), decode(repeat('00', 32), 'hex'), 'oidc', '', '', now()+interval '1 minute')`); err == nil {
		t.Fatal("empty OIDC fields accepted")
	}
}

func testLoginFlow(expiresAt time.Time) authn.LoginFlow {
	return authn.LoginFlow{StateHash: [32]byte{1}, BrowserHash: [32]byte{2}, Provider: "oidc", Nonce: "nonce", CodeVerifier: "verifier", ReturnTo: "/", CreatedAt: expiresAt.Add(-time.Minute), ExpiresAt: expiresAt}
}

func testSession(expiresAt time.Time) authn.SessionRecord {
	return authn.SessionRecord{TokenHash: [32]byte{3}, Provider: "oidc", DisplayName: "Ada", Principal: authn.Principal{Subject: "subject", Method: "oidc", InstallationID: 10, RepositoryIDs: []int64{101, 102}}, CreatedAt: expiresAt.Add(-time.Minute), ExpiresAt: expiresAt}
}
