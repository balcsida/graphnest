//go:build integration

package postgres

import (
	"errors"
	"testing"

	"github.com/balcsida/graphnest/internal/graphartifact"
	"github.com/balcsida/graphnest/internal/graphquery"
	"github.com/balcsida/graphnest/pkg/api"
	"github.com/jackc/pgx/v5/pgconn"
)

// A SCIP upload publishes the v2 generation the entity, discovery, exploration
// and symbol workflows read, next to the v1 fallback graph the legacy
// workflows read.
func TestReplaceSCIPPublishesV2Generation(t *testing.T) {
	store, repositoryID := readyGraphStore(t, testSHA('a'))
	if err := store.ReplaceSCIP(t.Context(), repositoryID, testSHA('a'), uploadWith("a.go", globalSymbol, definitionRole)); err != nil {
		t.Fatal(err)
	}
	active, err := store.ActiveGraphGeneration(t.Context(), repositoryID)
	if err != nil || active == nil || active.SchemaVersion != 2 || active.Source != api.GraphSourceSCIP || active.Producer != graphartifact.SCIPProducer || active.Commit != testSHA('a') {
		t.Fatalf("active=%#v err=%v", active, err)
	}
	snapshots := []graphquery.QuerySnapshot{{RepositoryID: repositoryID, Commit: testSHA('a')}}
	generations, err := store.EntityGenerations(t.Context(), snapshots)
	if err != nil || len(generations) != 1 || generations[0].UploadID != active.ID || generations[0].Publisher != graphartifact.SCIPProducer || generations[0].NodeCount != 2 || len(generations[0].Capabilities) == 0 {
		t.Fatalf("generations=%#v err=%v", generations, err)
	}
	entities, err := store.QueryEntities(t.Context(), graphquery.EntityQuery{Snapshots: []graphquery.QuerySnapshot{{RepositoryID: repositoryID, UploadID: active.ID, Commit: testSHA('a')}}, Limit: 10})
	if err != nil || len(entities) != 2 {
		t.Fatalf("entities=%#v err=%v", entities, err)
	}
	manifests, err := store.GraphManifests(t.Context())
	if err != nil || len(manifests) != 1 || manifests[0].SchemaVersion != 1 || manifests[0].Source != string(GraphSourceSCIP) {
		t.Fatalf("v1 fallback manifest=%#v err=%v", manifests, err)
	}

	// The same index again is a deduplicated retry, not a new generation.
	if err := store.ReplaceSCIP(t.Context(), repositoryID, testSHA('a'), uploadWith("a.go", globalSymbol, definitionRole)); err != nil {
		t.Fatal(err)
	}
	if again, err := store.ActiveGraphGeneration(t.Context(), repositoryID); err != nil || again == nil || again.ID != active.ID {
		t.Fatalf("retry replaced the generation: %#v err=%v", again, err)
	}
	// A different index replaces the SCIP-derived generation.
	if err := store.ReplaceSCIP(t.Context(), repositoryID, testSHA('a'), uploadWith("b.go", globalSymbol, definitionRole)); err != nil {
		t.Fatal(err)
	}
	replaced, err := store.ActiveGraphGeneration(t.Context(), repositoryID)
	if err != nil || replaced == nil || replaced.ID == active.ID {
		t.Fatalf("new index kept the old generation: %#v err=%v", replaced, err)
	}
	assertActiveCount(t, store, repositoryID, 2)

	// A publisher's generation needs explicit replacement and is then kept by
	// later SCIP uploads.
	published := storageV2Artifact()
	if _, err := store.ReplaceGraphV2(t.Context(), repositoryID, GraphPublication{Publisher: "api_token:42", ExpectedActiveID: replaced.ID}, published); !errors.Is(err, ErrGraphProviderConflict) {
		t.Fatalf("publisher over scip=%v", err)
	}
	takeover, err := store.ReplaceGraphV2(t.Context(), repositoryID, GraphPublication{Publisher: "api_token:42", ExpectedActiveID: replaced.ID, AllowProviderChange: true}, published)
	if err != nil || takeover.ReplacedID != replaced.ID {
		t.Fatalf("takeover=%#v err=%v", takeover, err)
	}
	if err := store.ReplaceSCIP(t.Context(), repositoryID, testSHA('a'), uploadWith("c.go", globalSymbol, definitionRole)); err != nil {
		t.Fatal(err)
	}
	if kept, err := store.ActiveGraphGeneration(t.Context(), repositoryID); err != nil || kept == nil || kept.ID != takeover.Upload.ID {
		t.Fatalf("SCIP upload replaced the publisher's generation: %#v err=%v", kept, err)
	}
	if status, err := store.GraphStatus(t.Context(), repositoryID); err != nil || status.State != api.GraphStateFallback {
		t.Fatalf("v1 status=%#v err=%v", status, err)
	}
}

func TestGraphGenerationSlotsAllowOneActivePerVersion(t *testing.T) {
	store, repositoryID := readyGraphStore(t, testSHA('a'))
	if _, err := store.ReplaceGraph(t.Context(), repositoryID, GraphSourceManaged, artifactFor(repositoryID, testSHA('a'), "managed")); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ReplaceGraphV2(t.Context(), repositoryID, GraphPublication{Publisher: "api_token:42"}, storageV2Artifact()); err != nil {
		t.Fatal(err)
	}
	assertActiveCount(t, store, repositoryID, 2)
	for _, version := range []int{1, 2} {
		_, err := store.pool.Exec(t.Context(), `insert into graph_uploads (repository_id, commit, schema_version, source, analyzer_name, analyzer_version, content_hash, node_count, edge_count, public_repository, producer_name, producer_version, producer_configuration, artifact_header)
			values ($1, $2, $3, 'external', '', '', $4, 0, 0, '101', '', '', '', '')`, repositoryID, testSHA('a'), version, []byte("0123456789abcdef0123456789abcdef"))
		var pgErr *pgconn.PgError
		if !errors.As(err, &pgErr) || pgErr.Code != "23505" {
			t.Fatalf("second active v%d generation err=%v", version, err)
		}
	}
	assertActiveCount(t, store, repositoryID, 2)
}

func assertActiveCount(t *testing.T, store *Store, repositoryID int64, want int) {
	t.Helper()
	var count int
	if err := store.pool.QueryRow(t.Context(), `select count(*) from graph_uploads where repository_id=$1 and active`, repositoryID).Scan(&count); err != nil || count != want {
		t.Fatalf("active generations=%d err=%v, want %d", count, err, want)
	}
}

// A failure while copying the derived generation rolls back the whole upload:
// navigation rows, the v1 fallback and the v2 generation stay as they were.
func TestReplaceSCIPRollsBackWhenTheDerivedGenerationCannotBeStored(t *testing.T) {
	store, repositoryID := readyGraphStore(t, testSHA('a'))
	if err := store.ReplaceSCIP(t.Context(), repositoryID, testSHA('a'), uploadWith("a.go", globalSymbol, definitionRole)); err != nil {
		t.Fatal(err)
	}
	before, err := store.ActiveGraphGeneration(t.Context(), repositoryID)
	if err != nil || before == nil {
		t.Fatalf("active=%#v err=%v", before, err)
	}
	if _, err := store.pool.Exec(t.Context(), `alter table graph_v2_nodes add check (occurrence <> 'file:b.go')`); err != nil {
		t.Fatal(err)
	}
	if err := store.ReplaceSCIP(t.Context(), repositoryID, testSHA('a'), uploadWith("b.go", globalSymbol, definitionRole)); err == nil {
		t.Fatal("upload with an unstorable generation succeeded")
	}
	if occurrence, err := store.OccurrenceAt(t.Context(), repositoryID, testSHA('a'), "a.go", 0, occurrencePosition(1)); err != nil || occurrence.Path != "a.go" {
		t.Fatalf("navigation rows after rollback=%#v err=%v", occurrence, err)
	}
	if after, err := store.ActiveGraphGeneration(t.Context(), repositoryID); err != nil || after == nil || after.ID != before.ID {
		t.Fatalf("generation after rollback=%#v err=%v", after, err)
	}
	assertActiveCount(t, store, repositoryID, 2)
}
