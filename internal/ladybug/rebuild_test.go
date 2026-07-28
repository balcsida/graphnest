//go:build system_ladybug

package ladybug

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/grepnest/grepnest/internal/graphartifact"
)

func TestRebuildLoadsEverySnapshotAndSwapsLiveDatabase(t *testing.T) {
	path := filepath.Join(t.TempDir(), "graph")
	writeDatabase(t, path, manifestA(), artifactA())
	before, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	artifact202 := repositoryArtifact(artifactA(), 202)
	manifest202 := artifactManifest(artifact202, 22, "managed")
	source := &fakeSource{
		manifests: []graphartifact.Manifest{manifest202, manifestB()},
		artifacts: map[artifactKey]graphartifact.Artifact{
			{101, manifestB().UploadID}: artifactB(),
			{202, manifest202.UploadID}: artifact202,
		},
	}
	if err := Rebuild(t.Context(), source, Options{Path: path}); err != nil {
		t.Fatal(err)
	}
	after, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if os.SameFile(before, after) {
		t.Fatal("live database was not atomically replaced")
	}
	db := openDatabase(t, path)
	got, err := db.Manifests(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[101].Commit != manifestB().Commit || got[202].UploadID != manifest202.UploadID {
		t.Fatalf("manifests = %#v, want rebuilt snapshots", got)
	}
	assertNoCandidates(t, path)
}

func TestRebuildVerificationFailurePreservesLiveDatabase(t *testing.T) {
	path := filepath.Join(t.TempDir(), "graph")
	writeDatabase(t, path, manifestA(), artifactA())
	duplicate := manifestB()
	duplicate.UploadID++
	source := &fakeSource{
		manifests: []graphartifact.Manifest{manifestB(), duplicate},
		artifacts: map[artifactKey]graphartifact.Artifact{
			{101, manifestB().UploadID}: artifactB(),
			{101, duplicate.UploadID}:   artifactB(),
		},
	}
	if err := Rebuild(t.Context(), source, Options{Path: path}); err == nil {
		t.Fatal("Rebuild unexpectedly accepted duplicate authoritative manifests")
	}
	assertLiveManifestA(t, path)
	assertNoCandidates(t, path)
}

func TestRebuildLoadFailurePreservesLiveDatabase(t *testing.T) {
	path := filepath.Join(t.TempDir(), "graph")
	writeDatabase(t, path, manifestA(), artifactA())
	want := errors.New("load failed")
	source := &fakeSource{manifests: []graphartifact.Manifest{manifestB()}, artifact: want}
	if err := Rebuild(t.Context(), source, Options{Path: path}); !errors.Is(err, want) {
		t.Fatalf("error = %v, want %v", err, want)
	}
	assertLiveManifestA(t, path)
	assertNoCandidates(t, path)
}

func TestRebuildRejectsSymlinkWithoutFollowingIt(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "target")
	writeDatabase(t, target, manifestA(), artifactA())
	path := filepath.Join(root, "graph")
	if err := os.Symlink(target, path); err != nil {
		t.Fatal(err)
	}
	source := &fakeSource{manifests: []graphartifact.Manifest{manifestB()}}
	if err := Rebuild(t.Context(), source, Options{Path: path}); err == nil {
		t.Fatal("Rebuild unexpectedly followed a symlink")
	}
	assertLiveManifestA(t, target)
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode()&os.ModeSymlink == 0 {
		t.Fatal("live symlink was replaced")
	}
	assertNoCandidates(t, path)
}

func TestRebuildRefusesOpenLiveDatabase(t *testing.T) {
	path := filepath.Join(t.TempDir(), "graph")
	db, err := Open(Options{Path: path})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Error(err)
		}
	})
	if err := db.Update(t.Context(), func(session *Session) error {
		return EnsureSchema(t.Context(), session.connection)
	}); err != nil {
		t.Fatal(err)
	}
	if err := db.ReplaceRepository(t.Context(), manifestA(), artifactA()); err != nil {
		t.Fatal(err)
	}
	source := &fakeSource{
		manifests: []graphartifact.Manifest{manifestB()},
		artifacts: map[artifactKey]graphartifact.Artifact{{101, manifestB().UploadID}: artifactB()},
	}
	if err := Rebuild(t.Context(), source, Options{Path: path}); err == nil {
		t.Fatal("Rebuild unexpectedly replaced an open live database")
	}
	assertManifestA(t, db)
	assertNoCandidates(t, path)
}

func writeDatabase(t *testing.T, path string, manifest graphartifact.Manifest, artifact graphartifact.Artifact) {
	t.Helper()
	db, err := Open(Options{Path: path})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Update(t.Context(), func(session *Session) error {
		return EnsureSchema(t.Context(), session.connection)
	}); err != nil {
		db.Close()
		t.Fatal(err)
	}
	if err := db.ReplaceRepository(t.Context(), manifest, artifact); err != nil {
		db.Close()
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
}

func openDatabase(t *testing.T, path string) *Database {
	t.Helper()
	db, err := Open(Options{Path: path})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Error(err)
		}
	})
	return db
}

func assertLiveManifestA(t *testing.T, path string) {
	t.Helper()
	db := openDatabase(t, path)
	assertManifestA(t, db)
}

func assertNoCandidates(t *testing.T, path string) {
	t.Helper()
	entries, err := os.ReadDir(filepath.Dir(path))
	if err != nil {
		t.Fatal(err)
	}
	prefix := filepath.Base(path) + ".new-"
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), prefix) {
			t.Fatalf("candidate %q was not cleaned up", entry.Name())
		}
	}
}
