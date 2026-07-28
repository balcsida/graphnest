//go:build system_ladybug

package ladybug

import (
	"bytes"
	"errors"
	"strings"
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

func TestManifestsContinuesAfterByteTruncation(t *testing.T) {
	db := testDatabase(t, Options{})
	insertManifestRows(t, db, []map[string]any{
		{"id": int64(1), "name": "repo", "commit": artifactA().Commit, "upload_id": int64(1), "source": strings.Repeat("x", defaultMaxBytes*2/3)},
		{"id": int64(2), "name": "repo", "commit": artifactA().Commit, "upload_id": int64(2), "source": strings.Repeat("x", defaultMaxBytes*2/3)},
	})
	got, err := db.Manifests(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("manifest count = %d, want 2", len(got))
	}
}

func TestManifestsRejectsTruncationWithoutProgress(t *testing.T) {
	db := testDatabase(t, Options{})
	insertManifestRows(t, db, []map[string]any{
		{"id": int64(1), "name": "repo", "commit": artifactA().Commit, "upload_id": int64(1), "source": strings.Repeat("x", defaultMaxBytes)},
	})
	if _, err := db.Manifests(t.Context()); err == nil {
		t.Fatal("Manifests() unexpectedly accepted a page with no readable row")
	}
}

func TestStoreBatchesHaveByteAndRowCeilings(t *testing.T) {
	rows := []map[string]any{
		{"value": strings.Repeat("x", storeBatchBytes/2)},
		{"value": strings.Repeat("x", storeBatchBytes/2)},
	}
	end, err := batchEnd(rows, 0)
	if err != nil {
		t.Fatal(err)
	}
	if end != 1 {
		t.Fatalf("batch end = %d, want 1", end)
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

func TestReplaceRepositoryAcceptsManagedSource(t *testing.T) {
	db := testDatabase(t, Options{})
	manifest := manifestA()
	manifest.Source = "managed"
	if err := db.ReplaceRepository(t.Context(), manifest, artifactA()); err != nil {
		t.Fatal(err)
	}
}

func TestReplaceRepositoryStoresAllLegalRelationshipKinds(t *testing.T) {
	db := testDatabase(t, Options{})
	artifact := artifactWithAllEdges()
	manifest := manifestA()
	manifest.ContentHash = bytes.Clone(artifact.ContentHash)
	if err := db.ReplaceRepository(t.Context(), manifest, artifact); err != nil {
		t.Fatal(err)
	}
	var got []int64
	err := db.View(t.Context(), func(session *Session) error {
		for _, table := range []string{"IMPORTS", "REFERENCES", "CALLS", "EXTENDS", "IMPLEMENTS"} {
			result, err := session.Execute(t.Context(), `MATCH ()-[r:`+table+`]->() RETURN count(r)`, nil, QueryLimits{})
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
	for i, count := range got {
		if count != 1 {
			t.Fatalf("relationship count %d = %d, want 1", i, count)
		}
	}
}

func TestManifestsRoundTripsContentHash(t *testing.T) {
	db := seededDatabase(t, artifactA())
	got, err := db.Manifests(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got[101].ContentHash, manifestA().ContentHash) {
		t.Fatalf("content hash = %x, want %x", got[101].ContentHash, manifestA().ContentHash)
	}
}

func TestManifestIsInvisibleUntilCommitAndRollbackRestoresOld(t *testing.T) {
	db := seededDatabase(t, artifactA())
	written, release, finished := make(chan struct{}), make(chan struct{}), make(chan error, 1)
	rollback := errors.New("rollback")
	go func() {
		finished <- db.Update(t.Context(), func(session *Session) error {
			if err := deleteRepository(t.Context(), session, 101); err != nil {
				return err
			}
			manifest := manifestB()
			if err := executeStore(t.Context(), session, `CREATE (:Repository {id: $id, name: $name, commit: $commit, upload_id: $upload_id, schema_version: $schema_version, source: $source, content_hash: BLOB($content_hash)})`, map[string]any{
				"id": manifest.RepositoryID, "name": "acme/repo", "commit": manifest.Commit, "upload_id": manifest.UploadID,
				"schema_version": int32(manifest.SchemaVersion), "source": manifest.Source, "content_hash": blobLiteral(manifest.ContentHash),
			}); err != nil {
				return err
			}
			close(written)
			<-release
			return rollback
		})
	}()
	<-written
	assertManifestA(t, db)
	close(release)
	if err := <-finished; !errors.Is(err, rollback) {
		t.Fatalf("Update() error = %v, want rollback", err)
	}
	assertManifestA(t, db)
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
		func(m *graphartifact.Manifest) { m.Source = "unknown" },
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

func insertManifestRows(t *testing.T, db *Database, rows []map[string]any) {
	t.Helper()
	if err := db.Update(t.Context(), func(session *Session) error {
		return executeStore(t.Context(), session, `UNWIND $rows AS row CREATE (:Repository {id: row.id, name: row.name, commit: row.commit, upload_id: row.upload_id, schema_version: $schema_version, source: row.source, content_hash: BLOB($content_hash)})`, map[string]any{
			"rows": rows, "schema_version": int32(1), "content_hash": blobLiteral(artifactA().ContentHash),
		})
	}); err != nil {
		t.Fatal(err)
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

func artifactWithAllEdges() graphartifact.Artifact {
	artifact := artifactA()
	artifact.Nodes = append(artifact.Nodes,
		graphartifact.Node{UID: "file:b.go", Kind: graphartifact.NodeFile, Path: "b.go"},
		graphartifact.Node{UID: "symbol:pkg.B", Kind: graphartifact.NodeSymbol, Path: "b.go", Language: "go", SymbolKind: "type", QualifiedName: "pkg.B"},
	)
	artifact.Edges = append(artifact.Edges,
		graphartifact.Edge{SourceUID: "repository:101", TargetUID: "file:b.go", Kind: graphartifact.EdgeContains, Path: "b.go", Confidence: 1},
		graphartifact.Edge{SourceUID: "file:a.go", TargetUID: "file:b.go", Kind: graphartifact.EdgeImports, Path: "b.go", Confidence: 1},
		graphartifact.Edge{SourceUID: "symbol:pkg.A", TargetUID: "symbol:pkg.B", Kind: graphartifact.EdgeReferences, Confidence: 1},
		graphartifact.Edge{SourceUID: "symbol:pkg.A", TargetUID: "symbol:pkg.B", Kind: graphartifact.EdgeCalls, Confidence: 1},
		graphartifact.Edge{SourceUID: "symbol:pkg.A", TargetUID: "symbol:pkg.B", Kind: graphartifact.EdgeExtends, Confidence: 1},
		graphartifact.Edge{SourceUID: "symbol:pkg.A", TargetUID: "symbol:pkg.B", Kind: graphartifact.EdgeImplements, Confidence: 1},
	)
	return artifact
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
