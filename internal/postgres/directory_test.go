//go:build integration

package postgres

import (
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/grepnest/grepnest/internal/authn"
	"github.com/grepnest/grepnest/internal/repository"
	"github.com/jackc/pgx/v5"
)

func TestDirectoryPrincipalUnionsActiveGrantsAndRoles(t *testing.T) {
	store := migratedStore(t)
	userID := insertIdentityUser(t, store, "directory-1", "ada")
	for _, id := range []int64{101, 102, 103} {
		seedReadyRepository(t, store, id, testSHA(byte('a'+id-101)))
	}
	if _, err := store.pool.Exec(t.Context(), `insert into user_repository_grants (user_id, repository_id) values ($1, 101)`, userID); err != nil {
		t.Fatal(err)
	}
	var activeGroup, deletedGroup int64
	if err := store.pool.QueryRow(t.Context(), `insert into groups (external_id, display_name) values ('eng', 'Engineering') returning id`).Scan(&activeGroup); err != nil {
		t.Fatal(err)
	}
	if err := store.pool.QueryRow(t.Context(), `insert into groups (external_id, display_name, deleted_at) values ('old', 'Old', now()) returning id`).Scan(&deletedGroup); err != nil {
		t.Fatal(err)
	}
	for _, groupID := range []int64{activeGroup, activeGroup, deletedGroup} {
		if _, err := store.pool.Exec(t.Context(), `insert into group_memberships (group_id, user_id) values ($1, $2) on conflict do nothing`, groupID, userID); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := store.pool.Exec(t.Context(), `insert into group_repository_grants (group_id, repository_id) values ($1, 102), ($2, 103)`, activeGroup, deletedGroup); err != nil {
		t.Fatal(err)
	}
	if _, err := store.pool.Exec(t.Context(), `insert into group_roles (group_id, administrator) values ($1, true)`, activeGroup); err != nil {
		t.Fatal(err)
	}
	principal, err := store.UserPrincipal(t.Context(), userID, nil)
	if err != nil || !principal.Administrator || len(principal.RepositoryIDs) != 0 {
		t.Fatalf("principal=%#v err=%v", principal, err)
	}
	now := time.Now().UTC().Truncate(time.Microsecond)
	if err := store.CreateSession(t.Context(), authn.SessionRecord{TokenHash: [32]byte{1}, UserID: userID, Provider: "oidc", CreatedAt: now, LastSeenAt: now, IdleExpiresAt: now.Add(time.Minute), ExpiresAt: now.Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	sessionPrincipal, err := store.SessionPrincipal(t.Context(), [32]byte{1}, now, now.Add(time.Minute))
	if err != nil || !reflect.DeepEqual(sessionPrincipal, principal) {
		t.Fatalf("session principal=%#v principal=%#v err=%v", sessionPrincipal, principal, err)
	}
	if _, err := store.pool.Exec(t.Context(), `insert into user_roles (user_id, administrator) values ($1, true)`, userID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.pool.Exec(t.Context(), `update groups set deleted_at=now() where id=$1`, activeGroup); err != nil {
		t.Fatal(err)
	}
	principal, err = store.UserPrincipal(t.Context(), userID, nil)
	if err != nil || !principal.Administrator || len(principal.RepositoryIDs) != 0 {
		t.Fatalf("principal after group deletion=%#v err=%v", principal, err)
	}
	var memberships int
	if err := store.pool.QueryRow(t.Context(), `select count(*) from group_memberships where group_id=$1 and user_id=$2`, activeGroup, userID).Scan(&memberships); err != nil || memberships != 1 {
		t.Fatalf("memberships=%d err=%v", memberships, err)
	}
}

func TestAPIPrincipalIntersectsTokenCeilingAndRejectsInvalidTokens(t *testing.T) {
	store := migratedStore(t)
	userID := insertIdentityUser(t, store, "directory-2", "grace")
	for _, id := range []int64{101, 102, 103} {
		seedReadyRepository(t, store, id, testSHA(byte('a'+id-101)))
	}
	if _, err := store.pool.Exec(t.Context(), `insert into user_repository_grants (user_id, repository_id) values ($1, 101), ($1, 102)`, userID); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Microsecond)
	create := func(hash byte, ceiling []int64, expires *time.Time) int64 {
		t.Helper()
		id, err := store.CreateAPIToken(t.Context(), authn.APITokenRecord{TokenHash: [32]byte{hash}, Prefix: "gn_test", UserID: userID, RepositoryIDs: ceiling, CreatedAt: now, ExpiresAt: expires})
		if err != nil {
			t.Fatal(err)
		}
		return id
	}
	tokenID := create(1, []int64{102, 103}, nil)
	principal, err := store.APIPrincipal(t.Context(), [32]byte{1}, now)
	if err != nil || principal.Method != "api_token" || !reflect.DeepEqual(principal.RepositoryIDs, []int64{102}) {
		t.Fatalf("principal=%#v err=%v", principal, err)
	}
	var lastUsed time.Time
	if err := store.pool.QueryRow(t.Context(), `select last_used_at from api_tokens where id=$1`, tokenID).Scan(&lastUsed); err != nil || lastUsed.Before(now) {
		t.Fatalf("last used=%v err=%v", lastUsed, err)
	}
	if err := store.RevokeAPIToken(t.Context(), userID, tokenID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.APIPrincipal(t.Context(), [32]byte{1}, now); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("revoked token error=%v", err)
	}
	var revokedLastUsed time.Time
	if err := store.pool.QueryRow(t.Context(), `select last_used_at from api_tokens where id=$1`, tokenID).Scan(&revokedLastUsed); err != nil || !revokedLastUsed.Equal(lastUsed) {
		t.Fatalf("revoked last used=%v want %v err=%v", revokedLastUsed, lastUsed, err)
	}
	past := now.Add(-time.Second)
	create(2, nil, &past)
	if _, err := store.APIPrincipal(t.Context(), [32]byte{2}, now); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("expired token error=%v", err)
	}
	create(3, nil, nil)
	if _, err := store.pool.Exec(t.Context(), `update users set suspended_at=now() where id=$1`, userID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.APIPrincipal(t.Context(), [32]byte{3}, now); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("suspended token error=%v", err)
	}
}

func TestAdministratorAPITokenStaysRepositoryScoped(t *testing.T) {
	store := migratedStore(t)
	userID := insertIdentityUser(t, store, "directory-3", "lin")
	for _, id := range []int64{101, 102} {
		seedReadyRepository(t, store, id, testSHA(byte('a'+id-101)))
	}
	if _, err := store.pool.Exec(t.Context(), `insert into user_roles (user_id, administrator) values ($1, true)`, userID); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Microsecond)
	if _, err := store.CreateAPIToken(t.Context(), authn.APITokenRecord{TokenHash: [32]byte{9}, Prefix: "gn_test", UserID: userID, RepositoryIDs: []int64{102}, CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	principal, err := store.APIPrincipal(t.Context(), [32]byte{9}, now)
	if err != nil || !principal.Administrator {
		t.Fatalf("principal=%#v err=%v", principal, err)
	}
	service := repository.Service{Store: store}
	if _, err := service.Status(t.Context(), principal, 101); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("token accessed repo 101: %v", err)
	}
	if _, err := service.Status(t.Context(), principal, 102); !errors.Is(err, repository.ErrSearchNodeUnavailable) {
		t.Fatalf("token repo 102 error=%v", err)
	}
}

func TestOIDCAdministratorIsGlobalWhileAPITokenKeepsCeiling(t *testing.T) {
	store := migratedStore(t)
	userID := insertIdentityUser(t, store, "directory-5", "sam")
	for _, id := range []int64{101, 102} {
		seedReadyRepository(t, store, id, testSHA(byte('a'+id-101)))
	}
	if _, err := store.pool.Exec(t.Context(), `insert into user_roles (user_id, administrator) values ($1, true)`, userID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.pool.Exec(t.Context(), `insert into user_repository_grants (user_id, repository_id) values ($1, 101)`, userID); err != nil {
		t.Fatal(err)
	}
	principal, err := store.UserPrincipal(t.Context(), userID, nil)
	if err != nil || !principal.Administrator || len(principal.RepositoryIDs) != 0 {
		t.Fatalf("OIDC principal=%#v err=%v", principal, err)
	}

	now := time.Now().UTC().Truncate(time.Microsecond)
	if _, err := store.CreateAPIToken(t.Context(), authn.APITokenRecord{
		TokenHash: [32]byte{10}, Prefix: "gn_test", UserID: userID,
		RepositoryIDs: []int64{102}, CreatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	tokenPrincipal, err := store.APIPrincipal(t.Context(), [32]byte{10}, now)
	if err != nil || !tokenPrincipal.Administrator || tokenPrincipal.Method != "api_token" ||
		!reflect.DeepEqual(tokenPrincipal.RepositoryIDs, []int64{102}) {
		t.Fatalf("API principal=%#v err=%v", tokenPrincipal, err)
	}
	if _, err := store.CreateAPIToken(t.Context(), authn.APITokenRecord{
		TokenHash: [32]byte{11}, Prefix: "gn_test", UserID: userID, CreatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.APIPrincipal(t.Context(), [32]byte{11}, now); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("administrator token without ceiling error=%v", err)
	}
}

func TestAPIPrincipalDistinguishesEmptyTokenCeiling(t *testing.T) {
	store := migratedStore(t)
	userID := insertIdentityUser(t, store, "directory-4", "kai")
	seedReadyRepository(t, store, 101, testSHA('a'))
	if _, err := store.pool.Exec(t.Context(), `insert into user_repository_grants (user_id, repository_id) values ($1, 101)`, userID); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Microsecond)
	if _, err := store.CreateAPIToken(t.Context(), authn.APITokenRecord{TokenHash: [32]byte{4}, Prefix: "gn_test", UserID: userID, CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateAPIToken(t.Context(), authn.APITokenRecord{TokenHash: [32]byte{5}, Prefix: "gn_test", UserID: userID, RepositoryIDs: []int64{}, CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	unrestricted, err := store.APIPrincipal(t.Context(), [32]byte{4}, now)
	if err != nil || !reflect.DeepEqual(unrestricted.RepositoryIDs, []int64{101}) {
		t.Fatalf("unrestricted=%#v err=%v", unrestricted, err)
	}
	empty, err := store.APIPrincipal(t.Context(), [32]byte{5}, now)
	if err != nil || len(empty.RepositoryIDs) != 0 {
		t.Fatalf("empty=%#v err=%v", empty, err)
	}
}
