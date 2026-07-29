//go:build integration

package postgres

import (
	"errors"
	"strconv"
	"testing"
	"time"

	"github.com/grepnest/grepnest/internal/authn"
	"github.com/jackc/pgx/v5"
)

func TestAuthStoreBindsUsersAndResolvesLivePrincipal(t *testing.T) {
	store := migratedStore(t)
	userID := insertIdentityUser(t, store, "directory-42", "ada")
	if _, err := store.BindOIDCUser(t.Context(), "https://id.example.test", "subject-1", "directory-42"); err != nil {
		t.Fatal(err)
	}
	otherID := insertIdentityUser(t, store, "directory-43", "grace")
	if _, err := store.BindOIDCUser(t.Context(), "https://id.example.test", "subject-1", "directory-43"); err == nil {
		t.Fatal("second user bound the same identity")
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
	if err != nil || principal.Subject != strconv.FormatInt(userID, 10) || principal.Method != "oidc" || !principal.Administrator || len(principal.RepositoryIDs) != 1 || principal.RepositoryIDs[0] != 101 {
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

func insertIdentityUser(t *testing.T, store *Store, externalID, userName string) int64 {
	t.Helper()
	var userID int64
	if err := store.pool.QueryRow(t.Context(), `insert into users (external_id, user_name, source) values ($1, $2, 'scim') returning id`, externalID, userName).Scan(&userID); err != nil {
		t.Fatal(err)
	}
	return userID
}
