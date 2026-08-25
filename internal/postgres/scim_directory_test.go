//go:build integration

package postgres

import (
	"errors"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/balcsida/graphnest/internal/scim"
	"github.com/jackc/pgx/v5"
)

func TestSCIMUserLifecycle(t *testing.T) {
	store := migratedStore(t)
	active := true
	input := scim.User{
		ExternalID: "directory-42", UserName: "ada", DisplayName: "Ada",
		Active: &active, Name: scim.Name{GivenName: "Ada", FamilyName: "Lovelace"},
		Emails: []scim.Email{{Value: "ada@example.com", Primary: true}},
	}
	created, err := store.CreateUser(t.Context(), input)
	if err != nil {
		t.Fatal(err)
	}
	id := scimID(t, created.ID)
	if created.ID == "" || created.Meta.Created == "" || created.Meta.LastModified == "" {
		t.Fatalf("created=%#v", created)
	}
	got, err := store.User(t.Context(), id)
	if err != nil || got.UserName != "ada" || got.Name.FamilyName != "Lovelace" ||
		len(got.Emails) != 1 || got.Active == nil || !*got.Active {
		t.Fatalf("user=%#v err=%v", got, err)
	}
	before := got.Meta.LastModified
	time.Sleep(time.Millisecond)
	got, err = store.ReplaceUser(t.Context(), id, got)
	if err != nil || got.Meta.LastModified != before {
		t.Fatalf("identical replace=%#v err=%v", got, err)
	}
	createIdentityCredentials(t, store, id)
	inactive := false
	got, err = store.PatchUser(t.Context(), id, scim.UserMutation{
		Active: scim.Optional[bool]{Set: true, Value: inactive},
	})
	if err != nil || got.Active == nil || *got.Active {
		t.Fatalf("deactivated=%#v err=%v", got, err)
	}
	assertIdentityCredentialsRevoked(t, store, id)
	users, total, err := store.ListUsers(t.Context(), scim.Filter{Attribute: "externalId", Value: "directory-42"}, scim.Page{StartIndex: 1, Count: 10})
	if err != nil || total != 1 || len(users) != 1 || users[0].ID != created.ID {
		t.Fatalf("users=%#v total=%d err=%v", users, total, err)
	}
	if err := store.DeleteUser(t.Context(), id); err != nil {
		t.Fatal(err)
	}
	if _, err := store.User(t.Context(), id); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("deleted user err=%v", err)
	}
	users, total, err = store.ListUsers(t.Context(), scim.Filter{}, scim.Page{StartIndex: 1, Count: 10})
	if err != nil || total != 0 || len(users) != 0 {
		t.Fatalf("tombstone users=%#v total=%d err=%v", users, total, err)
	}
}

func TestSCIMCannotReadMutateOrAddLocalRecoveryUsers(t *testing.T) {
	store := migratedStore(t)
	localID := seedSecurityUser(t, store, "recovery-admin", "local", true)
	group, err := store.CreateGroup(t.Context(), scim.Group{ExternalID: "g-local", DisplayName: "Local"})
	if err != nil {
		t.Fatal(err)
	}
	groupID := scimID(t, group.ID)

	users, total, err := store.ListUsers(t.Context(), scim.Filter{}, scim.Page{StartIndex: 1, Count: 10})
	if err != nil || total != 0 || len(users) != 0 {
		t.Fatalf("local user leaked from list: users=%#v total=%d err=%v", users, total, err)
	}
	if _, err := store.User(t.Context(), localID); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("local user read error=%v", err)
	}
	if _, err := store.ReplaceUser(t.Context(), localID, scim.User{ExternalID: "changed", UserName: "changed"}); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("local user replace error=%v", err)
	}
	if _, err := store.PatchUser(t.Context(), localID, scim.UserMutation{
		UserName: scim.Optional[string]{Set: true, Value: "changed"},
	}); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("local user patch error=%v", err)
	}
	if err := store.DeleteUser(t.Context(), localID); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("local user delete error=%v", err)
	}
	if _, err := store.PatchGroup(t.Context(), groupID, scim.GroupMutation{AddMembers: []int64{localID}}); !errors.Is(err, scim.ErrInvalidMember) {
		t.Fatalf("local group member error=%v", err)
	}
	var userName string
	var deletedAt *time.Time
	if err := store.pool.QueryRow(t.Context(), `select user_name,deleted_at from users where id=$1`, localID).Scan(&userName, &deletedAt); err != nil ||
		userName != "recovery-admin" || deletedAt != nil {
		t.Fatalf("local user mutated: userName=%q deletedAt=%v err=%v", userName, deletedAt, err)
	}
}

func TestSCIMGroupLifecycleRollsBackInvalidMembersAndPreservesGrants(t *testing.T) {
	store := migratedStore(t)
	first, err := store.CreateUser(t.Context(), scim.User{ExternalID: "u-1", UserName: "ada"})
	if err != nil {
		t.Fatal(err)
	}
	second, err := store.CreateUser(t.Context(), scim.User{ExternalID: "u-2", UserName: "grace"})
	if err != nil {
		t.Fatal(err)
	}
	firstID, secondID := scimID(t, first.ID), scimID(t, second.ID)
	group, err := store.CreateGroup(t.Context(), scim.Group{
		ExternalID: "g-1", DisplayName: "Engineering",
		Members: []scim.Member{{Value: first.ID}, {Value: first.ID}},
	})
	if err != nil || len(group.Members) != 1 {
		t.Fatalf("group=%#v err=%v", group, err)
	}
	groupID := scimID(t, group.ID)
	seedReadyRepository(t, store, 101, testSHA('a'))
	if _, err := store.pool.Exec(t.Context(), `insert into group_repository_grants (group_id, repository_id) values ($1, 101)`, groupID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.PatchGroup(t.Context(), groupID, scim.GroupMutation{AddMembers: []int64{firstID, secondID}}); err != nil {
		t.Fatal(err)
	}
	replace := []int64{secondID}
	group, err = store.PatchGroup(t.Context(), groupID, scim.GroupMutation{ReplaceMembers: &replace})
	if err != nil || len(group.Members) != 1 || group.Members[0].Value != second.ID {
		t.Fatalf("replace=%#v err=%v", group, err)
	}
	before := group.Meta.LastModified
	time.Sleep(time.Millisecond)
	group, err = store.ReplaceGroup(t.Context(), groupID, group)
	if err != nil || group.Meta.LastModified != before {
		t.Fatalf("identical group replace=%#v err=%v", group, err)
	}
	var preserved int
	if err := store.pool.QueryRow(t.Context(), `select count(*) from group_repository_grants where group_id=$1`, groupID).Scan(&preserved); err != nil || preserved != 1 {
		t.Fatalf("preserved grants=%d err=%v", preserved, err)
	}
	group, err = store.PatchGroup(t.Context(), groupID, scim.GroupMutation{RemoveMembers: []int64{secondID}})
	if err != nil || len(group.Members) != 0 {
		t.Fatalf("remove=%#v err=%v", group, err)
	}
	group, err = store.PatchGroup(t.Context(), groupID, scim.GroupMutation{AddMembers: []int64{secondID}})
	if err != nil || len(group.Members) != 1 {
		t.Fatalf("re-add=%#v err=%v", group, err)
	}
	if _, err := store.PatchGroup(t.Context(), groupID, scim.GroupMutation{
		AddMembers: []int64{firstID, 999999},
	}); !errors.Is(err, scim.ErrInvalidMember) {
		t.Fatalf("invalid member err=%v", err)
	}
	group, err = store.Group(t.Context(), groupID)
	if err != nil || len(group.Members) != 1 || group.Members[0].Value != second.ID {
		t.Fatalf("rollback group=%#v err=%v", group, err)
	}
	if _, err := store.PatchGroup(t.Context(), groupID, scim.GroupMutation{
		AddMembers: []int64{firstID}, RemoveMembers: []int64{999999},
	}); !errors.Is(err, scim.ErrNoTarget) {
		t.Fatalf("missing removal err=%v", err)
	}
	group, err = store.Group(t.Context(), groupID)
	if err != nil || len(group.Members) != 1 || group.Members[0].Value != second.ID {
		t.Fatalf("no-target rollback group=%#v err=%v", group, err)
	}
	if err := store.DeleteGroup(t.Context(), groupID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Group(t.Context(), groupID); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("deleted group err=%v", err)
	}
	groups, total, err := store.ListGroups(t.Context(), scim.Filter{}, scim.Page{StartIndex: 1, Count: 10})
	if err != nil || total != 0 || len(groups) != 0 {
		t.Fatalf("tombstone groups=%#v total=%d err=%v", groups, total, err)
	}
	var memberships, grants int
	if err := store.pool.QueryRow(t.Context(), `select count(*) from group_memberships where group_id=$1`, groupID).Scan(&memberships); err != nil {
		t.Fatal(err)
	}
	if err := store.pool.QueryRow(t.Context(), `select count(*) from group_repository_grants where group_id=$1`, groupID).Scan(&grants); err != nil {
		t.Fatal(err)
	}
	if memberships != 0 || grants != 0 {
		t.Fatalf("memberships=%d grants=%d", memberships, grants)
	}
}

func TestSCIMGroupPatchAppliesDeltasAfterReplacement(t *testing.T) {
	store := migratedStore(t)
	users := make([]scim.User, 3)
	for index := range users {
		var err error
		users[index], err = store.CreateUser(t.Context(), scim.User{
			ExternalID: "patch-user-" + strconv.Itoa(index),
			UserName:   "patch-user-" + strconv.Itoa(index),
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	group, err := store.CreateGroup(t.Context(), scim.Group{ExternalID: "patch-group", DisplayName: "Patch Group"})
	if err != nil {
		t.Fatal(err)
	}
	groupID := scimID(t, group.ID)

	t.Run("replace then add", func(t *testing.T) {
		mutation, err := scim.ParseGroupPatch(scim.NewPatchRequest([]scim.PatchOperation{
			{Op: "replace", Path: "members", Value: []byte(`[{"value":"` + users[0].ID + `"}]`)},
			{Op: "add", Path: "members", Value: []byte(`[{"value":"` + users[1].ID + `"}]`)},
		}))
		if err != nil {
			t.Fatal(err)
		}
		got, err := store.PatchGroup(t.Context(), groupID, mutation)
		if err != nil || len(got.Members) != 2 {
			t.Fatalf("group=%#v err=%v", got, err)
		}
	})

	t.Run("replace then remove", func(t *testing.T) {
		mutation, err := scim.ParseGroupPatch(scim.NewPatchRequest([]scim.PatchOperation{
			{Op: "replace", Path: "members", Value: []byte(`[{"value":"` + users[1].ID + `"},{"value":"` + users[2].ID + `"}]`)},
			{Op: "remove", Path: `members[value eq "` + users[2].ID + `"]`},
		}))
		if err != nil {
			t.Fatal(err)
		}
		got, err := store.PatchGroup(t.Context(), groupID, mutation)
		if err != nil || len(got.Members) != 1 || got.Members[0].Value != users[1].ID {
			t.Fatalf("group=%#v err=%v", got, err)
		}
	})
}

func TestSCIMStablePagingAndUniquenessRace(t *testing.T) {
	store := migratedStore(t)
	for _, name := range []string{"charlie", "ada", "grace"} {
		if _, err := store.CreateUser(t.Context(), scim.User{ExternalID: "id-" + name, UserName: name}); err != nil {
			t.Fatal(err)
		}
	}
	page, total, err := store.ListUsers(t.Context(), scim.Filter{}, scim.Page{StartIndex: 2, Count: 1})
	if err != nil || total != 3 || len(page) != 1 || page[0].UserName != "ada" {
		t.Fatalf("page=%#v total=%d err=%v", page, total, err)
	}
	start := make(chan struct{})
	errs := make(chan error, 2)
	var wait sync.WaitGroup
	for range 2 {
		wait.Add(1)
		go func() {
			defer wait.Done()
			<-start
			_, err := store.CreateGroup(t.Context(), scim.Group{ExternalID: "same", DisplayName: "Same"})
			errs <- err
		}()
	}
	close(start)
	wait.Wait()
	close(errs)
	var succeeded, collided int
	for err := range errs {
		switch {
		case err == nil:
			succeeded++
		case errors.Is(err, scim.ErrUniqueness):
			collided++
		default:
			t.Fatalf("race err=%v", err)
		}
	}
	if succeeded != 1 || collided != 1 {
		t.Fatalf("succeeded=%d collided=%d", succeeded, collided)
	}
}

func TestSCIMFinalAdministratorMutationsAreSerialized(t *testing.T) {
	store := migratedStore(t)
	first, err := store.CreateUser(t.Context(), scim.User{ExternalID: "admin-1", UserName: "ada"})
	if err != nil {
		t.Fatal(err)
	}
	second, err := store.CreateUser(t.Context(), scim.User{ExternalID: "admin-2", UserName: "grace"})
	if err != nil {
		t.Fatal(err)
	}
	firstID, secondID := scimID(t, first.ID), scimID(t, second.ID)
	if _, err := store.pool.Exec(t.Context(), `insert into user_roles (user_id, administrator) values ($1, true), ($2, true)`, firstID, secondID); err != nil {
		t.Fatal(err)
	}
	start := make(chan struct{})
	errs := make(chan error, 2)
	var wait sync.WaitGroup
	for _, id := range []int64{firstID, secondID} {
		wait.Add(1)
		go func() {
			defer wait.Done()
			<-start
			inactive := false
			_, err := store.PatchUser(t.Context(), id, scim.UserMutation{Active: scim.Optional[bool]{Set: true, Value: inactive}})
			errs <- err
		}()
	}
	close(start)
	wait.Wait()
	close(errs)
	var succeeded, protected int
	for err := range errs {
		switch {
		case err == nil:
			succeeded++
		case errors.Is(err, scim.ErrFinalAdministrator):
			protected++
		default:
			t.Fatalf("mutation err=%v", err)
		}
	}
	if succeeded != 1 || protected != 1 {
		t.Fatalf("succeeded=%d protected=%d", succeeded, protected)
	}
}

func scimID(t *testing.T, value string) int64 {
	t.Helper()
	id, err := strconv.ParseInt(value, 10, 64)
	if err != nil {
		t.Fatal(err)
	}
	return id
}
