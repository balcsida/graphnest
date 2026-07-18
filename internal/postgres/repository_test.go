//go:build integration

package postgres

import (
	"testing"
	"time"
)

func migratedStore(t *testing.T) *Store {
	t.Helper()
	pool := testPool(t)
	if err := Migrate(t.Context(), pool); err != nil {
		t.Fatal(err)
	}
	return New(pool)
}

func TestRepositoryStorePreservesDurableIDs(t *testing.T) {
	store := migratedStore(t)
	if err := store.UpsertInstallation(t.Context(), InstallationUpdate{GitHubID: 10, AccountLogin: "acme", AccountType: "Organization", Status: "active"}); err != nil {
		t.Fatal(err)
	}
	got, err := store.UpsertRepository(t.Context(), RepositoryUpdate{GitHubID: 101, InstallationID: 10, Owner: "acme", Name: "one", CloneURL: "https://example.invalid/acme/one.git", WebURL: "https://example.invalid/acme/one", DefaultBranch: "main", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	if got.InstallationID != 10 || got.GitHubID != 101 || got.Name != "acme/one" || got.ZoektID == 0 {
		t.Fatalf("got %#v", got)
	}
	for _, check := range []struct {
		name string
		fn   func() (string, error)
	}{
		{"index lookup", func() (string, error) {
			repository, err := store.RepositoryForIndex(t.Context(), got.ID)
			return repository.Name, err
		}},
		{"desired sha", func() (string, error) { return store.DesiredSHA(t.Context(), got.ID) }},
	} {
		value, err := check.fn()
		if check.name == "desired sha" && value != "" || err != nil || check.name == "index lookup" && value != "acme/one" {
			t.Fatalf("%s: value=%q err=%v", check.name, value, err)
		}
	}
	if err := store.UpsertSearchNode(t.Context(), "node-a", "http://zoekt.invalid"); err != nil {
		t.Fatal(err)
	}
	if err := store.UpsertSearchNode(t.Context(), "node-b", "http://zoekt.invalid"); err != nil {
		t.Fatal(err)
	}
	var nodes int
	if err := store.pool.QueryRow(t.Context(), "select count(*) from search_nodes").Scan(&nodes); err != nil || nodes != 1 {
		t.Fatalf("nodes=%d err=%v", nodes, err)
	}
}

func TestAuthorizedRepositoriesExcludeInactiveStates(t *testing.T) {
	store := migratedStore(t)
	for _, update := range []InstallationUpdate{
		{GitHubID: 10, AccountLogin: "active", AccountType: "Organization", Status: "active"},
		{GitHubID: 20, AccountLogin: "disabled", AccountType: "Organization", Status: "suspended", SuspendedAt: timePtr(time.Now())},
	} {
		if err := store.UpsertInstallation(t.Context(), update); err != nil {
			t.Fatal(err)
		}
	}
	for _, update := range []RepositoryUpdate{
		{GitHubID: 101, InstallationID: 10, Owner: "acme", Name: "enabled", CloneURL: "clone", WebURL: "web", DefaultBranch: "main", Enabled: true},
		{GitHubID: 102, InstallationID: 10, Owner: "acme", Name: "disabled", CloneURL: "clone", WebURL: "web", DefaultBranch: "main", Enabled: false},
		{GitHubID: 103, InstallationID: 10, Owner: "acme", Name: "archived", CloneURL: "clone", WebURL: "web", DefaultBranch: "main", Enabled: true, Archived: true},
		{GitHubID: 201, InstallationID: 20, Owner: "other", Name: "suspended", CloneURL: "clone", WebURL: "web", DefaultBranch: "main", Enabled: true},
	} {
		if _, err := store.UpsertRepository(t.Context(), update); err != nil {
			t.Fatal(err)
		}
	}
	got, err := store.AuthorizedRepositories(t.Context(), 10, []int64{101, 102, 103, 201}, nil)
	if err != nil || len(got) != 1 || got[0].GitHubID != 101 {
		t.Fatalf("got=%#v err=%v", got, err)
	}
	if got, err := store.AuthorizedRepositories(t.Context(), 10, nil, nil); err != nil || len(got) != 0 {
		t.Fatalf("empty IDs: got=%#v err=%v", got, err)
	}
}

func timePtr(value time.Time) *time.Time { return &value }
