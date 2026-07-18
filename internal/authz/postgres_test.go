//go:build integration

package authz_test

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"os"
	"testing"

	"github.com/grepnest/grepnest/internal/authn"
	"github.com/grepnest/grepnest/internal/authz"
	"github.com/grepnest/grepnest/internal/postgres"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestAuthorizedRepositoriesUseDurableIDs(t *testing.T) {
	store := testStore(t)
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

func testStore(t *testing.T) *postgres.Store {
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
