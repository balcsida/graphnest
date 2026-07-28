package admin

import (
	"context"
	"errors"
	"testing"

	"github.com/grepnest/grepnest/internal/authn"
	"github.com/grepnest/grepnest/internal/githubapp"
	"github.com/grepnest/grepnest/internal/repository"
)

func TestServiceRejectsNonAdministrators(t *testing.T) {
	service := &Service{Store: &fakeStore{}, GitHub: fakeGitHub{}}
	for name, call := range map[string]func() error{
		"overview":  func() error { _, err := service.Overview(t.Context(), authn.Principal{}); return err },
		"reindex":   func() error { return service.Reindex(t.Context(), authn.Principal{}, 101) },
		"reconcile": func() error { return service.Reconcile(t.Context(), authn.Principal{}) },
		"retry":     func() error { return service.Retry(t.Context(), authn.Principal{}, 1) },
	} {
		t.Run(name, func(t *testing.T) {
			if err := call(); !errors.Is(err, ErrForbidden) {
				t.Fatalf("error = %v", err)
			}
		})
	}
}

func TestServiceResolvesReindexDefaultBranchSHA(t *testing.T) {
	store := &fakeStore{repository: repository.Repository{
		ID: 7, InstallationID: 10, GitHubID: 101, Name: "acme/one", Branch: "main", Enabled: true,
	}}
	service := &Service{Store: store, GitHub: fakeGitHub{sha: testSHA}}
	if err := service.Reindex(t.Context(), authn.Principal{Administrator: true, InstallationID: 10, RepositoryIDs: []int64{101}}, 101); err != nil {
		t.Fatal(err)
	}
	if store.enqueued.RepositoryID != 7 || store.enqueued.TargetSHA != testSHA ||
		store.enqueued.TargetRef != "refs/heads/main" || store.enqueued.Reason != "admin_reindex" {
		t.Fatalf("request = %#v", store.enqueued)
	}
}

func TestServiceScopesReconcileAndRetry(t *testing.T) {
	store := &fakeStore{}
	service := &Service{Store: store, GitHub: fakeGitHub{repositories: []githubapp.Repository{
		{ID: 101, InstallationID: 10, Owner: "acme", Name: "one", DefaultBranch: "main"},
		{ID: 102, InstallationID: 10, Owner: "acme", Name: "unscoped", DefaultBranch: "main"},
		{ID: 202, InstallationID: 20, Owner: "other", Name: "two", DefaultBranch: "main"},
	}}}
	admin := authn.Principal{Administrator: true, InstallationID: 10, RepositoryIDs: []int64{101}}
	if err := service.Reconcile(t.Context(), admin); err != nil {
		t.Fatal(err)
	}
	if err := service.Retry(t.Context(), admin, 42); err != nil {
		t.Fatal(err)
	}
	if len(store.reconciled) != 1 || store.reconciled[0].ID != 101 || store.retried != 42 ||
		store.retryInstallationID != 10 || len(store.retryRepositoryIDs) != 1 || store.retryRepositoryIDs[0] != 101 {
		t.Fatalf("reconciled=%#v retried=%d retry scope=(%d,%v)", store.reconciled, store.retried, store.retryInstallationID, store.retryRepositoryIDs)
	}
}

const testSHA = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

type fakeStore struct {
	repository          repository.Repository
	enqueued            IndexRequest
	retried             int64
	retryInstallationID int64
	retryRepositoryIDs  []int64
	reconciled          []githubapp.Repository
}

func (*fakeStore) AdminOverview(context.Context, int64, []int64) (Overview, error) {
	return Overview{}, nil
}
func (*fakeStore) AdminRepositories(context.Context, int64, []int64, int) ([]Repository, bool, error) {
	return nil, false, nil
}
func (*fakeStore) AdminJobs(context.Context, int64, []int64, int) ([]Job, bool, error) {
	return nil, false, nil
}
func (*fakeStore) AdminSCIPUploads(context.Context, int64, []int64, int) ([]SCIPUpload, bool, error) {
	return nil, false, nil
}
func (*fakeStore) AdminSCIPDependencies(context.Context, int64, []int64, int) ([]SCIPDependency, bool, error) {
	return nil, false, nil
}
func (*fakeStore) AdminDeliveries(context.Context, int64, []int64, int) ([]Delivery, bool, error) {
	return nil, false, nil
}
func (*fakeStore) AdminGitHub(context.Context, int64, []int64, GitHubConfig, int) (GitHub, error) {
	return GitHub{}, nil
}
func (store *fakeStore) AdminRepository(_ context.Context, installationID int64, repositoryIDs []int64, githubID int64) (repository.Repository, error) {
	if installationID != store.repository.InstallationID || len(repositoryIDs) != 1 ||
		repositoryIDs[0] != githubID || store.repository.GitHubID != githubID {
		return repository.Repository{}, errors.New("missing")
	}
	return store.repository, nil
}
func (store *fakeStore) EnqueueAdminIndex(_ context.Context, request IndexRequest) error {
	store.enqueued = request
	return nil
}
func (store *fakeStore) RetryAdminJob(_ context.Context, installationID int64, repositoryIDs []int64, id int64) error {
	store.retried = id
	store.retryInstallationID = installationID
	store.retryRepositoryIDs = append([]int64(nil), repositoryIDs...)
	return nil
}
func (store *fakeStore) ReconcileAdminRepositories(_ context.Context, _ int64, _ []int64, repositories []githubapp.Repository) error {
	store.reconciled = append([]githubapp.Repository(nil), repositories...)
	return nil
}

type fakeGitHub struct {
	sha          string
	repositories []githubapp.Repository
}

func (github fakeGitHub) DefaultBranchSHA(context.Context, int64, string, string, string) (string, error) {
	return github.sha, nil
}
func (github fakeGitHub) InstallationRepositories(context.Context, int64) ([]githubapp.Repository, error) {
	return github.repositories, nil
}
