//go:build system_ladybug

package ladybug

import (
	"bytes"
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/grepnest/grepnest/internal/graphartifact"
)

type artifactKey struct {
	repositoryID int64
	uploadID     int64
}

type fakeSource struct {
	mu        sync.Mutex
	manifests []graphartifact.Manifest
	artifacts map[artifactKey]graphartifact.Artifact
	manifest  error
	artifact  error
	calls     []artifactKey
	synced    chan struct{}
}

func (source *fakeSource) GraphManifests(ctx context.Context) ([]graphartifact.Manifest, error) {
	source.mu.Lock()
	defer source.mu.Unlock()
	if source.synced != nil {
		select {
		case source.synced <- struct{}{}:
		default:
		}
	}
	if source.manifest != nil {
		return nil, source.manifest
	}
	return append([]graphartifact.Manifest(nil), source.manifests...), ctx.Err()
}

func (source *fakeSource) GraphArtifact(ctx context.Context, repositoryID, uploadID int64) (graphartifact.Artifact, error) {
	source.mu.Lock()
	defer source.mu.Unlock()
	key := artifactKey{repositoryID, uploadID}
	source.calls = append(source.calls, key)
	if source.artifact != nil {
		return graphartifact.Artifact{}, source.artifact
	}
	return source.artifacts[key], ctx.Err()
}

func TestSyncOnceLoadsNewRepositoriesInIDOrder(t *testing.T) {
	db := testDatabase(t, Options{})
	artifact202 := repositoryArtifact(artifactA(), 202)
	manifest202 := artifactManifest(artifact202, 22, "managed")
	source := &fakeSource{
		manifests: []graphartifact.Manifest{manifest202, manifestA()},
		artifacts: map[artifactKey]graphartifact.Artifact{
			{101, manifestA().UploadID}: artifactA(),
			{202, manifest202.UploadID}: artifact202,
		},
	}
	if err := (&Syncer{Source: source, Database: db}).SyncOnce(t.Context()); err != nil {
		t.Fatal(err)
	}
	want := []artifactKey{{101, manifestA().UploadID}, {202, manifest202.UploadID}}
	if len(source.calls) != len(want) || source.calls[0] != want[0] || source.calls[1] != want[1] {
		t.Fatalf("artifact calls = %v, want %v", source.calls, want)
	}
	got, err := db.Manifests(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("manifest count = %d, want 2", len(got))
	}
}

func TestSyncOnceReplacesChangedUpload(t *testing.T) {
	db := seededDatabase(t, artifactA())
	changed := manifestA()
	changed.UploadID++
	source := &fakeSource{
		manifests: []graphartifact.Manifest{changed},
		artifacts: map[artifactKey]graphartifact.Artifact{{101, changed.UploadID}: artifactA()},
	}
	if err := (&Syncer{Source: source, Database: db}).SyncOnce(t.Context()); err != nil {
		t.Fatal(err)
	}
	got, err := db.Manifests(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if got[101].UploadID != changed.UploadID {
		t.Fatalf("upload ID = %d, want %d", got[101].UploadID, changed.UploadID)
	}
}

func TestSyncOnceReplacesChangedCommit(t *testing.T) {
	db := seededDatabase(t, artifactA())
	source := &fakeSource{
		manifests: []graphartifact.Manifest{manifestB()},
		artifacts: map[artifactKey]graphartifact.Artifact{{101, manifestB().UploadID}: artifactB()},
	}
	if err := (&Syncer{Source: source, Database: db}).SyncOnce(t.Context()); err != nil {
		t.Fatal(err)
	}
	assertFileCount(t, db, 101, "a.go", 0)
	assertFileCount(t, db, 101, "b.go", 1)
}

func TestSyncOnceSkipsUnchangedRepository(t *testing.T) {
	db := seededDatabase(t, artifactA())
	source := &fakeSource{manifests: []graphartifact.Manifest{manifestA()}}
	if err := (&Syncer{Source: source, Database: db}).SyncOnce(t.Context()); err != nil {
		t.Fatal(err)
	}
	if len(source.calls) != 0 {
		t.Fatalf("artifact calls = %v, want none", source.calls)
	}
}

func TestSyncOnceDeletesAbsentRepository(t *testing.T) {
	db := seededDatabase(t, artifactA())
	syncer := Syncer{Source: &fakeSource{}, Database: db}
	if err := syncer.SyncOnce(t.Context()); err != nil {
		t.Fatal(err)
	}
	got, err := db.Manifests(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("manifests = %#v, want none", got)
	}
}

func TestSyncOnceFailurePreservesCurrentAndAbsentRepositories(t *testing.T) {
	db := seededDatabase(t, artifactA())
	artifact202 := repositoryArtifact(artifactA(), 202)
	manifest202 := artifactManifest(artifact202, 22, "managed")
	if err := db.ReplaceRepository(t.Context(), manifest202, artifact202); err != nil {
		t.Fatal(err)
	}
	broken := artifactB()
	broken.ContentHash = bytes.Repeat([]byte{9}, 32)
	source := &fakeSource{
		manifests: []graphartifact.Manifest{manifestB()},
		artifacts: map[artifactKey]graphartifact.Artifact{{101, manifestB().UploadID}: broken},
	}
	if err := (&Syncer{Source: source, Database: db}).SyncOnce(t.Context()); !errors.Is(err, graphartifact.ErrInvalidArtifact) {
		t.Fatalf("error = %v, want ErrInvalidArtifact", err)
	}
	got, err := db.Manifests(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[101].Commit != manifestA().Commit || got[202].Commit != manifest202.Commit {
		t.Fatalf("manifests = %#v, want both originals", got)
	}
}

func TestSyncOnceRejectsDuplicateRepositoriesBeforeMutation(t *testing.T) {
	db := seededDatabase(t, artifactA())
	artifact202 := repositoryArtifact(artifactA(), 202)
	manifest202 := artifactManifest(artifact202, 22, "managed")
	if err := db.ReplaceRepository(t.Context(), manifest202, artifact202); err != nil {
		t.Fatal(err)
	}
	duplicate := manifestB()
	duplicate.UploadID++
	source := &fakeSource{
		manifests: []graphartifact.Manifest{manifestB(), duplicate},
		artifacts: map[artifactKey]graphartifact.Artifact{
			{101, manifestB().UploadID}: artifactB(),
			{101, duplicate.UploadID}:   artifactB(),
		},
	}
	if err := (&Syncer{Source: source, Database: db}).SyncOnce(t.Context()); err == nil {
		t.Fatal("SyncOnce unexpectedly accepted duplicate repositories")
	}
	got, err := db.Manifests(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(source.calls) != 0 || len(got) != 2 || got[101].Commit != manifestA().Commit || got[202].Commit != manifest202.Commit {
		t.Fatalf("calls = %v, manifests = %#v, want no mutation", source.calls, got)
	}
}

func TestRunSyncsImmediatelyAndOnTicker(t *testing.T) {
	db := testDatabase(t, Options{})
	synced := make(chan struct{}, 2)
	source := &fakeSource{synced: synced}
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() {
		done <- (&Syncer{Source: source, Database: db, Interval: time.Millisecond}).Run(ctx)
	}()
	for range 2 {
		select {
		case <-synced:
		case <-time.After(time.Second):
			t.Fatal("Run did not synchronize")
		}
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Run error = %v, want context.Canceled", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Run leaked after cancellation")
	}
}

func TestRunRejectsInvalidIntervalBeforeSync(t *testing.T) {
	db := seededDatabase(t, artifactA())
	source := &fakeSource{manifest: errors.New("unexpected sync")}
	if err := (&Syncer{Source: source, Database: db}).Run(t.Context()); err == nil || errors.Is(err, source.manifest) {
		t.Fatalf("Run error = %v, want interval validation", err)
	}
	assertManifestA(t, db)
}

func repositoryArtifact(artifact graphartifact.Artifact, repositoryID int64) graphartifact.Artifact {
	artifact.RepositoryID = repositoryID
	oldUID := artifact.Nodes[0].UID
	newUID := "repository:202"
	artifact.Nodes[0].UID = newUID
	for i := range artifact.Edges {
		if artifact.Edges[i].SourceUID == oldUID {
			artifact.Edges[i].SourceUID = newUID
		}
	}
	return artifact
}

func artifactManifest(artifact graphartifact.Artifact, uploadID int64, source string) graphartifact.Manifest {
	return graphartifact.Manifest{
		RepositoryID:  artifact.RepositoryID,
		UploadID:      uploadID,
		Commit:        artifact.Commit,
		Source:        source,
		SchemaVersion: artifact.SchemaVersion,
		ContentHash:   bytes.Clone(artifact.ContentHash),
	}
}
