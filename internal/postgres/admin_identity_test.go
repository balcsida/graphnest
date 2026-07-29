//go:build integration

package postgres

import (
	"errors"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/grepnest/grepnest/internal/admin"
	"github.com/grepnest/grepnest/internal/authn"
	"github.com/jackc/pgx/v5"
)

func TestAdminIdentityListsEffectiveUserAndGroupAccess(t *testing.T) {
	store := migratedStore(t)
	userID := insertIdentityUser(t, store, "directory-1", "ada")
	otherID := insertIdentityUser(t, store, "directory-2", "grace")
	seedReadyRepository(t, store, 101, testSHA('a'))
	seedReadyRepository(t, store, 102, testSHA('b'))
	seedReadyRepository(t, store, 103, testSHA('c'))
	groupID := insertIdentityGroup(t, store, "engineering", "Engineering")
	for _, statement := range []struct {
		sql  string
		args []any
	}{
		{`insert into user_repository_grants (user_id, repository_id) values ($1, 101), ($1, 103)`, []any{userID}},
		{`insert into group_memberships (group_id, user_id) values ($1, $2), ($1, $3)`, []any{groupID, userID, otherID}},
		{`insert into group_roles (group_id, administrator) values ($1, true)`, []any{groupID}},
		{`insert into group_repository_grants (group_id, repository_id) values ($1, 102)`, []any{groupID}},
		{`update repositories set enabled=false where github_id=103`, nil},
	} {
		if _, err := store.pool.Exec(t.Context(), statement.sql, statement.args...); err != nil {
			t.Fatal(err)
		}
	}

	users, truncated, err := store.AdminUsers(t.Context(), 1)
	if err != nil || !truncated || len(users) != 1 || users[0].ID != userID ||
		!users[0].Administrator || !reflect.DeepEqual(users[0].RepositoryIDs, []int64{101, 102}) {
		t.Fatalf("users=%#v truncated=%v err=%v", users, truncated, err)
	}
	user, err := store.AdminUser(t.Context(), userID)
	if err != nil || user.ExternalID != "directory-1" || user.UserName != "ada" ||
		user.Source != "scim" || !user.SCIMActive || user.Suspended ||
		!user.Administrator || !reflect.DeepEqual(user.RepositoryIDs, []int64{101, 102}) ||
		user.DirectAdministrator || !reflect.DeepEqual(user.DirectRepositoryIDs, []int64{101, 103}) {
		t.Fatalf("user=%#v err=%v", user, err)
	}
	groups, truncated, err := store.AdminGroups(t.Context(), 1)
	if err != nil || truncated || len(groups) != 1 || groups[0].ID != groupID ||
		!groups[0].Administrator || groups[0].MemberCount != 2 ||
		!reflect.DeepEqual(groups[0].RepositoryIDs, []int64{102}) {
		t.Fatalf("groups=%#v truncated=%v err=%v", groups, truncated, err)
	}
	group, err := store.AdminGroup(t.Context(), groupID)
	if err != nil || !reflect.DeepEqual(group, groups[0]) {
		t.Fatalf("group=%#v want=%#v err=%v", group, groups[0], err)
	}
}

func TestAdminIdentityRoundTripsGroupOnlyAccessWithoutDirectGrants(t *testing.T) {
	store := migratedStore(t)
	userID := insertIdentityUser(t, store, "directory-1", "ada")
	seedReadyRepository(t, store, 101, testSHA('a'))
	groupID := insertIdentityGroup(t, store, "engineering", "Engineering")
	if _, err := store.pool.Exec(t.Context(), `insert into group_memberships (group_id, user_id) values ($1, $2)`, groupID, userID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.pool.Exec(t.Context(), `insert into group_roles (group_id, administrator) values ($1, true)`, groupID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.pool.Exec(t.Context(), `insert into group_repository_grants (group_id, repository_id) values ($1, 101)`, groupID); err != nil {
		t.Fatal(err)
	}

	user, err := store.AdminUser(t.Context(), userID)
	if err != nil || !user.Administrator || !reflect.DeepEqual(user.RepositoryIDs, []int64{101}) ||
		user.DirectAdministrator || len(user.DirectRepositoryIDs) != 0 {
		t.Fatalf("group-only user=%#v err=%v", user, err)
	}
	if err := store.ReplaceAdminUserAccess(t.Context(), userID, userID, user.DirectAdministrator, user.DirectRepositoryIDs); err != nil {
		t.Fatal(err)
	}
	var roles, grants int
	if err := store.pool.QueryRow(t.Context(), `select count(*) from user_roles where user_id=$1`, userID).Scan(&roles); err != nil {
		t.Fatal(err)
	}
	if err := store.pool.QueryRow(t.Context(), `select count(*) from user_repository_grants where user_id=$1`, userID).Scan(&grants); err != nil {
		t.Fatal(err)
	}
	if roles != 0 || grants != 0 {
		t.Fatalf("round trip created direct roles=%d grants=%d", roles, grants)
	}
	if _, err := store.pool.Exec(t.Context(), `delete from group_memberships where group_id=$1 and user_id=$2`, groupID, userID); err != nil {
		t.Fatal(err)
	}
	user, err = store.AdminUser(t.Context(), userID)
	if err != nil || user.Administrator || len(user.RepositoryIDs) != 0 ||
		user.DirectAdministrator || len(user.DirectRepositoryIDs) != 0 {
		t.Fatalf("user after membership removal=%#v err=%v", user, err)
	}
}

func TestAdminIdentityGroupAdministratorEditsOwnDirectExceptions(t *testing.T) {
	store := migratedStore(t)
	userID := insertIdentityUser(t, store, "directory-1", "ada")
	seedReadyRepository(t, store, 101, testSHA('a'))
	groupID := insertIdentityGroup(t, store, "engineering", "Engineering")
	if _, err := store.pool.Exec(t.Context(), `insert into group_memberships (group_id, user_id) values ($1, $2)`, groupID, userID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.pool.Exec(t.Context(), `insert into group_roles (group_id, administrator) values ($1, true)`, groupID); err != nil {
		t.Fatal(err)
	}

	if err := store.ReplaceAdminUserAccess(t.Context(), userID, userID, false, []int64{101}); err != nil {
		t.Fatal(err)
	}
	user, err := store.AdminUser(t.Context(), userID)
	if err != nil || !user.Administrator || user.DirectAdministrator ||
		!reflect.DeepEqual(user.RepositoryIDs, []int64{101}) ||
		!reflect.DeepEqual(user.DirectRepositoryIDs, []int64{101}) {
		t.Fatalf("user=%#v err=%v", user, err)
	}
}

func TestAdminIdentityRejectsTrueFinalDirectAdministratorRemoval(t *testing.T) {
	store := migratedStore(t)
	userID := insertIdentityUser(t, store, "directory-1", "ada")
	if _, err := store.pool.Exec(t.Context(), `insert into user_roles (user_id, administrator) values ($1, true)`, userID); err != nil {
		t.Fatal(err)
	}
	if err := store.ReplaceAdminUserAccess(t.Context(), userID, userID, false, nil); !errors.Is(err, admin.ErrFinalAdministrator) {
		t.Fatalf("final administrator removal error=%v", err)
	}
}

func TestAdminIdentityRoundTripsExistingDisabledRepositoryGrants(t *testing.T) {
	store := migratedStore(t)
	actorID := insertIdentityUser(t, store, "directory-1", "ada")
	userID := insertIdentityUser(t, store, "directory-2", "grace")
	groupID := insertIdentityGroup(t, store, "engineering", "Engineering")
	seedReadyRepository(t, store, 101, testSHA('a'))
	seedReadyRepository(t, store, 102, testSHA('b'))
	if _, err := store.pool.Exec(t.Context(), `insert into user_roles (user_id, administrator) values ($1, true)`, actorID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.pool.Exec(t.Context(), `insert into group_memberships (group_id, user_id) values ($1, $2)`, groupID, userID); err != nil {
		t.Fatal(err)
	}

	if err := store.ReplaceAdminUserAccess(t.Context(), actorID, userID, true, []int64{102, 101}); err != nil {
		t.Fatal(err)
	}
	user, err := store.AdminUser(t.Context(), userID)
	if err != nil || !user.Administrator || !reflect.DeepEqual(user.RepositoryIDs, []int64{101, 102}) {
		t.Fatalf("user=%#v err=%v", user, err)
	}
	if err := store.ReplaceAdminGroupAccess(t.Context(), actorID, groupID, true, []int64{101}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.pool.Exec(t.Context(), `update repositories set enabled=false where github_id=101`); err != nil {
		t.Fatal(err)
	}
	group, err := store.AdminGroup(t.Context(), groupID)
	if err != nil || !group.Administrator || !reflect.DeepEqual(group.RepositoryIDs, []int64{101}) {
		t.Fatalf("group=%#v err=%v", group, err)
	}
	if err := store.ReplaceAdminGroupAccess(t.Context(), actorID, groupID, group.Administrator, group.RepositoryIDs); err != nil {
		t.Fatalf("round-trip disabled group grant: %v", err)
	}
	if err := store.ReplaceAdminUserAccess(t.Context(), actorID, userID, false, []int64{101}); err != nil {
		t.Fatalf("round-trip disabled direct grant: %v", err)
	}
	user, err = store.AdminUser(t.Context(), userID)
	if err != nil || len(user.RepositoryIDs) != 0 ||
		!reflect.DeepEqual(user.DirectRepositoryIDs, []int64{101}) {
		t.Fatalf("user=%#v err=%v", user, err)
	}

	if err := store.ReplaceAdminUserAccess(t.Context(), actorID, userID, false, []int64{101, 999}); !errors.Is(err, admin.ErrInvalid) {
		t.Fatalf("missing repository error=%v", err)
	}
	user, err = store.AdminUser(t.Context(), userID)
	if err != nil || !user.Administrator || len(user.RepositoryIDs) != 0 ||
		!reflect.DeepEqual(user.DirectRepositoryIDs, []int64{101}) {
		t.Fatalf("failed replacement changed user=%#v err=%v", user, err)
	}
	var externalID, userName string
	var memberships int
	if err := store.pool.QueryRow(t.Context(), `select external_id, user_name from users where id=$1`, userID).Scan(&externalID, &userName); err != nil {
		t.Fatal(err)
	}
	if err := store.pool.QueryRow(t.Context(), `select count(*) from group_memberships where user_id=$1`, userID).Scan(&memberships); err != nil {
		t.Fatal(err)
	}
	if externalID != "directory-2" || userName != "grace" || memberships != 1 {
		t.Fatalf("SCIM fields external=%q name=%q memberships=%d", externalID, userName, memberships)
	}
}

func TestAdminIdentitySuspendsAndRevokesAllCredentials(t *testing.T) {
	store := migratedStore(t)
	actorID := insertIdentityUser(t, store, "directory-1", "ada")
	userID := insertIdentityUser(t, store, "directory-2", "grace")
	if _, err := store.pool.Exec(t.Context(), `insert into user_roles (user_id, administrator) values ($1, true), ($2, true)`, actorID, userID); err != nil {
		t.Fatal(err)
	}
	createIdentityCredentials(t, store, userID)

	if err := store.SuspendAdminUser(t.Context(), actorID, userID, true); err != nil {
		t.Fatal(err)
	}
	assertIdentityCredentialsRevoked(t, store, userID)
	user, err := store.AdminUser(t.Context(), userID)
	if err != nil || !user.Suspended {
		t.Fatalf("user=%#v err=%v", user, err)
	}
	if err := store.SuspendAdminUser(t.Context(), actorID, userID, false); err != nil {
		t.Fatal(err)
	}
	createIdentityCredentials(t, store, userID)
	if err := store.RevokeAdminUserCredentials(t.Context(), userID); err != nil {
		t.Fatal(err)
	}
	assertIdentityCredentialsRevoked(t, store, userID)
}

func TestAdminIdentityProtectsSelfAndFinalEffectiveAdministrator(t *testing.T) {
	store := migratedStore(t)
	actorID := insertIdentityUser(t, store, "directory-1", "ada")
	otherID := insertIdentityUser(t, store, "directory-2", "grace")
	groupID := insertIdentityGroup(t, store, "engineering", "Engineering")
	if _, err := store.pool.Exec(t.Context(), `insert into group_memberships (group_id, user_id) values ($1, $2)`, groupID, actorID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.pool.Exec(t.Context(), `insert into group_roles (group_id, administrator) values ($1, true)`, groupID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.pool.Exec(t.Context(), `insert into user_roles (user_id, administrator) values ($1, true)`, otherID); err != nil {
		t.Fatal(err)
	}
	if err := store.ReplaceAdminGroupAccess(t.Context(), actorID, groupID, false, nil); !errors.Is(err, admin.ErrSelfAdministration) {
		t.Fatalf("self group removal error=%v", err)
	}
	if err := store.SuspendAdminUser(t.Context(), 0, otherID, true); err != nil {
		t.Fatal(err)
	}
	if err := store.ReplaceAdminGroupAccess(t.Context(), 0, groupID, false, nil); !errors.Is(err, admin.ErrFinalAdministrator) {
		t.Fatalf("final group removal error=%v", err)
	}
}

func TestAdminIdentityReportsNoEffectiveAccessForInactiveUser(t *testing.T) {
	store := migratedStore(t)
	userID := insertIdentityUser(t, store, "directory-1", "ada")
	seedReadyRepository(t, store, 101, testSHA('a'))
	if _, err := store.pool.Exec(t.Context(), `insert into user_roles (user_id, administrator) values ($1, true)`, userID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.pool.Exec(t.Context(), `insert into user_repository_grants (user_id, repository_id) values ($1, 101)`, userID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.pool.Exec(t.Context(), `update users set suspended_at=now() where id=$1`, userID); err != nil {
		t.Fatal(err)
	}
	user, err := store.AdminUser(t.Context(), userID)
	if err != nil || user.Administrator || len(user.RepositoryIDs) != 0 {
		t.Fatalf("inactive user=%#v err=%v", user, err)
	}
}

func TestAdminIdentitySerializesFinalAdministratorProtection(t *testing.T) {
	store := migratedStore(t)
	firstID := insertIdentityUser(t, store, "directory-1", "ada")
	secondID := insertIdentityUser(t, store, "directory-2", "grace")
	if _, err := store.pool.Exec(t.Context(), `insert into user_roles (user_id, administrator) values ($1, true), ($2, true)`, firstID, secondID); err != nil {
		t.Fatal(err)
	}

	start := make(chan struct{})
	results := make(chan error, 2)
	var group sync.WaitGroup
	for _, id := range []int64{firstID, secondID} {
		group.Add(1)
		go func() {
			defer group.Done()
			<-start
			results <- store.SuspendAdminUser(t.Context(), 0, id, true)
		}()
	}
	close(start)
	group.Wait()
	close(results)
	var succeeded, protected int
	for err := range results {
		switch {
		case err == nil:
			succeeded++
		case errors.Is(err, admin.ErrFinalAdministrator):
			protected++
		default:
			t.Fatalf("unexpected error=%v", err)
		}
	}
	if succeeded != 1 || protected != 1 {
		t.Fatalf("succeeded=%d protected=%d", succeeded, protected)
	}
	var activeAdmins int
	if err := store.pool.QueryRow(t.Context(), `select count(*) from users where suspended_at is null and exists (select 1 from user_roles where user_id=users.id)`).Scan(&activeAdmins); err != nil || activeAdmins != 1 {
		t.Fatalf("active administrators=%d err=%v", activeAdmins, err)
	}
}

func insertIdentityGroup(t *testing.T, store *Store, externalID, displayName string) int64 {
	t.Helper()
	var id int64
	if err := store.pool.QueryRow(t.Context(), `insert into groups (external_id, display_name) values ($1, $2) returning id`, externalID, displayName).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

func createIdentityCredentials(t *testing.T, store *Store, userID int64) {
	t.Helper()
	now := time.Now().UTC().Truncate(time.Microsecond)
	var sessions, tokens int
	if err := store.pool.QueryRow(t.Context(), `select count(*) from auth_sessions where user_id=$1`, userID).Scan(&sessions); err != nil {
		t.Fatal(err)
	}
	if err := store.pool.QueryRow(t.Context(), `select count(*) from api_tokens where user_id=$1`, userID).Scan(&tokens); err != nil {
		t.Fatal(err)
	}
	if err := store.CreateSession(t.Context(), authn.SessionRecord{
		TokenHash: [32]byte{byte(sessions + 1)}, UserID: userID, Provider: "oidc",
		CreatedAt: now, LastSeenAt: now, IdleExpiresAt: now.Add(time.Minute), ExpiresAt: now.Add(time.Hour),
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateAPIToken(t.Context(), authn.APITokenRecord{
		TokenHash: [32]byte{byte(tokens + 101)}, Prefix: "gn_test", UserID: userID, CreatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
}

func assertIdentityCredentialsRevoked(t *testing.T, store *Store, userID int64) {
	t.Helper()
	var activeSessions, activeTokens int
	if err := store.pool.QueryRow(t.Context(), `select count(*) from auth_sessions where user_id=$1 and revoked_at is null`, userID).Scan(&activeSessions); err != nil {
		t.Fatal(err)
	}
	if err := store.pool.QueryRow(t.Context(), `select count(*) from api_tokens where user_id=$1 and revoked_at is null`, userID).Scan(&activeTokens); err != nil {
		t.Fatal(err)
	}
	if activeSessions != 0 || activeTokens != 0 {
		t.Fatalf("active sessions=%d tokens=%d", activeSessions, activeTokens)
	}
}

func TestAdminIdentityMissingResourcesReturnNoRows(t *testing.T) {
	store := migratedStore(t)
	if _, err := store.AdminUser(t.Context(), 999); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("user error=%v", err)
	}
	if _, err := store.AdminGroup(t.Context(), 999); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("group error=%v", err)
	}
}
