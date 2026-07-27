//go:build integration

package postgres

import (
	"testing"
)

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
