package graphingest

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/balcsida/graphnest/internal/authn"
	"github.com/balcsida/graphnest/internal/graphartifact"
	graphv1 "github.com/balcsida/graphnest/internal/graphartifact/v1"
	"github.com/balcsida/graphnest/internal/postgres"
	"github.com/balcsida/graphnest/internal/repository"
	"github.com/balcsida/graphnest/pkg/api"
	"github.com/jackc/pgx/v5"
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
	store := &fakeStore{repository: readyRepository(101, testCommit), authorizeErr: pgx.ErrNoRows}
	service := Service{Store: store, Limits: testArtifactLimits()}
	if _, err := service.UploadExternal(t.Context(), adminPrincipal(101), 101, testCommit, validArtifactBytes(t, 101)); !errors.Is(err, pgx.ErrNoRows) || store.replaced {
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

func TestUploadExternalConvertsPublicArtifactRepositoryID(t *testing.T) {
	store := &fakeStore{repository: readyRepository(101, testCommit), status: api.GraphStatus{State: api.GraphStateReady}}
	service := Service{Store: store, Limits: testArtifactLimits()}
	if _, err := service.UploadExternal(t.Context(), adminPrincipal(101), 101, testCommit, validArtifactBytes(t, 101)); err != nil {
		t.Fatal(err)
	}
	if store.replacedRepositoryID != store.repository.ID || store.replacedArtifact.RepositoryID != store.repository.ID {
		t.Fatalf("repositoryID=%d artifact=%#v", store.replacedRepositoryID, store.replacedArtifact)
	}
}

func TestUploadExternalDoesNotReadStatusAfterReplacement(t *testing.T) {
	store := &fakeStore{repository: readyRepository(101, testCommit), statusErr: errors.New("transient status failure")}
	service := Service{Store: store, Limits: testArtifactLimits()}
	status, err := service.UploadExternal(t.Context(), adminPrincipal(101), 101, testCommit, validArtifactBytes(t, 101))
	if err != nil || status.RepositoryID != 101 || status.Commit != testCommit || status.State != api.GraphStateReady ||
		status.Source != api.GraphSourceExternal || store.statusCalls != 0 {
		t.Fatalf("status=%#v err=%v statusCalls=%d", status, err, store.statusCalls)
	}
}

func TestUploadExternalRejectsFinalStaleReplacement(t *testing.T) {
	store := &fakeStore{repository: readyRepository(101, testCommit), replacement: postgres.GraphReplacement{Applied: false}, replacementSet: true}
	service := Service{Store: store, Limits: testArtifactLimits()}
	if _, err := service.UploadExternal(t.Context(), adminPrincipal(101), 101, testCommit, validArtifactBytes(t, 101)); !errors.Is(err, ErrNotIndexed) {
		t.Fatalf("err=%v", err)
	}
}

func TestStatusAllowsAuthorizedUser(t *testing.T) {
	store := &fakeStore{repository: readyRepository(101, testCommit), status: api.GraphStatus{RepositoryID: 101, Commit: testCommit, State: api.GraphStateReady, Source: api.GraphSourceExternal}}
	got, err := (&Service{Store: store}).Status(t.Context(), authn.Principal{InstallationID: 10, RepositoryIDs: []int64{101}}, 101)
	if err != nil || got.State != api.GraphStateReady || got.Source != api.GraphSourceExternal {
		t.Fatalf("status=%#v err=%v", got, err)
	}
}

func TestAuthorizationErrorsAreBounded(t *testing.T) {
	for _, test := range []struct {
		name string
		err  error
		want error
	}{
		{name: "not found", err: pgx.ErrNoRows, want: pgx.ErrNoRows},
		{name: "backend", err: errors.New("database password"), want: ErrUnavailable},
	} {
		t.Run(test.name, func(t *testing.T) {
			store := &fakeStore{authorizeErr: test.err}
			err := (&Service{Store: store}).ValidateExternalUpload(t.Context(), adminPrincipal(101), 101, testCommit)
			if !errors.Is(err, test.want) || strings.Contains(err.Error(), "password") {
				t.Fatalf("err=%v", err)
			}
		})
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
		{RepositoryID: 101, State: api.GraphStateNotIndexed},
		{RepositoryID: 101, Commit: testCommit, State: api.GraphStatePending},
		{RepositoryID: 101, Commit: testCommit, State: api.GraphStateFallback, SCIPFallback: &api.SCIPFallbackStatus{Commit: testCommit}},
		{RepositoryID: 101, Commit: testCommit, State: api.GraphStateDegraded, ErrorCode: "parse_failed", SCIPFallback: &api.SCIPFallbackStatus{Commit: testCommit}},
	} {
		t.Run(string(status.State), func(t *testing.T) {
			store := &fakeStore{repository: readyRepository(101, testCommit), status: status}
			got, err := (&Service{Store: store}).Status(t.Context(), authn.Principal{InstallationID: 10, RepositoryIDs: []int64{101}}, 101)
			if err != nil || got != status {
				t.Fatalf("status=%#v err=%v", got, err)
			}
		})
	}
}

const testCommit = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

type fakeStore struct {
	repository           repository.Repository
	authorizeErr         error
	status               api.GraphStatus
	statusErr            error
	replacement          postgres.GraphReplacement
	replacementSet       bool
	afterAuthorize       func()
	authorizedCalls      int
	statusCalls          int
	replaced             bool
	replacedRepositoryID int64
	replacedArtifact     graphartifact.Artifact
}

func (store *fakeStore) AuthorizedRepository(_ context.Context, _ int64, _ []int64, _ int64) (repository.Repository, error) {
	store.authorizedCalls++
	if store.afterAuthorize != nil && store.authorizedCalls == 1 {
		defer store.afterAuthorize()
	}
	return store.repository, store.authorizeErr
}

func (store *fakeStore) ReplaceGraph(_ context.Context, repositoryID int64, _ postgres.GraphSource, artifact graphartifact.Artifact) (postgres.GraphReplacement, error) {
	store.replaced = true
	store.replacedRepositoryID, store.replacedArtifact = repositoryID, artifact
	if !store.replacementSet {
		return postgres.GraphReplacement{Applied: true}, nil
	}
	return store.replacement, nil
}

func (store *fakeStore) GraphStatus(_ context.Context, _ int64) (api.GraphStatus, error) {
	store.statusCalls++
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
