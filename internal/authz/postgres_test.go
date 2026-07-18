//go:build integration

package authz_test

import (
	"os"
	"testing"
	"time"

	"github.com/grepnest/grepnest/internal/authn"
	"github.com/grepnest/grepnest/internal/authz"
	"github.com/grepnest/grepnest/internal/postgres"
)

func TestAuthorizedRepositoriesUseDurableIDs(t *testing.T) {
	store := testStore(t)
	installationID := time.Now().UnixNano()
	repositoryID := installationID + 1
	oldName := "acme/old-" + time.Now().Format("150405.000000000")
	newName := "acme/new-" + time.Now().Format("150405.000000000")
	seedRepository(t, store, installationID, repositoryID, oldName, true)
	principal := authn.Principal{InstallationID: installationID, RepositoryIDs: []int64{repositoryID}}
	got, err := authz.NewPostgres(store).AuthorizedRepositories(t.Context(), principal, authz.RepositorySelection{})
	if err != nil || len(got) != 1 || got[0].GitHubID != repositoryID {
		t.Fatalf("got=%#v err=%v", got, err)
	}
	seedRepository(t, store, installationID, repositoryID, newName, true)
	seedRepository(t, store, installationID, repositoryID+1, oldName, true)
	got, err = authz.NewPostgres(store).AuthorizedRepositories(t.Context(), principal, authz.RepositorySelection{Names: []string{oldName}})
	if err != nil || len(got) != 0 {
		t.Fatalf("old-name reuse authorized: %#v err=%v", got, err)
	}
	one, err := authz.NewPostgres(store).AuthorizedRepository(t.Context(), principal, repositoryID)
	if err != nil || one.Name != newName {
		t.Fatalf("got=%#v err=%v", one, err)
	}
}

func testStore(t *testing.T) *postgres.Store {
	t.Helper()
	pool, err := postgres.Open(t.Context(), testDSN(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	if err := postgres.Migrate(t.Context(), pool); err != nil {
		t.Fatal(err)
	}
	return postgres.New(pool)
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
