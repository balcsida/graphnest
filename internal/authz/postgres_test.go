//go:build integration

package authz_test

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/grepnest/grepnest/internal/authn"
	"github.com/grepnest/grepnest/internal/authz"
	"github.com/grepnest/grepnest/internal/postgres"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestAuthorizedRepositoriesUseDurableIDs(t *testing.T) {
	store, _ := testStore(t)
	seedRepository(t, store, 10, 101, "acme/old", true)
	principal := authn.Principal{InstallationID: 10, RepositoryIDs: []int64{101}}
	got, err := authz.NewPostgres(store).AuthorizedRepositories(t.Context(), principal, authz.RepositorySelection{})
	if err != nil || len(got) != 1 || got[0].GitHubID != 101 {
		t.Fatalf("got=%#v err=%v", got, err)
	}
	seedRepository(t, store, 10, 101, "acme/new", true)
	seedRepository(t, store, 10, 102, "acme/old", true)
	got, err = authz.NewPostgres(store).AuthorizedRepositories(t.Context(), principal, authz.RepositorySelection{Names: []string{"acme/old"}})
	if err != nil || len(got) != 0 {
		t.Fatalf("old-name reuse authorized: %#v err=%v", got, err)
	}
	one, err := authz.NewPostgres(store).AuthorizedRepository(t.Context(), principal, 101)
	if err != nil || one.Name != "acme/new" {
		t.Fatalf("got=%#v err=%v", one, err)
	}
}

func TestAdministratorsAuthorizeActiveRepositoriesAcrossInstallations(t *testing.T) {
	store, _ := testStore(t)
	for _, installation := range []postgres.InstallationUpdate{
		{GitHubID: 10, AccountLogin: "acme", AccountType: "Organization", Status: "active"},
		{GitHubID: 20, AccountLogin: "other", AccountType: "Organization", Status: "active"},
		{GitHubID: 30, AccountLogin: "inactive", AccountType: "Organization", Status: "suspended"},
	} {
		if err := store.UpsertInstallation(t.Context(), installation); err != nil {
			t.Fatal(err)
		}
	}
	for _, item := range []postgres.RepositoryUpdate{
		{GitHubID: 101, InstallationID: 10, Owner: "acme", Name: "one", CloneURL: "clone", WebURL: "web", DefaultBranch: "main", Enabled: true},
		{GitHubID: 201, InstallationID: 20, Owner: "other", Name: "two", CloneURL: "clone", WebURL: "web", DefaultBranch: "main", Enabled: true},
		{GitHubID: 202, InstallationID: 20, Owner: "other", Name: "disabled", CloneURL: "clone", WebURL: "web", DefaultBranch: "main", Enabled: false},
		{GitHubID: 203, InstallationID: 20, Owner: "other", Name: "archived", CloneURL: "clone", WebURL: "web", DefaultBranch: "main", Enabled: true, Archived: true},
		{GitHubID: 301, InstallationID: 30, Owner: "inactive", Name: "three", CloneURL: "clone", WebURL: "web", DefaultBranch: "main", Enabled: true},
	} {
		if _, err := store.UpsertRepository(t.Context(), item); err != nil {
			t.Fatal(err)
		}
	}
	authorizer := authz.NewPostgres(store)
	nonAdmin := authn.Principal{InstallationID: 10, RepositoryIDs: []int64{101}}
	if _, err := authorizer.AuthorizedRepository(t.Context(), nonAdmin, 201); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("non-admin cross-installation lookup error = %v", err)
	}
	admin := authn.Principal{Administrator: true, InstallationID: 10, RepositoryIDs: []int64{101}}
	got, err := authorizer.AuthorizedRepositories(t.Context(), admin, authz.RepositorySelection{})
	if err != nil || len(got) != 2 || got[0].GitHubID != 101 || got[1].GitHubID != 201 {
		t.Fatalf("admin repositories = %#v, err = %v", got, err)
	}
	filtered, err := authorizer.AuthorizedRepositories(t.Context(), admin, authz.RepositorySelection{Names: []string{"other/two"}})
	if err != nil || len(filtered) != 1 || filtered[0].GitHubID != 201 {
		t.Fatalf("admin filtered repositories = %#v, err = %v", filtered, err)
	}
	gotRepository, err := authorizer.AuthorizedRepository(t.Context(), admin, 201)
	if err != nil || gotRepository.GitHubID != 201 {
		t.Fatalf("admin cross-installation repository = %#v, err = %v", gotRepository, err)
	}
}

func TestAdministratorAPITokenStaysCeilingScoped(t *testing.T) {
	store, pool := testStore(t)
	for _, installation := range []postgres.InstallationUpdate{
		{GitHubID: 10, AccountLogin: "acme", AccountType: "Organization", Status: "active"},
		{GitHubID: 20, AccountLogin: "other", AccountType: "Organization", Status: "active"},
	} {
		if err := store.UpsertInstallation(t.Context(), installation); err != nil {
			t.Fatal(err)
		}
	}
	for _, repository := range []postgres.RepositoryUpdate{
		{GitHubID: 101, InstallationID: 10, Owner: "acme", Name: "one", CloneURL: "clone", WebURL: "web", DefaultBranch: "main", Enabled: true},
		{GitHubID: 102, InstallationID: 20, Owner: "other", Name: "two", CloneURL: "clone", WebURL: "web", DefaultBranch: "main", Enabled: true},
	} {
		if _, err := store.UpsertRepository(t.Context(), repository); err != nil {
			t.Fatal(err)
		}
	}
	var userID int64
	if err := pool.QueryRow(t.Context(), `insert into users (external_id, user_name, source) values ('directory-admin', 'admin', 'scim') returning id`).Scan(&userID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(t.Context(), `insert into user_roles (user_id, administrator) values ($1, true)`, userID); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Microsecond)
	if _, err := store.CreateAPIToken(t.Context(), authn.APITokenRecord{TokenHash: [32]byte{1}, Prefix: "gn_test", UserID: userID, RepositoryIDs: []int64{102}, CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	principal, err := store.APIPrincipal(t.Context(), [32]byte{1}, now)
	if err != nil || !principal.Administrator || principal.Method != "api_token" || len(principal.RepositoryIDs) != 1 || principal.RepositoryIDs[0] != 102 {
		t.Fatalf("principal=%#v err=%v", principal, err)
	}
	authorizer := authz.NewPostgres(store)
	repositories, err := authorizer.AuthorizedRepositories(t.Context(), principal, authz.RepositorySelection{})
	if err != nil || len(repositories) != 1 || repositories[0].GitHubID != 102 {
		t.Fatalf("repositories=%#v err=%v", repositories, err)
	}
	if _, err := authorizer.AuthorizedRepository(t.Context(), principal, 101); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("token accessed repo 101: %v", err)
	}
	repository, err := authorizer.AuthorizedRepository(t.Context(), principal, 102)
	if err != nil || repository.GitHubID != 102 {
		t.Fatalf("repository=%#v err=%v", repository, err)
	}
}

func testStore(t *testing.T) (*postgres.Store, *pgxpool.Pool) {
	t.Helper()
	admin, err := pgxpool.New(t.Context(), testDSN(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(admin.Close)
	bytes := make([]byte, 8)
	if _, err := rand.Read(bytes); err != nil {
		t.Fatal(err)
	}
	schema := "grepnest_authz_" + hex.EncodeToString(bytes)
	if _, err := admin.Exec(t.Context(), "create schema "+schema); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = admin.Exec(t.Context(), "drop schema "+schema+" cascade") })
	config, err := pgxpool.ParseConfig(testDSN(t))
	if err != nil {
		t.Fatal(err)
	}
	config.AfterConnect = func(ctx context.Context, connection *pgx.Conn) error {
		_, err := connection.Exec(ctx, "set search_path to "+schema)
		return err
	}
	pool, err := pgxpool.NewWithConfig(t.Context(), config)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	if err := postgres.Migrate(t.Context(), pool); err != nil {
		t.Fatal(err)
	}
	return postgres.New(pool), pool
}

func testDSN(t *testing.T) string {
	t.Helper()
	dsn := os.Getenv("GREPNEST_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("GREPNEST_TEST_POSTGRES_DSN is not set")
	}
	return dsn
}

func seedRepository(t *testing.T, store *postgres.Store, installationID, repositoryID int64, fullName string, enabled bool) {
	t.Helper()
	if err := store.UpsertInstallation(t.Context(), postgres.InstallationUpdate{GitHubID: installationID, AccountLogin: "acme", AccountType: "Organization", Status: "active"}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.UpsertRepository(t.Context(), postgres.RepositoryUpdate{GitHubID: repositoryID, InstallationID: installationID, Owner: fullName[:4], Name: fullName[5:], CloneURL: "clone", WebURL: "web", DefaultBranch: "main", Enabled: enabled}); err != nil {
		t.Fatal(err)
	}
}
