//go:build integration

package postgres

import (
	"testing"
	"time"

	"github.com/balcsida/graphnest/internal/graphartifact"
	"github.com/jackc/pgx/v5"
)

var graphFactTables = []string{"graph_nodes", "graph_edges", "graph_v2_nodes", "graph_v2_edges", "graph_v2_files", "graph_v2_unresolved", "graph_v2_diagnostics", "graph_v2_discovery"}

// graphFactRows counts the rows of every fact table that belong to a generation.
func graphFactRows(t *testing.T, store *Store, uploadID int64) int {
	t.Helper()
	total := 0
	for _, table := range graphFactTables {
		var count int
		if err := store.pool.QueryRow(t.Context(), `select count(*) from `+table+` where upload_id=$1`, uploadID).Scan(&count); err != nil {
			t.Fatal(err)
		}
		total += count
	}
	return total
}

func retireGraphsEarlier(t *testing.T, store *Store, age time.Duration) {
	t.Helper()
	if _, err := store.pool.Exec(t.Context(), `update graph_uploads set retired_at=now()-$1::interval where not active`, age); err != nil {
		t.Fatal(err)
	}
}

// Retired generations are deleted only after the grace period, a bounded
// number of rows per call, and active generations keep every fact.
func TestPruneRetiredGraphGenerationsAfterGrace(t *testing.T) {
	store, repositoryID := readyGraphStore(t, testSHA('a'))
	oldV1, err := store.ReplaceGraph(t.Context(), repositoryID, GraphSourceManaged, artifactFor(repositoryID, testSHA('a'), "old"))
	if err != nil {
		t.Fatal(err)
	}
	newV1, err := store.ReplaceGraph(t.Context(), repositoryID, GraphSourceManaged, artifactFor(repositoryID, testSHA('a'), "new"))
	if err != nil {
		t.Fatal(err)
	}
	oldV2, err := store.ReplaceGraphV2(t.Context(), repositoryID, GraphPublication{Publisher: "api_token:42"}, storageV2Artifact())
	if err != nil {
		t.Fatal(err)
	}
	next := storageV2Artifact()
	next.Nodes[0].Name, next.ContentHash = "Renamed", nil
	if next.ContentHash, err = graphartifact.SemanticHashV2(next, graphartifact.Limits{}); err != nil {
		t.Fatal(err)
	}
	newV2, err := store.ReplaceGraphV2(t.Context(), repositoryID, GraphPublication{Publisher: "api_token:42", ExpectedActiveID: oldV2.Upload.ID}, next)
	if err != nil || newV2.ReplacedID != oldV2.Upload.ID {
		t.Fatalf("replacement=%#v err=%v", newV2, err)
	}
	retired := map[int64]int{oldV1.Upload.ID: graphFactRows(t, store, oldV1.Upload.ID), oldV2.Upload.ID: graphFactRows(t, store, oldV2.Upload.ID)}
	active := map[int64]int{newV1.Upload.ID: graphFactRows(t, store, newV1.Upload.ID), newV2.Upload.ID: graphFactRows(t, store, newV2.Upload.ID)}
	wantRows := 0
	for id, rows := range retired {
		if rows == 0 {
			t.Fatalf("retired generation %d has no facts to prune", id)
		}
		wantRows += rows
	}

	// Inside the grace period a request pinned to the replaced generation
	// still reads it.
	pruned, err := store.PruneRetiredGraphGenerations(t.Context(), time.Hour, 1000)
	if err != nil || pruned != (GraphPruned{}) {
		t.Fatalf("pruned inside the grace period=%#v err=%v", pruned, err)
	}
	if old, err := store.LoadGraph(t.Context(), oldV1.Upload.ID); err != nil || old.Analyzer.Name != "old" {
		t.Fatalf("retired v1 generation inside the grace period=%#v err=%v", old.Analyzer, err)
	}
	if _, err := store.LoadGraphV2(t.Context(), repositoryID, oldV2.Upload.ID); err != nil {
		t.Fatalf("retired v2 generation inside the grace period: %v", err)
	}

	retireGraphsEarlier(t, store, 2*time.Hour)
	var total GraphPruned
	for calls := 0; ; calls++ {
		pruned, err := store.PruneRetiredGraphGenerations(t.Context(), time.Hour, 3)
		if err != nil {
			t.Fatal(err)
		}
		if pruned.Rows > 3 || pruned.Generations > 1 {
			t.Fatalf("one call pruned %#v, want at most 3 rows of one generation", pruned)
		}
		if pruned == (GraphPruned{}) {
			break
		}
		if calls > 100 {
			t.Fatal("pruning does not finish")
		}
		total.Generations += pruned.Generations
		total.Rows += pruned.Rows
	}
	if total.Generations != 2 || total.Rows != int64(wantRows) {
		t.Fatalf("pruned %#v, want 2 generations and %d rows", total, wantRows)
	}
	for id := range retired {
		var exists bool
		if err := store.pool.QueryRow(t.Context(), `select exists(select 1 from graph_uploads where id=$1)`, id).Scan(&exists); err != nil || exists {
			t.Fatalf("retired generation %d still stored: %v err=%v", id, exists, err)
		}
		if rows := graphFactRows(t, store, id); rows != 0 {
			t.Fatalf("retired generation %d keeps %d facts", id, rows)
		}
	}
	for id, rows := range active {
		if got := graphFactRows(t, store, id); got != rows {
			t.Fatalf("active generation %d has %d facts, want %d", id, got, rows)
		}
	}
	assertActiveCount(t, store, repositoryID, 2)
	if kept, err := store.LoadGraph(t.Context(), newV1.Upload.ID); err != nil || kept.Analyzer.Name != "new" {
		t.Fatalf("active v1 generation=%#v err=%v", kept.Analyzer, err)
	}
	if _, err := store.LoadGraphV2(t.Context(), repositoryID, newV2.Upload.ID); err != nil {
		t.Fatalf("active v2 generation: %v", err)
	}
	if _, err := store.LoadGraphV2(t.Context(), repositoryID, oldV2.Upload.ID); err != pgx.ErrNoRows {
		t.Fatalf("pruned v2 generation err=%v, want no rows", err)
	}
}

// A pass that stops part-way leaves the generation row in place, so the next
// pass finishes it and nothing reads a half-deleted generation as complete.
func TestPruneRetiredGraphGenerationsResumes(t *testing.T) {
	store, repositoryID := readyGraphStore(t, testSHA('a'))
	old, err := store.ReplaceGraphV2(t.Context(), repositoryID, GraphPublication{Publisher: "api_token:42"}, storageV2Artifact())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.pool.Exec(t.Context(), `update graph_uploads set active=false, retired_at=now()-interval '2 hours' where id=$1`, old.Upload.ID); err != nil {
		t.Fatal(err)
	}
	rows := graphFactRows(t, store, old.Upload.ID)
	pruned, err := store.PruneRetiredGraphGenerations(t.Context(), time.Hour, rows-1)
	if err != nil || pruned.Generations != 0 || pruned.Rows != int64(rows-1) {
		t.Fatalf("first pass=%#v err=%v, want %d rows and no generation", pruned, err, rows-1)
	}
	if left := graphFactRows(t, store, old.Upload.ID); left != 1 {
		t.Fatalf("facts left=%d, want 1", left)
	}
	pruned, err = store.PruneRetiredGraphGenerations(t.Context(), time.Hour, rows)
	if err != nil || pruned.Generations != 1 || pruned.Rows != 1 {
		t.Fatalf("second pass=%#v err=%v", pruned, err)
	}
}
