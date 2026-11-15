//go:build integration

package postgres

import (
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/balcsida/graphnest/internal/audit"
	"github.com/balcsida/graphnest/internal/authn"
	"github.com/balcsida/graphnest/internal/repository"
	"github.com/balcsida/graphnest/pkg/api"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
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

// A delegation-only administrator token is the one exception to "administrator
// API tokens need a non-empty ceiling": it authenticates with no repository
// access at all, and only Delegate consults the flag.
func TestDelegationOnlyAdministratorTokenAuthenticatesWithoutCeiling(t *testing.T) {
	store := migratedStore(t)
	userID := insertIdentityUser(t, store, "directory-6", "broker")
	seedReadyRepository(t, store, 101, testSHA('a'))
	if _, err := store.pool.Exec(t.Context(), `insert into user_roles (user_id, administrator) values ($1, true)`, userID); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Microsecond)
	if _, err := store.CreateAPIToken(t.Context(), authn.APITokenRecord{
		TokenHash: [32]byte{12}, Prefix: "gn_test", UserID: userID, DelegationOnly: true, CreatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	principal, err := store.APIPrincipal(t.Context(), [32]byte{12}, now)
	if err != nil || !principal.Administrator || !principal.DelegationOnly || principal.Method != "api_token" || len(principal.RepositoryIDs) != 0 {
		t.Fatalf("principal=%#v err=%v", principal, err)
	}
	// It is listed with the flag so operators can tell it apart.
	tokens, err := store.ListAPITokens(t.Context(), userID)
	if err != nil || len(tokens) != 1 || !tokens[0].DelegationOnly {
		t.Fatalf("tokens=%#v err=%v", tokens, err)
	}
	// The flag never widens a non-administrator: such a token does not
	// authenticate at all, because read as an ordinary token its NULL ceiling
	// would mean "every grant the owner has".
	plainUser := insertIdentityUser(t, store, "directory-7", "plain")
	if _, err := store.CreateAPIToken(t.Context(), authn.APITokenRecord{
		TokenHash: [32]byte{13}, Prefix: "gn_test", UserID: plainUser, DelegationOnly: true, CreatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	if plain, err := store.APIPrincipal(t.Context(), [32]byte{13}, now); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("non-administrator principal=%#v err=%v, want ErrNoRows", plain, err)
	}
}

// A delegation-only token is stored with no ceiling, so if it were ever read
// as an ordinary token it would inherit every grant its owner has. Demoting
// the owner must therefore invalidate the token, not widen it.
func TestDelegationOnlyTokenIsRejectedOnceOwnerIsDemoted(t *testing.T) {
	store := migratedStore(t)
	userID := insertIdentityUser(t, store, "directory-8", "demoted-broker")
	seedReadyRepository(t, store, 101, testSHA('a'))
	if _, err := store.pool.Exec(t.Context(), `insert into user_roles (user_id, administrator) values ($1, true)`, userID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.pool.Exec(t.Context(), `insert into user_repository_grants (user_id, repository_id) values ($1, 101)`, userID); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Microsecond)
	if _, err := store.CreateAPIToken(t.Context(), authn.APITokenRecord{
		TokenHash: [32]byte{14}, Prefix: "gn_test", UserID: userID, DelegationOnly: true, CreatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	broker, err := store.APIPrincipal(t.Context(), [32]byte{14}, now)
	if err != nil || !broker.DelegationOnly || len(broker.RepositoryIDs) != 0 {
		t.Fatalf("principal before demotion=%#v err=%v", broker, err)
	}
	if _, err := store.pool.Exec(t.Context(), `delete from user_roles where user_id=$1`, userID); err != nil {
		t.Fatal(err)
	}
	demoted, err := store.APIPrincipal(t.Context(), [32]byte{14}, now)
	if !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("demoted owner: principal=%#v err=%v, want ErrNoRows", demoted, err)
	}
}

// inventoryWorker creates an administrator who also holds a direct grant on
// repository 101, and an inventory-only token for them. The grant is what an
// ordinary token without a ceiling would fall back to.
func inventoryWorker(t *testing.T, store *Store, externalID string, now time.Time) (int64, authn.TokenManager, string) {
	t.Helper()
	userID := insertIdentityUser(t, store, externalID, externalID)
	seedReadyRepository(t, store, 101, testSHA('a'))
	if err := store.UpsertSearchNode(t.Context(), "node-a", "http://zoekt.invalid"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.pool.Exec(t.Context(), `insert into user_roles (user_id, administrator) values ($1, true)`, userID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.pool.Exec(t.Context(), `insert into user_repository_grants (user_id, repository_id) values ($1, 101)`, userID); err != nil {
		t.Fatal(err)
	}
	manager := authn.TokenManager{Store: store, Now: func() time.Time { return now }}
	_, plaintext, err := manager.CreateInventoryOnly(t.Context(), userID, authn.ProviderLocal, nil)
	if err != nil {
		t.Fatal(err)
	}
	return userID, manager, plaintext
}

// An inventory-only token has no ceiling to go stale: it lists every active
// repository, including one onboarded after it was minted, with the size a
// worker orders by. Repositories that are disabled, archived, or on an
// installation that is not active are not listed at all.
func TestInventoryOnlyTokenSeesRepositoriesOnboardedAfterItWasMinted(t *testing.T) {
	store := migratedStore(t)
	now := time.Now().UTC().Truncate(time.Microsecond)
	userID, manager, plaintext := inventoryWorker(t, store, "inventory-1", now)

	worker, err := manager.Authenticate(t.Context(), plaintext)
	if err != nil || !worker.Administrator || !worker.InventoryOnly || worker.DelegationOnly || worker.Delegated || worker.Method != "api_token" || len(worker.RepositoryIDs) != 0 {
		t.Fatalf("principal=%#v err=%v", worker, err)
	}
	service := &repository.Service{Store: store, SCIP: store, Graph: store}
	listed := func() map[string]int64 {
		t.Helper()
		// A worker authenticates on every request, so it never holds a stale principal.
		principal, err := manager.Authenticate(t.Context(), plaintext)
		if err != nil {
			t.Fatal(err)
		}
		summaries, err := service.List(t.Context(), principal)
		if err != nil {
			t.Fatal(err)
		}
		sizes := map[string]int64{}
		for _, summary := range summaries {
			sizes[summary.Name] = summary.SizeBytes
		}
		return sizes
	}
	if got := listed(); !reflect.DeepEqual(got, map[string]int64{"acme/repo-101": 0}) {
		t.Fatalf("listed at mint = %v", got)
	}

	// Onboarded after the token was minted.
	if _, err := store.UpsertRepository(t.Context(), RepositoryUpdate{GitHubID: 102, InstallationID: 10, Owner: "acme", Name: "later", CloneURL: "clone", WebURL: "web", DefaultBranch: "main", SizeBytes: 2048, Enabled: true}); err != nil {
		t.Fatal(err)
	}
	if err := store.UpsertInstallation(t.Context(), InstallationUpdate{GitHubID: 20, AccountLogin: "other", AccountType: "Organization", Status: "suspended", SuspendedAt: timePtr(now)}); err != nil {
		t.Fatal(err)
	}
	for _, update := range []RepositoryUpdate{
		{GitHubID: 103, InstallationID: 10, Owner: "acme", Name: "disabled", CloneURL: "clone", WebURL: "web", DefaultBranch: "main", Enabled: false},
		{GitHubID: 104, InstallationID: 10, Owner: "acme", Name: "archived", CloneURL: "clone", WebURL: "web", DefaultBranch: "main", Enabled: true, Archived: true},
		{GitHubID: 201, InstallationID: 20, Owner: "other", Name: "suspended", CloneURL: "clone", WebURL: "web", DefaultBranch: "main", Enabled: true},
	} {
		if _, err := store.UpsertRepository(t.Context(), update); err != nil {
			t.Fatal(err)
		}
	}
	want := map[string]int64{"acme/repo-101": 0, "acme/later": 2048}
	if got := listed(); !reflect.DeepEqual(got, want) {
		t.Fatalf("listed after onboarding = %v, want %v", got, want)
	}
	// It lists what its owner would list in a session, by the same query.
	session, err := store.UserPrincipal(t.Context(), userID, nil)
	if err != nil {
		t.Fatal(err)
	}
	fromSession, err := service.List(t.Context(), session)
	if err != nil {
		t.Fatal(err)
	}
	fromToken, err := service.List(t.Context(), worker)
	if err != nil || !reflect.DeepEqual(fromToken, fromSession) {
		t.Fatalf("token lists %#v, session %#v, err=%v", fromToken, fromSession, err)
	}

	// The status of a listed repository resolves what the listing leaves unknown.
	status, err := service.Status(t.Context(), worker, 102)
	if err != nil || status.Name != "acme/later" || status.SizeBytes != 2048 || status.SCIPStatus != "absent" || status.GraphStatus != "absent" {
		t.Fatalf("status=%#v err=%v", status, err)
	}
	for _, hidden := range []int64{103, 104, 201, 999} {
		if _, err := service.Status(t.Context(), worker, hidden); !errors.Is(err, pgx.ErrNoRows) {
			t.Errorf("status of repository %d err=%v, want ErrNoRows", hidden, err)
		}
	}
	// No content: file reads keep to the ceiling, and the token has none.
	if _, err := service.ReadFile(t.Context(), worker, api.ReadFileRequest{RepositoryID: 101, Path: "README.md"}); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("file read err=%v, want ErrNoRows", err)
	}

	tokens, err := store.ListAPITokens(t.Context(), userID)
	if err != nil || len(tokens) != 1 || !tokens[0].InventoryOnly || tokens[0].DelegationOnly || tokens[0].Delegated || tokens[0].RepositoryIDs != nil {
		t.Fatalf("tokens=%#v err=%v", tokens, err)
	}
}

// An inventory-only token is stored with no ceiling, so read as an ordinary
// token it would inherit every grant its owner has. Whatever ends the owner's
// administrator access must therefore end the token, not turn it into one
// that reads repository 101.
func TestInventoryOnlyTokenStopsAuthenticatingWithItsOwner(t *testing.T) {
	for name, end := range map[string]string{
		"demoted":          `delete from user_roles where user_id=$1`,
		"suspended":        `update users set suspended_at=now() where id=$1`,
		"deleted":          `update users set deleted_at=now() where id=$1`,
		"SCIM-deactivated": `update users set scim_active=false where id=$1`,
		"token revoked":    `update api_tokens set revoked_at=now() where user_id=$1`,
		"token expired":    `update api_tokens set expires_at=now() - interval '1 hour' where user_id=$1`,
	} {
		t.Run(name, func(t *testing.T) {
			store := migratedStore(t)
			now := time.Now().UTC().Truncate(time.Microsecond)
			userID, manager, plaintext := inventoryWorker(t, store, "inventory-2", now)
			if principal, err := manager.Authenticate(t.Context(), plaintext); err != nil || !principal.InventoryOnly {
				t.Fatalf("principal before=%#v err=%v", principal, err)
			}
			if _, err := store.pool.Exec(t.Context(), end, userID); err != nil {
				t.Fatal(err)
			}
			if principal, err := manager.Authenticate(t.Context(), plaintext); !errors.Is(err, authn.ErrUnauthenticated) {
				t.Fatalf("principal after=%#v err=%v, want ErrUnauthenticated", principal, err)
			}
		})
	}

	// Losing the role through a group is the same demotion.
	store := migratedStore(t)
	now := time.Now().UTC().Truncate(time.Microsecond)
	userID := insertIdentityUser(t, store, "inventory-3", "group-admin")
	var groupID int64
	if err := store.pool.QueryRow(t.Context(), `insert into groups (external_id, display_name) values ('ops', 'Operations') returning id`).Scan(&groupID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.pool.Exec(t.Context(), `insert into group_memberships (group_id, user_id) values ($1, $2)`, groupID, userID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.pool.Exec(t.Context(), `insert into group_roles (group_id, administrator) values ($1, true)`, groupID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateAPIToken(t.Context(), authn.APITokenRecord{TokenHash: [32]byte{21}, Prefix: "gn_test", UserID: userID, RepositoryIDs: []int64{}, InventoryOnly: true, CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if principal, err := store.APIPrincipal(t.Context(), [32]byte{21}, now); err != nil || !principal.InventoryOnly || !principal.Administrator {
		t.Fatalf("group administrator principal=%#v err=%v", principal, err)
	}
	if _, err := store.pool.Exec(t.Context(), `delete from group_memberships where user_id=$1`, userID); err != nil {
		t.Fatal(err)
	}
	if principal, err := store.APIPrincipal(t.Context(), [32]byte{21}, now); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("after leaving the group principal=%#v err=%v, want ErrNoRows", principal, err)
	}
	// A token for someone who never was an administrator does not authenticate either.
	plain := insertIdentityUser(t, store, "inventory-4", "plain")
	if _, err := store.CreateAPIToken(t.Context(), authn.APITokenRecord{TokenHash: [32]byte{22}, Prefix: "gn_test", UserID: plain, RepositoryIDs: []int64{}, InventoryOnly: true, CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if principal, err := store.APIPrincipal(t.Context(), [32]byte{22}, now); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("non-administrator principal=%#v err=%v, want ErrNoRows", principal, err)
	}
}

// The marker is what confines an inventory-only token, and a server from before
// migration 040 does not read it. The token is therefore stored with an empty
// ceiling, never an absent one: read as an ordinary token it is refused while
// its owner is an administrator and matches no repository afterwards, where an
// absent ceiling would have meant every grant the owner holds.
func TestInventoryOnlyTokenIsStoredWithAnEmptyCeiling(t *testing.T) {
	store := migratedStore(t)
	now := time.Now().UTC().Truncate(time.Microsecond)
	userID, manager, plaintext := inventoryWorker(t, store, "inventory-6", now)
	var empty bool
	if err := store.pool.QueryRow(t.Context(), `select repository_ids is not null and cardinality(repository_ids) = 0 from api_tokens where user_id=$1`, userID).Scan(&empty); err != nil || !empty {
		t.Fatalf("stored with an empty ceiling=%v err=%v", empty, err)
	}
	var violation *pgconn.PgError
	if _, err := store.CreateAPIToken(t.Context(), authn.APITokenRecord{TokenHash: [32]byte{25}, Prefix: "gn_test", UserID: userID, InventoryOnly: true, CreatedAt: now}); !errors.As(err, &violation) || violation.ConstraintName != "api_tokens_inventory_only_empty_ceiling" {
		t.Fatalf("absent ceiling err=%v, want api_tokens_inventory_only_empty_ceiling violation", err)
	}
	if _, err := store.CreateAPIToken(t.Context(), authn.APITokenRecord{TokenHash: [32]byte{26}, Prefix: "gn_test", UserID: userID, RepositoryIDs: []int64{101}, InventoryOnly: true, CreatedAt: now}); !errors.As(err, &violation) || violation.ConstraintName != "api_tokens_inventory_only_empty_ceiling" {
		t.Fatalf("non-empty ceiling err=%v, want api_tokens_inventory_only_empty_ceiling violation", err)
	}

	// What a server that ignores the marker makes of the same row.
	if _, err := store.pool.Exec(t.Context(), `update api_tokens set inventory_only=false where user_id=$1`, userID); err != nil {
		t.Fatal(err)
	}
	if principal, err := manager.Authenticate(t.Context(), plaintext); !errors.Is(err, authn.ErrUnauthenticated) {
		t.Fatalf("administrator's unmarked token principal=%#v err=%v, want ErrUnauthenticated", principal, err)
	}
	if _, err := store.pool.Exec(t.Context(), `delete from user_roles where user_id=$1`, userID); err != nil {
		t.Fatal(err)
	}
	// The owner still holds a direct grant on repository 101.
	principal, err := manager.Authenticate(t.Context(), plaintext)
	if err != nil || principal.Administrator || len(principal.RepositoryIDs) != 0 {
		t.Fatalf("demoted owner's unmarked token principal=%#v err=%v, want no repository", principal, err)
	}
}

// One token, one purpose: the database refuses a token marked as both kinds,
// whichever write path tries.
func TestAPITokenCannotBeBothDelegationOnlyAndInventoryOnly(t *testing.T) {
	store := migratedStore(t)
	userID := insertIdentityUser(t, store, "inventory-5", "both")
	now := time.Now().UTC().Truncate(time.Microsecond)
	both := authn.APITokenRecord{TokenHash: [32]byte{23}, Prefix: "gn_test", UserID: userID, DelegationOnly: true, RepositoryIDs: []int64{}, InventoryOnly: true, CreatedAt: now}
	var violation *pgconn.PgError
	if _, err := store.CreateAPIToken(t.Context(), both); !errors.As(err, &violation) || violation.ConstraintName != "api_tokens_single_purpose" {
		t.Fatalf("CreateAPIToken err=%v, want api_tokens_single_purpose violation", err)
	}
	event := audit.Event{ActorType: "user", ActorID: "1", TargetType: "api_token", AuthenticationMethod: "oidc", Operation: audit.OperationAPITokenCreated, Outcome: "success"}
	if _, err := store.CreateAPITokenAudited(t.Context(), both, event); !errors.As(err, &violation) || violation.ConstraintName != "api_tokens_single_purpose" {
		t.Fatalf("CreateAPITokenAudited err=%v, want api_tokens_single_purpose violation", err)
	}
	if _, err := store.CreateAPIToken(t.Context(), authn.APITokenRecord{TokenHash: [32]byte{24}, Prefix: "gn_test", UserID: userID, RepositoryIDs: []int64{}, InventoryOnly: true, CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.pool.Exec(t.Context(), `update api_tokens set delegation_only=true where user_id=$1`, userID); !errors.As(err, &violation) || violation.ConstraintName != "api_tokens_single_purpose" {
		t.Fatalf("update err=%v, want api_tokens_single_purpose violation", err)
	}
}

// A token minted by Delegate is an ordinary scoped administrator token that
// must additionally come back marked Delegated, so Delegate can refuse it and
// a chain of children cannot renew itself indefinitely.
func TestAPIPrincipalSurfacesDelegatedTokens(t *testing.T) {
	store := migratedStore(t)
	userID := insertIdentityUser(t, store, "directory-9", "job")
	seedReadyRepository(t, store, 101, testSHA('a'))
	if _, err := store.pool.Exec(t.Context(), `insert into user_roles (user_id, administrator) values ($1, true)`, userID); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Microsecond)
	expires := now.Add(time.Hour)
	manager := authn.TokenManager{Store: store, Now: func() time.Time { return now }}
	_, plaintext, err := manager.CreateDelegated(t.Context(), userID, "api_token", []int64{101}, &expires)
	if err != nil {
		t.Fatal(err)
	}
	child, err := manager.Authenticate(t.Context(), plaintext)
	if err != nil || !child.Delegated || !child.Administrator || child.DelegationOnly || child.Method != "api_token" ||
		!reflect.DeepEqual(child.RepositoryIDs, []int64{101}) {
		t.Fatalf("delegated principal=%#v err=%v", child, err)
	}
	// An ordinary scoped token created the usual way is not marked.
	_, plaintext, err = manager.CreateWithMethod(t.Context(), userID, "oidc", []int64{101}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if plain, err := manager.Authenticate(t.Context(), plaintext); err != nil || plain.Delegated {
		t.Fatalf("ordinary principal=%#v err=%v", plain, err)
	}
	// The child is listed with the marker so operators can tell it apart.
	tokens, err := store.ListAPITokens(t.Context(), userID)
	if err != nil || len(tokens) != 2 || !tokens[0].Delegated || tokens[1].Delegated {
		t.Fatalf("tokens=%#v err=%v", tokens, err)
	}
}

// The delegation-only path needs to know whether a repository is active
// without any principal ceiling; that is what Delegate checks each requested
// ID against.
func TestActiveRepositoryReportsOnlyEnabledActiveInstallations(t *testing.T) {
	store := migratedStore(t)
	seedReadyRepository(t, store, 101, testSHA('a'))
	if ok, err := store.ActiveRepository(t.Context(), 101); err != nil || !ok {
		t.Fatalf("active repository ok=%v err=%v", ok, err)
	}
	if ok, err := store.ActiveRepository(t.Context(), 999); err != nil || ok {
		t.Fatalf("unknown repository ok=%v err=%v", ok, err)
	}
	if _, err := store.pool.Exec(t.Context(), `update repositories set enabled=false where github_id=101`); err != nil {
		t.Fatal(err)
	}
	if ok, err := store.ActiveRepository(t.Context(), 101); err != nil || ok {
		t.Fatalf("disabled repository ok=%v err=%v", ok, err)
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
