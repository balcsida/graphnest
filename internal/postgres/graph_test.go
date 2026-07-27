//go:build integration

package postgres

import (
	"bytes"
	"errors"
	"testing"

	"github.com/grepnest/grepnest/internal/graphartifact"
)

func TestReplaceGraphExternalWins(t *testing.T) {
	store, repositoryID := readyGraphStore(t, testSHA('a'))
	managed := artifactFor(repositoryID, testSHA('a'), "managed")
	if got, err := store.ReplaceGraph(t.Context(), repositoryID, GraphSourceManaged, managed); err != nil || !got.Applied {
		t.Fatalf("managed = %#v, %v", got, err)
	}
	external := artifactFor(repositoryID, testSHA('a'), "external")
	if got, err := store.ReplaceGraph(t.Context(), repositoryID, GraphSourceExternal, external); err != nil || !got.Applied {
		t.Fatalf("external = %#v, %v", got, err)
	}
	if got, err := store.ReplaceGraph(t.Context(), repositoryID, GraphSourceManaged, managed); err != nil || got.Applied {
		t.Fatalf("late managed = %#v, %v", got, err)
	}
	loaded, err := store.LoadGraph(t.Context(), 2)
	if err != nil || loaded.Analyzer.Name != "external" {
		t.Fatalf("load external = %#v, %v", loaded, err)
	}
}

func TestReplaceGraphStaleCommitDoesNotReplaceCurrentUpload(t *testing.T) {
	store, repositoryID := readyGraphStore(t, testSHA('a'))
	current := artifactFor(repositoryID, testSHA('a'), "current")
	if _, err := store.ReplaceGraph(t.Context(), repositoryID, GraphSourceManaged, current); err != nil {
		t.Fatal(err)
	}
	stale := artifactFor(repositoryID, testSHA('b'), "stale")
	if got, err := store.ReplaceGraph(t.Context(), repositoryID, GraphSourceExternal, stale); err != nil || got.Applied {
		t.Fatalf("stale = %#v, %v", got, err)
	}
	loaded, err := store.LoadGraph(t.Context(), 1)
	if err != nil || loaded.Analyzer.Name != "current" {
		t.Fatalf("load current = %#v, %v", loaded, err)
	}
}

func TestReplaceGraphInvalidEdgePreservesOldUpload(t *testing.T) {
	store, repositoryID := readyGraphStore(t, testSHA('a'))
	old := artifactFor(repositoryID, testSHA('a'), "old")
	if _, err := store.ReplaceGraph(t.Context(), repositoryID, GraphSourceManaged, old); err != nil {
		t.Fatal(err)
	}
	invalid := artifactFor(repositoryID, testSHA('a'), "invalid")
	invalid.Edges[0].TargetUID = "missing"
	if _, err := store.ReplaceGraph(t.Context(), repositoryID, GraphSourceExternal, invalid); !errors.Is(err, graphartifact.ErrInvalidArtifact) {
		t.Fatalf("invalid edge error = %v", err)
	}
	loaded, err := store.LoadGraph(t.Context(), 1)
	if err != nil || loaded.Analyzer.Name != "old" {
		t.Fatalf("load old = %#v, %v", loaded, err)
	}
}

func TestReplaceGraphRoundTripsArtifact(t *testing.T) {
	store, repositoryID := readyGraphStore(t, testSHA('a'))
	want := artifactFor(repositoryID, testSHA('a'), "round-trip")
	got, err := store.ReplaceGraph(t.Context(), repositoryID, GraphSourceManaged, want)
	if err != nil || !got.Applied {
		t.Fatalf("replace = %#v, %v", got, err)
	}
	loaded, err := store.LoadGraph(t.Context(), got.Upload.ID)
	if err != nil || !graphArtifactsEqual(loaded, want) {
		t.Fatalf("load = %#v, %v", loaded, err)
	}
}

func readyGraphStore(t *testing.T, sha string) (*Store, int64) {
	t.Helper()
	store := migratedStore(t)
	return store, seedReadyRepository(t, store, 101, sha)
}

func artifactFor(repositoryID int64, commit, analyzer string) graphartifact.Artifact {
	return graphartifact.Artifact{
		SchemaVersion: 1, RepositoryID: repositoryID, Commit: commit, ContentHash: bytes.Repeat([]byte{1}, 32),
		Analyzer: graphartifact.Analyzer{Name: analyzer, Version: "1"},
		Nodes: []graphartifact.Node{
			{UID: "repository", Kind: graphartifact.NodeRepository},
			{UID: "symbol", Kind: graphartifact.NodeSymbol, Path: "a.go", Language: "go", QualifiedName: "Thing", Range: graphartifact.Range{EndCharacter: 1}},
		},
		Edges: []graphartifact.Edge{{SourceUID: "repository", TargetUID: "symbol", Kind: graphartifact.EdgeContains, Path: "a.go", Confidence: 1}},
	}
}

func graphArtifactsEqual(got, want graphartifact.Artifact) bool {
	return got.SchemaVersion == want.SchemaVersion && got.RepositoryID == want.RepositoryID && got.Commit == want.Commit &&
		got.Analyzer == want.Analyzer && bytes.Equal(got.ContentHash, want.ContentHash) && len(got.Nodes) == len(want.Nodes) && len(got.Edges) == len(want.Edges) &&
		got.Nodes[0] == want.Nodes[0] && got.Nodes[1] == want.Nodes[1] && got.Edges[0] == want.Edges[0]
}

func TestGraphSchemaEnforcesArtifactBoundaries(t *testing.T) {
	store := migratedStore(t)
	firstRepositoryID := seedReadyRepository(t, store, 101, testSHA('a'))
	secondRepositoryID := seedReadyRepository(t, store, 102, testSHA('b'))

	var uploadID int64
	if err := store.pool.QueryRow(t.Context(), `insert into graph_uploads
		(repository_id, commit, schema_version, source, analyzer_name, analyzer_version, content_hash, node_count, edge_count)
		values ($1, $2, 1, 'managed', 'test', '1', decode(repeat('01', 32), 'hex'), 2, 1) returning id`, firstRepositoryID, testSHA('a')).Scan(&uploadID); err != nil {
		t.Fatal(err)
	}
	for _, uid := range []string{"repository", "symbol"} {
		if _, err := store.pool.Exec(t.Context(), `insert into graph_nodes
			(upload_id, uid, kind, path, language, symbol_kind, qualified_name, signature, scip_symbol,
			 start_line, start_character, end_line, end_character)
			values ($1, $2, 3, '', '', '', '', '', '', 0, 0, 0, 0)`, uploadID, uid); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := store.pool.Exec(t.Context(), `insert into graph_edges
		(upload_id, source_uid, target_uid, kind, path, start_line, start_character, end_line, end_character, confidence, resolution_reason)
		values ($1, 'repository', 'symbol', 1, '', 0, 0, 0, 0, 1, '')`, uploadID); err != nil {
		t.Fatal(err)
	}

	mustReject := func(query string, arguments ...any) {
		t.Helper()
		if _, err := store.pool.Exec(t.Context(), query, arguments...); err == nil {
			t.Fatalf("invalid graph row accepted: %s", query)
		}
	}
	mustReject(`insert into graph_uploads
		(repository_id, commit, schema_version, source, analyzer_name, analyzer_version, content_hash, node_count, edge_count)
		values ($1, $2, 1, 'invalid', 'test', '1', decode(repeat('01', 32), 'hex'), 0, 0)`, secondRepositoryID, testSHA('b'))
	mustReject(`insert into graph_nodes
		(upload_id, uid, kind, path, language, symbol_kind, qualified_name, signature, scip_symbol,
		 start_line, start_character, end_line, end_character)
		values ($1, 'bad-kind', 0, '', '', '', '', '', '', 0, 0, 0, 0)`, uploadID)
	mustReject(`insert into graph_nodes
		(upload_id, uid, kind, path, language, symbol_kind, qualified_name, signature, scip_symbol,
		 start_line, start_character, end_line, end_character)
		values ($1, 'bad-range', 3, '', '', '', '', '', '', 1, 0, 0, 0)`, uploadID)
	mustReject(`insert into graph_edges
		(upload_id, source_uid, target_uid, kind, path, start_line, start_character, end_line, end_character, confidence, resolution_reason)
		values ($1, 'repository', 'symbol', 0, '', 0, 0, 0, 0, 1, '')`, uploadID)
	mustReject(`insert into graph_edges
		(upload_id, source_uid, target_uid, kind, path, start_line, start_character, end_line, end_character, confidence, resolution_reason)
		values ($1, 'repository', 'symbol', 1, '', 0, 0, 0, 0, 2, '')`, uploadID)
	mustReject(`insert into graph_edges
		(upload_id, source_uid, target_uid, kind, path, start_line, start_character, end_line, end_character, confidence, resolution_reason)
		values ($1, 'repository', 'missing', 1, '', 0, 0, 0, 0, 1, '')`, uploadID)

	var otherUploadID int64
	if err := store.pool.QueryRow(t.Context(), `insert into graph_uploads
		(repository_id, commit, schema_version, source, analyzer_name, analyzer_version, content_hash, node_count, edge_count)
		values ($1, $2, 1, 'managed', 'test', '1', decode(repeat('01', 32), 'hex'), 1, 0) returning id`, secondRepositoryID, testSHA('b')).Scan(&otherUploadID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.pool.Exec(t.Context(), `insert into graph_nodes
		(upload_id, uid, kind, path, language, symbol_kind, qualified_name, signature, scip_symbol,
		 start_line, start_character, end_line, end_character)
		values ($1, 'other', 3, '', '', '', '', '', '', 0, 0, 0, 0)`, otherUploadID); err != nil {
		t.Fatal(err)
	}
	mustReject(`insert into graph_edges
		(upload_id, source_uid, target_uid, kind, path, start_line, start_character, end_line, end_character, confidence, resolution_reason)
		values ($1, 'repository', 'other', 1, '', 0, 0, 0, 0, 1, '')`, uploadID)

	if _, err := store.pool.Exec(t.Context(), `insert into graph_jobs
		(repository_id, target_sha, state, max_attempts, lease_owner, lease_expires_at)
		values ($1, $2, 'queued', 5, null, null), ($1, $2, 'running', 5, 'worker', now())`, secondRepositoryID, testSHA('b')); err != nil {
		t.Fatal(err)
	}
	for _, index := range []string{"graph_jobs_one_queued", "graph_jobs_one_running"} {
		var found bool
		if err := store.pool.QueryRow(t.Context(), `select exists(select 1 from pg_indexes where schemaname=current_schema() and indexname=$1)`, index).Scan(&found); err != nil || !found {
			t.Fatalf("index %s: found=%v err=%v", index, found, err)
		}
	}
	mustReject(`insert into graph_jobs (repository_id, target_sha, state, max_attempts)
		values ($1, $2, 'queued', 5)`, secondRepositoryID, testSHA('b'))
	mustReject(`insert into graph_jobs
		(repository_id, target_sha, state, max_attempts, lease_owner, lease_expires_at)
		values ($1, $2, 'running', 5, 'other-worker', now())`, secondRepositoryID, testSHA('b'))
	if _, err := store.pool.Exec(t.Context(), `delete from repositories where id=$1`, firstRepositoryID); err != nil {
		t.Fatal(err)
	}
	for _, query := range []string{
		`select count(*) from graph_uploads where id=$1`,
		`select count(*) from graph_nodes where upload_id=$1`,
		`select count(*) from graph_edges where upload_id=$1`,
	} {
		var count int
		if err := store.pool.QueryRow(t.Context(), query, uploadID).Scan(&count); err != nil || count != 0 {
			t.Fatalf("cascade query=%q count=%d err=%v", query, count, err)
		}
	}
	if _, err := store.pool.Exec(t.Context(), `delete from repositories where id=$1`, secondRepositoryID); err != nil {
		t.Fatal(err)
	}
	var jobs int
	if err := store.pool.QueryRow(t.Context(), `select count(*) from graph_jobs`).Scan(&jobs); err != nil || jobs != 0 {
		t.Fatalf("graph jobs=%d err=%v", jobs, err)
	}
}
