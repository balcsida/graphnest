//go:build integration

package postgres

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/balcsida/graphnest/internal/graphartifact"
	"github.com/balcsida/graphnest/internal/graphprotocol"
	"github.com/balcsida/graphnest/internal/graphquery"
	"github.com/balcsida/graphnest/internal/scipgraph"
	"github.com/balcsida/graphnest/pkg/api"
)

// activeV1 returns the repository's active v1 generation, or zero values.
func activeV1(t *testing.T, store *Store, repositoryID int64) (id int64, source GraphSource) {
	t.Helper()
	rows, err := store.pool.Query(t.Context(), `select id, source from graph_uploads where repository_id=$1 and active and schema_version=1`, repositoryID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		if err := rows.Scan(&id, &source); err != nil {
			t.Fatal(err)
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return id, source
}

// seedSCIPFallbackGraph stores the v1 graph releases before this one derived
// from every SCIP upload, next to whatever v2 generation is active.
func seedSCIPFallbackGraph(t *testing.T, store *Store, repositoryID int64, commit string, upload scipgraph.Upload) int64 {
	t.Helper()
	artifact, err := graphartifact.FromSCIP(graphartifact.SCIPRepository{ID: repositoryID, Commit: commit}, upload.Occurrences, upload.Relationships)
	if err != nil {
		t.Fatal(err)
	}
	tx, err := store.pool.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(t.Context())
	replacement, err := replaceGraph(t.Context(), tx, repositoryID, GraphSourceSCIP, artifact)
	if err != nil || !replacement.Applied {
		t.Fatalf("fallback graph=%#v err=%v", replacement, err)
	}
	if err := tx.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	return replacement.Upload.ID
}

func contextStatus(t *testing.T, store *Store, repositoryID int64, commit string) graphprotocol.ContextResponse {
	t.Helper()
	service := &graphquery.Service{Store: store, Limits: graphquery.Limits{MaxFanout: 10, MaxNodes: 10, MaxEdges: 10}}
	scope := graphprotocol.Scope{SelectedRepositoryID: repositoryID, Repositories: []graphprotocol.RepositorySnapshot{{ID: repositoryID, Name: "acme/repo", Commit: commit}}}
	result, err := service.Context(t.Context(), graphprotocol.ContextRequest{Scope: scope, UID: "symbol:" + globalSymbol, Relations: []string{"references"}})
	if err != nil {
		t.Fatal(err)
	}
	return result
}

// A SCIP upload whose v2 generation is stored derives no v1 graph: context,
// impact and trace answer from the generation.
func TestReplaceSCIPDerivesNoV1GraphWhenTheGenerationFits(t *testing.T) {
	store, repositoryID := readyGraphStore(t, testSHA('a'))
	if err := store.ReplaceSCIP(t.Context(), repositoryID, testSHA('a'), uploadWith("a.go", globalSymbol, definitionRole)); err != nil {
		t.Fatal(err)
	}
	var v1 int
	if err := store.pool.QueryRow(t.Context(), `select count(*) from graph_uploads where repository_id=$1 and schema_version=1`, repositoryID).Scan(&v1); err != nil || v1 != 0 {
		t.Fatalf("v1 generations=%d err=%v, want none", v1, err)
	}
	generation, err := scipGeneration(t, store, repositoryID)
	if err != nil || generation == nil {
		t.Fatalf("generation=%#v err=%v", generation, err)
	}
	manifests, err := store.Manifests(t.Context())
	if err != nil || manifests[repositoryID].UploadID != generation.ID || manifests[repositoryID].SchemaVersion != 2 {
		t.Fatalf("manifest=%#v err=%v", manifests[repositoryID], err)
	}
	if result := contextStatus(t, store, repositoryID, testSHA('a')); result.Status != graphprotocol.StatusFound {
		t.Fatalf("context=%#v", result)
	}
}

// A SCIP upload whose v2 generation is dropped keeps the v1 graph, and context
// answers from it. Once a later index fits, the v1 graph is retired.
func TestReplaceSCIPKeepsV1GraphWhenTheGenerationIsDropped(t *testing.T) {
	store, repositoryID := readyGraphStore(t, testSHA('a'))
	store.SCIPGraphLimits = graphartifact.Limits{MaxNodes: 1}
	if err := store.ReplaceSCIP(t.Context(), repositoryID, testSHA('a'), uploadWith("a.go", globalSymbol, definitionRole)); err != nil {
		t.Fatal(err)
	}
	fallback, source := activeV1(t, store, repositoryID)
	if fallback == 0 || source != GraphSourceSCIP {
		t.Fatalf("v1 generation=%d source=%q, want the SCIP fallback", fallback, source)
	}
	if generation, err := scipGeneration(t, store, repositoryID); err != nil || generation != nil {
		t.Fatalf("dropped generation is active: %#v err=%v", generation, err)
	}
	manifests, err := store.Manifests(t.Context())
	if err != nil || manifests[repositoryID].UploadID != fallback || manifests[repositoryID].SchemaVersion != 1 {
		t.Fatalf("manifest=%#v err=%v", manifests[repositoryID], err)
	}
	if result := contextStatus(t, store, repositoryID, testSHA('a')); result.Status != graphprotocol.StatusFound {
		t.Fatalf("context from the v1 fallback=%#v", result)
	}

	store.SCIPGraphLimits = graphartifact.Limits{}
	if err := store.ReplaceSCIP(t.Context(), repositoryID, testSHA('a'), uploadWith("b.go", globalSymbol, definitionRole)); err != nil {
		t.Fatal(err)
	}
	if id, _ := activeV1(t, store, repositoryID); id != 0 {
		t.Fatalf("v1 generation %d still active beside the v2 generation", id)
	}
	var retired bool
	if err := store.pool.QueryRow(t.Context(), `select not active and retired_at is not null from graph_uploads where id=$1`, fallback).Scan(&retired); err != nil || !retired {
		t.Fatalf("v1 fallback retired=%v err=%v", retired, err)
	}
	if result := contextStatus(t, store, repositoryID, testSHA('a')); result.Status != graphprotocol.StatusFound {
		t.Fatalf("context from the v2 generation=%#v", result)
	}
}

// A scanner or uploaded v1 graph is never retired by a SCIP upload or by the
// background retirement.
func TestSCIPNeverRetiresManagedOrExternalV1Graphs(t *testing.T) {
	for _, source := range []GraphSource{GraphSourceManaged, GraphSourceExternal} {
		t.Run(string(source), func(t *testing.T) {
			store, repositoryID := readyGraphStore(t, testSHA('a'))
			explicit, err := store.ReplaceGraph(t.Context(), repositoryID, source, artifactFor(repositoryID, testSHA('a'), "explicit"))
			if err != nil {
				t.Fatal(err)
			}
			if err := store.ReplaceSCIP(t.Context(), repositoryID, testSHA('a'), uploadWith("a.go", globalSymbol, definitionRole)); err != nil {
				t.Fatal(err)
			}
			if retired, err := store.RetireSupersededSCIPGraphs(t.Context(), 100); err != nil || retired != 0 {
				t.Fatalf("retired=%d err=%v", retired, err)
			}
			if id, kept := activeV1(t, store, repositoryID); id != explicit.Upload.ID || kept != source {
				t.Fatalf("v1 generation=%d source=%q, want %d %q", id, kept, explicit.Upload.ID, source)
			}
			if pruned, err := store.PruneRetiredGraphGenerations(t.Context(), 0, 1000); err != nil || pruned != (GraphPruned{}) {
				t.Fatalf("pruned=%#v err=%v", pruned, err)
			}
			if loaded, err := store.LoadGraph(t.Context(), explicit.Upload.ID); err != nil || len(loaded.Nodes) != 2 {
				t.Fatalf("explicit graph=%#v err=%v", loaded, err)
			}
		})
	}
}

// SCIP-derived v1 graphs written before this release are retired in bounded
// batches where the repository has a SCIP-derived v2 generation, and only
// there.
func TestRetireSupersededSCIPGraphs(t *testing.T) {
	store := migratedStore(t)
	upload := uploadWith("a.go", globalSymbol, definitionRole)
	var superseded []int64
	for _, githubID := range []int64{101, 102, 103} {
		repositoryID := seedReadyRepository(t, store, githubID, testSHA('a'))
		if err := store.ReplaceSCIP(t.Context(), repositoryID, testSHA('a'), upload); err != nil {
			t.Fatal(err)
		}
		superseded = append(superseded, seedSCIPFallbackGraph(t, store, repositoryID, testSHA('a'), upload))
	}
	// Its generation was dropped: the v1 graph is all it has.
	only := seedReadyRepository(t, store, 104, testSHA('a'))
	store.SCIPGraphLimits = graphartifact.Limits{MaxNodes: 1}
	if err := store.ReplaceSCIP(t.Context(), only, testSHA('a'), upload); err != nil {
		t.Fatal(err)
	}
	store.SCIPGraphLimits = graphartifact.Limits{}
	onlyFallback, _ := activeV1(t, store, only)
	// A generation for an older commit does not answer for the indexed one.
	moved := seedReadyRepository(t, store, 105, testSHA('a'))
	if err := store.ReplaceSCIP(t.Context(), moved, testSHA('a'), upload); err != nil {
		t.Fatal(err)
	}
	if _, err := store.pool.Exec(t.Context(), `update repositories set indexed_sha=$2 where id=$1`, moved, testSHA('b')); err != nil {
		t.Fatal(err)
	}
	movedFallback := seedSCIPFallbackGraph(t, store, moved, testSHA('b'), upload)

	for _, want := range []int64{2, 1, 0} {
		retired, err := store.RetireSupersededSCIPGraphs(t.Context(), 2)
		if err != nil || retired != want {
			t.Fatalf("retired=%d err=%v, want %d", retired, err, want)
		}
	}
	for _, id := range superseded {
		var retired bool
		if err := store.pool.QueryRow(t.Context(), `select not active and retired_at is not null from graph_uploads where id=$1`, id).Scan(&retired); err != nil || !retired {
			t.Fatalf("superseded v1 graph %d retired=%v err=%v", id, retired, err)
		}
	}
	for repositoryID, want := range map[int64]int64{only: onlyFallback, moved: movedFallback} {
		if id, source := activeV1(t, store, repositoryID); id != want || source != GraphSourceSCIP {
			t.Fatalf("repository %d v1 generation=%d source=%q, want %d", repositoryID, id, source, want)
		}
	}
	if result := contextStatus(t, store, only, testSHA('a')); result.Status != graphprotocol.StatusFound {
		t.Fatalf("context from the remaining v1 fallback=%#v", result)
	}

	// The retired graphs are then pruned like any other retired generation.
	retireGraphsEarlier(t, store, 2*time.Hour)
	for range superseded {
		if pruned, err := store.PruneRetiredGraphGenerations(t.Context(), time.Hour, 1000); err != nil || pruned.Generations != 1 {
			t.Fatalf("pruned=%#v err=%v", pruned, err)
		}
	}
	var left int
	if err := store.pool.QueryRow(t.Context(), `select count(*) from graph_uploads where id=any($1)`, superseded).Scan(&left); err != nil || left != 0 {
		t.Fatalf("superseded v1 graphs left=%d err=%v", left, err)
	}
}

// The migration 019 backfill runs again when its version row is missing. It
// must not bring back the v1 graph of a repository that has a generation.
func TestMigrateLegacyBackfillDoesNotRecreateSupersededGraphs(t *testing.T) {
	store := migratedStore(t)
	upload := uploadWith("a.go", globalSymbol, definitionRole)
	fits := seedReadyRepository(t, store, 101, testSHA('a'))
	if err := store.ReplaceSCIP(t.Context(), fits, testSHA('a'), upload); err != nil {
		t.Fatal(err)
	}
	dropped := seedReadyRepository(t, store, 102, testSHA('a'))
	store.SCIPGraphLimits = graphartifact.Limits{MaxNodes: 1}
	if err := store.ReplaceSCIP(t.Context(), dropped, testSHA('a'), upload); err != nil {
		t.Fatal(err)
	}
	// Its fallback was lost; the backfill still restores this one.
	if _, err := store.pool.Exec(t.Context(), `delete from graph_uploads where repository_id=$1`, dropped); err != nil {
		t.Fatal(err)
	}
	if _, err := store.pool.Exec(t.Context(), `delete from schema_migrations where version=19`); err != nil {
		t.Fatal(err)
	}
	if err := Migrate(t.Context(), store.pool); err != nil {
		t.Fatal(err)
	}
	if id, _ := activeV1(t, store, fits); id != 0 {
		t.Fatalf("backfill recreated v1 graph %d beside a v2 generation", id)
	}
	if id, source := activeV1(t, store, dropped); id == 0 || source != GraphSourceSCIP {
		t.Fatalf("backfill left the repository without a generation: id=%d source=%q", id, source)
	}
}

// Why a generation was dropped is recorded with the upload and reported by the
// repository's graph status, until an upload whose generation is stored.
func TestReplaceSCIPRecordsWhyTheGenerationWasDropped(t *testing.T) {
	store, repositoryID := readyGraphStore(t, testSHA('a'))
	if err := store.ReplaceSCIP(t.Context(), repositoryID, testSHA('a'), uploadWith("a.go", globalSymbol, definitionRole)); err != nil {
		t.Fatal(err)
	}
	if status, err := store.GraphStatus(t.Context(), repositoryID); err != nil || status.SCIPGenerationDropped != "" {
		t.Fatalf("status with a stored generation=%#v err=%v", status, err)
	}
	store.SCIPGraphLimits = graphartifact.Limits{MaxNodes: 1}
	if err := store.ReplaceSCIP(t.Context(), repositoryID, testSHA('a'), uploadWith("b.go", globalSymbol, definitionRole)); err != nil {
		t.Fatal(err)
	}
	const want = "scip graph exceeds generation limits: 2 entities (limit 1), 1 edges (limit 2000000)"
	status, err := store.GraphStatus(t.Context(), repositoryID)
	if err != nil || status.State != api.GraphStateFallback || status.SCIPGenerationDropped != want {
		t.Fatalf("status=%#v err=%v, want dropped reason %q", status, err, want)
	}
	// A scanner graph makes the repository ready; the reason still shows.
	if _, err := store.ReplaceGraph(t.Context(), repositoryID, GraphSourceManaged, artifactFor(repositoryID, testSHA('a'), "managed")); err != nil {
		t.Fatal(err)
	}
	if status, err = store.GraphStatus(t.Context(), repositoryID); err != nil || status.State != api.GraphStateReady || status.SCIPGenerationDropped != want {
		t.Fatalf("ready status=%#v err=%v", status, err)
	}
	store.SCIPGraphLimits = graphartifact.Limits{}
	if err := store.ReplaceSCIP(t.Context(), repositoryID, testSHA('a'), uploadWith("c.go", globalSymbol, definitionRole)); err != nil {
		t.Fatal(err)
	}
	if status, err = store.GraphStatus(t.Context(), repositoryID); err != nil || status.SCIPGenerationDropped != "" {
		t.Fatalf("status after a stored generation=%#v err=%v", status, err)
	}
}

// A graph over the default artifact budget is dropped by default and stored
// once the deployment raises the limit.
func TestReplaceSCIPStoresAGenerationAboveTheDefaultBudget(t *testing.T) {
	store, repositoryID := readyGraphStore(t, testSHA('a'))
	// 530 symbols with documentation at the per-field cap: about 133 MiB.
	upload := scipgraph.Upload{ProjectRoot: "file:///src", IndexerName: "test", IndexerVersion: "1"}
	documentation := strings.Repeat("documented ", (256<<10)/len("documented "))
	for index := range 530 {
		symbol := fmt.Sprintf("scip go example.com/acme v1 pkg/Type%03d#", index)
		upload.Occurrences = append(upload.Occurrences, scipgraph.Occurrence{Path: "a.go", Symbol: symbol, StartLine: int32(index), EndLine: int32(index), EndCharacter: 2, PositionEncoding: 1, Roles: definitionRole})
		upload.Symbols = append(upload.Symbols, scipgraph.SymbolInformation{Symbol: symbol, Documentation: documentation})
	}
	if err := store.ReplaceSCIP(t.Context(), repositoryID, testSHA('a'), upload); err != nil {
		t.Fatal(err)
	}
	status, err := store.GraphStatus(t.Context(), repositoryID)
	if err != nil || !strings.Contains(status.SCIPGenerationDropped, "need more than the 134217728 byte artifact budget") {
		t.Fatalf("status under the default budget=%#v err=%v", status, err)
	}
	if id, source := activeV1(t, store, repositoryID); id == 0 || source != GraphSourceSCIP {
		t.Fatalf("v1 fallback=%d source=%q", id, source)
	}

	store.SCIPGraphLimits = graphartifact.Limits{MaxArtifactBytes: 512 << 20}
	if err := store.ReplaceSCIP(t.Context(), repositoryID, testSHA('a'), upload); err != nil {
		t.Fatal(err)
	}
	generation, err := scipGeneration(t, store, repositoryID)
	if err != nil || generation == nil {
		t.Fatalf("generation under the raised budget=%#v err=%v", generation, err)
	}
	if id, _ := activeV1(t, store, repositoryID); id != 0 {
		t.Fatalf("v1 fallback %d still active", id)
	}
	if status, err = store.GraphStatus(t.Context(), repositoryID); err != nil || status.SCIPGenerationDropped != "" {
		t.Fatalf("status under the raised budget=%#v err=%v", status, err)
	}
	// A stored generation stays loadable whatever limits built it.
	if loaded, err := store.LoadGraphV2(t.Context(), repositoryID, generation.ID); err != nil || len(loaded.Nodes) != 531 {
		t.Fatalf("loaded generation err=%v", err)
	}
}
