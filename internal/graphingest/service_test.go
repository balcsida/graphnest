package graphingest

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/grepnest/grepnest/internal/authn"
	"github.com/grepnest/grepnest/internal/graphartifact"
	graphv1 "github.com/grepnest/grepnest/internal/graphartifact/v1"
	"github.com/grepnest/grepnest/internal/postgres"
	"github.com/grepnest/grepnest/internal/repository"
	"github.com/grepnest/grepnest/pkg/api"
	"google.golang.org/protobuf/proto"
)

func TestUploadExternalAuthorizesBeforeParsing(t *testing.T) {
	store := &fakeStore{repository: readyRepository(101, testCommit)}
	service := Service{Store: store, Limits: testArtifactLimits()}
	_, err := service.UploadExternal(t.Context(), authn.Principal{InstallationID: 10, RepositoryIDs: []int64{101}}, 101, testCommit, []byte("bad"))
	if !errors.Is(err, ErrForbidden) || store.authorizedCalls != 0 {
		t.Fatalf("err=%v authorizedCalls=%d", err, store.authorizedCalls)
	}
}

func TestUploadExternalRequiresAuthorizedCurrentRepository(t *testing.T) {
	store := &fakeStore{repository: readyRepository(101, testCommit), authorizeErr: errUnauthorizedRepository}
	service := Service{Store: store, Limits: testArtifactLimits()}
	if _, err := service.UploadExternal(t.Context(), adminPrincipal(101), 101, testCommit, validArtifactBytes(t, 101)); !errors.Is(err, errUnauthorizedRepository) || store.replaced {
		t.Fatalf("err=%v replaced=%v", err, store.replaced)
	}
	store.authorizeErr = nil
	store.repository.IndexedSHA = strings.Repeat("b", 40)
	if _, err := service.UploadExternal(t.Context(), adminPrincipal(101), 101, testCommit, validArtifactBytes(t, 101)); !errors.Is(err, ErrNotIndexed) || store.replaced {
		t.Fatalf("err=%v replaced=%v", err, store.replaced)
	}
}

func TestUploadExternalRejectsInvalidArtifact(t *testing.T) {
	store := &fakeStore{repository: readyRepository(101, testCommit)}
	service := Service{Store: store, Limits: testArtifactLimits()}
	if _, err := service.UploadExternal(t.Context(), adminPrincipal(101), 101, testCommit, []byte("bad")); !errors.Is(err, ErrInvalidArtifact) || store.replaced {
		t.Fatalf("err=%v replaced=%v", err, store.replaced)
	}
}

func TestUploadExternalRevalidatesAfterParse(t *testing.T) {
	store := &fakeStore{repository: readyRepository(101, testCommit)}
	service := Service{Store: store, Limits: testArtifactLimits()}
	store.afterAuthorize = func() { store.repository.IndexedSHA = strings.Repeat("b", 40) }
	_, err := service.UploadExternal(t.Context(), adminPrincipal(101), 101, testCommit, validArtifactBytes(t, 101))
	if !errors.Is(err, ErrNotIndexed) || store.replaced {
		t.Fatalf("err=%v replaced=%v", err, store.replaced)
	}
}

func TestStatusAllowsAuthorizedUser(t *testing.T) {
	store := &fakeStore{repository: readyRepository(101, testCommit), status: api.GraphStatus{RepositoryID: 101, Commit: testCommit, State: "ready", Source: "external"}}
	got, err := (&Service{Store: store}).Status(t.Context(), authn.Principal{InstallationID: 10, RepositoryIDs: []int64{101}}, 101)
	if err != nil || got.State != "ready" || got.Source != "external" {
		t.Fatalf("status=%#v err=%v", got, err)
	}
}

func TestStatusBoundsStorageError(t *testing.T) {
	store := &fakeStore{repository: readyRepository(101, testCommit), statusErr: errors.New("database password")}
	_, err := (&Service{Store: store}).Status(t.Context(), authn.Principal{InstallationID: 10, RepositoryIDs: []int64{101}}, 101)
	if !errors.Is(err, ErrUnavailable) || strings.Contains(err.Error(), "password") {
		t.Fatalf("err=%v", err)
	}
}

func TestStatusPreservesGraphStates(t *testing.T) {
	for _, status := range []api.GraphStatus{
		{RepositoryID: 101, State: "not_indexed"},
		{RepositoryID: 101, Commit: testCommit, State: "pending"},
		{RepositoryID: 101, Commit: testCommit, State: "fallback", SCIPFallback: &api.SCIPFallbackStatus{Commit: testCommit}},
		{RepositoryID: 101, Commit: testCommit, State: "degraded", ErrorCode: "parse_failed", SCIPFallback: &api.SCIPFallbackStatus{Commit: testCommit}},
	} {
		t.Run(status.State, func(t *testing.T) {
			store := &fakeStore{repository: readyRepository(101, testCommit), status: status}
			got, err := (&Service{Store: store}).Status(t.Context(), authn.Principal{InstallationID: 10, RepositoryIDs: []int64{101}}, 101)
			if err != nil || got != status {
				t.Fatalf("status=%#v err=%v", got, err)
			}
		})
	}
}

const testCommit = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

var errUnauthorizedRepository = errors.New("unauthorized repository")

type fakeStore struct {
	repository      repository.Repository
	authorizeErr    error
	status          api.GraphStatus
	statusErr       error
	afterAuthorize  func()
	authorizedCalls int
	replaced        bool
}

func (store *fakeStore) AuthorizedRepository(_ context.Context, _ int64, _ []int64, _ int64) (repository.Repository, error) {
	store.authorizedCalls++
	if store.afterAuthorize != nil && store.authorizedCalls == 1 {
		defer store.afterAuthorize()
	}
	return store.repository, store.authorizeErr
}

func (store *fakeStore) ReplaceGraph(_ context.Context, _ int64, _ postgres.GraphSource, _ graphartifact.Artifact) (postgres.GraphReplacement, error) {
	store.replaced = true
	return postgres.GraphReplacement{Applied: true}, nil
}

func (store *fakeStore) GraphStatus(_ context.Context, _ int64) (api.GraphStatus, error) {
	return store.status, store.statusErr
}

func readyRepository(githubID int64, sha string) repository.Repository {
	return repository.Repository{ID: 1, GitHubID: githubID, InstallationID: 10, IndexedSHA: sha}
}

func adminPrincipal(repositoryID int64) authn.Principal {
	return authn.Principal{Administrator: true, InstallationID: 10, RepositoryIDs: []int64{repositoryID}}
}

func testArtifactLimits() graphartifact.Limits { return graphartifact.Limits{MaxNodes: 2, MaxEdges: 1} }

func validArtifactBytes(t *testing.T, repositoryID int64) []byte {
	t.Helper()
	data, err := proto.Marshal(&graphv1.Artifact{SchemaVersion: 1, RepositoryId: repositoryID, Commit: testCommit,
		ContentHash: bytes.Repeat([]byte{1}, 32), Analyzer: &graphv1.Analyzer{Name: "test", Version: "1"},
		Nodes: []*graphv1.Node{{Uid: "repository", Kind: graphv1.NodeKind_NODE_KIND_REPOSITORY}, {Uid: "symbol", Kind: graphv1.NodeKind_NODE_KIND_SYMBOL, Path: "a.go", Language: "go", QualifiedName: "Thing", Range: &graphv1.Range{EndCharacter: 1}}},
		Edges: []*graphv1.Edge{{SourceUid: "repository", TargetUid: "symbol", Kind: graphv1.EdgeKind_EDGE_KIND_CONTAINS, Path: "a.go", Confidence: 1}}})
	if err != nil {
		t.Fatal(err)
	}
	return data
}
