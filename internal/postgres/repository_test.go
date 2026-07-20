//go:build integration

package postgres

import (
	"testing"
	"time"

	"github.com/grepnest/grepnest/internal/githubapp"
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
	got, err := store.UpsertRepository(t.Context(), RepositoryUpdate{GitHubID: 101, InstallationID: 10, Owner: "acme", Name: "one", CloneURL: "https://example.invalid/acme/one.git", WebURL: "https://example.invalid/acme/one", DefaultBranch: "main", SizeBytes: 123456, Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	if got.InstallationID != 10 || got.GitHubID != 101 || got.Name != "acme/one" || got.ZoektID == 0 || got.SizeBytes != 123456 {
		t.Fatalf("got %#v", got)
	}
	for _, check := range []struct {
		name string
		fn   func() (string, error)
	}{
		{"index lookup", func() (string, error) {
			repository, err := store.RepositoryForIndex(t.Context(), got.ID)
			if err == nil && repository.SizeBytes != 123456 {
				t.Fatalf("repository size = %d", repository.SizeBytes)
			}
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
	if err := store.UpsertSearchNode(t.Context(), "node-b", "http://zoekt-b.invalid"); err != nil {
		t.Fatal(err)
	}
	authorized, err := store.AuthorizedRepository(t.Context(), 10, []int64{101}, 101)
	if err != nil || authorized.SearchNode != "node-b" {
		t.Fatalf("authorized repository = %#v, err=%v", authorized, err)
	}
	var nodes int
	var nodeID, baseURL string
	if err := store.pool.QueryRow(t.Context(), "select count(*), min(node_id), min(base_url) from search_nodes").Scan(&nodes, &nodeID, &baseURL); err != nil || nodes != 1 || nodeID != "node-b" || baseURL != "http://zoekt-b.invalid" {
		t.Fatalf("nodes=%d nodeID=%q baseURL=%q err=%v", nodes, nodeID, baseURL, err)
	}
}

func TestReconcileInstallationCoalescesQuietDefaultHeads(t *testing.T) {
	store := migratedStore(t)
	installation := githubapp.Installation{ID: 10, AccountLogin: "acme", AccountType: "Organization", Status: "active"}
	repository := githubapp.Repository{ID: 101, InstallationID: 10, Owner: "acme", Name: "one", CloneURL: "clone", HTMLURL: "web", DefaultBranch: "main", DefaultSHA: testSHA('a')}

	if err := store.ReconcileInstallation(t.Context(), installation, []githubapp.Repository{repository}); err != nil {
		t.Fatal(err)
	}
	firstID, firstZoektID := reconciledRepositoryIDs(t, store, 101)
	assertReconciledRepository(t, store, 101, "acme", "one", "main", testSHA('a'), true, false, 1)

	repository.Owner, repository.Name = "renamed", "quiet"
	replacement := githubapp.Repository{ID: 102, InstallationID: 10, Owner: "acme", Name: "one", CloneURL: "replacement-clone", HTMLURL: "replacement-web", DefaultBranch: "main", DefaultSHA: testSHA('d')}
	if err := store.ReconcileInstallation(t.Context(), installation, []githubapp.Repository{replacement, repository}); err != nil {
		t.Fatal(err)
	}
	assertReconciledRepository(t, store, 101, "renamed", "quiet", "main", testSHA('a'), true, false, 1)
	assertReconciledRepository(t, store, 102, "acme", "one", "main", testSHA('d'), true, false, 1)

	repository.DefaultBranch, repository.DefaultSHA = "trunk", testSHA('b')
	if err := store.ReconcileInstallation(t.Context(), installation, []githubapp.Repository{repository}); err != nil {
		t.Fatal(err)
	}
	assertReconciledRepository(t, store, 101, "renamed", "quiet", "trunk", testSHA('b'), true, false, 1)

	repository.DefaultSHA = testSHA('c')
	if err := store.ReconcileInstallation(t.Context(), installation, []githubapp.Repository{repository}); err != nil {
		t.Fatal(err)
	}
	assertReconciledRepository(t, store, 101, "renamed", "quiet", "trunk", testSHA('c'), true, false, 1)
	if id, zoektID := reconciledRepositoryIDs(t, store, 101); id != firstID || zoektID != firstZoektID {
		t.Fatalf("IDs changed from (%d,%d) to (%d,%d)", firstID, firstZoektID, id, zoektID)
	}

	repository.Archived = true
	if err := store.ReconcileInstallation(t.Context(), installation, []githubapp.Repository{repository}); err != nil {
		t.Fatal(err)
	}
	assertReconciledRepository(t, store, 101, "renamed", "quiet", "trunk", testSHA('c'), false, true, 1)
	repository.Archived = false
	if err := store.ReconcileInstallation(t.Context(), installation, []githubapp.Repository{repository}); err != nil {
		t.Fatal(err)
	}
	assertRepositoryState(t, store, 101, "pending", "")

	if err := store.ReconcileInstallation(t.Context(), installation, nil); err != nil {
		t.Fatal(err)
	}
	assertReconciledRepository(t, store, 101, "renamed", "quiet", "trunk", testSHA('c'), false, false, 1)
	recreated := githubapp.Repository{ID: 103, InstallationID: 10, Owner: "acme", Name: "one", CloneURL: "recreated-clone", HTMLURL: "recreated-web", DefaultBranch: "main", DefaultSHA: testSHA('e')}
	if err := store.ReconcileInstallation(t.Context(), installation, []githubapp.Repository{recreated}); err != nil {
		t.Fatal(err)
	}
	assertReconciledRepository(t, store, 103, "acme", "one", "main", testSHA('e'), true, false, 1)

	installation.Status = "suspended"
	repository.Archived = false
	if err := store.ReconcileInstallation(t.Context(), installation, []githubapp.Repository{repository}); err != nil {
		t.Fatal(err)
	}
	assertReconciledRepository(t, store, 101, "renamed", "quiet", "trunk", testSHA('c'), false, false, 1)

	ids, err := store.InstallationIDs(t.Context())
	if err != nil || len(ids) != 1 || ids[0] != 10 {
		t.Fatalf("installation IDs = %v, err = %v", ids, err)
	}
	if err := store.DisableInstallation(t.Context(), 10, "deleted"); err != nil {
		t.Fatal(err)
	}
	var status string
	if err := store.pool.QueryRow(t.Context(), "select status from installations where github_id=10").Scan(&status); err != nil || status != "deleted" {
		t.Fatalf("status = %q, err = %v", status, err)
	}
}

func reconciledRepositoryIDs(t *testing.T, store *Store, githubID int64) (int64, int64) {
	t.Helper()
	var id, zoektID int64
	if err := store.pool.QueryRow(t.Context(), "select id, zoekt_repo_id from repositories where github_id=$1", githubID).Scan(&id, &zoektID); err != nil {
		t.Fatal(err)
	}
	return id, zoektID
}

func assertReconciledRepository(t *testing.T, store *Store, githubID int64, owner, name, branch, desiredSHA string, enabled, archived bool, jobs int) {
	t.Helper()
	var gotOwner, gotName, gotBranch, gotSHA string
	var gotEnabled, gotArchived bool
	if err := store.pool.QueryRow(t.Context(), `select owner, name, default_branch, coalesce(desired_sha, ''), enabled, archived from repositories where github_id=$1`, githubID).Scan(&gotOwner, &gotName, &gotBranch, &gotSHA, &gotEnabled, &gotArchived); err != nil {
		t.Fatal(err)
	}
	var gotJobs int
	if err := store.pool.QueryRow(t.Context(), `select count(*) from index_jobs where repository_id=(select id from repositories where github_id=$1) and state='queued'`, githubID).Scan(&gotJobs); err != nil {
		t.Fatal(err)
	}
	if gotOwner != owner || gotName != name || gotBranch != branch || gotSHA != desiredSHA || gotEnabled != enabled || gotArchived != archived || gotJobs != jobs {
		t.Fatalf("metadata=(%q,%q,%q,%q,%v,%v) jobs=%d", gotOwner, gotName, gotBranch, gotSHA, gotEnabled, gotArchived, gotJobs)
	}
}

func assertRepositoryState(t *testing.T, store *Store, githubID int64, status, errorCode string) {
	t.Helper()
	var gotStatus, gotError string
	if err := store.pool.QueryRow(t.Context(), `select status, coalesce(error_code, '') from repositories where github_id=$1`, githubID).Scan(&gotStatus, &gotError); err != nil {
		t.Fatal(err)
	}
	if gotStatus != status || gotError != errorCode {
		t.Fatalf("status=%q error=%q", gotStatus, gotError)
	}
}

func testSHA(character byte) string {
	result := make([]byte, 40)
	for index := range result {
		result[index] = character
	}
	return string(result)
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
