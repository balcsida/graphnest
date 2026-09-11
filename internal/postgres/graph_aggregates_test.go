//go:build integration

package postgres

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/balcsida/graphnest/internal/graphartifact"
	graphv2 "github.com/balcsida/graphnest/internal/graphartifact/v2"
	"github.com/balcsida/graphnest/internal/graphprotocol"
	"github.com/balcsida/graphnest/internal/graphquery"
	"google.golang.org/protobuf/proto"
)

type graphAggregateFixture struct {
	Reference         string `json:"reference"`
	CaptureSHA256     string `json:"capture_sha256"`
	UncertaintySHA256 string `json:"uncertainty_sha256"`
	Facts             struct {
		Nodes      int `json:"nodes"`
		Edges      int `json:"edges"`
		Files      int `json:"files"`
		Unresolved int `json:"unresolved"`
	} `json:"facts"`
	UpstreamStorage struct {
		DatabaseBytes         int  `json:"database_bytes"`
		WALBytes              int  `json:"wal_bytes"`
		LastUpdatedIsCallTime bool `json:"last_updated_is_call_time"`
	} `json:"upstream_storage"`
	IDs             []string                       `json:"ids"`
	Stats           graphprotocol.GraphStats       `json:"stats"`
	FanIn           []graphprotocol.AggregateCount `json:"fan_in"`
	FanOut          []graphprotocol.AggregateCount `json:"fan_out"`
	NodeMetrics     graphprotocol.NodeMetrics      `json:"node_metrics"`
	AmbiguousNames  []string                       `json:"ambiguous_names"`
	ExportLanguages []string                       `json:"export_languages"`
	UnresolvedNames []string                       `json:"unresolved_names"`
	TopDependedOn   []graphprotocol.DependedOn     `json:"top_depended_on"`
	TopCallingFiles []graphprotocol.CallingFile    `json:"top_calling_files"`
	FilePaths       []string                       `json:"file_paths"`
	FileNodes       []struct {
		Occurrence string `json:"occurrence"`
		Path       string `json:"path"`
		Language   string `json:"language"`
		StartLine  int32  `json:"start_line"`
		EndLine    int32  `json:"end_line"`
	} `json:"file_nodes"`
	Assignments   []graphprotocol.ModuleAssignment `json:"assignments"`
	ModuleOptions struct {
		Kinds           []string
		MinConfidence   float64  `json:"min_confidence"`
		TopPairsPerLink int      `json:"top_pairs_per_link"`
		PairKinds       []string `json:"pair_kinds"`
	} `json:"module_options"`
	ModuleLinks    []graphprotocol.ModuleLink `json:"module_links"`
	ModulePairs    []graphprotocol.ModulePair `json:"module_pairs"`
	UncertainLinks []graphprotocol.ModuleLink `json:"uncertain_links"`
	Unresolved     struct {
		Source   string `json:"source"`
		Path     string `json:"path"`
		SourceID string `json:"source_id"`
		Name     string `json:"name"`
		Kind     string `json:"kind"`
		Language string `json:"language"`
		Line     int32  `json:"line"`
		Column   int32  `json:"column"`
	} `json:"unresolved"`
}

func loadGraphAggregateFixture(t *testing.T) graphAggregateFixture {
	t.Helper()
	data, err := os.ReadFile("../../test/fixtures/codegraph/graph-aggregates.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixture graphAggregateFixture
	if err = json.Unmarshal(data, &fixture); err != nil {
		t.Fatal(err)
	}
	if fixture.Reference != "b9ca4b7981116909900368cc1686a1074cd4d4c1" || fixture.CaptureSHA256 != "3021b492066cd0605062650c627953da106ebab9ec2e85b2bcff9829602500b0" || fixture.UncertaintySHA256 != "fbba9baa31a07bfa18fde8b62c40140646521c43f9aac4c4dbde456ee9d95a5a" {
		t.Fatalf("aggregate oracle identity=%+v", fixture)
	}
	return fixture
}

func TestAggregateStatsAuthenticatesSnapshot(t *testing.T) {
	raw, err := os.ReadFile(os.Getenv("GRAPHNEST_TEST_CODEGRAPH_V2_FIXTURE"))
	if err != nil {
		t.Fatal(err)
	}
	artifact, err := graphartifact.ParseV2(raw, graphartifact.Limits{})
	if err != nil {
		t.Fatal(err)
	}
	store, service, scope := fileDependencyService(t, artifact)
	current, err := service.GraphStats(t.Context(), scope)
	if err != nil {
		t.Fatal(err)
	}
	generation := current.Generations[0]
	snapshot := graphquery.QuerySnapshot{RepositoryID: scopeRepositoryID(t, store, generation.UploadID), UploadID: generation.UploadID, Commit: generation.Commit}

	for name, mismatch := range map[string]graphquery.QuerySnapshot{
		"repository": {RepositoryID: snapshot.RepositoryID + 1, UploadID: snapshot.UploadID, Commit: snapshot.Commit},
		"commit":     {RepositoryID: snapshot.RepositoryID, UploadID: snapshot.UploadID, Commit: strings.Repeat("b", 40)},
		"upload":     {RepositoryID: snapshot.RepositoryID, UploadID: snapshot.UploadID + 1, Commit: snapshot.Commit},
	} {
		t.Run(name, func(t *testing.T) {
			got, callErr := store.AggregateStats(t.Context(), mismatch)
			if callErr != nil || !emptyGraphStats(got) {
				t.Fatalf("mismatched snapshot leaked stats=%+v err=%v", got, callErr)
			}
		})
	}

	var legacyUploadID int64
	if err = store.pool.QueryRow(t.Context(), `insert into graph_uploads(repository_id,commit,schema_version,source,analyzer_name,analyzer_version,content_hash,node_count,edge_count,active)
	 values($1,$2,1,'managed','legacy','1',$3,68,93,false) returning id`, snapshot.RepositoryID, snapshot.Commit, generation.ContentHash).Scan(&legacyUploadID); err != nil {
		t.Fatal(err)
	}
	legacySnapshot := snapshot
	legacySnapshot.UploadID = legacyUploadID
	got, err := store.AggregateStats(t.Context(), legacySnapshot)
	if err != nil || !emptyGraphStats(got) {
		t.Fatalf("non-v2 upload leaked stats=%+v err=%v", got, err)
	}
}

func emptyGraphStats(value graphprotocol.GraphStats) bool {
	return value.NodeCount == 0 && value.EdgeCount == 0 && value.FileCount == 0 && value.UnresolvedCount == 0 && len(value.NodesByKind) == 0 && len(value.EdgesByKind) == 0 && len(value.FilesByLanguage) == 0
}

func TestGraphAggregatesRealOracle(t *testing.T) {
	raw, err := os.ReadFile(os.Getenv("GRAPHNEST_TEST_CODEGRAPH_V2_FIXTURE"))
	if err != nil {
		t.Fatal(err)
	}
	artifact, err := graphartifact.ParseV2(raw, graphartifact.Limits{})
	if err != nil {
		t.Fatal(err)
	}
	fixture := loadGraphAggregateFixture(t)
	if len(artifact.Nodes) != fixture.Facts.Nodes || len(artifact.Edges) != fixture.Facts.Edges || len(artifact.Files) != fixture.Facts.Files || len(artifact.Unresolved) != fixture.Facts.Unresolved {
		t.Fatalf("incomplete original facts: nodes=%d edges=%d files=%d unresolved=%d", len(artifact.Nodes), len(artifact.Edges), len(artifact.Files), len(artifact.Unresolved))
	}
	verifyAggregateSources(t)
	store, service, scope := fileDependencyService(t, artifact)

	stats, err := service.GraphStats(t.Context(), scope)
	if err != nil || !reflect.DeepEqual(stats.Stats, fixture.Stats) || fixture.UpstreamStorage.DatabaseBytes != 208896 || fixture.UpstreamStorage.WALBytes != 0 || !fixture.UpstreamStorage.LastUpdatedIsCallTime {
		t.Fatalf("stats=%+v err=%v", stats, err)
	}
	values := graphprotocol.AggregateValuesRequest{Scope: scope, Values: fixture.IDs}
	for _, test := range []struct {
		name string
		call func() (graphprotocol.AggregateCountsResponse, error)
		want []graphprotocol.AggregateCount
	}{{"fan-in", func() (graphprotocol.AggregateCountsResponse, error) { return service.FanIn(t.Context(), values) }, fixture.FanIn}, {"fan-out", func() (graphprotocol.AggregateCountsResponse, error) { return service.FanOut(t.Context(), values) }, fixture.FanOut}} {
		t.Run(test.name, func(t *testing.T) {
			got, callErr := test.call()
			if callErr != nil || !reflect.DeepEqual(got.Counts, test.want) {
				t.Fatalf("counts=%+v err=%v", got, callErr)
			}
		})
	}
	for _, call := range []func() (graphprotocol.AggregateCountsResponse, error){
		func() (graphprotocol.AggregateCountsResponse, error) {
			return service.FanIn(t.Context(), graphprotocol.AggregateValuesRequest{Scope: scope})
		},
		func() (graphprotocol.AggregateCountsResponse, error) {
			return service.FanOut(t.Context(), graphprotocol.AggregateValuesRequest{Scope: scope})
		},
	} {
		if got, callErr := call(); callErr != nil || len(got.Counts) != 0 {
			t.Fatalf("empty fan=%+v err=%v", got, callErr)
		}
	}
	metrics, err := service.NodeMetrics(t.Context(), graphprotocol.NodeMetricsRequest{Scope: scope, Occurrence: fixture.IDs[0]})
	if err != nil || !reflect.DeepEqual(metrics.Metrics, fixture.NodeMetrics) {
		t.Fatalf("metrics=%+v err=%v", metrics, err)
	}
	missingMetrics, err := service.NodeMetrics(t.Context(), graphprotocol.NodeMetricsRequest{Scope: scope, Occurrence: "missing-fixture-id"})
	if err != nil || !reflect.DeepEqual(missingMetrics.Metrics, graphprotocol.NodeMetrics{}) {
		t.Fatalf("missing metrics=%+v err=%v", missingMetrics, err)
	}
	nameCases := []struct {
		name   string
		values []string
		want   []string
		call   func(graphprotocol.AggregateValuesRequest) (graphprotocol.AggregateNamesResponse, error)
	}{
		{"ambiguous", []string{"normalize", "dormantUtility", "MissingFixtureSymbol987", "", "normalize"}, fixture.AmbiguousNames, func(r graphprotocol.AggregateValuesRequest) (graphprotocol.AggregateNamesResponse, error) {
			return service.AmbiguousReferencedNames(t.Context(), r)
		}},
		{"exports", []string{"typescript", "tsx", "ruby", "swift", "missing", "", "typescript"}, fixture.ExportLanguages, func(r graphprotocol.AggregateValuesRequest) (graphprotocol.AggregateNamesResponse, error) {
			return service.LanguagesWithExports(t.Context(), r)
		}},
		{"unresolved", []string{"name.trim", "trim", "expo-router", "MissingFixtureSymbol987", "", "trim"}, fixture.UnresolvedNames, func(r graphprotocol.AggregateValuesRequest) (graphprotocol.AggregateNamesResponse, error) {
			return service.UnresolvedNamesAmong(t.Context(), r)
		}},
	}
	for _, test := range nameCases {
		t.Run(test.name, func(t *testing.T) {
			got, callErr := test.call(graphprotocol.AggregateValuesRequest{Scope: scope, Values: test.values})
			if callErr != nil || !reflect.DeepEqual(got.Names, test.want) {
				t.Fatalf("names=%+v err=%v", got, callErr)
			}
		})
		if got, callErr := test.call(graphprotocol.AggregateValuesRequest{Scope: scope}); callErr != nil || len(got.Names) != 0 {
			t.Fatalf("empty %s=%+v err=%v", test.name, got, callErr)
		}
	}
	depended, err := service.TopDependedOn(t.Context(), graphprotocol.AggregateLimitRequest{Scope: scope, Limit: 5})
	if err != nil || !reflect.DeepEqual(depended.Nodes, fixture.TopDependedOn) {
		t.Fatalf("top depended=%+v err=%v", depended, err)
	}
	zeroDepended, err := service.TopDependedOn(t.Context(), graphprotocol.AggregateLimitRequest{Scope: scope})
	if err != nil || len(zeroDepended.Nodes) != 0 || zeroDepended.Partial {
		t.Fatalf("zero top depended=%+v err=%v", zeroDepended, err)
	}
	calling, err := service.TopCallingFiles(t.Context(), graphprotocol.AggregateLimitRequest{Scope: scope, Limit: 5})
	if err != nil || !reflect.DeepEqual(calling.Files, fixture.TopCallingFiles) {
		t.Fatalf("top calling=%+v err=%v", calling, err)
	}
	zeroCalling, err := service.TopCallingFiles(t.Context(), graphprotocol.AggregateLimitRequest{Scope: scope})
	if err != nil || len(zeroCalling.Files) != 0 || zeroCalling.Partial {
		t.Fatalf("zero top calling=%+v err=%v", zeroCalling, err)
	}
	files, err := service.FileNodes(t.Context(), graphprotocol.FileNodesRequest{Scope: scope, Paths: fixture.FilePaths})
	if err != nil || len(files.Entities) != len(fixture.FileNodes) {
		t.Fatalf("file nodes=%+v err=%v", files, err)
	}
	for index, want := range fixture.FileNodes {
		fact := files.Entities[index].Fact
		if fact == nil || fact.Occurrence != want.Occurrence || fact.GetPath() != want.Path || fact.Language != want.Language || fact.GetLocation().GetStart().GetLine() != want.StartLine || fact.GetLocation().GetEnd().GetLine() != want.EndLine {
			t.Fatalf("file node %d=%+v want=%+v", index, fact, want)
		}
		var original *graphv2.Node
		for _, node := range artifact.Nodes {
			if node.Occurrence == want.Occurrence {
				original = node
				break
			}
		}
		if original == nil || !proto.Equal(fact, original) {
			t.Fatalf("file node %d lost original fields: got=%+v original=%+v", index, fact, original)
		}
	}
	emptyFiles, err := service.FileNodes(t.Context(), graphprotocol.FileNodesRequest{Scope: scope})
	if err != nil || len(emptyFiles.Entities) != 0 {
		t.Fatalf("empty file nodes=%+v err=%v", emptyFiles, err)
	}
	request := graphprotocol.ModuleAggregationRequest{Scope: scope, Assignments: fixture.Assignments, Kinds: fixture.ModuleOptions.Kinds, MinConfidence: fixture.ModuleOptions.MinConfidence, TopPairsPerLink: fixture.ModuleOptions.TopPairsPerLink, PairKinds: fixture.ModuleOptions.PairKinds}
	modules, err := service.ModuleAggregation(t.Context(), request)
	if err != nil || !reflect.DeepEqual(modules.Links, fixture.ModuleLinks) || !reflect.DeepEqual(modules.Pairs, fixture.ModulePairs) {
		t.Fatalf("modules=%+v err=%v", modules, err)
	}
	for _, empty := range []graphprotocol.ModuleAggregationRequest{
		{Scope: scope, Kinds: request.Kinds, MinConfidence: request.MinConfidence, TopPairsPerLink: request.TopPairsPerLink, PairKinds: request.PairKinds},
		{Scope: scope, Assignments: request.Assignments, MinConfidence: request.MinConfidence, TopPairsPerLink: request.TopPairsPerLink, PairKinds: request.PairKinds},
	} {
		if got, callErr := service.ModuleAggregation(t.Context(), empty); callErr != nil || len(got.Links) != 0 || len(got.Pairs) != 0 {
			t.Fatalf("empty module aggregate=%+v err=%v", got, callErr)
		}
	}
	request.MinConfidence = .95
	uncertain, err := service.ModuleAggregation(t.Context(), request)
	if err != nil || !reflect.DeepEqual(uncertain.Links, fixture.UncertainLinks) || len(uncertain.Pairs) != 0 {
		t.Fatalf("uncertain modules=%+v err=%v", uncertain, err)
	}
	assertAggregateUnresolved(t, service, scope, fixture, artifact)
	assertGraphAggregatePlan(t, store, modules.Generations[0].UploadID, artifact.Commit, fixture)
	assertGraphAggregateBounds(t, store, service, scope, modules.Generations[0].UploadID, artifact.Commit, fixture)
}

func assertAggregateUnresolved(t *testing.T, service *graphquery.Service, scope graphprotocol.Scope, fixture graphAggregateFixture, artifact *graphv2.Artifact) {
	t.Helper()
	from, err := service.UnresolvedReferencesFrom(t.Context(), graphprotocol.UnresolvedReferencesRequest{Scope: scope, Occurrence: fixture.Unresolved.Source})
	if err != nil || len(from.References) != 1 {
		t.Fatalf("unresolved from=%+v err=%v", from, err)
	}
	want := fixture.Unresolved
	fact := from.References[0].Fact
	if fact.SourceId != want.SourceID || fact.Source != want.Source || fact.Name != want.Name || fact.Kind != want.Kind || fact.GetPath() != want.Path || fact.GetLanguage() != want.Language || fact.GetLocation().GetStart().GetLine() != want.Line || fact.GetLocation().GetStart().GetCharacter() != want.Column || fact.Occurrence == "" || from.References[0].ID == "" {
		t.Fatalf("unresolved fact=%+v envelope=%+v", fact, from.References[0])
	}
	var original *graphv2.UnresolvedReference
	for _, reference := range artifact.Unresolved {
		if reference.SourceId == want.SourceID {
			original = reference
			break
		}
	}
	if original == nil || !proto.Equal(fact, original) {
		t.Fatalf("unresolved lost original fields: got=%+v original=%+v", fact, original)
	}
	all, err := service.UnresolvedReferencesInFile(t.Context(), graphprotocol.UnresolvedReferencesRequest{Scope: scope, Path: want.Path})
	if err != nil || len(all.References) != 1 || all.References[0].RepositoryID != from.References[0].RepositoryID || all.References[0].ID != from.References[0].ID || !proto.Equal(all.References[0].Fact, from.References[0].Fact) {
		t.Fatalf("all unresolved file=%+v err=%v", all, err)
	}
	one := 1
	inFile, err := service.UnresolvedReferencesInFile(t.Context(), graphprotocol.UnresolvedReferencesRequest{Scope: scope, Path: want.Path, Limit: &one})
	if err != nil || len(inFile.References) != 1 || inFile.References[0].RepositoryID != from.References[0].RepositoryID || inFile.References[0].ID != from.References[0].ID || !proto.Equal(inFile.References[0].Fact, from.References[0].Fact) {
		t.Fatalf("unresolved file=%+v err=%v", inFile, err)
	}
	missing, err := service.UnresolvedReferencesFrom(t.Context(), graphprotocol.UnresolvedReferencesRequest{Scope: scope, Occurrence: "missing-fixture-id"})
	if err != nil || len(missing.References) != 0 {
		t.Fatalf("missing unresolved=%+v err=%v", missing, err)
	}
	missingFile, err := service.UnresolvedReferencesInFile(t.Context(), graphprotocol.UnresolvedReferencesRequest{Scope: scope, Path: "__missing__.ts", Limit: &one})
	if err != nil || len(missingFile.References) != 0 {
		t.Fatalf("missing unresolved file=%+v err=%v", missingFile, err)
	}
	ordered, err := service.UnresolvedReferencesInFile(t.Context(), graphprotocol.UnresolvedReferencesRequest{Scope: scope, Path: "app/index.tsx"})
	if err != nil || len(ordered.References) != 2 || ordered.References[0].Fact.Name != "expo-router" || ordered.References[1].Fact.Name != "router" {
		t.Fatalf("ordered unresolved=%+v err=%v", ordered, err)
	}
	orderedOne, err := service.UnresolvedReferencesInFile(t.Context(), graphprotocol.UnresolvedReferencesRequest{Scope: scope, Path: "app/index.tsx", Limit: &one})
	if err != nil || len(orderedOne.References) != 1 || orderedOne.References[0].Fact.Name != "expo-router" {
		t.Fatalf("capped unresolved=%+v err=%v", orderedOne, err)
	}
}

func assertGraphAggregateBounds(t *testing.T, store *Store, service *graphquery.Service, scope graphprotocol.Scope, uploadID int64, commit string, fixture graphAggregateFixture) {
	t.Helper()
	repositoryID := scopeRepositoryID(t, store, uploadID)
	snapshot := graphquery.QuerySnapshot{RepositoryID: repositoryID, UploadID: uploadID, Commit: commit}
	paths, modules := make([][]byte, 0, len(fixture.Assignments)), make([]string, 0, len(fixture.Assignments))
	for _, assignment := range fixture.Assignments {
		paths, modules = append(paths, []byte(assignment.FilePath)), append(modules, assignment.Module)
	}
	kinds, err := graphqueryTestRelationKinds(fixture.ModuleOptions.Kinds)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = store.QueryModuleAggregation(t.Context(), graphquery.AggregateModuleQuery{Snapshot: snapshot, Assignments: fixture.Assignments, Kinds: kinds, MinConfidence: fixture.ModuleOptions.MinConfidence, Limit: 1}); !errors.Is(err, graphquery.ErrQuerySize) {
		t.Fatalf("module row bound=%v", err)
	}
	if _, err = store.QueryAggregateNames(t.Context(), graphquery.AggregateNamesQuery{Snapshot: snapshot, Values: []string{"trim"}, Operation: "unresolved", Limit: 1}); !errors.Is(err, graphquery.ErrQuerySize) {
		t.Fatalf("unresolved scan bound=%v", err)
	}
	canceled, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err = service.GraphStats(canceled, scope); !errors.Is(err, context.Canceled) {
		t.Fatalf("aggregate cancellation=%v", err)
	}
	if _, err = store.pool.Exec(t.Context(), `with assignments as (select * from unnest($3::bytea[],$4::text[]) as a(path,module)), candidate as (
 select e.occurrence_key from graph_v2_edges e
 join graph_v2_nodes sn on sn.upload_id=e.upload_id and sn.occurrence_key=e.source_key and sn.occurrence=e.source
 join graph_v2_nodes tn on tn.upload_id=e.upload_id and tn.occurrence_key=e.target_key and tn.occurrence=e.target
 join assignments sm on sm.path=sn.path join assignments tm on tm.path=tn.path
 where e.upload_id=$1 and e.kind=any($5::smallint[]) and sm.module<>tm.module order by e.ordinal limit 1)
 update graph_v2_edges e set payload=convert_to(repeat('x',$2),'UTF8') from candidate c where e.upload_id=$1 and e.occurrence_key=c.occurrence_key`, uploadID, graphquery.MaxEntityQueryBytes+1, paths, modules, kinds); err != nil {
		t.Fatal(err)
	}
	if _, err = store.QueryModuleAggregation(t.Context(), graphquery.AggregateModuleQuery{Snapshot: snapshot, Assignments: fixture.Assignments, Kinds: kinds, MinConfidence: fixture.ModuleOptions.MinConfidence, Limit: 5001}); !errors.Is(err, graphquery.ErrQuerySize) {
		t.Fatalf("module byte bound=%v", err)
	}
}

func verifyAggregateSources(t *testing.T) {
	t.Helper()
	data, err := os.ReadFile("../../test/fixtures/codegraph/manifest.json")
	if err != nil {
		t.Fatal(err)
	}
	var manifest struct {
		SHA256 map[string]string `json:"sha256"`
	}
	if err = json.Unmarshal(data, &manifest); err != nil {
		t.Fatal(err)
	}
	checked := 0
	for name, want := range manifest.SHA256 {
		if !strings.HasPrefix(name, "source/") {
			continue
		}
		raw, readErr := os.ReadFile(filepath.Join("../../test/fixtures/codegraph", name))
		if readErr != nil || fmt.Sprintf("%x", sha256.Sum256(raw)) != want {
			t.Fatalf("source identity %s hash=%x err=%v", name, sha256.Sum256(raw), readErr)
		}
		checked++
	}
	if checked != 16 {
		t.Fatalf("source evidence files=%d", checked)
	}
}

func assertGraphAggregatePlan(t *testing.T, store *Store, uploadID int64, commit string, fixture graphAggregateFixture) {
	t.Helper()
	paths, modules := make([][]byte, 0, len(fixture.Assignments)), make([]string, 0, len(fixture.Assignments))
	for _, assignment := range fixture.Assignments {
		paths, modules = append(paths, []byte(assignment.FilePath)), append(modules, assignment.Module)
	}
	kinds, _ := graphqueryTestRelationKinds(fixture.ModuleOptions.Kinds)
	rows, err := store.pool.Query(t.Context(), "explain (analyze,buffers) "+moduleAggregationSQL, scopeRepositoryID(t, store, uploadID), uploadID, commit, paths, modules, graphquery.MaxEntityQueryBytes, kinds, 5001)
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
	if err = rows.Err(); err != nil || !strings.Contains(plan, "Limit") || !strings.Contains(plan, "Buffers") || !strings.Contains(plan, "graph_v2_edges") {
		t.Fatalf("aggregate plan=%s err=%v", plan, err)
	}
	t.Logf("PostgreSQL aggregate plan:\n%s", plan)
}

func graphqueryTestRelationKinds(names []string) ([]int16, error) {
	result := make([]int16, 0, len(names))
	for _, name := range names {
		relation, ok := graphartifact.ParseRelationship(name)
		if !ok {
			return nil, fmt.Errorf("unknown relation %s", name)
		}
		result = append(result, int16(relation.Kind))
	}
	return result, nil
}

func scopeRepositoryID(t *testing.T, store *Store, uploadID int64) int64 {
	t.Helper()
	var repositoryID int64
	if err := store.pool.QueryRow(t.Context(), "select repository_id from graph_uploads where id=$1", uploadID).Scan(&repositoryID); err != nil {
		t.Fatal(err)
	}
	return repositoryID
}
