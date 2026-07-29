//go:build integration

package postgres

import "testing"

func TestAdminDeliveriesOnlyIncludeAllowedRepositories(t *testing.T) {
	store := migratedStore(t)
	queueRepository(t, store)
	unscoped, err := store.UpsertRepository(t.Context(), RepositoryUpdate{
		GitHubID: 102, InstallationID: 10, Owner: "acme", Name: "unscoped",
		CloneURL: "clone", WebURL: "web", DefaultBranch: "main", Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.UpsertInstallation(t.Context(), InstallationUpdate{
		GitHubID: 20, AccountLogin: "other", AccountType: "Organization", Status: "active",
	}); err != nil {
		t.Fatal(err)
	}
	other, err := store.UpsertRepository(t.Context(), RepositoryUpdate{
		GitHubID: 202, InstallationID: 20, Owner: "other", Name: "other",
		CloneURL: "clone", WebURL: "web", DefaultBranch: "main", Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}

	installationID := int64(10)
	otherInstallationID := int64(20)
	scopedRepositoryID := int64(101)
	unscopedRepositoryID := unscoped.GitHubID
	otherRepositoryID := other.GitHubID
	for _, delivery := range []Delivery{
		{ID: "scoped", Event: "push", State: "accepted", InstallationID: &installationID, RepositoryID: &scopedRepositoryID},
		{ID: "unscoped", Event: "push", State: "failed", InstallationID: &installationID, RepositoryID: &unscopedRepositoryID},
		{ID: "installation-wide", Event: "installation", State: "accepted", InstallationID: &installationID},
		{ID: "installation-repositories", Event: "installation_repositories", State: "accepted", InstallationID: &installationID},
		{ID: "other", Event: "push", State: "accepted", InstallationID: &otherInstallationID, RepositoryID: &otherRepositoryID},
	} {
		if inserted, err := store.ApplyDelivery(t.Context(), delivery, nil); err != nil || !inserted {
			t.Fatalf("apply %q: inserted=%v err=%v", delivery.ID, inserted, err)
		}
	}

	deliveries, more, err := store.AdminDeliveries(t.Context(), 10, []int64{101}, 10)
	if err != nil || more || len(deliveries) != 3 || deliveries[0].DeliveryID != "installation-repositories" ||
		deliveries[1].DeliveryID != "installation-wide" || deliveries[2].DeliveryID != "scoped" {
		t.Fatalf("deliveries=%#v more=%v err=%v", deliveries, more, err)
	}
	overview, err := store.AdminOverview(t.Context(), 10, []int64{101})
	if err != nil || len(overview.Deliveries) != 1 || overview.Deliveries["accepted"] != 3 {
		t.Fatalf("delivery overview=%#v err=%v", overview.Deliveries, err)
	}
}

func TestAdministratorAPITokenDeliveriesExcludeUnscopedRepositories(t *testing.T) {
	store := migratedStore(t)
	queueRepository(t, store)
	if _, err := store.UpsertRepository(t.Context(), RepositoryUpdate{
		GitHubID: 102, InstallationID: 10, Owner: "acme", Name: "unscoped",
		CloneURL: "clone", WebURL: "web", DefaultBranch: "main", Enabled: true,
	}); err != nil {
		t.Fatal(err)
	}
	installationID := int64(10)
	for _, item := range []struct {
		id           string
		repositoryID int64
	}{{"scoped", 101}, {"unscoped", 102}} {
		delivery := Delivery{ID: item.id, Event: "push", State: "accepted", InstallationID: &installationID, RepositoryID: &item.repositoryID}
		if inserted, err := store.ApplyDelivery(t.Context(), delivery, nil); err != nil || !inserted {
			t.Fatalf("apply %d: inserted=%v err=%v", item.repositoryID, inserted, err)
		}
	}

	deliveries, _, err := store.AdminDeliveries(t.Context(), 0, []int64{101}, 10)
	if err != nil || len(deliveries) != 1 || deliveries[0].DeliveryID != "scoped" {
		t.Fatalf("deliveries=%#v err=%v", deliveries, err)
	}
}
