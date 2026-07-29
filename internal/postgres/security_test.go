//go:build integration

package postgres

import (
	"crypto/sha256"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/grepnest/grepnest/internal/audit"
	"github.com/grepnest/grepnest/internal/authn"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func testCredential(fill byte) authn.PasswordCredential {
	return authn.PasswordCredential{
		Salt:      []byte{fill, fill, fill, fill, fill, fill, fill, fill, fill, fill, fill, fill, fill, fill, fill, fill},
		Hash:      []byte{fill, fill, fill, fill, fill, fill, fill, fill, fill, fill, fill, fill, fill, fill, fill, fill, fill, fill, fill, fill, fill, fill, fill, fill, fill, fill, fill, fill, fill, fill, fill, fill},
		MemoryKiB: 65536, Iterations: 3, Parallelism: 2, ForceRotation: true,
	}
}

func testAudit(operation string) audit.Event {
	return audit.Event{
		ActorType: "operator", ActorID: "test", TargetType: "user",
		TargetID: "test", AuthenticationMethod: "local", Operation: operation,
		Outcome: "success", RequestID: "test-request", CreatedAt: time.Now().UTC(),
	}
}

func seedSecurityUser(t *testing.T, store *Store, name, source string, administrator bool) int64 {
	t.Helper()
	var id int64
	if err := store.pool.QueryRow(t.Context(), `insert into users
		(external_id, user_name, source) values ($1,$1,$2) returning id`, name, source).Scan(&id); err != nil {
		t.Fatal(err)
	}
	if administrator {
		if _, err := store.pool.Exec(t.Context(), `insert into user_roles (user_id,administrator) values ($1,true)`, id); err != nil {
			t.Fatal(err)
		}
	}
	return id
}

func TestSecurityCredentialReplacementIsAtomic(t *testing.T) {
	store := New(testPool(t))
	if err := Migrate(t.Context(), store.pool); err != nil {
		t.Fatal(err)
	}
	userID := seedSecurityUser(t, store, "recovery-admin", "local", true)
	if err := store.SetPasswordCredential(t.Context(), userID, testCredential(1), testAudit("password_set")); err != nil {
		t.Fatal(err)
	}
	if _, err := store.pool.Exec(t.Context(), `insert into auth_sessions
		(token_hash,user_id,provider,created_at,last_seen_at,idle_expires_at,expires_at)
		values ($1,$2,'local',now(),now(),now()+interval '1 hour',now()+interval '2 hours')`,
		make([]byte, 32), userID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.pool.Exec(t.Context(), `insert into api_tokens
		(token_hash,prefix,user_id) values ($1,'gn_test',$2)`, bytes32(1), userID); err != nil {
		t.Fatal(err)
	}
	if err := store.SetPasswordCredential(t.Context(), userID, testCredential(2), testAudit("password_rotated")); err != nil {
		t.Fatal(err)
	}
	gotID, got, err := store.PasswordCredential(t.Context(), "RECOVERY-admin")
	if err != nil || gotID != userID || got.Hash[0] != 2 {
		t.Fatalf("id=%d credential=%#v err=%v", gotID, got, err)
	}
	var liveSessions, liveTokens int
	if err := store.pool.QueryRow(t.Context(), `select
		(select count(*) from auth_sessions where user_id=$1 and revoked_at is null),
		(select count(*) from api_tokens where user_id=$1 and revoked_at is null)`, userID).
		Scan(&liveSessions, &liveTokens); err != nil || liveSessions != 0 || liveTokens != 0 {
		t.Fatalf("sessions=%d tokens=%d err=%v", liveSessions, liveTokens, err)
	}
	events, _, err := store.AuditEvents(t.Context(), 10)
	if err != nil || len(events) != 2 || events[0].Operation != "password_rotated" {
		t.Fatalf("events=%#v err=%v", events, err)
	}

	bad := testAudit("password_not_changed")
	bad.Outcome = "secret"
	if err := store.SetPasswordCredential(t.Context(), userID, testCredential(3), bad); err == nil {
		t.Fatal("invalid audit event accepted")
	}
	_, got, err = store.PasswordCredential(t.Context(), "recovery-admin")
	if err != nil || got.Hash[0] != 2 {
		t.Fatalf("credential changed without audit: %#v err=%v", got, err)
	}

	replacement := testCredential(3)
	replacement.ForceRotation = false
	now := time.Now().UTC()
	session := authn.SessionRecord{
		TokenHash: sha256.Sum256([]byte("rotation-session")), UserID: userID, Provider: "local",
		CreatedAt: now, LastSeenAt: now, IdleExpiresAt: now.Add(time.Hour), ExpiresAt: now.Add(2 * time.Hour),
	}
	if err := store.RotatePasswordCredential(t.Context(), userID, testCredential(2), replacement, session, [32]byte{}, [32]byte{}, testAudit("password_rotated")); err != nil {
		t.Fatal(err)
	}
	staleReplacement := testCredential(4)
	staleReplacement.ForceRotation = false
	if err := store.RotatePasswordCredential(t.Context(), userID, testCredential(2), staleReplacement, session, [32]byte{}, [32]byte{}, testAudit("password_rotated")); !errors.Is(err, authn.ErrUnauthenticated) {
		t.Fatalf("stale rotation error=%v", err)
	}
	_, got, err = store.PasswordCredential(t.Context(), "recovery-admin")
	if err != nil || got.Hash[0] != 3 || got.ForceRotation {
		t.Fatalf("credential after compare-and-replace: %#v err=%v", got, err)
	}
	if err := store.pool.QueryRow(t.Context(), `select count(*) from auth_sessions where user_id=$1 and revoked_at is null`, userID).Scan(&liveSessions); err != nil || liveSessions != 1 {
		t.Fatalf("rotation sessions=%d err=%v", liveSessions, err)
	}
	if err := store.SetPasswordCredential(t.Context(), userID, testCredential(4), testAudit("password_set")); err != nil {
		t.Fatal(err)
	}
	if err := store.pool.QueryRow(t.Context(), `select count(*) from auth_sessions where user_id=$1 and revoked_at is null`, userID).Scan(&liveSessions); err != nil || liveSessions != 0 {
		t.Fatalf("sessions after operator reset=%d err=%v", liveSessions, err)
	}

	normalCredential := testCredential(5)
	normalCredential.ForceRotation = false
	if err := store.SetPasswordCredential(t.Context(), userID, normalCredential, testAudit("password_set")); err != nil {
		t.Fatal(err)
	}
	normalSession := session
	normalSession.TokenHash = sha256.Sum256([]byte("normal-session"))
	if err := store.CreatePasswordSession(t.Context(), userID, normalCredential, normalSession, [32]byte{}, [32]byte{}); err != nil {
		t.Fatal(err)
	}
	if err := store.SetPasswordCredential(t.Context(), userID, testCredential(6), testAudit("password_set")); err != nil {
		t.Fatal(err)
	}
	if err := store.CreatePasswordSession(t.Context(), userID, normalCredential, normalSession, [32]byte{}, [32]byte{}); !errors.Is(err, authn.ErrUnauthenticated) {
		t.Fatalf("stale login session error=%v", err)
	}
	if err := store.pool.QueryRow(t.Context(), `select count(*) from auth_sessions where user_id=$1 and revoked_at is null`, userID).Scan(&liveSessions); err != nil || liveSessions != 0 {
		t.Fatalf("normal sessions after operator reset=%d err=%v", liveSessions, err)
	}
}

func TestSecurityRejectsNonLocalAdministratorCredentials(t *testing.T) {
	store := New(testPool(t))
	if err := Migrate(t.Context(), store.pool); err != nil {
		t.Fatal(err)
	}
	for _, userID := range []int64{
		seedSecurityUser(t, store, "oidc-admin", "scim", true),
		seedSecurityUser(t, store, "local-user", "local", false),
	} {
		if err := store.SetPasswordCredential(t.Context(), userID, testCredential(1), testAudit("password_set")); !errors.Is(err, ErrInvalidSecurityPrincipal) {
			t.Fatalf("user=%d error=%v", userID, err)
		}
	}
}

func TestSecurityUpsertBreakGlassAdminDoesNotConvertUsers(t *testing.T) {
	store := New(testPool(t))
	if err := Migrate(t.Context(), store.pool); err != nil {
		t.Fatal(err)
	}
	for _, source := range []string{"scim", "local"} {
		name := source + "-existing"
		seedSecurityUser(t, store, name, source, source == "scim")
		if _, err := store.UpsertBreakGlassAdmin(t.Context(), name, testCredential(1), testAudit("break_glass_password_set")); !errors.Is(err, ErrInvalidSecurityPrincipal) {
			t.Fatalf("source=%s error=%v", source, err)
		}
	}
	id, err := store.UpsertBreakGlassAdmin(t.Context(), "new-admin", testCredential(1), testAudit("break_glass_password_set"))
	if err != nil {
		t.Fatal(err)
	}
	rotated, err := store.UpsertBreakGlassAdmin(t.Context(), "NEW-admin", testCredential(2), testAudit("break_glass_password_set"))
	if err != nil || rotated != id {
		t.Fatalf("id=%d rotated=%d err=%v", id, rotated, err)
	}
	var source string
	var administrator bool
	if err := store.pool.QueryRow(t.Context(), `select source,
		exists(select 1 from user_roles where user_id=users.id)
		from users where id=$1`, id).Scan(&source, &administrator); err != nil || source != "local" || !administrator {
		t.Fatalf("source=%q administrator=%v err=%v", source, administrator, err)
	}
	if _, err := store.UpsertBreakGlassAdmin(t.Context(), "bad\nname", testCredential(1), testAudit("break_glass_password_set")); !errors.Is(err, ErrInvalidSecurityPrincipal) {
		t.Fatalf("unsafe username error=%v", err)
	}
}

func TestSecurityUpsertBreakGlassAdminSerializesFirstCreate(t *testing.T) {
	store := New(testPool(t))
	if err := Migrate(t.Context(), store.pool); err != nil {
		t.Fatal(err)
	}
	blocker, err := store.pool.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := blocker.Exec(t.Context(), `select pg_advisory_xact_lock($1,hashtext(lower($2)))`,
		breakGlassUserLockNamespace, "concurrent-admin"); err != nil {
		t.Fatal(err)
	}
	start := make(chan struct{})
	type result struct {
		id  int64
		err error
	}
	results := make(chan result, 2)
	var group sync.WaitGroup
	for index := range 2 {
		group.Add(1)
		go func() {
			defer group.Done()
			<-start
			id, err := store.UpsertBreakGlassAdmin(t.Context(), "concurrent-admin",
				testCredential(byte(index+1)), testAudit("break_glass_password_set"))
			results <- result{id: id, err: err}
		}()
	}
	close(start)
	deadline := time.Now().Add(5 * time.Second)
	for {
		var waiting int
		if err := store.pool.QueryRow(t.Context(), `select count(*) from pg_locks
			where locktype='advisory' and not granted`).Scan(&waiting); err != nil {
			t.Fatal(err)
		}
		if waiting >= 2 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("concurrent upserts did not wait on separate connections")
		}
		time.Sleep(5 * time.Millisecond)
	}
	if err := blocker.Rollback(t.Context()); err != nil {
		t.Fatal(err)
	}
	group.Wait()
	close(results)
	var userID int64
	for result := range results {
		if result.err != nil {
			t.Fatal(result.err)
		}
		if userID != 0 && result.id != userID {
			t.Fatalf("user IDs %d and %d differ", userID, result.id)
		}
		userID = result.id
	}
	var users, credentials, events int
	if err := store.pool.QueryRow(t.Context(), `select
		(select count(*) from users where lower(user_name)=lower('concurrent-admin')),
		(select count(*) from password_credentials where user_id=$1),
		(select count(*) from audit_events where operation='break_glass_password_set')`, userID).
		Scan(&users, &credentials, &events); err != nil || users != 1 || credentials != 1 || events != 2 {
		t.Fatalf("users=%d credentials=%d events=%d err=%v", users, credentials, events, err)
	}
}

func TestSecurityLoginAttemptConsumptionIsConcurrentAndResettable(t *testing.T) {
	store := New(testPool(t))
	if err := Migrate(t.Context(), store.pool); err != nil {
		t.Fatal(err)
	}
	key := sha256.Sum256([]byte("account"))
	now := time.Now().UTC()
	var group sync.WaitGroup
	start := make(chan struct{})
	type result struct {
		allowed bool
		retryAt time.Time
		err     error
	}
	results := make(chan result, 6)
	connections := make([]*pgxpool.Conn, 0, 6)
	for range 6 {
		connection, err := store.pool.Acquire(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		connections = append(connections, connection)
		t.Cleanup(connection.Release)
		group.Add(1)
		go func(connection *pgxpool.Conn) {
			defer group.Done()
			<-start
			allowed, retryAt, err := consumeLoginAttempt(t.Context(), connection, key, now)
			results <- result{allowed: allowed, retryAt: retryAt, err: err}
		}(connection)
	}
	close(start)
	group.Wait()
	close(results)
	var allowed, blocked int
	for result := range results {
		if result.err != nil {
			t.Fatal(result.err)
		}
		if result.allowed {
			allowed++
		} else {
			blocked++
			if result.retryAt.Before(now) || result.retryAt.After(now.Add(loginFailureWindow)) {
				t.Fatalf("unbounded retry time %v", result.retryAt)
			}
		}
	}
	if allowed != loginFailureLimit || blocked != 1 {
		t.Fatalf("allowed=%d blocked=%d", allowed, blocked)
	}
	sourceKey := [32]byte{2}
	for range loginFailureLimit {
		if _, _, err := store.ConsumeLoginAttempt(t.Context(), sourceKey, now); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.ClearLoginFailures(t.Context(), key, sourceKey); err != nil {
		t.Fatal(err)
	}
	if allowed, _, err := store.ConsumeLoginAttempt(t.Context(), key, now); err != nil || !allowed {
		t.Fatalf("allowed after clear=%v err=%v", allowed, err)
	}
	if allowed, _, err := store.ConsumeLoginAttempt(t.Context(), sourceKey, now); err != nil || !allowed {
		t.Fatalf("source allowed after clear=%v err=%v", allowed, err)
	}
	for range loginFailureLimit {
		if _, _, err := store.ConsumeLoginAttempt(t.Context(), key, now); err != nil {
			t.Fatal(err)
		}
	}
	if allowed, _, err := store.ConsumeLoginAttempt(t.Context(), key, now.Add(loginFailureWindow)); err != nil || !allowed {
		t.Fatalf("allowed after window=%v err=%v", allowed, err)
	}
}

func TestSecurityAuditEventsAreBoundedNewestFirst(t *testing.T) {
	store := New(testPool(t))
	if err := Migrate(t.Context(), store.pool); err != nil {
		t.Fatal(err)
	}
	when := time.Now().UTC()
	operations := []string{audit.OperationOIDCLoginDenied, audit.OperationLocalLoginDenied, audit.OperationAPITokenUseRejected}
	for _, operation := range operations {
		event := testAudit(operation)
		event.CreatedAt = when
		if err := store.AppendAudit(t.Context(), event); err != nil {
			t.Fatal(err)
		}
	}
	events, more, err := store.AuditEvents(t.Context(), 2)
	if err != nil || !more || len(events) != 2 || events[0].Operation != operations[2] || events[1].Operation != operations[1] {
		t.Fatalf("events=%#v more=%v err=%v", events, more, err)
	}
	if _, _, err := store.AuditEvents(t.Context(), 0); err == nil {
		t.Fatal("unbounded audit list accepted")
	}
	if _, err := store.pool.Exec(t.Context(), `update audit_events set operation='changed'`); err == nil {
		t.Fatal("audit update accepted")
	}
	if _, err := store.pool.Exec(t.Context(), `delete from audit_events`); err == nil {
		t.Fatal("audit delete accepted")
	}
	if _, err := store.pool.Exec(t.Context(), `truncate audit_events`); err == nil {
		t.Fatal("audit truncate accepted")
	}
}

func TestSecurityPasswordCredentialDoesNotRevealAccountShape(t *testing.T) {
	store := New(testPool(t))
	if err := Migrate(t.Context(), store.pool); err != nil {
		t.Fatal(err)
	}
	seedSecurityUser(t, store, "local-no-password", "local", true)
	for _, name := range []string{"local-no-password", "absent"} {
		if _, _, err := store.PasswordCredential(t.Context(), name); !errors.Is(err, pgx.ErrNoRows) {
			t.Fatalf("name=%q error=%v", name, err)
		}
	}
}

func bytes32(fill byte) []byte {
	value := make([]byte, 32)
	for index := range value {
		value[index] = fill
	}
	return value
}
