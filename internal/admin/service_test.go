package admin

import (
	"context"
	"errors"
	"testing"

	"github.com/grepnest/grepnest/internal/authn"
	"github.com/grepnest/grepnest/internal/repository"
)

func TestServiceRejectsNonAdministrators(t *testing.T) {
	service := &Service{Store: &fakeStore{}, GitHub: fakeGitHub{}, Reconciler: &fakeReconciler{}}
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
	service := &Service{Store: store, GitHub: fakeGitHub{sha: testSHA}, Reconciler: &fakeReconciler{}}
	if err := service.Reindex(t.Context(), authn.Principal{Administrator: true}, 101); err != nil {
		t.Fatal(err)
	}
	if store.enqueued.RepositoryID != 7 || store.enqueued.TargetSHA != testSHA ||
		store.enqueued.TargetRef != "refs/heads/main" || store.enqueued.Reason != "admin_reindex" {
		t.Fatalf("request = %#v", store.enqueued)
	}
}

func TestServiceRunsReconcileAndRetry(t *testing.T) {
	store := &fakeStore{}
	reconciler := fakeReconciler{}
	service := &Service{Store: store, GitHub: fakeGitHub{}, Reconciler: &reconciler}
	admin := authn.Principal{Administrator: true}
	if err := service.Reconcile(t.Context(), admin); err != nil {
		t.Fatal(err)
	}
	if err := service.Retry(t.Context(), admin, 42); err != nil {
		t.Fatal(err)
	}
	if reconciler.calls != 1 || store.retried != 42 {
		t.Fatalf("reconciles=%d retried=%d", reconciler.calls, store.retried)
	}
}

const testSHA = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

type fakeStore struct {
	repository repository.Repository
	enqueued   IndexRequest
	retried    int64
}

func (*fakeStore) AdminOverview(context.Context) (Overview, error) { return Overview{}, nil }
func (*fakeStore) AdminRepositories(context.Context, int) ([]Repository, bool, error) {
	return nil, false, nil
}
func (*fakeStore) AdminJobs(context.Context, int) ([]Job, bool, error) { return nil, false, nil }
func (*fakeStore) AdminSCIPUploads(context.Context, int) ([]SCIPUpload, bool, error) {
	return nil, false, nil
}
func (*fakeStore) AdminSCIPDependencies(context.Context, int) ([]SCIPDependency, bool, error) {
	return nil, false, nil
}
func (*fakeStore) AdminDeliveries(context.Context, int) ([]Delivery, bool, error) {
	return nil, false, nil
}
func (*fakeStore) AdminGitHub(context.Context, GitHubConfig, int) (GitHub, error) {
	return GitHub{}, nil
}
func (store *fakeStore) AdminRepository(_ context.Context, githubID int64) (repository.Repository, error) {
	if store.repository.GitHubID != githubID {
		return repository.Repository{}, errors.New("missing")
	}
	return store.repository, nil
}
func (store *fakeStore) EnqueueAdminIndex(_ context.Context, request IndexRequest) error {
	store.enqueued = request
	return nil
}
func (store *fakeStore) RetryAdminJob(_ context.Context, id int64) error {
	store.retried = id
	return nil
}

type fakeGitHub struct{ sha string }

func (github fakeGitHub) DefaultBranchSHA(context.Context, int64, string, string, string) (string, error) {
	return github.sha, nil
}

type fakeReconciler struct{ calls int }

func (reconciler *fakeReconciler) All(context.Context) error {
	reconciler.calls++
	return nil
}
