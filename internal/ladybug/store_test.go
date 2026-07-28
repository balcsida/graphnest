//go:build system_ladybug

package ladybug

import (
	"bytes"
	"errors"
	"testing"

	"github.com/grepnest/grepnest/internal/graphartifact"
)

func TestReplaceRepositoryCreatesNodesBeforeEdges(t *testing.T) {
	db := testDatabase(t, Options{})
	if err := db.ReplaceRepository(t.Context(), manifestA(), artifactA()); err != nil {
		t.Fatal(err)
	}
	assertGraphCounts(t, db, 1, 1, 1, 2)
}

func TestManifestsReturnsMoreThanOneResultPage(t *testing.T) {
	db := testDatabase(t, Options{})
	rows := make([]map[string]any, defaultMaxRows+1)
	for i := range rows {
		rows[i] = map[string]any{"id": int64(i + 1), "name": "repo", "commit": artifactA().Commit, "upload_id": int64(i + 1)}
	}
	err := db.Update(t.Context(), func(session *Session) error {
		return executeStore(t.Context(), session, `UNWIND $rows AS row CREATE (:Repository {id: row.id, name: row.name, commit: row.commit, upload_id: row.upload_id, schema_version: $schema_version, source: $source, content_hash: BLOB($content_hash)})`, map[string]any{
			"rows": rows, "schema_version": int32(1), "source": "test", "content_hash": blobLiteral(artifactA().ContentHash),
		})
	})
	if err != nil {
		t.Fatal(err)
	}
	got, err := db.Manifests(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != len(rows) {
		t.Fatalf("manifest count = %d, want %d", len(got), len(rows))
	}
}

func TestReplaceRepositoryCompletelyReplacesSubgraph(t *testing.T) {
	db := seededDatabase(t, artifactA())
	if err := db.ReplaceRepository(t.Context(), manifestB(), artifactB()); err != nil {
		t.Fatal(err)
	}
	assertFileCount(t, db, 101, "a.go", 0)
	assertFileCount(t, db, 101, "b.go", 1)
	got, err := db.Manifests(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[101].Commit != manifestB().Commit {
		t.Fatalf("manifests = %#v", got)
	}
}

func TestReplaceRepositoryIsolatesRepositories(t *testing.T) {
	db := seededDatabase(t, artifactA())
	otherManifest, otherArtifact := manifestA(), artifactA()
	otherManifest.RepositoryID, otherManifest.UploadID = 202, 22
	otherArtifact.RepositoryID = 202
	otherArtifact.Nodes[0].UID = "repository:202"
	for i := range otherArtifact.Edges {
		if otherArtifact.Edges[i].SourceUID == "repository:101" {
			otherArtifact.Edges[i].SourceUID = "repository:202"
		}
	}
	if err := db.ReplaceRepository(t.Context(), otherManifest, otherArtifact); err != nil {
		t.Fatal(err)
	}
	if err := db.ReplaceRepository(t.Context(), manifestB(), artifactB()); err != nil {
		t.Fatal(err)
	}
	got, err := db.Manifests(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[202].UploadID != 22 {
		t.Fatalf("manifests = %#v", got)
	}
	assertRepositoryNodeCount(t, db, 202, 3)
}

func TestDeleteRepositoryRemovesOnlyRequestedSubgraph(t *testing.T) {
	db := seededDatabase(t, artifactA())
	otherManifest, otherArtifact := manifestA(), artifactA()
	otherManifest.RepositoryID, otherManifest.UploadID = 202, 22
	otherArtifact.RepositoryID = 202
	otherArtifact.Nodes[0].UID = "repository:202"
	for i := range otherArtifact.Edges {
		if otherArtifact.Edges[i].SourceUID == "repository:101" {
			otherArtifact.Edges[i].SourceUID = "repository:202"
		}
	}
	if err := db.ReplaceRepository(t.Context(), otherManifest, otherArtifact); err != nil {
		t.Fatal(err)
	}
	if err := db.DeleteRepository(t.Context(), 101); err != nil {
		t.Fatal(err)
	}
	got, err := db.Manifests(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[202].RepositoryID != 202 {
		t.Fatalf("manifests = %#v", got)
	}
	assertRepositoryNodeCount(t, db, 101, 0)
	assertRepositoryNodeCount(t, db, 202, 3)
}

func TestReplaceRepositoryRollsBackInvalidEdge(t *testing.T) {
	db := seededDatabase(t, artifactA())
	broken := artifactB()
	broken.Edges[0].TargetUID = "missing"
	if err := db.ReplaceRepository(t.Context(), manifestB(), broken); !errors.Is(err, graphartifact.ErrInvalidArtifact) {
		t.Fatalf("error = %v, want ErrInvalidArtifact", err)
	}
	assertManifestA(t, db)
	assertFileCount(t, db, 101, "a.go", 1)
	assertFileCount(t, db, 101, "b.go", 0)
}

func TestReplaceRepositoryRollsBackDuplicate(t *testing.T) {
	db := seededDatabase(t, artifactA())
	broken := artifactB()
	broken.Nodes = append(broken.Nodes, broken.Nodes[1])
	if err := db.ReplaceRepository(t.Context(), manifestB(), broken); !errors.Is(err, graphartifact.ErrInvalidArtifact) {
		t.Fatalf("error = %v, want ErrInvalidArtifact", err)
	}
	assertManifestA(t, db)
	assertFileCount(t, db, 101, "a.go", 1)
}

func TestReplaceRepositoryRejectsMismatchedManifest(t *testing.T) {
	db := seededDatabase(t, artifactA())
	for _, mutate := range []func(*graphartifact.Manifest){
		func(m *graphartifact.Manifest) { m.RepositoryID++ },
		func(m *graphartifact.Manifest) { m.Commit = artifactA().Commit },
		func(m *graphartifact.Manifest) { m.SchemaVersion++ },
		func(m *graphartifact.Manifest) { m.ContentHash[0]++ },
	} {
		manifest := manifestB()
		manifest.ContentHash = bytes.Clone(manifest.ContentHash)
		mutate(&manifest)
		if err := db.ReplaceRepository(t.Context(), manifest, artifactB()); !errors.Is(err, graphartifact.ErrInvalidArtifact) {
			t.Fatalf("error = %v, want ErrInvalidArtifact", err)
		}
		assertManifestA(t, db)
	}
}

func seededDatabase(t *testing.T, artifact graphartifact.Artifact) *Database {
	t.Helper()
	db := testDatabase(t, Options{})
	if err := db.ReplaceRepository(t.Context(), manifestA(), artifact); err != nil {
		t.Fatal(err)
	}
	return db
}

func artifactA() graphartifact.Artifact {
	return testArtifact(
		"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		1,
		"a.go",
		"pkg.A",
	)
}

func artifactB() graphartifact.Artifact {
	return testArtifact(
		"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
		2,
		"b.go",
		"pkg.B",
	)
}

func testArtifact(commit string, hashByte byte, path, symbol string) graphartifact.Artifact {
	repositoryUID := "repository:101"
	fileUID := "file:" + path
	symbolUID := "symbol:" + symbol
	return graphartifact.Artifact{
		SchemaVersion: 1,
		Analyzer:      graphartifact.Analyzer{Name: "test", Version: "1"},
		RepositoryID:  101,
		Commit:        commit,
		ContentHash:   bytes.Repeat([]byte{hashByte}, 32),
		Nodes: []graphartifact.Node{
			{UID: repositoryUID, Kind: graphartifact.NodeRepository, QualifiedName: "acme/repo"},
			{UID: fileUID, Kind: graphartifact.NodeFile, Path: path},
			{UID: symbolUID, Kind: graphartifact.NodeSymbol, Path: path, Language: "go", SymbolKind: "type", QualifiedName: symbol},
		},
		Edges: []graphartifact.Edge{
			{SourceUID: repositoryUID, TargetUID: fileUID, Kind: graphartifact.EdgeContains, Path: path, Confidence: 1},
			{SourceUID: fileUID, TargetUID: symbolUID, Kind: graphartifact.EdgeContains, Path: path, Confidence: 1},
		},
	}
}

func manifestA() graphartifact.Manifest {
	artifact := artifactA()
	return graphartifact.Manifest{RepositoryID: 101, UploadID: 11, Commit: artifact.Commit, Source: "scip", SchemaVersion: artifact.SchemaVersion, ContentHash: bytes.Clone(artifact.ContentHash)}
}

func manifestB() graphartifact.Manifest {
	artifact := artifactB()
	return graphartifact.Manifest{RepositoryID: 101, UploadID: 12, Commit: artifact.Commit, Source: "external", SchemaVersion: artifact.SchemaVersion, ContentHash: bytes.Clone(artifact.ContentHash)}
}

func assertManifestA(t *testing.T, db *Database) {
	t.Helper()
	got, err := db.Manifests(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[101].Commit != manifestA().Commit || got[101].UploadID != manifestA().UploadID {
		t.Fatalf("manifests = %#v", got)
	}
}

func assertGraphCounts(t *testing.T, db *Database, repositories, files, symbols, edges int64) {
	t.Helper()
	var got []int64
	err := db.View(t.Context(), func(session *Session) error {
		for _, query := range []string{
			`MATCH (n:Repository) RETURN count(n)`,
			`MATCH (n:File) RETURN count(n)`,
			`MATCH (n:Symbol) RETURN count(n)`,
			`MATCH ()-[r:CONTAINS]->() RETURN count(r)`,
		} {
			result, err := session.Execute(t.Context(), query, nil, QueryLimits{})
			if err != nil {
				return err
			}
			got = append(got, result.Rows[0][0].(int64))
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	want := []int64{repositories, files, symbols, edges}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("counts = %v, want %v", got, want)
		}
	}
}

func assertFileCount(t *testing.T, db *Database, repositoryID int64, path string, want int64) {
	t.Helper()
	var got int64
	err := db.View(t.Context(), func(session *Session) error {
		result, err := session.Execute(t.Context(), `MATCH (n:File) WHERE n.repository_id = $id AND n.path = $path RETURN count(n)`, map[string]any{"id": repositoryID, "path": path}, QueryLimits{})
		if err == nil {
			got = result.Rows[0][0].(int64)
		}
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("repository %d path %q count = %d, want %d", repositoryID, path, got, want)
	}
}

func assertRepositoryNodeCount(t *testing.T, db *Database, repositoryID, want int64) {
	t.Helper()
	var got int64
	err := db.View(t.Context(), func(session *Session) error {
		for _, query := range []string{
			`MATCH (n:Repository) WHERE n.id = $id RETURN count(n)`,
			`MATCH (n) WHERE n.repository_id = $id RETURN count(n)`,
		} {
			result, err := session.Execute(t.Context(), query, map[string]any{"id": repositoryID}, QueryLimits{})
			if err != nil {
				return err
			}
			got += result.Rows[0][0].(int64)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("repository %d node count = %d, want %d", repositoryID, got, want)
	}
}
