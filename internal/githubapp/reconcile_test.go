package githubapp

import (
	"context"
	"fmt"
	"reflect"
	"testing"
)

type reconcileAPIStub struct {
	installations []Installation
	repositories  map[int64][]Repository
	shas          map[int64]string
	reads         int
}

func (api *reconcileAPIStub) Installations(context.Context) ([]Installation, error) {
	return api.installations, nil
}

func (api *reconcileAPIStub) InstallationRepositories(_ context.Context, installationID int64) ([]Repository, error) {
	return api.repositories[installationID], nil
}

func (api *reconcileAPIStub) DefaultBranchSHA(_ context.Context, _ int64, _ string, name, _ string) (string, error) {
	api.reads++
	sha, ok := api.shas[repositoryID(name)]
	if !ok {
		return "", fmt.Errorf("missing SHA for %s", name)
	}
	return sha, nil
}

type reconcileStoreStub struct {
	installationIDs []int64
	reconciled      []int64
	disabled        []int64
	api             *reconcileAPIStub
}

func (store *reconcileStoreStub) InstallationIDs(context.Context) ([]int64, error) {
	return store.installationIDs, nil
}

func (store *reconcileStoreStub) ReconcileInstallation(_ context.Context, installation Installation, repositories []Repository) error {
	expectedReads := 0
	for _, repository := range repositories {
		if !repository.Archived && !repository.Disabled {
			expectedReads++
		}
	}
	if store.api.reads != expectedReads {
		return fmt.Errorf("store called after %d of %d SHA reads", store.api.reads, expectedReads)
	}
	for _, repository := range repositories {
		if !repository.Archived && !repository.Disabled && repository.DefaultSHA != store.api.shas[repository.ID] {
			return fmt.Errorf("repository %d SHA = %q", repository.ID, repository.DefaultSHA)
		}
	}
	store.reconciled = append(store.reconciled, installation.ID)
	return nil
}

func TestReconcileUnavailableObjectsWithoutBranchReads(t *testing.T) {
	for _, test := range []struct {
		name         string
		installation Installation
		repository   Repository
	}{
		{"suspended installation", Installation{ID: 7, AccountLogin: "acme", Status: "suspended"}, Repository{ID: 101, InstallationID: 7, Owner: "acme", Name: "one", DefaultBranch: "main"}},
		{"archived repository", Installation{ID: 7, AccountLogin: "acme", Status: "active"}, Repository{ID: 101, InstallationID: 7, Owner: "acme", Name: "one", DefaultBranch: "main", Archived: true}},
		{"disabled repository", Installation{ID: 7, AccountLogin: "acme", Status: "active"}, Repository{ID: 101, InstallationID: 7, Owner: "acme", Name: "one", DefaultBranch: "main", Disabled: true}},
	} {
		t.Run(test.name, func(t *testing.T) {
			api := &reconcileAPIStub{
				installations: []Installation{test.installation},
				repositories:  map[int64][]Repository{7: {test.repository}},
				shas:          map[int64]string{},
			}
			store := &reconcileStoreStub{api: api}
			if err := (&Reconciler{github: api, store: store}).Installation(t.Context(), 7); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func (store *reconcileStoreStub) DisableInstallation(_ context.Context, installationID int64, status string) error {
	if status != "deleted" {
		return fmt.Errorf("status = %q", status)
	}
	store.disabled = append(store.disabled, installationID)
	return nil
}

func TestReconcileInstallationReadsDefaultHeadsBeforeStore(t *testing.T) {
	api := &reconcileAPIStub{
		installations: []Installation{{ID: 7, AccountLogin: "acme", Status: "active"}},
		repositories: map[int64][]Repository{7: {
			{ID: 101, InstallationID: 7, Owner: "acme", Name: "one", DefaultBranch: "main"},
			{ID: 102, InstallationID: 7, Owner: "acme", Name: "two", DefaultBranch: "trunk"},
		}},
		shas: map[int64]string{101: sha('a'), 102: sha('b')},
	}
	store := &reconcileStoreStub{api: api}
	reconciler := &Reconciler{github: api, store: store}

	if err := reconciler.Installation(t.Context(), 7); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(store.reconciled, []int64{7}) {
		t.Fatalf("reconciled = %v", store.reconciled)
	}
}

func TestReconcileAllDisablesInstallationsMissingUpstream(t *testing.T) {
	api := &reconcileAPIStub{
		installations: []Installation{{ID: 7, AccountLogin: "acme", Status: "active"}},
		repositories:  map[int64][]Repository{7: {}},
		shas:          map[int64]string{},
	}
	store := &reconcileStoreStub{installationIDs: []int64{7, 9}, api: api}
	reconciler := &Reconciler{github: api, store: store}

	if err := reconciler.All(t.Context()); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(store.reconciled, []int64{7}) || !reflect.DeepEqual(store.disabled, []int64{9}) {
		t.Fatalf("reconciled=%v disabled=%v", store.reconciled, store.disabled)
	}
}

func repositoryID(name string) int64 {
	if name == "one" {
		return 101
	}
	return 102
}

func sha(character byte) string {
	result := make([]byte, 40)
	for index := range result {
		result[index] = character
	}
	return string(result)
}
