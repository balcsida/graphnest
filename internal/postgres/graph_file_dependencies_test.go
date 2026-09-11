//go:build integration

package postgres

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/balcsida/graphnest/internal/graphartifact"
	graphv2 "github.com/balcsida/graphnest/internal/graphartifact/v2"
	"github.com/balcsida/graphnest/internal/graphprotocol"
	"github.com/balcsida/graphnest/internal/graphquery"
	"google.golang.org/protobuf/proto"
)

type fileDependencyFixture struct {
	Source struct {
		PositiveFactsSHA256         string `json:"positive_facts_sha256"`
		PositiveRawFactsPass1SHA256 string `json:"positive_raw_facts_pass1_sha256"`
		PositiveRawFactsPass2SHA256 string `json:"positive_raw_facts_pass2_sha256"`
	} `json:"source"`
	Original struct {
		Pairs           []graphprotocol.FileDependencyPair  `json:"pairs"`
		Dependencies    []string                            `json:"dependencies"`
		Dependents      []string                            `json:"dependents"`
		DependentCounts []graphprotocol.FileDependencyCount `json:"dependent_counts"`
		ReachCounts     []graphprotocol.FileDependencyCount `json:"reach_counts"`
		Affected        graphprotocol.AffectedTestsResponse `json:"affected"`
		Unrelated       graphprotocol.AffectedTestsResponse `json:"unrelated"`
	} `json:"original"`
	Positive struct {
		Files    []positiveFile                      `json:"files"`
		Nodes    []positiveNode                      `json:"nodes"`
		Edges    []positiveEdge                      `json:"edges"`
		Pairs    []graphprotocol.FileDependencyPair  `json:"pairs"`
		Cycles   [][]string                          `json:"cycles"`
		Affected graphprotocol.AffectedTestsResponse `json:"affected"`
	} `json:"positive"`
}

type positiveFile struct {
	Path        string          `json:"path"`
	ContentHash string          `json:"content_hash"`
	Language    string          `json:"language"`
	Size        int64           `json:"size"`
	ModifiedAt  int64           `json:"modified_at"`
	IndexedAt   int64           `json:"indexed_at"`
	NodeCount   int64           `json:"node_count"`
	Errors      json.RawMessage `json:"errors"`
	Generated   int             `json:"generated"`
}

type positiveNode struct {
	ID             string          `json:"id"`
	Kind           string          `json:"kind"`
	Name           string          `json:"name"`
	QualifiedName  string          `json:"qualified_name"`
	FilePath       string          `json:"file_path"`
	Language       string          `json:"language"`
	StartLine      int32           `json:"start_line"`
	EndLine        int32           `json:"end_line"`
	StartColumn    int32           `json:"start_column"`
	EndColumn      int32           `json:"end_column"`
	Documentation  *string         `json:"docstring"`
	Signature      *string         `json:"signature"`
	Visibility     *string         `json:"visibility"`
	IsExported     int             `json:"is_exported"`
	IsAsync        int             `json:"is_async"`
	IsStatic       int             `json:"is_static"`
	IsAbstract     int             `json:"is_abstract"`
	Decorators     json.RawMessage `json:"decorators"`
	TypeParameters json.RawMessage `json:"type_parameters"`
	ReturnType     *string         `json:"return_type"`
	UpdatedAt      int64           `json:"updated_at"`
}

type positiveEdge struct {
	ID                   int64
	Source, Target, Kind string
	Metadata, Provenance *string
	Line, Col            *int32
}

func positiveStringList(t *testing.T, data json.RawMessage) *graphv2.StringList {
	t.Helper()
	if len(data) == 0 || string(data) == "null" {
		return nil
	}
	var values []string
	if err := json.Unmarshal(data, &values); err != nil {
		t.Fatal(err)
	}
	return &graphv2.StringList{Values: values}
}

func positivePoint(line, column *int32) *graphv2.Position {
	if line == nil && column == nil {
		return nil
	}
	if line != nil {
		line = proto.Int32(*line - 1)
	}
	return &graphv2.Position{Line: line, Character: column}
}

func positiveExtension(namespace string, data json.RawMessage) *graphv2.Extension {
	if len(data) == 0 || string(data) == "null" {
		return nil
	}
	return &graphv2.Extension{Namespace: namespace, Json: data}
}

func loadFileDependencyFixture(t *testing.T) fileDependencyFixture {
	t.Helper()
	data, err := os.ReadFile("../../test/fixtures/codegraph/file-dependencies.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixture fileDependencyFixture
	if err := json.Unmarshal(data, &fixture); err != nil {
		t.Fatal(err)
	}
	return fixture
}

func fileDependencyService(t *testing.T, artifact *graphv2.Artifact) (*Store, *graphquery.Service, graphprotocol.Scope) {
	t.Helper()
	store, repositoryID := readyGraphStore(t, artifact.Commit)
	if _, err := store.ReplaceGraphV2(t.Context(), repositoryID, GraphPublication{Publisher: "file-dependency-oracle"}, artifact); err != nil {
		t.Fatal(err)
	}
	service := &graphquery.Service{Store: store}
	scope := graphprotocol.Scope{SelectedRepositoryID: repositoryID, Repositories: []graphprotocol.RepositorySnapshot{{ID: repositoryID, GitHubID: 101, Commit: artifact.Commit}}}
	return store, service, scope
}

func TestGraphFileDependenciesRealOracle(t *testing.T) {
	data, err := os.ReadFile(os.Getenv("GRAPHNEST_TEST_CODEGRAPH_V2_FIXTURE"))
	if err != nil {
		t.Fatal(err)
	}
	artifact, err := graphartifact.ParseV2(data, graphartifact.Limits{})
	if err != nil {
		t.Fatal(err)
	}
	fixture := loadFileDependencyFixture(t)
	store, service, scope := fileDependencyService(t, artifact)
	request := graphprotocol.FileDependencyRequest{Scope: scope}
	pairs, err := service.FileDependencyPairs(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	gotPairs := slices.Clone(pairs.Pairs)
	for index := range gotPairs {
		gotPairs[index].References = 0
	}
	if !reflect.DeepEqual(gotPairs, fixture.Original.Pairs) {
		t.Fatalf("pairs=%+v", pairs.Pairs)
	}
	request.MinConfidence = 0.95
	confidentPairs, err := service.FileDependencyPairs(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	for index := range confidentPairs.Pairs {
		confidentPairs.Pairs[index].References = 0
	}
	if want := []graphprotocol.FileDependencyPair{{Source: "app/index.tsx", Target: "app/details.tsx"}}; !reflect.DeepEqual(confidentPairs.Pairs, want) {
		t.Fatalf("confidence pairs=%+v", confidentPairs.Pairs)
	}
	request.MinConfidence = 0
	request.Paths = []string{"main.ts"}
	dependencies, err := service.FileDependencies(t.Context(), request)
	if err != nil || !reflect.DeepEqual(dependencies.Files, fixture.Original.Dependencies) {
		t.Fatalf("dependencies=%+v err=%v", dependencies, err)
	}
	request.Paths = []string{"core.ts"}
	dependents, err := service.FileDependents(t.Context(), request)
	if err != nil || !reflect.DeepEqual(dependents.Files, fixture.Original.Dependents) {
		t.Fatalf("dependents=%+v err=%v", dependents, err)
	}
	request.Paths = []string{"core.ts", "main.ts", "orphan.ts", "__missing__.ts"}
	dependentCounts, err := service.FileDependentCounts(t.Context(), request)
	if err != nil || !reflect.DeepEqual(dependentCounts.Counts, fixture.Original.DependentCounts) {
		t.Fatalf("dependent counts=%+v err=%v", dependentCounts, err)
	}
	request.Paths = []string{"main.ts", "consumer.test.ts", "orphan.ts", "__missing__.ts"}
	reachCounts, err := service.FileReachCounts(t.Context(), request)
	if err != nil || !reflect.DeepEqual(reachCounts.Counts, fixture.Original.ReachCounts) {
		t.Fatalf("reach counts=%+v err=%v", reachCounts, err)
	}
	request.Paths = nil
	emptyDependentCounts, err := service.FileDependentCounts(t.Context(), request)
	if err != nil || len(emptyDependentCounts.Counts) != 0 || len(emptyDependentCounts.Generations) != 1 {
		t.Fatalf("empty dependent counts=%+v err=%v", emptyDependentCounts, err)
	}
	emptyReachCounts, err := service.FileReachCounts(t.Context(), request)
	if err != nil || len(emptyReachCounts.Counts) != 0 || len(emptyReachCounts.Generations) != 1 {
		t.Fatalf("empty reach counts=%+v err=%v", emptyReachCounts, err)
	}
	cycles, err := service.CircularDependencies(t.Context(), graphprotocol.FileDependencyRequest{Scope: scope})
	if err != nil || len(cycles.Cycles) != 0 {
		t.Fatalf("cycles=%+v err=%v", cycles, err)
	}
	for _, want := range []graphprotocol.AffectedTestsResponse{fixture.Original.Affected, fixture.Original.Unrelated} {
		got, err := service.AffectedTests(t.Context(), graphprotocol.AffectedTestsRequest{Scope: scope, ChangedFiles: want.ChangedFiles})
		if err != nil || !reflect.DeepEqual(got.AffectedTests, want.AffectedTests) || got.TotalDependentsTraversed != want.TotalDependentsTraversed {
			t.Fatalf("affected=%+v want=%+v err=%v", got, want, err)
		}
	}
	assertFileDependencyPlan(t, store, scope.Repositories[0].ID, pairs.Generations[0].UploadID, artifact.Commit)
}

func TestGraphFileDependenciesPositiveControl(t *testing.T) {
	fixture := loadFileDependencyFixture(t)
	if len(fixture.Positive.Nodes) != 11 || len(fixture.Positive.Edges) != 16 || len(fixture.Positive.Files) != 4 {
		t.Fatalf("incomplete positive facts: nodes=%d edges=%d files=%d", len(fixture.Positive.Nodes), len(fixture.Positive.Edges), len(fixture.Positive.Files))
	}
	if fixture.Source.PositiveFactsSHA256 != "b8643057b983b9c6a26ef0c46aff983c41b3608aed48a3cabeeb4a37614a9fa0" ||
		fixture.Source.PositiveRawFactsPass1SHA256 != "ab0d8291d67ba4b77e9b7eff0f46a548f085dc1a56dec9618da5e7eb8cc80c45" ||
		fixture.Source.PositiveRawFactsPass2SHA256 != "c7748e7a467cc974541d66de5fefe5cb1e9be5dd073075828ce1dbb7939280a0" {
		t.Fatalf("positive fact identity=%+v", fixture.Source)
	}
	for _, file := range fixture.Positive.Files {
		data, err := os.ReadFile(filepath.Join("../../test/fixtures/codegraph/file-dependencies-source", file.Path))
		if err != nil || int64(len(data)) != file.Size || fmt.Sprintf("%x", sha256.Sum256(data)) != file.ContentHash {
			t.Fatalf("source %s size=%d hash=%x err=%v", file.Path, len(data), sha256.Sum256(data), err)
		}
	}
	t.Logf("verified positive fixture sources: files=4 normalized=%s raw-pass1=%s raw-pass2=%s", fixture.Source.PositiveFactsSHA256, fixture.Source.PositiveRawFactsPass1SHA256, fixture.Source.PositiveRawFactsPass2SHA256)
	artifact := storageV2Artifact()
	artifact.ContentHash, artifact.Nodes, artifact.Edges, artifact.Files, artifact.Unresolved = nil, nil, nil, nil, nil
	for _, node := range fixture.Positive.Nodes {
		path := node.FilePath
		artifact.Nodes = append(artifact.Nodes, &graphv2.Node{
			SourceId: node.ID, Occurrence: node.ID, Kind: node.Kind, Name: node.Name,
			QualifiedName: node.QualifiedName, Path: &path, Language: node.Language,
			Location: &graphv2.Location{
				Start: positivePoint(proto.Int32(node.StartLine), proto.Int32(node.StartColumn)),
				End:   positivePoint(proto.Int32(node.EndLine), proto.Int32(node.EndColumn)),
			},
			Documentation: node.Documentation, Signature: node.Signature, Visibility: node.Visibility,
			IsExported: proto.Bool(node.IsExported != 0), IsAsync: proto.Bool(node.IsAsync != 0),
			IsStatic: proto.Bool(node.IsStatic != 0), IsAbstract: proto.Bool(node.IsAbstract != 0),
			Decorators: positiveStringList(t, node.Decorators), TypeParameters: positiveStringList(t, node.TypeParameters),
			ReturnType: node.ReturnType, UpdatedAt: proto.Int64(node.UpdatedAt),
		})
	}
	for _, edge := range fixture.Positive.Edges {
		relation, ok := graphartifact.ParseRelationship(edge.Kind)
		if !ok {
			t.Fatal(edge.Kind)
		}
		result := &graphv2.Edge{
			SourceId: strconv.FormatInt(edge.ID, 10), Occurrence: "edge:" + strconv.FormatInt(edge.ID, 10),
			Source: edge.Source, Target: edge.Target, Kind: relation.WireKind(), Provenance: edge.Provenance,
		}
		if point := positivePoint(edge.Line, edge.Col); point != nil {
			result.Location = &graphv2.Location{Start: point}
		}
		if edge.Metadata != nil {
			result.Extensions = []*graphv2.Extension{{Namespace: "codegraph.edge-metadata", Json: []byte(*edge.Metadata)}}
			var metadata struct {
				Confidence *float64
				ResolvedBy *string
			}
			if err := json.Unmarshal([]byte(*edge.Metadata), &metadata); err != nil {
				t.Fatal(err)
			}
			result.Confidence, result.ResolutionReason = metadata.Confidence, metadata.ResolvedBy
		}
		artifact.Edges = append(artifact.Edges, result)
	}
	for _, file := range fixture.Positive.Files {
		artifact.Files = append(artifact.Files, &graphv2.File{
			Path: file.Path, ContentHash: file.ContentHash, Language: file.Language, Size: file.Size,
			ModifiedAt: proto.Int64(file.ModifiedAt), IndexedAt: proto.Int64(file.IndexedAt),
			NodeCount: proto.Int64(file.NodeCount), Generated: proto.Bool(file.Generated != 0),
			Errors: positiveExtension("codegraph.extraction-errors", file.Errors),
		})
	}
	store, service, scope := fileDependencyService(t, artifact)
	pairs, err := service.FileDependencyPairs(t.Context(), graphprotocol.FileDependencyRequest{Scope: scope})
	if err != nil {
		t.Fatal(err)
	}
	gotPairs := slices.Clone(pairs.Pairs)
	for index := range gotPairs {
		gotPairs[index].References = 0
	}
	if !reflect.DeepEqual(gotPairs, fixture.Positive.Pairs) {
		t.Fatalf("pairs=%+v", pairs.Pairs)
	}
	var nodes, edges, files int
	if err := store.pool.QueryRow(t.Context(), `select
		(select count(*) from graph_v2_nodes where upload_id=$1),
		(select count(*) from graph_v2_edges where upload_id=$1),
		(select count(*) from graph_v2_files where upload_id=$1)`, pairs.Generations[0].UploadID).Scan(&nodes, &edges, &files); err != nil || nodes != 11 || edges != 16 || files != 4 {
		t.Fatalf("published positive facts: nodes=%d edges=%d files=%d err=%v", nodes, edges, files, err)
	}
	t.Logf("published complete positive fixture: nodes=%d edges=%d files=%d", nodes, edges, files)
	cycles, err := service.CircularDependencies(t.Context(), graphprotocol.FileDependencyRequest{Scope: scope})
	if err != nil || !reflect.DeepEqual(cycles.Cycles, fixture.Positive.Cycles) {
		t.Fatalf("cycles=%+v err=%v", cycles, err)
	}
	want := fixture.Positive.Affected
	affected, err := service.AffectedTests(t.Context(), graphprotocol.AffectedTestsRequest{Scope: scope, ChangedFiles: want.ChangedFiles})
	if err != nil || !reflect.DeepEqual(affected.AffectedTests, want.AffectedTests) || affected.TotalDependentsTraversed != want.TotalDependentsTraversed {
		t.Fatalf("affected=%+v want=%+v err=%v", affected, want, err)
	}
}

func assertFileDependencyPlan(t *testing.T, store *Store, repositoryID, uploadID int64, commit string) {
	t.Helper()
	statement, args := fileDependencyStatement(graphquery.FileDependencyQuery{Snapshot: graphquery.QuerySnapshot{RepositoryID: repositoryID, UploadID: uploadID, Commit: commit}, Paths: []string{"core.ts"}, Direction: "target", Limit: 5001})
	rows, err := store.pool.Query(t.Context(), "explain (analyze,buffers) "+statement, args...)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	lines := []string{}
	for rows.Next() {
		var line string
		if err := rows.Scan(&line); err != nil {
			t.Fatal(err)
		}
		lines = append(lines, line)
	}
	plan := strings.Join(lines, "\n")
	if err := rows.Err(); err != nil || !strings.Contains(plan, "Limit") || !strings.Contains(plan, "actual") || !strings.Contains(plan, "Buffers") || !strings.Contains(plan, "sha256(path)") || !strings.Contains(plan, "target_key") || !strings.Contains(plan, "occurrence_key") || strings.Contains(plan, "Seq Scan") {
		t.Fatalf("file dependency plan=%s err=%v", plan, err)
	}
	t.Logf("PostgreSQL file dependency plan:\n%s", plan)
}
