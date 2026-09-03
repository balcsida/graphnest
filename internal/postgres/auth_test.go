//go:build integration

package postgres

import (
	"errors"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/balcsida/graphnest/internal/audit"
	"github.com/balcsida/graphnest/internal/authn"
	"github.com/jackc/pgx/v5"
)

func TestAuthStoreBindsUsersAndResolvesLivePrincipal(t *testing.T) {
	store := migratedStore(t)
	userID := insertIdentityUser(t, store, "directory-42", "ada")
	if _, err := store.BindFederatedUser(t.Context(), "https://id.example.test", "subject-1", "directory-42"); err != nil {
		t.Fatal(err)
	}
	if boundID, err := store.BindFederatedUser(t.Context(), "https://id.example.test", "subject-1", "directory-42"); err != nil || boundID != userID {
		t.Fatalf("repeated binding userID=%d err=%v", boundID, err)
	}
	otherID := insertIdentityUser(t, store, "directory-43", "grace")
	if boundID, err := store.BindFederatedUser(t.Context(), "https://id.example.test", "subject-1", "directory-43"); err != nil || boundID != userID {
		t.Fatalf("identity rebound userID=%d err=%v", boundID, err)
	}
	if otherID == userID {
		t.Fatal("distinct users share an ID")
	}

	now := time.Now().UTC().Truncate(time.Microsecond)
	flow := authn.LoginFlow{StateHash: [32]byte{1}, BrowserHash: [32]byte{2}, Provider: "oidc", Nonce: "nonce", CodeVerifier: "verifier", ReturnTo: "/", CreatedAt: now, ExpiresAt: now.Add(time.Minute)}
	if err := store.CreateLoginFlow(t.Context(), flow); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ConsumeLoginFlow(t.Context(), flow.StateHash, flow.BrowserHash, flow.Provider, now); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ConsumeLoginFlow(t.Context(), flow.StateHash, flow.BrowserHash, flow.Provider, now); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("reused flow error = %v", err)
	}

	if _, err := store.pool.Exec(t.Context(), `insert into installations (github_id, account_login, account_type, status) values (1, 'acme', 'Organization', 'active')`); err != nil {
		t.Fatal(err)
	}
	if _, err := store.pool.Exec(t.Context(), `insert into repositories (github_id, installation_id, owner, name, clone_url, web_url, default_branch, private, archived, enabled, status) values (101, 1, 'acme', 'one', '', '', 'main', false, false, true, 'ready')`); err != nil {
		t.Fatal(err)
	}
	if _, err := store.pool.Exec(t.Context(), `insert into user_roles (user_id, administrator) values ($1, true)`, userID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.pool.Exec(t.Context(), `insert into user_repository_grants (user_id, repository_id) values ($1, 101)`, userID); err != nil {
		t.Fatal(err)
	}
	tokenHash := [32]byte{3}
	if err := store.CreateSession(t.Context(), authn.SessionRecord{TokenHash: tokenHash, UserID: userID, Provider: "oidc", CreatedAt: now, LastSeenAt: now, IdleExpiresAt: now.Add(time.Minute), ExpiresAt: now.Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	principal, err := store.SessionPrincipal(t.Context(), tokenHash, now, now.Add(30*time.Minute))
	if err != nil || principal.Subject != strconv.FormatInt(userID, 10) || principal.Method != "oidc" || !principal.Administrator || len(principal.RepositoryIDs) != 0 {
		t.Fatalf("principal=%#v err=%v", principal, err)
	}
	if _, err := store.pool.Exec(t.Context(), `delete from user_roles where user_id=$1`, userID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.pool.Exec(t.Context(), `delete from user_repository_grants where user_id=$1`, userID); err != nil {
		t.Fatal(err)
	}
	principal, err = store.SessionPrincipal(t.Context(), tokenHash, now.Add(time.Second), now.Add(30*time.Minute))
	if err != nil || principal.Administrator || len(principal.RepositoryIDs) != 0 {
		t.Fatalf("changed principal=%#v err=%v", principal, err)
	}
}

func TestFederatedBindingIsImmutableAcrossLinkClaimChanges(t *testing.T) {
	store := migratedStore(t)
	firstID := insertIdentityUser(t, store, "directory-first", "first")
	insertIdentityUser(t, store, "directory-second", "second")

	boundID, err := store.BindFederatedUser(t.Context(), "https://id.example.test", "subject-1", "directory-first")
	if err != nil || boundID != firstID {
		t.Fatalf("first binding userID=%d err=%v", boundID, err)
	}
	boundID, err = store.BindFederatedUser(t.Context(), "https://id.example.test", "subject-1", "directory-second")
	if err != nil || boundID != firstID {
		t.Fatalf("repeated binding userID=%d err=%v", boundID, err)
	}
	now := time.Now().UTC()
	tokenHash := [32]byte{42}
	if err := store.CreateFederatedSessionAudited(t.Context(), authn.Identity{
		Provider: authn.ProviderOIDC, Issuer: "https://id.example.test", Subject: "subject-1", LinkID: "directory-second",
	}, authn.SessionRecord{
		TokenHash: tokenHash, AuditID: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", Provider: authn.ProviderOIDC,
		CreatedAt: now, LastSeenAt: now, IdleExpiresAt: now.Add(time.Minute), ExpiresAt: now.Add(time.Hour),
	}, audit.OperationOIDCLoginSucceeded); err != nil {
		t.Fatal(err)
	}
	var sessionUserID int64
	if err := store.pool.QueryRow(t.Context(), `select user_id from auth_sessions where token_hash=$1`, tokenHash[:]).Scan(&sessionUserID); err != nil ||
		sessionUserID != firstID {
		t.Fatalf("session userID=%d err=%v", sessionUserID, err)
	}
}

func TestOAuthBindingPreservesIdentityAfterLoginRename(t *testing.T) {
	store := migratedStore(t)
	userID := insertIdentityUser(t, store, "github:https://github.com:123", "ada")
	identity := authn.Identity{Provider: authn.ProviderOAuth, Issuer: "https://github.com", Subject: "123", LinkID: "github:https://github.com:123"}
	if boundID, err := store.BindFederatedUser(t.Context(), identity.Issuer, identity.Subject, identity.LinkID); err != nil || boundID != userID {
		t.Fatalf("first binding userID=%d err=%v", boundID, err)
	}
	if _, err := store.pool.Exec(t.Context(), `update users set user_name='renamed' where id=$1`, userID); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	tokenHash := [32]byte{43}
	if err := store.CreateFederatedSessionAudited(t.Context(), identity, authn.SessionRecord{
		TokenHash: tokenHash, AuditID: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", Provider: authn.ProviderOAuth,
		CreatedAt: now, LastSeenAt: now, IdleExpiresAt: now.Add(time.Minute), ExpiresAt: now.Add(time.Hour),
	}, audit.OperationOAuthLoginSucceeded); err != nil {
		t.Fatal(err)
	}
	principal, err := store.SessionPrincipal(t.Context(), tokenHash, now, now.Add(time.Minute))
	if err != nil || principal.Subject != strconv.FormatInt(userID, 10) || principal.Method != authn.ProviderOAuth {
		t.Fatalf("principal=%#v err=%v", principal, err)
	}
	events, _, err := store.AuditEvents(t.Context(), 10)
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, event := range events {
		seen[event.Operation] = true
	}
	for _, operation := range []string{audit.OperationOAuthLoginSucceeded, audit.OperationSessionCreated} {
		if !seen[operation] {
			t.Fatalf("missing audit operation %q", operation)
		}
	}
}

func TestGitHubAccessSyncProvisionsUserAndMirrorsGrants(t *testing.T) {
	store := migratedStore(t)
	if _, err := store.pool.Exec(t.Context(), `insert into installations (github_id, account_login, account_type, status) values (1, 'acme', 'Organization', 'active')`); err != nil {
		t.Fatal(err)
	}
	for _, id := range []int64{101, 102, 103} {
		if _, err := store.pool.Exec(t.Context(), `insert into repositories (github_id, installation_id, owner, name, clone_url, web_url, default_branch, private, archived, enabled, status) values ($1, 1, 'acme', $2, '', '', 'main', false, false, true, 'ready')`, id, strconv.FormatInt(id, 10)); err != nil {
			t.Fatal(err)
		}
	}
	identity := authn.Identity{
		Provider: authn.ProviderOAuth, Issuer: "https://github.com", Subject: "777", LinkID: "github:https://github.com:777",
		DisplayName: "Ada", Login: "ada", AccessSync: &authn.AccessSync{RepositoryIDs: []int64{101, 102, 999}},
	}
	now := time.Now().UTC().Truncate(time.Microsecond)
	session := func(token byte) authn.SessionRecord {
		return authn.SessionRecord{
			TokenHash: [32]byte{token}, AuditID: strings.Repeat(string(rune('a'+token-50)), 32), Provider: authn.ProviderOAuth,
			CreatedAt: now, LastSeenAt: now, IdleExpiresAt: now.Add(time.Minute), ExpiresAt: now.Add(time.Hour),
		}
	}
	// An unprovisioned identity without access sync is still denied.
	if err := store.CreateFederatedSessionAudited(t.Context(), authn.Identity{Provider: identity.Provider, Issuer: identity.Issuer, Subject: identity.Subject, LinkID: identity.LinkID, DisplayName: identity.DisplayName}, session(50), audit.OperationOAuthLoginSucceeded); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("unprovisioned login error = %v", err)
	}
	if err := store.CreateFederatedSessionAudited(t.Context(), identity, session(51), audit.OperationOAuthLoginSucceeded); err != nil {
		t.Fatal(err)
	}
	principal, err := store.SessionPrincipal(t.Context(), [32]byte{51}, now, now.Add(time.Minute))
	if err != nil || principal.Administrator || !slices.Equal(principal.RepositoryIDs, []int64{101, 102}) {
		t.Fatalf("principal=%#v err=%v", principal, err)
	}
	var source, userName, externalID string
	if err := store.pool.QueryRow(t.Context(), `select source, user_name, external_id from users where id=$1`, principal.Subject).Scan(&source, &userName, &externalID); err != nil || source != "github" || userName != "ada" || externalID != identity.LinkID {
		t.Fatalf("user source=%q name=%q external=%q err=%v", source, userName, externalID, err)
	}
	user, err := store.AdminUser(t.Context(), mustInt64(t, principal.Subject))
	if err != nil || !slices.Equal(user.RepositoryIDs, []int64{101, 102}) || len(user.DirectRepositoryIDs) != 0 || !slices.Equal(user.GitHubRepositoryIDs, []int64{101, 102}) {
		t.Fatalf("admin user=%#v err=%v", user, err)
	}

	// The next login replaces GitHub-derived grants and renames the user, while direct grants survive.
	if _, err := store.pool.Exec(t.Context(), `insert into user_repository_grants (user_id, repository_id) values ($1, 103)`, mustInt64(t, principal.Subject)); err != nil {
		t.Fatal(err)
	}
	identity.Login, identity.AccessSync = "ada-renamed", &authn.AccessSync{RepositoryIDs: []int64{102}}
	if err := store.CreateFederatedSessionAudited(t.Context(), identity, session(52), audit.OperationOAuthLoginSucceeded); err != nil {
		t.Fatal(err)
	}
	principal, err = store.SessionPrincipal(t.Context(), [32]byte{52}, now, now.Add(time.Minute))
	if err != nil || !slices.Equal(principal.RepositoryIDs, []int64{102, 103}) {
		t.Fatalf("resynced principal=%#v err=%v", principal, err)
	}
	if err := store.pool.QueryRow(t.Context(), `select user_name from users where id=$1`, principal.Subject).Scan(&userName); err != nil || userName != "ada-renamed" {
		t.Fatalf("renamed user_name=%q err=%v", userName, err)
	}

	// A colliding login is not fatal: the login name stays unique through the GitHub ID suffix.
	other := authn.Identity{Provider: authn.ProviderOAuth, Issuer: "https://github.com", Subject: "778", LinkID: "github:https://github.com:778", Login: "ada-renamed", AccessSync: &authn.AccessSync{}}
	if err := store.CreateFederatedSessionAudited(t.Context(), other, session(53), audit.OperationOAuthLoginSucceeded); err != nil {
		t.Fatal(err)
	}
	if err := store.pool.QueryRow(t.Context(), `select user_name from users where external_id=$1`, other.LinkID).Scan(&userName); err != nil || userName != "ada-renamed-778" {
		t.Fatalf("colliding user_name=%q err=%v", userName, err)
	}

	// Suspended GitHub users stay denied even with a fresh sync.
	if _, err := store.pool.Exec(t.Context(), `update users set suspended_at=now() where external_id=$1`, identity.LinkID); err != nil {
		t.Fatal(err)
	}
	if err := store.CreateFederatedSessionAudited(t.Context(), identity, session(54), audit.OperationOAuthLoginSucceeded); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("suspended login error = %v", err)
	}
}

func mustInt64(t *testing.T, value string) int64 {
	t.Helper()
	parsed, err := strconv.ParseInt(value, 10, 64)
	if err != nil {
		t.Fatal(err)
	}
	return parsed
}

func TestOIDCBindingRejectsLocalExternalIDCollision(t *testing.T) {
	store := migratedStore(t)
	localID := seedSecurityUser(t, store, "recovery-admin", "local", true)

	if _, err := store.BindFederatedUser(t.Context(), "https://id.example.test", "subject-1", "recovery-admin"); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("local user %d binding error=%v", localID, err)
	}
	var identities int
	if err := store.pool.QueryRow(t.Context(), `select count(*) from user_identities where user_id=$1`, localID).Scan(&identities); err != nil || identities != 0 {
		t.Fatalf("local identities=%d err=%v", identities, err)
	}
}

func TestSessionPrincipalRejectsInvalidState(t *testing.T) {
	store := migratedStore(t)
	userID := insertIdentityUser(t, store, "directory-42", "ada")
	now := time.Now().UTC().Truncate(time.Microsecond)
	newSession := func(token byte) [32]byte {
		tokenHash := [32]byte{token}
		if err := store.CreateSession(t.Context(), authn.SessionRecord{TokenHash: tokenHash, UserID: userID, Provider: "oidc", CreatedAt: now, LastSeenAt: now, IdleExpiresAt: now.Add(time.Minute), ExpiresAt: now.Add(time.Hour)}); err != nil {
			t.Fatal(err)
		}
		return tokenHash
	}
	requireNoPrincipal := func(tokenHash [32]byte, at time.Time) {
		t.Helper()
		if _, err := store.SessionPrincipal(t.Context(), tokenHash, at, at.Add(time.Minute)); !errors.Is(err, pgx.ErrNoRows) {
			t.Fatalf("SessionPrincipal error = %v", err)
		}
	}

	t.Run("revoked", func(t *testing.T) {
		tokenHash := newSession(4)
		if err := store.RevokeSession(t.Context(), tokenHash); err != nil {
			t.Fatal(err)
		}
		requireNoPrincipal(tokenHash, now)
	})
	t.Run("expired", func(t *testing.T) {
		tokenHash := newSession(5)
		requireNoPrincipal(tokenHash, now.Add(2*time.Hour))
	})
	t.Run("idle expired", func(t *testing.T) {
		tokenHash := newSession(6)
		requireNoPrincipal(tokenHash, now.Add(2*time.Minute))
	})
	for _, state := range []struct {
		name, update string
	}{
		{"inactive", "scim_active=false"},
		{"suspended", "suspended_at=now()"},
		{"deleted", "deleted_at=now()"},
	} {
		t.Run(state.name, func(t *testing.T) {
			tokenHash := newSession(byte(len(state.name) + 10))
			if _, err := store.pool.Exec(t.Context(), "update users set "+state.update+" where id=$1", userID); err != nil {
				t.Fatal(err)
			}
			requireNoPrincipal(tokenHash, now)
			if _, err := store.pool.Exec(t.Context(), "update users set scim_active=true, suspended_at=null, deleted_at=null where id=$1", userID); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestLocalSessionRequiresLiveBreakGlassEligibility(t *testing.T) {
	store := migratedStore(t)
	userID := seedSecurityUser(t, store, "recovery-admin", "local", true)
	now := time.Now().UTC().Truncate(time.Microsecond)
	tokenHash := [32]byte{31}
	if err := store.CreateSession(t.Context(), authn.SessionRecord{
		TokenHash: tokenHash, UserID: userID, Provider: "local",
		CreatedAt: now, LastSeenAt: now, IdleExpiresAt: now.Add(time.Minute), ExpiresAt: now.Add(time.Hour),
	}); err != nil {
		t.Fatal(err)
	}
	if principal, err := store.SessionPrincipal(t.Context(), tokenHash, now, now.Add(time.Minute)); err != nil || !principal.Administrator || principal.Method != "local" {
		t.Fatalf("principal=%#v err=%v", principal, err)
	}
	if _, err := store.pool.Exec(t.Context(), `delete from user_roles where user_id=$1`, userID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.SessionPrincipal(t.Context(), tokenHash, now.Add(time.Second), now.Add(time.Minute)); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("role-removed local session error = %v", err)
	}
	if _, err := store.pool.Exec(t.Context(), `insert into user_roles (user_id,administrator) values ($1,true)`, userID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.pool.Exec(t.Context(), `update users set source='scim' where id=$1`, userID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.SessionPrincipal(t.Context(), tokenHash, now.Add(time.Second), now.Add(time.Minute)); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("source-changed local session error = %v", err)
	}
	if _, err := store.pool.Exec(t.Context(), `update users set source='local',scim_active=false where id=$1`, userID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.SessionPrincipal(t.Context(), tokenHash, now.Add(time.Second), now.Add(time.Minute)); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("inactive local session error = %v", err)
	}
}

func TestSessionPrincipalClampsIdleRenewalToAbsoluteExpiry(t *testing.T) {
	store := migratedStore(t)
	userID := insertIdentityUser(t, store, "directory-42", "ada")
	now := time.Now().UTC().Truncate(time.Microsecond)
	for index, test := range []struct {
		name        string
		idleExpires time.Time
		expires     time.Time
		at          time.Time
		idleUntil   time.Time
	}{
		{
			name: "near absolute expiry", idleExpires: now.Add(30 * time.Second), expires: now.Add(time.Minute),
			at: now.Add(15 * time.Second), idleUntil: now.Add(30 * time.Minute),
		},
		{
			name: "idle TTL equals absolute TTL", idleExpires: now.Add(time.Hour), expires: now.Add(time.Hour),
			at: now.Add(time.Minute), idleUntil: now.Add(61 * time.Minute),
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			tokenHash := [32]byte{byte(index + 20)}
			if err := store.CreateSession(t.Context(), authn.SessionRecord{
				TokenHash: tokenHash, UserID: userID, Provider: "oidc",
				CreatedAt: now, LastSeenAt: now, IdleExpiresAt: test.idleExpires, ExpiresAt: test.expires,
			}); err != nil {
				t.Fatal(err)
			}
			if _, err := store.SessionPrincipal(t.Context(), tokenHash, test.at, test.idleUntil); err != nil {
				t.Fatal(err)
			}
			var idleExpires time.Time
			if err := store.pool.QueryRow(t.Context(), `select idle_expires_at from auth_sessions where token_hash=$1`, tokenHash[:]).Scan(&idleExpires); err != nil {
				t.Fatal(err)
			}
			if !idleExpires.Equal(test.expires) {
				t.Fatalf("idle expiry=%v want absolute expiry=%v", idleExpires, test.expires)
			}
		})
	}
}

func TestDeleteExpiredAuthRemovesIdleExpiredSessions(t *testing.T) {
	store := migratedStore(t)
	userID := insertIdentityUser(t, store, "directory-42", "ada")
	now := time.Now().UTC().Truncate(time.Microsecond)
	if err := store.CreateLoginFlow(t.Context(), authn.LoginFlow{
		StateHash: [32]byte{1}, BrowserHash: [32]byte{2}, Provider: "oidc",
		Nonce: "nonce", CodeVerifier: "verifier", ReturnTo: "/",
		CreatedAt: now.Add(-time.Minute), ExpiresAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	for _, session := range []authn.SessionRecord{
		{TokenHash: [32]byte{3}, UserID: userID, Provider: "oidc", CreatedAt: now.Add(-2 * time.Hour), LastSeenAt: now.Add(-time.Hour), IdleExpiresAt: now, ExpiresAt: now.Add(time.Hour)},
		{TokenHash: [32]byte{4}, UserID: userID, Provider: "oidc", CreatedAt: now.Add(-2 * time.Hour), LastSeenAt: now.Add(-time.Hour), IdleExpiresAt: now.Add(-time.Minute), ExpiresAt: now},
		{TokenHash: [32]byte{5}, UserID: userID, Provider: "oidc", CreatedAt: now.Add(-time.Hour), LastSeenAt: now.Add(-time.Minute), IdleExpiresAt: now.Add(time.Minute), ExpiresAt: now.Add(time.Hour)},
		{TokenHash: [32]byte{6}, UserID: userID, Provider: "oidc", CreatedAt: now.Add(-time.Hour), LastSeenAt: now.Add(-time.Minute), IdleExpiresAt: now.Add(time.Minute), ExpiresAt: now.Add(time.Hour)},
	} {
		if err := store.CreateSession(t.Context(), session); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.RevokeSession(t.Context(), [32]byte{5}); err != nil {
		t.Fatal(err)
	}

	flows, sessions, err := store.DeleteExpiredAuth(t.Context(), now)
	if err != nil || flows != 1 || sessions != 3 {
		t.Fatalf("flows=%d sessions=%d err=%v", flows, sessions, err)
	}
	var remaining int
	if err := store.pool.QueryRow(t.Context(), `select count(*) from auth_sessions`).Scan(&remaining); err != nil || remaining != 1 {
		t.Fatalf("remaining=%d err=%v", remaining, err)
	}
}

func insertIdentityUser(t *testing.T, store *Store, externalID, userName string) int64 {
	t.Helper()
	var userID int64
	if err := store.pool.QueryRow(t.Context(), `insert into users (external_id, user_name, source) values ($1, $2, 'scim') returning id`, externalID, userName).Scan(&userID); err != nil {
		t.Fatal(err)
	}
	return userID
}
