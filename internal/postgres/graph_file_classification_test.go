//go:build integration

package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/balcsida/graphnest/internal/graphartifact"
	graphv2 "github.com/balcsida/graphnest/internal/graphartifact/v2"
	"github.com/balcsida/graphnest/internal/graphprotocol"
	"github.com/balcsida/graphnest/internal/graphquery"
	"google.golang.org/protobuf/proto"
)

func TestGraphFileClassificationRealOracle(t *testing.T) {
	fixture := os.Getenv("GRAPHNEST_TEST_CODEGRAPH_V2_FIXTURE")
	if fixture == "" {
		t.Skip("requires exported real CodeGraph fixture")
	}
	data, err := os.ReadFile(fixture)
	if err != nil {
		t.Fatal(err)
	}
	a, err := graphartifact.ParseV2(data, graphartifact.Limits{})
	if err != nil {
		t.Fatal(err)
	}
	data, err = os.ReadFile("../../test/fixtures/codegraph/file-classification.json")
	if err != nil {
		t.Fatal(err)
	}
	var oracle struct {
		Reference               string   `json:"reference"`
		RealPaths               []string `json:"realPaths"`
		PersistedGeneratedCount int64    `json:"persistedGeneratedCount"`
		TotalFiles              int64    `json:"totalFiles"`
	}
	if err = json.Unmarshal(data, &oracle); err != nil {
		t.Fatal(err)
	}
	if oracle.Reference != "b9ca4b7981116909900368cc1686a1074cd4d4c1" || len(oracle.RealPaths) != 13 {
		t.Fatal("wrong file classification oracle")
	}
	s, id := readyGraphStore(t, a.Commit)
	if _, err = s.ReplaceGraphV2(t.Context(), id, GraphPublication{Publisher: "classification-oracle"}, a); err != nil {
		t.Fatal(err)
	}
	service := &graphquery.Service{Store: s}
	scope := graphprotocol.Scope{SelectedRepositoryID: id, Repositories: []graphprotocol.RepositorySnapshot{{ID: id, GitHubID: 101, Commit: a.Commit}}}
	got, err := service.FileClassifications(t.Context(), graphprotocol.FileClassificationRequest{Scope: scope, Paths: oracle.RealPaths})
	if err != nil || len(got.Files) != 13 {
		t.Fatalf("real classifications=%+v err=%v", got, err)
	}
	for i, file := range got.Files {
		if file.Path != oracle.RealPaths[i] || !file.Present || file.PersistedGenerated == nil || *file.PersistedGenerated || file.Generated || file.Ambient {
			t.Fatalf("real row %d=%+v", i, file)
		}
	}
	count, err := service.GeneratedFileCount(t.Context(), graphprotocol.GeneratedFileCountRequest{Scope: scope})
	if err != nil || count.GeneratedFiles != oracle.PersistedGeneratedCount || count.TotalFiles != oracle.TotalFiles {
		t.Fatalf("real count=%+v err=%v", count, err)
	}
	outside, err := service.FileClassifications(t.Context(), graphprotocol.FileClassificationRequest{Scope: scope, Paths: []string{"outside/contract.pb.go", "outside/ordinary.ts"}})
	if err != nil || len(outside.Files) != 2 || outside.Files[0].Present || !outside.Files[0].Generated || outside.Files[0].Ambient || outside.Files[1].Present || outside.Files[1].Generated || outside.Files[1].Ambient {
		t.Fatalf("outside-list behavior=%+v err=%v", outside, err)
	}
	t.Log("13 original rows retain explicit false producer flags; generated count 0/13; constructed outside paths are not producer facts")
}

func TestGraphFileClassificationStructureCountsAndReplacement(t *testing.T) {
	s, id := readyGraphStore(t, testSHA('a'))
	a := storageV2Artifact()
	a.ContentHash = nil
	a.Nodes = nil
	a.Edges = nil
	a.Files = nil
	a.Unresolved = nil
	generated, ordinary := true, false
	paths := []string{"ambient.d.ts", "standalone-types.ts", "type-only.ts", "runtime.d.ts", "calls.d.ts", "instantiates.d.ts", "bookkeeping.d.ts", "empty.d.ts", "unparsed.d.ts", "outside.ts", "banner.ts", "false.ts", "absent.ts", "filename.generated.ts"}
	for i, path := range paths {
		file := &graphv2.File{Path: path, ContentHash: fmt.Sprintf("%064x", i+1), Language: "typescript"}
		switch path {
		case "banner.ts":
			file.Generated = &generated
		case "false.ts":
			file.Generated = &ordinary
		case "unparsed.d.ts":
			file.Errors = &graphv2.Extension{Namespace: "codegraph.extraction-errors", Json: []byte(`[{"message":"parse failed"}]`)}
		}
		a.Files = append(a.Files, file)
	}
	add := func(occurrence, kind, path string) {
		a.Nodes = append(a.Nodes, &graphv2.Node{SourceId: occurrence, Occurrence: occurrence, Kind: kind, Name: occurrence, Path: proto.String(path), Language: "typescript"})
	}
	for _, kind := range []string{"interface", "type_alias", "enum", "enum_member", "namespace", "file", "import", "export", "parameter"} {
		add("ambient-"+kind, kind, "ambient.d.ts")
	}
	add("standalone-type", "type_alias", "standalone-types.ts")
	add("node-only", "interface", "node-only.d.ts")
	add("unparsed-partial", "interface", "unparsed.d.ts")
	add("type-only", "interface", "type-only.ts")
	add("runtime", "class", "runtime.d.ts")
	add("calls", "interface", "calls.d.ts")
	add("instantiates", "interface", "instantiates.d.ts")
	for _, kind := range []string{"file", "import", "export", "parameter"} {
		add("bookkeeping-"+kind, kind, "bookkeeping.d.ts")
	}
	add("outside", "function", "outside.ts")
	a.Edges = []*graphv2.Edge{
		{Occurrence: "ambient-internal", Source: "ambient-interface", Target: "ambient-type_alias", Kind: graphv2.EdgeKind_EDGE_KIND_REFERENCES},
		{Occurrence: "external-dependent", Source: "outside", Target: "type-only", Kind: graphv2.EdgeKind_EDGE_KIND_REFERENCES},
		{Occurrence: "originating-call", Source: "calls", Target: "outside", Kind: graphv2.EdgeKind_EDGE_KIND_CALLS},
		{Occurrence: "originating-instantiation", Source: "instantiates", Target: "runtime", Kind: graphv2.EdgeKind_EDGE_KIND_INSTANTIATES},
	}
	pub, err := s.ReplaceGraphV2(t.Context(), id, GraphPublication{Publisher: "classification"}, a)
	if err != nil {
		t.Fatal(err)
	}
	service := &graphquery.Service{Store: s}
	scope := graphprotocol.Scope{SelectedRepositoryID: id, Repositories: []graphprotocol.RepositorySnapshot{{ID: id, GitHubID: 101, Commit: a.Commit}}}
	requested := []string{"ambient.d.ts", "standalone-types.ts", "type-only.ts", "runtime.d.ts", "calls.d.ts", "instantiates.d.ts", "bookkeeping.d.ts", "empty.d.ts", "unparsed.d.ts", "banner.ts", "false.ts", "absent.ts", "filename.generated.ts", "missing.d.ts", "node-only.d.ts"}
	got, err := service.FileClassifications(t.Context(), graphprotocol.FileClassificationRequest{Scope: scope, Paths: requested})
	if err != nil {
		t.Fatal(err)
	}
	byPath := map[string]graphprotocol.FileClassification{}
	for _, file := range got.Files {
		byPath[file.Path] = file
	}
	if !byPath["ambient.d.ts"].Ambient || !byPath["standalone-types.ts"].Ambient {
		t.Fatalf("extension-independent allowed declarations not ambient: %+v %+v", byPath["ambient.d.ts"], byPath["standalone-types.ts"])
	}
	for _, path := range []string{"type-only.ts", "runtime.d.ts", "calls.d.ts", "instantiates.d.ts", "bookkeeping.d.ts", "empty.d.ts", "unparsed.d.ts", "missing.d.ts", "node-only.d.ts"} {
		if byPath[path].Ambient {
			t.Fatalf("structural negative %s=%+v", path, byPath[path])
		}
	}
	if !byPath["banner.ts"].Generated || byPath["banner.ts"].PersistedGenerated == nil || !*byPath["banner.ts"].PersistedGenerated || byPath["false.ts"].PersistedGenerated == nil || *byPath["false.ts"].PersistedGenerated || byPath["absent.ts"].PersistedGenerated != nil || !byPath["filename.generated.ts"].Generated || byPath["filename.generated.ts"].PersistedGenerated != nil {
		t.Fatalf("producer/derived flags=%+v", byPath)
	}
	count, err := service.GeneratedFileCount(t.Context(), graphprotocol.GeneratedFileCountRequest{Scope: scope})
	if err != nil || count.GeneratedFiles != 1 || count.TotalFiles != int64(len(paths)) {
		t.Fatalf("count=%+v err=%v", count, err)
	}
	boundary := make([]string, 64)
	for i := range boundary {
		boundary[i] = fmt.Sprintf("missing/%02d.pb.go", i)
	}
	bounded, err := service.FileClassifications(t.Context(), graphprotocol.FileClassificationRequest{Scope: scope, Paths: boundary})
	if err != nil || len(bounded.Files) != 64 || !bounded.Files[0].Generated || bounded.Files[0].Present {
		t.Fatalf("64-path boundary=%+v err=%v", bounded, err)
	}
	replacement := proto.Clone(a).(*graphv2.Artifact)
	replacement.ContentHash = nil
	for _, file := range replacement.Files {
		file.Generated = nil
	}
	replacementPub, err := s.ReplaceGraphV2(t.Context(), id, GraphPublication{Publisher: "classification", ExpectedActiveID: pub.Upload.ID}, replacement)
	if err != nil {
		t.Fatal(err)
	}
	count, err = service.GeneratedFileCount(t.Context(), graphprotocol.GeneratedFileCountRequest{Scope: scope})
	if err != nil || count.GeneratedFiles != 0 || count.TotalFiles != int64(len(paths)) {
		t.Fatalf("replacement count=%+v err=%v", count, err)
	}
	if _, err = s.pool.Exec(t.Context(), `update graph_v2_files set payload=$2 where upload_id=$1 and path=$3`, replacementPub.Upload.ID, []byte{0xff}, []byte("ambient.d.ts")); err != nil {
		t.Fatal(err)
	}
	corrupt, err := service.FileClassifications(t.Context(), graphprotocol.FileClassificationRequest{Scope: scope, Paths: []string{"false.ts", "ambient.d.ts"}})
	if err == nil || !strings.Contains(err.Error(), "decode graph file") || len(corrupt.Files) != 0 {
		t.Fatalf("corrupt payload result=%+v err=%v", corrupt, err)
	}
}

func TestGraphFileClassificationHiddenRepositoryAndCancellation(t *testing.T) {
	s, id := readyGraphStore(t, testSHA('a'))
	a := storageV2Artifact()
	a.ContentHash = nil
	a.Nodes = nil
	a.Edges = nil
	a.Unresolved = nil
	a.Files = []*graphv2.File{{Path: "visible.ts", ContentHash: strings.Repeat("a", 64)}}
	if _, err := s.ReplaceGraphV2(t.Context(), id, GraphPublication{Publisher: "visible"}, a); err != nil {
		t.Fatal(err)
	}
	hiddenID := seedReadyRepository(t, s, 202, a.Commit)
	hidden := proto.Clone(a).(*graphv2.Artifact)
	hidden.Repository = "202"
	hidden.ContentHash = nil
	hidden.Files[0].Generated = proto.Bool(true)
	if _, err := s.ReplaceGraphV2(t.Context(), hiddenID, GraphPublication{Publisher: "hidden"}, hidden); err != nil {
		t.Fatal(err)
	}
	scope := graphprotocol.Scope{SelectedRepositoryID: id, Repositories: []graphprotocol.RepositorySnapshot{{ID: id, GitHubID: 101, Commit: a.Commit}, {ID: hiddenID, GitHubID: 202, Commit: a.Commit}}}
	service := &graphquery.Service{Store: s}
	count, err := service.GeneratedFileCount(t.Context(), graphprotocol.GeneratedFileCountRequest{Scope: scope})
	if err != nil || count.GeneratedFiles != 0 || count.TotalFiles != 1 || len(count.Generations) != 2 {
		t.Fatalf("hidden count=%+v err=%v", count, err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err = service.FileClassifications(ctx, graphprotocol.FileClassificationRequest{Scope: scope, Paths: []string{"visible.ts"}}); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation=%v", err)
	}
}

func TestGraphFileClassificationQueryPlan(t *testing.T) {
	s, id := readyGraphStore(t, testSHA('a'))
	a := storageV2Artifact()
	a.ContentHash = nil
	a.Nodes = nil
	a.Edges = nil
	a.Files = nil
	a.Unresolved = nil
	for i := range 4000 {
		path := fmt.Sprintf("types/%04d.ts", i)
		occurrence := fmt.Sprintf("declaration:%04d", i)
		a.Files = append(a.Files, &graphv2.File{Path: path, ContentHash: fmt.Sprintf("%064x", i+1)})
		a.Nodes = append(a.Nodes, &graphv2.Node{SourceId: occurrence, Occurrence: occurrence, Kind: "interface", Name: occurrence, Path: &path})
		a.Edges = append(a.Edges, &graphv2.Edge{Occurrence: "call:" + occurrence, Source: occurrence, Target: occurrence, Kind: graphv2.EdgeKind_EDGE_KIND_CALLS})
	}
	pub, err := s.ReplaceGraphV2(t.Context(), id, GraphPublication{Publisher: "classification-plan"}, a)
	if err != nil {
		t.Fatal(err)
	}
	for _, table := range []string{"graph_v2_files", "graph_v2_nodes", "graph_v2_edges", "graph_uploads"} {
		if _, err = s.pool.Exec(t.Context(), "analyze "+table); err != nil {
			t.Fatal(err)
		}
	}
	query := graphquery.FileClassificationQuery{Snapshots: []graphquery.QuerySnapshot{{RepositoryID: id, UploadID: pub.Upload.ID, Commit: a.Commit}}, Paths: []string{"types/0123.ts"}}
	sql, args := fileClassificationSQL(query)
	rows, err := s.pool.Query(t.Context(), "explain (analyze,buffers) "+sql, args...)
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
	if err = rows.Err(); err != nil {
		t.Fatal(err)
	}
	plan := strings.Join(lines, "\n")
	if !strings.Contains(sql, "with ordinality") || !strings.Contains(plan, "graph_v2_files_pkey") || !strings.Contains(plan, "graph_v2_nodes_path") || !strings.Contains(plan, "graph_v2_edges_outgoing") || !strings.Contains(plan, "graph_v2_edges_incoming") || strings.Contains(plan, "Seq Scan on graph_v2_files") || strings.Contains(plan, "Seq Scan on graph_v2_nodes") || strings.Contains(plan, "Seq Scan on graph_v2_edges") {
		t.Fatalf("unbounded classification plan:\n%s", plan)
	}
}
