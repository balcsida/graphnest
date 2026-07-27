package httpapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/grepnest/grepnest/internal/admin"
	"github.com/grepnest/grepnest/internal/authn"
	"github.com/grepnest/grepnest/internal/githubapp"
	"github.com/grepnest/grepnest/internal/repository"
	"github.com/jackc/pgx/v5"
)

func TestAdminRoutesRequireAdministrator(t *testing.T) {
	mux := http.NewServeMux()
	RegisterAdmin(mux, authn.NewStatic(map[string]authn.Principal{
		"user":  {Subject: "user"},
		"admin": {Subject: "admin", Administrator: true, InstallationID: 10, RepositoryIDs: []int64{101}},
	}), &admin.Service{Store: &adminHTTPStore{}, GitHub: adminHTTPGitHub{}}, 2, 4096)

	for _, token := range []string{"", "user"} {
		request := httptest.NewRequest(http.MethodGet, "/v1/admin/overview", nil)
		if token != "" {
			request.Header.Set("Authorization", "Bearer "+token)
		}
		response := httptest.NewRecorder()
		mux.ServeHTTP(response, request)
		want := http.StatusUnauthorized
		if token == "user" {
			want = http.StatusForbidden
		}
		if response.Code != want || strings.Contains(response.Body.String(), "secret") {
			t.Fatalf("token=%q status=%d body=%q", token, response.Code, response.Body.String())
		}
	}
}

func TestAdminRoutesExposeBoundedDataAndActions(t *testing.T) {
	store := &adminHTTPStore{}
	service := &admin.Service{Store: store, GitHub: adminHTTPGitHub{}}
	mux := http.NewServeMux()
	RegisterAdmin(mux, authn.NewStatic(map[string]authn.Principal{
		"admin": {Administrator: true, InstallationID: 10, RepositoryIDs: []int64{101}},
	}), service, 2, 4096)

	for _, path := range []string{
		"/v1/admin/overview", "/v1/admin/repositories", "/v1/admin/jobs",
		"/v1/admin/scip/uploads", "/v1/admin/scip/dependencies",
		"/v1/admin/webhook-deliveries", "/v1/admin/github",
	} {
		request := httptest.NewRequest(http.MethodGet, path, nil)
		request.Header.Set("Authorization", "Bearer admin")
		response := httptest.NewRecorder()
		mux.ServeHTTP(response, request)
		if response.Code != http.StatusOK || response.Body.Len() > 4096 {
			t.Fatalf("%s status=%d bytes=%d body=%q", path, response.Code, response.Body.Len(), response.Body.String())
		}
		if path == "/v1/admin/overview" && strings.Contains(response.Body.String(), "search_nodes") {
			t.Fatalf("overview exposed unsupported search-node metrics: %q", response.Body.String())
		}
		if path == "/v1/admin/github" && (strings.Contains(response.Body.String(), "private_key_file") ||
			strings.Contains(response.Body.String(), "webhook_secret_file")) {
			t.Fatalf("GitHub response exposed secret: %q", response.Body.String())
		}
	}

	for _, path := range []string{
		"/v1/admin/repositories/101/reindex",
		"/v1/admin/reconcile",
		"/v1/admin/jobs/42/retry",
	} {
		request := httptest.NewRequest(http.MethodPost, path, nil)
		request.Header.Set("Authorization", "Bearer admin")
		response := httptest.NewRecorder()
		mux.ServeHTTP(response, request)
		if response.Code != http.StatusNoContent {
			t.Fatalf("%s status=%d body=%q", path, response.Code, response.Body.String())
		}
	}
	if store.reindexed != 7 || store.retried != 42 {
		t.Fatalf("reindexed=%d retried=%d", store.reindexed, store.retried)
	}

	request := httptest.NewRequest(http.MethodPost, "/v1/admin/repositories/202/reindex", nil)
	request.Header.Set("Authorization", "Bearer admin")
	response := httptest.NewRecorder()
	mux.ServeHTTP(response, request)
	if response.Code != http.StatusNotFound {
		t.Fatalf("cross-scope reindex status=%d body=%q", response.Code, response.Body.String())
	}
	request = httptest.NewRequest(http.MethodPost, "/v1/admin/jobs/99/retry", nil)
	request.Header.Set("Authorization", "Bearer admin")
	response = httptest.NewRecorder()
	mux.ServeHTTP(response, request)
	if response.Code != http.StatusNotFound {
		t.Fatalf("cross-scope retry status=%d body=%q", response.Code, response.Body.String())
	}
}

type adminHTTPStore struct {
	reindexed int64
	retried   int64
}

func (adminHTTPStore) AdminOverview(context.Context, int64, []int64) (admin.Overview, error) {
	return admin.Overview{Repositories: map[string]int64{"ready": 1}}, nil
}
func (adminHTTPStore) AdminRepositories(context.Context, int64, []int64, int) ([]admin.Repository, bool, error) {
	return []admin.Repository{{GitHubID: 101, Name: "acme/one"}}, false, nil
}
func (adminHTTPStore) AdminJobs(context.Context, int64, []int64, int) ([]admin.Job, bool, error) {
	return []admin.Job{{ID: 42, RepositoryID: 101}}, false, nil
}
func (adminHTTPStore) AdminSCIPUploads(context.Context, int64, []int64, int) ([]admin.SCIPUpload, bool, error) {
	return []admin.SCIPUpload{}, false, nil
}
func (adminHTTPStore) AdminSCIPDependencies(context.Context, int64, []int64, int) ([]admin.SCIPDependency, bool, error) {
	return []admin.SCIPDependency{}, false, nil
}
func (adminHTTPStore) AdminDeliveries(context.Context, int64, []int64, int) ([]admin.Delivery, bool, error) {
	return []admin.Delivery{}, false, nil
}
func (adminHTTPStore) AdminGitHub(context.Context, int64, []int64, admin.GitHubConfig, int) (admin.GitHub, error) {
	return admin.GitHub{AppID: 7, PrivateKeyConfigured: true, WebhookSecretConfigured: true}, nil
}
func (adminHTTPStore) AdminRepository(_ context.Context, installationID int64, repositoryIDs []int64, githubID int64) (repository.Repository, error) {
	if installationID != 10 || len(repositoryIDs) != 1 || repositoryIDs[0] != 101 || githubID != 101 {
		return repository.Repository{}, pgx.ErrNoRows
	}
	return repository.Repository{ID: 7, InstallationID: 10, GitHubID: 101, Name: "acme/one", Branch: "main", Enabled: true}, nil
}
func (store *adminHTTPStore) EnqueueAdminIndex(_ context.Context, request admin.IndexRequest) error {
	store.reindexed = request.RepositoryID
	return nil
}
func (store *adminHTTPStore) RetryAdminJob(_ context.Context, installationID int64, repositoryIDs []int64, id int64) error {
	if installationID != 10 || len(repositoryIDs) != 1 || repositoryIDs[0] != 101 || id != 42 {
		return pgx.ErrNoRows
	}
	store.retried = id
	return nil
}
func (*adminHTTPStore) ReconcileAdminRepositories(context.Context, int64, []int64, []githubapp.Repository) error {
	return nil
}

type adminHTTPGitHub struct{}

func (adminHTTPGitHub) DefaultBranchSHA(context.Context, int64, string, string, string) (string, error) {
	return strings.Repeat("a", 40), nil
}
func (adminHTTPGitHub) InstallationRepositories(context.Context, int64) ([]githubapp.Repository, error) {
	return []githubapp.Repository{{ID: 101, InstallationID: 10, Owner: "acme", Name: "one", DefaultBranch: "main"}}, nil
}
