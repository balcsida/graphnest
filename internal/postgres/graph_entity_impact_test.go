//go:build integration

package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/balcsida/graphnest/internal/graphartifact"
	graphv2 "github.com/balcsida/graphnest/internal/graphartifact/v2"
	"github.com/balcsida/graphnest/internal/graphprotocol"
	"github.com/balcsida/graphnest/internal/graphquery"
	"google.golang.org/protobuf/proto"
)

type entityImpactOracle struct {
	Nodes     []string                    `json:"nodes"`
	Edges     []entityImpactEdge          `json:"edges"`
	EdgeKinds map[string]int              `json:"edge_kinds"`
	Blast     *graphprotocol.BlastSummary `json:"blast"`
}

type entityImpactEdge struct {
	Occurrence string `json:"occurrence"`
	Source     string `json:"source"`
	Target     string `json:"target"`
}

type entityImpactFixture struct {
	Reference             string `json:"reference"`
	CaptureSHA256         string `json:"capture_sha256"`
	VerificationSHA256    string `json:"verification_sha256"`
	LibraryExpectedSHA256 string `json:"library_expected_sha256"`
	Facts                 struct {
		Nodes      int `json:"nodes"`
		Edges      int `json:"edges"`
		Files      int `json:"files"`
		Unresolved int `json:"unresolved"`
	} `json:"facts"`
	IDs struct {
		Run          string `json:"run"`
		Normalize    string `json:"normalize"`
		Service      string `json:"service"`
		ServiceGreet string `json:"service_greet"`
		Home         string `json:"home"`
	} `json:"ids"`
	CallGraph           entityImpactOracle              `json:"call_graph"`
	UsagesNormalize     entityImpactOracle              `json:"usages_normalize"`
	ImpactServiceDepth3 entityImpactOracle              `json:"impact_service_depth_3"`
	ImpactServiceDepth1 entityImpactOracle              `json:"impact_service_depth_1"`
	ImpactServiceGreet  entityImpactOracle              `json:"impact_service_greet"`
	HomeBlast           graphprotocol.BlastSummary      `json:"home_blast"`
	ExportedCore        []string                        `json:"exported_core"`
	Qualified           map[string][]string             `json:"qualified"`
	Modules             []graphprotocol.ModuleDirectory `json:"modules"`
	FilteredCore        entityImpactOracle              `json:"filtered_core_exported"`
}

func loadEntityImpactFixture(t *testing.T) entityImpactFixture {
	t.Helper()
	raw, err := os.ReadFile("../../test/fixtures/codegraph/entity-impact.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixture entityImpactFixture
	if err = json.Unmarshal(raw, &fixture); err != nil {
		t.Fatal(err)
	}
	if fixture.Reference != "b9ca4b7981116909900368cc1686a1074cd4d4c1" ||
		fixture.CaptureSHA256 != "0b8df25e1feb7844f4e1ca4684e0fa290f660a99ba2de6243f3425a132da1727" ||
		fixture.VerificationSHA256 != "cb2d59382bd4cb54fa522cc319fb436a49a918538afd0b60fa2615839235eca3" ||
		fixture.LibraryExpectedSHA256 != "b836bd1a569ba8b4cb2b4afa8057716fe693593ac8554e478dfeb10d99968676" {
		t.Fatalf("entity-impact oracle identity=%+v", fixture)
	}
	return fixture
}

func TestGraphEntityImpactRealOracle(t *testing.T) {
	raw, err := os.ReadFile(os.Getenv("GRAPHNEST_TEST_CODEGRAPH_V2_FIXTURE"))
	if err != nil {
		t.Fatal(err)
	}
	artifact, err := graphartifact.ParseV2(raw, graphartifact.Limits{})
	if err != nil {
		t.Fatal(err)
	}
	fixture := loadEntityImpactFixture(t)
	if len(artifact.Nodes) != fixture.Facts.Nodes || len(artifact.Edges) != fixture.Facts.Edges || len(artifact.Files) != fixture.Facts.Files || len(artifact.Unresolved) != fixture.Facts.Unresolved {
		t.Fatalf("artifact facts nodes=%d edges=%d files=%d unresolved=%d", len(artifact.Nodes), len(artifact.Edges), len(artifact.Files), len(artifact.Unresolved))
	}
	verifyAggregateSources(t)
	store, service, scope := fileDependencyService(t, artifact)
	originalNodes, originalEdges := entityImpactFacts(artifact)

	callGraph, err := service.CallGraph(t.Context(), graphprotocol.EntityImpactRequest{Scope: scope, Occurrence: fixture.IDs.Run})
	if err != nil {
		t.Fatal(err)
	}
	assertEntitySubgraph(t, callGraph, fixture.CallGraph, originalNodes, originalEdges)
	assertEntityImpactState(t, callGraph, true, fixture.Facts.Unresolved)
	zero := 0
	callGraphZero, err := service.CallGraph(t.Context(), graphprotocol.EntityImpactRequest{Scope: scope, Occurrence: fixture.IDs.Run, MaxDepth: &zero})
	if err != nil || len(callGraphZero.Entities) != 1 || callGraphZero.Entities[0].Fact.Occurrence != fixture.IDs.Run || len(callGraphZero.Edges) != 0 {
		t.Fatalf("depth-zero call graph=%+v err=%v", callGraphZero, err)
	}
	missingGraph, err := service.CallGraph(t.Context(), graphprotocol.EntityImpactRequest{Scope: scope, Occurrence: "missing-fixture-id"})
	if err != nil || missingGraph.Status != graphprotocol.StatusNotFound || len(missingGraph.Entities) != 0 || len(missingGraph.Edges) != 0 {
		t.Fatalf("missing call graph=%+v err=%v", missingGraph, err)
	}

	usages, err := service.Usages(t.Context(), graphprotocol.EntityRequest{Scope: scope, Occurrence: fixture.IDs.Normalize})
	if err != nil {
		t.Fatal(err)
	}
	assertEntityUsages(t, usages, fixture.UsagesNormalize, originalNodes, originalEdges)
	assertEntityImpactState(t, usages, true, fixture.Facts.Unresolved)
	missingUsages, err := service.Usages(t.Context(), graphprotocol.EntityRequest{Scope: scope, Occurrence: "missing-fixture-id"})
	if err != nil || missingUsages.Status != graphprotocol.StatusNotFound || len(missingUsages.Usages) != 0 {
		t.Fatalf("missing usages=%+v err=%v", missingUsages, err)
	}

	depth3 := 3
	impact, err := service.ImpactRadius(t.Context(), graphprotocol.EntityImpactRequest{Scope: scope, Occurrence: fixture.IDs.Service, MaxDepth: &depth3})
	if err != nil {
		t.Fatal(err)
	}
	assertEntitySubgraph(t, impact, fixture.ImpactServiceDepth3, originalNodes, originalEdges)
	assertEntityImpactState(t, impact, true, fixture.Facts.Unresolved)
	if impact.Partial || !reflect.DeepEqual(impact.Blast, fixture.ImpactServiceDepth3.Blast) {
		t.Fatalf("service blast=%+v want=%+v partial=%v boundaries=%+v", impact.Blast, fixture.ImpactServiceDepth3.Blast, impact.Partial, impact.Boundaries)
	}
	depth1 := 1
	impactOne, err := service.ImpactRadius(t.Context(), graphprotocol.EntityImpactRequest{Scope: scope, Occurrence: fixture.IDs.Service, MaxDepth: &depth1})
	if err != nil {
		t.Fatal(err)
	}
	assertEntitySubgraph(t, impactOne, fixture.ImpactServiceDepth1, originalNodes, originalEdges)
	assertEntityImpactState(t, impactOne, true, fixture.Facts.Unresolved)
	if !impactOne.Partial || impactOne.Blast == nil || len(impactOne.Boundaries) == 0 || impactOne.Boundaries[0].Reason != "depth_limit" {
		t.Fatalf("bounded depth-one impact blast=%+v partial=%v boundaries=%+v", impactOne.Blast, impactOne.Partial, impactOne.Boundaries)
	}
	impactZero, err := service.ImpactRadius(t.Context(), graphprotocol.EntityImpactRequest{Scope: scope, Occurrence: fixture.IDs.Service, MaxDepth: &zero})
	if err != nil || len(impactZero.Entities) != 1 || impactZero.Entities[0].Fact.Occurrence != fixture.IDs.Service || len(impactZero.Edges) != 0 || !impactZero.Partial || impactZero.Blast == nil {
		t.Fatalf("depth-zero impact=%+v err=%v", impactZero, err)
	}
	greet, err := service.ImpactRadius(t.Context(), graphprotocol.EntityImpactRequest{Scope: scope, Occurrence: fixture.IDs.ServiceGreet, MaxDepth: &depth3})
	if err != nil {
		t.Fatal(err)
	}
	assertEntitySubgraph(t, greet, fixture.ImpactServiceGreet, originalNodes, originalEdges)
	missingImpact, err := service.ImpactRadius(t.Context(), graphprotocol.EntityImpactRequest{Scope: scope, Occurrence: "missing-fixture-id"})
	if err != nil || missingImpact.Status != graphprotocol.StatusNotFound || len(missingImpact.Entities) != 0 || len(missingImpact.Edges) != 0 {
		t.Fatalf("missing impact=%+v err=%v", missingImpact, err)
	}
	home, err := service.ImpactRadius(t.Context(), graphprotocol.EntityImpactRequest{Scope: scope, Occurrence: fixture.IDs.Home, MaxDepth: &depth3})
	if err != nil || home.Partial || !reflect.DeepEqual(home.Blast, &fixture.HomeBlast) {
		t.Fatalf("home blast=%+v want=%+v err=%v", home.Blast, fixture.HomeBlast, err)
	}

	exported, err := service.ExportedSymbols(t.Context(), graphprotocol.FileEntityRequest{Scope: scope, Path: "core.ts"})
	if err != nil || !reflect.DeepEqual(entityOccurrences(exported.Entities), fixture.ExportedCore) {
		t.Fatalf("exported=%v err=%v", entityOccurrences(exported.Entities), err)
	}
	assertEntityFacts(t, exported.Entities, originalNodes)
	assertEntityImpactState(t, exported, true, fixture.Facts.Unresolved)
	missingExported, err := service.ExportedSymbols(t.Context(), graphprotocol.FileEntityRequest{Scope: scope, Path: "__missing__.ts"})
	if err != nil || len(missingExported.Entities) != 0 {
		t.Fatalf("missing exported=%+v err=%v", missingExported, err)
	}

	for name, test := range map[string]struct {
		pattern string
		want    []string
	}{
		"literal":  {"fixture.models::Model::normalize", fixture.Qualified["literal"]},
		"star":     {"*::greet", fixture.Qualified["star"]},
		"question": {"Service::gree?", fixture.Qualified["question"]},
		"missing":  {"MissingFixtureSymbol987", fixture.Qualified["missing"]},
	} {
		t.Run("qualified-"+name, func(t *testing.T) {
			got, callErr := service.FindByQualifiedName(t.Context(), graphprotocol.QualifiedNameRequest{Scope: scope, Pattern: test.pattern})
			if callErr != nil || !reflect.DeepEqual(sortedEntityOccurrences(got.Entities), sortedStrings(test.want)) {
				t.Fatalf("qualified=%v want=%v err=%v", sortedEntityOccurrences(got.Entities), sortedStrings(test.want), callErr)
			}
			assertEntityFacts(t, got.Entities, originalNodes)
			assertEntityImpactState(t, got, true, fixture.Facts.Unresolved)
		})
	}

	modules, err := service.ModuleStructure(t.Context(), graphprotocol.ScopeRequest{Scope: scope})
	if err != nil || !reflect.DeepEqual(modules.Modules, fixture.Modules) {
		t.Fatalf("modules=%+v err=%v", modules.Modules, err)
	}
	assertEntityImpactState(t, modules, true, fixture.Facts.Unresolved)
	exportedOnly := true
	filteredRequest := graphprotocol.FilteredSubgraphRequest{Scope: scope, Filter: graphprotocol.EntityFilter{Paths: []string{"core.ts"}, Exported: &exportedOnly}}
	filtered, err := service.FilteredSubgraph(t.Context(), filteredRequest)
	if err != nil {
		t.Fatal(err)
	}
	assertEntitySubgraph(t, filtered, fixture.FilteredCore, originalNodes, originalEdges)
	assertEntityImpactState(t, filtered, true, fixture.Facts.Unresolved)
	withoutEdges := false
	filteredRequest.IncludeEdges = &withoutEdges
	filteredWithoutEdges, err := service.FilteredSubgraph(t.Context(), filteredRequest)
	if err != nil || !reflect.DeepEqual(sortedEntityOccurrences(filteredWithoutEdges.Entities), fixture.FilteredCore.Nodes) || len(filteredWithoutEdges.Edges) != 0 {
		t.Fatalf("filtered without edges=%+v err=%v", filteredWithoutEdges, err)
	}
	filteredRequest.Filter.Paths = []string{"__missing__.ts"}
	filteredRequest.IncludeEdges = nil
	filteredMissing, err := service.FilteredSubgraph(t.Context(), filteredRequest)
	if err != nil || len(filteredMissing.Entities) != 0 || len(filteredMissing.Edges) != 0 {
		t.Fatalf("filtered missing=%+v err=%v", filteredMissing, err)
	}

	assertEntityImpactPlan(t, store, scopeRepositoryID(t, store, impact.Generations[0].UploadID), impact.Generations[0].UploadID, artifact.Commit, fixture.FilteredCore.Nodes)
}

func TestGraphEntityImpactScopeGenerationAndLimits(t *testing.T) {
	raw, err := os.ReadFile(os.Getenv("GRAPHNEST_TEST_CODEGRAPH_V2_FIXTURE"))
	if err != nil {
		t.Fatal(err)
	}
	artifact, err := graphartifact.ParseV2(raw, graphartifact.Limits{})
	if err != nil {
		t.Fatal(err)
	}
	fixture := loadEntityImpactFixture(t)
	store, service, scope := fileDependencyService(t, artifact)
	exported, err := service.ExportedSymbols(t.Context(), graphprotocol.FileEntityRequest{Scope: scope, Path: "core.ts"})
	if err != nil {
		t.Fatal(err)
	}
	uploadID := exported.Generations[0].UploadID
	snapshot := graphquery.QuerySnapshot{RepositoryID: scopeRepositoryID(t, store, uploadID), UploadID: uploadID, Commit: artifact.Commit}
	occurrences := fixture.FilteredCore.Nodes
	for name, mismatch := range map[string]graphquery.QuerySnapshot{
		"repository": {RepositoryID: snapshot.RepositoryID + 1, UploadID: snapshot.UploadID, Commit: snapshot.Commit},
		"upload":     {RepositoryID: snapshot.RepositoryID, UploadID: snapshot.UploadID + 1, Commit: snapshot.Commit},
		"commit":     {RepositoryID: snapshot.RepositoryID, UploadID: snapshot.UploadID, Commit: strings.Repeat("b", 40)},
	} {
		t.Run(name, func(t *testing.T) {
			entities, entityErr := store.QueryAnalysisEntities(t.Context(), graphquery.AnalysisEntityQuery{Snapshot: mismatch, Limit: 2})
			edges, edgeErr := store.QueryAnalysisEvidence(t.Context(), graphquery.AnalysisEvidenceQuery{Snapshot: mismatch, Occurrences: occurrences, Limit: 2})
			if entityErr != nil || edgeErr != nil || len(entities) != 0 || len(edges) != 0 {
				t.Fatalf("snapshot leaked entities=%d edges=%d entityErr=%v edgeErr=%v", len(entities), len(edges), entityErr, edgeErr)
			}
		})
	}
	var legacyUploadID int64
	if err = store.pool.QueryRow(t.Context(), `insert into graph_uploads(repository_id,commit,schema_version,source,analyzer_name,analyzer_version,content_hash,node_count,edge_count,active)
	 values($1,$2,1,'managed','legacy','1',$3,68,93,false) returning id`, snapshot.RepositoryID, snapshot.Commit, exported.Generations[0].ContentHash).Scan(&legacyUploadID); err != nil {
		t.Fatal(err)
	}
	legacy := snapshot
	legacy.UploadID = legacyUploadID
	entities, err := store.QueryAnalysisEntities(t.Context(), graphquery.AnalysisEntityQuery{Snapshot: legacy, Limit: 2})
	if err != nil || len(entities) != 0 {
		t.Fatalf("legacy entities=%+v err=%v", entities, err)
	}

	for name, query := range map[string]graphquery.AnalysisEntityQuery{
		"zero-limit":       {Snapshot: snapshot},
		"large-limit":      {Snapshot: snapshot, Limit: 1002},
		"many-paths":       {Snapshot: snapshot, Paths: make([]string, 1001), Limit: 1},
		"many-kinds":       {Snapshot: snapshot, Kinds: make([]string, 33), Limit: 1},
		"invalid-pattern":  {Snapshot: snapshot, QualifiedPattern: "[", Limit: 1},
		"oversize-pattern": {Snapshot: snapshot, QualifiedPattern: strings.Repeat("x", 32769), Limit: 1},
	} {
		t.Run("entity-"+name, func(t *testing.T) {
			if _, queryErr := store.QueryAnalysisEntities(t.Context(), query); !errors.Is(queryErr, graphquery.ErrInvalidRequest) {
				t.Fatalf("invalid entity query error=%v", queryErr)
			}
		})
	}
	for name, query := range map[string]graphquery.AnalysisEvidenceQuery{
		"zero-limit":        {Snapshot: snapshot, Occurrences: occurrences},
		"large-limit":       {Snapshot: snapshot, Occurrences: occurrences, Limit: 5002},
		"missing-endpoints": {Snapshot: snapshot, Limit: 1},
		"many-endpoints":    {Snapshot: snapshot, Occurrences: make([]string, 1001), Limit: 1},
	} {
		t.Run("evidence-"+name, func(t *testing.T) {
			if _, queryErr := store.QueryAnalysisEvidence(t.Context(), query); !errors.Is(queryErr, graphquery.ErrInvalidRequest) {
				t.Fatalf("invalid evidence query error=%v", queryErr)
			}
		})
	}
	canceled, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err = store.QueryAnalysisEntities(canceled, graphquery.AnalysisEntityQuery{Snapshot: snapshot, Limit: 1}); !errors.Is(err, context.Canceled) {
		t.Fatalf("entity cancellation=%v", err)
	}
	if _, err = store.QueryAnalysisEvidence(canceled, graphquery.AnalysisEvidenceQuery{Snapshot: snapshot, Occurrences: occurrences, Limit: 1}); !errors.Is(err, context.Canceled) {
		t.Fatalf("evidence cancellation=%v", err)
	}

	limited := &graphquery.Service{Store: store, Limits: graphquery.Limits{MaxNodes: 1, MaxEdges: 1}}
	if _, err = limited.ExportedSymbols(t.Context(), graphprotocol.FileEntityRequest{Scope: scope, Path: "core.ts"}); !errors.Is(err, graphquery.ErrQuerySize) {
		t.Fatalf("exported row limit=%v", err)
	}
	filtered, err := limited.FilteredSubgraph(t.Context(), graphprotocol.FilteredSubgraphRequest{Scope: scope, Filter: graphprotocol.EntityFilter{Paths: []string{"core.ts"}}})
	if err != nil || !filtered.Partial || len(filtered.Entities) != 1 || len(filtered.Boundaries) == 0 || filtered.Boundaries[0].Reason != "node_limit" {
		t.Fatalf("filtered row limit=%+v err=%v", filtered, err)
	}

	hiddenID := seedReadyRepository(t, store, 202, artifact.Commit)
	hidden := storageV2Artifact()
	hidden.Commit, hidden.Repository, hidden.ContentHash = artifact.Commit, "202", nil
	if _, err = store.ReplaceGraphV2(t.Context(), hiddenID, GraphPublication{Publisher: "hidden"}, hidden); err != nil {
		t.Fatal(err)
	}
	hiddenResult, err := service.ImpactRadius(t.Context(), graphprotocol.EntityImpactRequest{Scope: scope, Occurrence: hidden.Nodes[0].Occurrence})
	if err != nil || hiddenResult.Status != graphprotocol.StatusNotFound || len(hiddenResult.Entities) != 0 {
		t.Fatalf("hidden impact=%+v err=%v", hiddenResult, err)
	}
	emptyScope := scope
	emptyScope.Repositories = nil
	if _, err = service.ModuleStructure(t.Context(), graphprotocol.ScopeRequest{Scope: emptyScope}); !errors.Is(err, graphquery.ErrInvalidRequest) {
		t.Fatalf("hidden scope=%v", err)
	}
	if _, err = service.ImpactRadius(t.Context(), graphprotocol.EntityImpactRequest{Scope: scope}); !errors.Is(err, graphquery.ErrInvalidRequest) {
		t.Fatalf("missing selector=%v", err)
	}
	if _, err = service.FindByQualifiedName(t.Context(), graphprotocol.QualifiedNameRequest{Scope: scope}); !errors.Is(err, graphquery.ErrInvalidRequest) {
		t.Fatalf("missing qualified pattern=%v", err)
	}
	if _, err = service.ExportedSymbols(t.Context(), graphprotocol.FileEntityRequest{Scope: scope}); !errors.Is(err, graphquery.ErrInvalidRequest) {
		t.Fatalf("missing file path=%v", err)
	}
	if _, err = store.pool.Exec(t.Context(), `update repositories set indexed_sha=$2 where id=$1`, snapshot.RepositoryID, strings.Repeat("c", 40)); err != nil {
		t.Fatal(err)
	}
	if _, err = service.ImpactRadius(t.Context(), graphprotocol.EntityImpactRequest{Scope: scope, Occurrence: fixture.IDs.Service}); !errors.Is(err, graphquery.ErrGenerationChanged) {
		t.Fatalf("generation drift=%v", err)
	}
}

func TestGraphEntityImpactRejectsOversizedFacts(t *testing.T) {
	for _, kind := range []string{"entity", "evidence"} {
		t.Run(kind, func(t *testing.T) {
			artifact := storageV2Artifact()
			store, repositoryID := readyGraphStore(t, artifact.Commit)
			published, err := store.ReplaceGraphV2(t.Context(), repositoryID, GraphPublication{Publisher: "entity-impact-budget"}, artifact)
			if err != nil {
				t.Fatal(err)
			}
			snapshot := graphquery.QuerySnapshot{RepositoryID: repositoryID, UploadID: published.Upload.ID, Commit: artifact.Commit}
			if kind == "entity" {
				if _, err = store.pool.Exec(t.Context(), `update graph_v2_nodes set payload=convert_to(repeat('x',$2),'UTF8') where upload_id=$1 and occurrence=$3`, published.Upload.ID, graphquery.MaxEntityQueryBytes+1, []byte(artifact.Nodes[0].Occurrence)); err != nil {
					t.Fatal(err)
				}
				if _, err = store.QueryAnalysisEntities(t.Context(), graphquery.AnalysisEntityQuery{Snapshot: snapshot, Limit: 2}); !errors.Is(err, graphquery.ErrQuerySize) {
					t.Fatalf("oversized entity=%v", err)
				}
				return
			}
			if _, err = store.pool.Exec(t.Context(), `update graph_v2_edges set payload=convert_to(repeat('x',$2),'UTF8') where upload_id=$1 and occurrence=$3`, published.Upload.ID, graphquery.MaxEntityQueryBytes+1, []byte(artifact.Edges[0].Occurrence)); err != nil {
				t.Fatal(err)
			}
			if _, err = store.QueryAnalysisEvidence(t.Context(), graphquery.AnalysisEvidenceQuery{Snapshot: snapshot, Occurrences: []string{artifact.Edges[0].Source, artifact.Edges[0].Target}, Limit: 2}); !errors.Is(err, graphquery.ErrQuerySize) {
				t.Fatalf("oversized evidence=%v", err)
			}
		})
	}
}

func TestGraphEntityImpactPreservesMalformedProducerBytes(t *testing.T) {
	artifact := storageV2Artifact()
	store, repositoryID := readyGraphStore(t, artifact.Commit)
	published, err := store.ReplaceGraphV2(t.Context(), repositoryID, GraphPublication{Publisher: "entity-impact-bytes"}, artifact)
	if err != nil {
		t.Fatal(err)
	}
	producerName := []byte{0xff}
	if _, err = store.pool.Exec(t.Context(), `update graph_uploads set producer_name=$2 where id=$1`, published.Upload.ID, producerName); err != nil {
		t.Fatal(err)
	}
	if _, err = store.pool.Exec(t.Context(), `update graph_v2_nodes set qualified_name=$2 where upload_id=$1 and occurrence=$3`, published.Upload.ID, []byte{0xfe}, []byte(artifact.Nodes[0].Occurrence)); err != nil {
		t.Fatal(err)
	}
	snapshot := graphquery.QuerySnapshot{RepositoryID: repositoryID, UploadID: published.Upload.ID, Commit: artifact.Commit}
	entities, err := store.QueryAnalysisEntities(t.Context(), graphquery.AnalysisEntityQuery{Snapshot: snapshot, QualifiedPattern: `^a\.A$`, QualifiedLiteral: proto.String("a.A"), Limit: 2})
	if err != nil || len(entities) != 1 || !proto.Equal(entities[0].Fact, artifact.Nodes[0]) {
		t.Fatalf("malformed producer entity=%+v err=%v", entities, err)
	}
	producer := &graphv2.Producer{Name: string(producerName), Version: artifact.Producer.Version, Configuration: artifact.Producer.Configuration}
	wantID, err := graphartifact.IdentityV2(producer, artifact.Repository, artifact.Nodes[0].SourceId, artifact.Nodes[0].Occurrence)
	if err != nil || entities[0].ID != wantID {
		t.Fatalf("malformed producer entity id=%q want=%q err=%v", entities[0].ID, wantID, err)
	}
	evidence, err := store.QueryAnalysisEvidence(t.Context(), graphquery.AnalysisEvidenceQuery{Snapshot: snapshot, Occurrences: []string{artifact.Nodes[0].Occurrence, artifact.Nodes[1].Occurrence}, Limit: 3})
	if err != nil || len(evidence) != 2 {
		t.Fatalf("malformed producer evidence=%+v err=%v", evidence, err)
	}
	wantTargetID, err := graphartifact.IdentityV2(producer, artifact.Repository, artifact.Nodes[1].SourceId, artifact.Nodes[1].Occurrence)
	if err != nil {
		t.Fatal(err)
	}
	for i, edge := range evidence {
		if edge.SourceID != wantID || edge.TargetID != wantTargetID || !proto.Equal(edge.Fact, artifact.Edges[i]) {
			t.Fatalf("malformed producer edge=%+v", edge)
		}
	}
}

func TestGraphEntityImpactQualifiedLookupIsSelective(t *testing.T) {
	artifact := storageV2Artifact()
	artifact.Nodes = make([]*graphv2.Node, 1_102)
	for i := range 1_100 {
		artifact.Nodes[i] = &graphv2.Node{
			SourceId:      fmt.Sprintf("%04d", i+1),
			Occurrence:    fmt.Sprintf("function:bulk-%04d", i+1),
			Kind:          "function",
			Name:          fmt.Sprintf("Bulk%04d", i+1),
			QualifiedName: fmt.Sprintf("fixture.bulk::Bulk%04d", i+1),
		}
	}
	target := &graphv2.Node{SourceId: "1101", Occurrence: "function:zzzz-selective-target", Kind: "function", Name: "NeedleService", QualifiedName: "fixture.bulk::NeedleService::greet"}
	special := &graphv2.Node{SourceId: "1102", Occurrence: "function:zzzz-unicode-nul", Kind: "function", Name: "CaféService", QualifiedName: "fixture.bulk::Café\x00Service"}
	artifact.Nodes[len(artifact.Nodes)-2], artifact.Nodes[len(artifact.Nodes)-1] = target, special
	artifact.Edges, artifact.Files, artifact.Unresolved, artifact.Diagnostics = nil, nil, nil, nil
	artifact.ContentHash = nil
	var err error
	artifact.ContentHash, err = graphartifact.SemanticHashV2(artifact, graphartifact.Limits{})
	if err != nil {
		t.Fatal(err)
	}
	store, repositoryID := readyGraphStore(t, artifact.Commit)
	published, err := store.ReplaceGraphV2(t.Context(), repositoryID, GraphPublication{Publisher: "entity-impact-selective"}, artifact)
	if err != nil {
		t.Fatal(err)
	}
	producerName := []byte{0xff}
	if _, err = store.pool.Exec(t.Context(), `update graph_uploads set producer_name=$2 where id=$1`, published.Upload.ID, producerName); err != nil {
		t.Fatal(err)
	}
	if _, err = store.pool.Exec(t.Context(), `update graph_v2_nodes set qualified_name=$2 where upload_id=$1 and occurrence=$3`, published.Upload.ID, []byte{0xfe}, []byte(target.Occurrence)); err != nil {
		t.Fatal(err)
	}
	scope := graphprotocol.Scope{SelectedRepositoryID: repositoryID, Repositories: []graphprotocol.RepositorySnapshot{{ID: repositoryID, GitHubID: 101, Commit: artifact.Commit}}}
	service := &graphquery.Service{Store: store}
	producer := &graphv2.Producer{Name: string(producerName), Version: artifact.Producer.Version, Configuration: artifact.Producer.Configuration}
	for _, test := range []struct {
		name, pattern string
		want          *graphv2.Node
	}{
		{"literal", target.QualifiedName, target},
		{"wildcard", "*::NeedleService::gree?", target},
		{"unicode-nul-literal", special.QualifiedName, special},
		{"unicode-nul-wildcard", "fixture.bulk::Caf?\x00Serv*", special},
	} {
		t.Run(test.name, func(t *testing.T) {
			wantID, identityErr := graphartifact.IdentityV2(producer, artifact.Repository, test.want.SourceId, test.want.Occurrence)
			if identityErr != nil {
				t.Fatal(identityErr)
			}
			got, queryErr := service.FindByQualifiedName(t.Context(), graphprotocol.QualifiedNameRequest{Scope: scope, Pattern: test.pattern})
			if queryErr != nil || len(got.Entities) != 1 || !proto.Equal(got.Entities[0].Fact, test.want) || got.Entities[0].ID != wantID {
				t.Fatalf("qualified pattern=%q entities=%v wantID=%q err=%v", test.pattern, entityOccurrences(got.Entities), wantID, queryErr)
			}
		})
	}
	if _, err = service.FindByQualifiedName(t.Context(), graphprotocol.QualifiedNameRequest{Scope: scope, Pattern: "*"}); !errors.Is(err, graphquery.ErrQuerySize) {
		t.Fatalf("unselective wildcard limit=%v", err)
	}
	kinds := []string{"class", "union", "function", "method", "interface", "type_alias", "variable", "constant"}
	assertQualifiedImpactPlan(t, store, "literal", analysisQualifiedLiteralSQL, repositoryID, published.Upload.ID, artifact.Commit, [][]byte{}, kinds, []byte(target.QualifiedName), nil, graphquery.MaxEntityQueryBytes, 1_002)
	assertQualifiedImpactPlan(t, store, "wildcard", analysisQualifiedWildcardSQL, repositoryID, published.Upload.ID, artifact.Commit, [][]byte{}, kinds, [][]byte{[]byte("::NeedleService::gree")}, []byte{}, []byte{}, nil, graphquery.MaxEntityQueryBytes, 1_002)
	if _, err = store.pool.Exec(t.Context(), `update graph_uploads set discovery_version=3 where id=$1`, published.Upload.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = service.FindByQualifiedName(t.Context(), graphprotocol.QualifiedNameRequest{Scope: scope, Pattern: target.QualifiedName}); !errors.Is(err, graphquery.ErrDiscoveryUnavailable) {
		t.Fatalf("stale qualified projection=%v", err)
	}
}

func entityImpactFacts(artifact *graphv2.Artifact) (map[string]*graphv2.Node, map[string]*graphv2.Edge) {
	nodes := make(map[string]*graphv2.Node, len(artifact.Nodes))
	for _, node := range artifact.Nodes {
		nodes[node.Occurrence] = node
	}
	edges := make(map[string]*graphv2.Edge, len(artifact.Edges))
	for _, edge := range artifact.Edges {
		edges[edge.Occurrence] = edge
	}
	return nodes, edges
}

func assertEntitySubgraph(t *testing.T, got graphprotocol.SubgraphResponse, want entityImpactOracle, nodes map[string]*graphv2.Node, edges map[string]*graphv2.Edge) {
	t.Helper()
	if got.Status != graphprotocol.StatusOK || !reflect.DeepEqual(sortedEntityOccurrences(got.Entities), want.Nodes) || !reflect.DeepEqual(entityEdgeTuples(got.Edges), sortedEntityImpactEdges(want.Edges)) || !reflect.DeepEqual(entityEdgeKinds(got.Edges), want.EdgeKinds) {
		t.Fatalf("subgraph status=%s nodes=%v edges=%v edgeKinds=%v wantNodes=%v wantEdges=%v wantKinds=%v partial=%v boundaries=%+v", got.Status, sortedEntityOccurrences(got.Entities), entityEdgeTuples(got.Edges), entityEdgeKinds(got.Edges), want.Nodes, sortedEntityImpactEdges(want.Edges), want.EdgeKinds, got.Partial, got.Boundaries)
	}
	assertEntityFacts(t, got.Entities, nodes)
	assertEvidenceFacts(t, got.Edges, edges)
}

func assertEntityUsages(t *testing.T, got graphprotocol.UsagesResponse, want entityImpactOracle, nodes map[string]*graphv2.Node, edges map[string]*graphv2.Edge) {
	t.Helper()
	occurrences := make([]string, len(got.Usages))
	evidence := make([]graphprotocol.Evidence, len(got.Usages))
	for i, usage := range got.Usages {
		occurrences[i], evidence[i] = usage.Entity.Fact.Occurrence, usage.Edge
		assertEntityFacts(t, []graphprotocol.Entity{usage.Entity}, nodes)
	}
	slices.Sort(occurrences)
	wantOccurrences := slices.Clone(want.Nodes)
	slices.Sort(wantOccurrences)
	if got.Status != graphprotocol.StatusOK || !reflect.DeepEqual(occurrences, wantOccurrences) || !reflect.DeepEqual(entityEdgeTuples(evidence), sortedEntityImpactEdges(want.Edges)) || !reflect.DeepEqual(entityEdgeKinds(evidence), want.EdgeKinds) {
		t.Fatalf("usages status=%s nodes=%v edges=%v edgeKinds=%v wantNodes=%v wantEdges=%v wantKinds=%v", got.Status, occurrences, entityEdgeTuples(evidence), entityEdgeKinds(evidence), wantOccurrences, sortedEntityImpactEdges(want.Edges), want.EdgeKinds)
	}
	assertEvidenceFacts(t, evidence, edges)
}

func assertEntityFacts(t *testing.T, entities []graphprotocol.Entity, original map[string]*graphv2.Node) {
	t.Helper()
	for _, entity := range entities {
		if entity.RepositoryID != 101 || entity.Fact == nil || !proto.Equal(entity.Fact, original[entity.Fact.Occurrence]) {
			t.Fatalf("lost entity fact=%+v", entity)
		}
	}
}

func assertEvidenceFacts(t *testing.T, evidence []graphprotocol.Evidence, original map[string]*graphv2.Edge) {
	t.Helper()
	for _, edge := range evidence {
		if edge.RepositoryID != 101 || edge.Fact == nil || !proto.Equal(edge.Fact, original[edge.Fact.Occurrence]) {
			t.Fatalf("lost evidence fact=%+v", edge)
		}
	}
}

func entityOccurrences(entities []graphprotocol.Entity) []string {
	result := make([]string, len(entities))
	for i, entity := range entities {
		result[i] = entity.Fact.Occurrence
	}
	return result
}

func sortedEntityOccurrences(entities []graphprotocol.Entity) []string {
	result := entityOccurrences(entities)
	slices.Sort(result)
	return result
}

func sortedStrings(values []string) []string {
	result := slices.Clone(values)
	if result == nil {
		result = []string{}
	}
	slices.Sort(result)
	return result
}

func entityEdgeKinds(edges []graphprotocol.Evidence) map[string]int {
	result := map[string]int{}
	for _, edge := range edges {
		relation, ok := graphartifact.RelationshipFromWire(edge.Fact.Kind)
		if ok {
			result[relation.Name]++
		}
	}
	return result
}

func entityEdgeTuples(edges []graphprotocol.Evidence) []entityImpactEdge {
	result := make([]entityImpactEdge, len(edges))
	for i, edge := range edges {
		result[i] = entityImpactEdge{Occurrence: edge.Fact.Occurrence, Source: edge.Fact.Source, Target: edge.Fact.Target}
	}
	return sortedEntityImpactEdges(result)
}

func sortedEntityImpactEdges(edges []entityImpactEdge) []entityImpactEdge {
	result := slices.Clone(edges)
	slices.SortFunc(result, func(a, b entityImpactEdge) int {
		if a.Occurrence != b.Occurrence {
			return strings.Compare(a.Occurrence, b.Occurrence)
		}
		if a.Source != b.Source {
			return strings.Compare(a.Source, b.Source)
		}
		return strings.Compare(a.Target, b.Target)
	})
	return result
}

func assertEntityImpactState(t *testing.T, response any, complete bool, unresolved int) {
	t.Helper()
	raw, err := json.Marshal(response)
	if err != nil {
		t.Fatal(err)
	}
	var value struct {
		Analysis *struct {
			Freshness  string `json:"freshness"`
			Coverage   string `json:"coverage"`
			Unresolved int    `json:"unresolved"`
			Complete   bool   `json:"complete"`
		} `json:"analysis"`
	}
	if err = json.Unmarshal(raw, &value); err != nil {
		t.Fatal(err)
	}
	if value.Analysis == nil || value.Analysis.Freshness != "current" || value.Analysis.Coverage != "not_assessed" || value.Analysis.Unresolved != unresolved || value.Analysis.Complete != complete {
		t.Fatalf("analysis state=%+v raw=%s", value.Analysis, raw)
	}
}

func assertEntityImpactPlan(t *testing.T, store *Store, repositoryID, uploadID int64, commit string, occurrences []string) {
	t.Helper()
	values := make([][]byte, len(occurrences))
	for i, occurrence := range occurrences {
		values[i] = []byte(occurrence)
	}
	rows, err := store.pool.Query(t.Context(), "explain (analyze,buffers) "+analysisEvidenceSQL, repositoryID, uploadID, commit, values, graphquery.MaxEntityQueryBytes, 5001)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	lines := []string{}
	for rows.Next() {
		var line string
		if err = rows.Scan(&line); err != nil {
			t.Fatal(err)
		}
		lines = append(lines, line)
	}
	plan := strings.Join(lines, "\n")
	if err = rows.Err(); err != nil || !strings.Contains(plan, "Limit") || !strings.Contains(plan, "actual") || !strings.Contains(plan, "Buffers") || !strings.Contains(plan, "graph_v2_edges") || !strings.Contains(plan, "graph_v2_nodes") {
		t.Fatalf("entity-impact plan=%s err=%v", plan, err)
	}
	t.Logf("PostgreSQL entity-impact plan:\n%s", plan)
}

func assertQualifiedImpactPlan(t *testing.T, store *Store, name, sql string, args ...any) {
	t.Helper()
	rows, err := store.pool.Query(t.Context(), "explain (analyze,buffers) "+sql, args...)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	lines := []string{}
	for rows.Next() {
		var line string
		if err = rows.Scan(&line); err != nil {
			t.Fatal(err)
		}
		lines = append(lines, line)
	}
	plan := strings.Join(lines, "\n")
	if err = rows.Err(); err != nil || !strings.Contains(plan, "Limit") || !strings.Contains(plan, "actual") || !strings.Contains(plan, "Buffers") || !strings.Contains(plan, "graph_v2_discovery") || !strings.Contains(plan, "graph_v2_nodes") {
		t.Fatalf("qualified %s plan=%s err=%v", name, plan, err)
	}
	t.Logf("PostgreSQL qualified %s plan (1,102 nodes):\n%s", name, plan)
}
