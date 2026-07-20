package githubapp

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
	"time"
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
	installations   []Installation
	repositories    [][]Repository
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
	store.installations = append(store.installations, installation)
	store.repositories = append(store.repositories, append([]Repository(nil), repositories...))
	store.reconciled = append(store.reconciled, installation.ID)
	return nil
}

func TestReconcileClientInstallationWithoutStatusReadsDefaultHead(t *testing.T) {
	api := &reconcileAPIStub{shas: map[int64]string{101: sha('a')}}
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		switch request.URL.EscapedPath() {
		case "/api/v3/app/installations":
			fmt.Fprint(w, `[{"id":7,"account":{"login":"acme","type":"Organization"},"suspended_at":null}]`)
		case "/api/v3/app/installations/7/access_tokens":
			fmt.Fprint(w, `{"token":"installation-token","expires_at":"2026-07-20T13:00:00Z"}`)
		case "/api/v3/installation/repositories":
			fmt.Fprint(w, `{"repositories":[{"id":101,"full_name":"acme/one","owner":{"login":"acme"},"name":"one","clone_url":"https://example.invalid/acme/one.git","html_url":"https://example.invalid/acme/one","default_branch":"main"}]}`)
		case "/api/v3/repos/acme/one/branches/main":
			api.reads++
			fmt.Fprintf(w, `{"commit":{"sha":%q}}`, sha('a'))
		default:
			http.NotFound(w, request)
		}
	}))
	defer server.Close()
	now := time.Date(2026, time.July, 20, 12, 0, 0, 0, time.UTC)
	store := &reconcileStoreStub{api: api}

	if err := NewReconciler(testClient(t, server, &now, 4096), store).All(t.Context()); err != nil {
		t.Fatal(err)
	}
	if len(store.installations) != 1 || store.installations[0].Status != "active" || len(store.repositories[0]) != 1 || store.repositories[0][0].DefaultSHA != sha('a') {
		t.Fatalf("installations=%#v repositories=%#v", store.installations, store.repositories)
	}
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
