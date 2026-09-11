//go:build integration

package postgres

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/balcsida/graphnest/internal/authn"
	"github.com/balcsida/graphnest/internal/githubapp"
	"github.com/balcsida/graphnest/internal/graphartifact"
	graphv2 "github.com/balcsida/graphnest/internal/graphartifact/v2"
	"github.com/balcsida/graphnest/internal/graphprotocol"
	"github.com/balcsida/graphnest/internal/graphquery"
	"github.com/balcsida/graphnest/internal/graphservice"
	"github.com/balcsida/graphnest/internal/repository"
	"github.com/balcsida/graphnest/pkg/api"
	"google.golang.org/protobuf/proto"
)

type inspectionGitHub struct {
	t      *testing.T
	commit string
	after  func()
	reads  int
}

type inspectionDirectSource func(context.Context, authn.Principal, api.ReadFileRequest, string) (api.ReadFileResponse, error)

func (f inspectionDirectSource) ReadFileAt(ctx context.Context, p authn.Principal, r api.ReadFileRequest, sha string) (api.ReadFileResponse, error) {
	return f(ctx, p, r, sha)
}

func TestGraphInspectionLegacySourceReplacement(t *testing.T) {
	s, id := readyGraphStore(t, testSHA('a'))
	if _, err := s.ReplaceGraph(t.Context(), id, GraphSourceManaged, artifactFor(id, testSHA('a'), "old")); err != nil {
		t.Fatal(err)
	}
	service := &graphservice.Service{Store: s, Backend: &graphquery.Service{Store: s}, Files: inspectionDirectSource(func(_ context.Context, _ authn.Principal, r api.ReadFileRequest, sha string) (api.ReadFileResponse, error) {
		queueLegacyGraph(t, s, id)
		if _, err := s.ReplaceGraph(t.Context(), id, GraphSourceManaged, artifactFor(id, testSHA('a'), "new")); err != nil {
			t.Fatal(err)
		}
		return api.ReadFileResponse{RepositoryID: r.RepositoryID, Path: r.Path, IndexedSHA: sha, Content: "buffered"}, nil
	})}
	got, err := service.Context(t.Context(), authn.Principal{InstallationID: 10, RepositoryIDs: []int64{101}}, api.GraphContextRequest{Repo: api.GraphRepositorySelector{ID: 101}, GraphSymbolSelector: api.GraphSymbolSelector{UID: "symbol"}, IncludeContent: true})
	if err == nil || got.Symbol != nil || len(got.Commits) > 0 {
		t.Fatalf("legacy replacement exposed source: %+v err=%v", got, err)
	}
}

func TestGraphInspectionOverloadPagination(t *testing.T) {
	s, id := readyGraphStore(t, testSHA('a'))
	a := storageV2Artifact()
	a.ContentHash = nil
	for _, node := range a.Nodes {
		node.Name = "overloaded"
	}
	if _, err := s.ReplaceGraphV2(t.Context(), id, GraphPublication{Publisher: "overloads"}, a); err != nil {
		t.Fatal(err)
	}
	service := &graphservice.Service{Store: s, Backend: &graphquery.Service{Store: s}}
	request := graphservice.InspectEntityRequest{Repo: api.GraphRepositorySelector{ID: 101}, Selector: graphprotocol.EntitySelector{Name: proto.String("overloaded")}, Limit: 1, OmitSource: true}
	p := authn.Principal{InstallationID: 10, RepositoryIDs: []int64{101}}
	first, err := service.InspectEntity(t.Context(), p, request)
	if err != nil || first.Status != "ambiguous" || len(first.Entities) != 1 || first.NextCursor == "" {
		t.Fatalf("first overload page=%+v err=%v", first, err)
	}
	request.Cursor = first.NextCursor
	last, err := service.InspectEntity(t.Context(), p, request)
	if err != nil || last.Status != "ambiguous" || len(last.Entities) != 1 || last.NextCursor != "" || last.Entities[0].ID == first.Entities[0].ID || len(last.Sources) > 0 {
		t.Fatalf("last overload became unique=%+v err=%v", last, err)
	}
}

func TestGraphInspectionOutlineContinuation(t *testing.T) {
	s, id := readyGraphStore(t, testSHA('a'))
	a := storageV2Artifact()
	a.ContentHash, a.Unresolved, a.Diagnostics = nil, nil, nil
	a.Nodes[0].Kind = "file"
	for _, node := range a.Nodes {
		node.Path = proto.String("core.ts")
	}
	a.Files[0].Path = "core.ts"
	a.Edges = []*graphv2.Edge{{Occurrence: "contains", Source: a.Nodes[0].Occurrence, Target: a.Nodes[1].Occurrence, Kind: graphv2.EdgeKind_EDGE_KIND_CONTAINS}}
	if _, err := s.ReplaceGraphV2(t.Context(), id, GraphPublication{Publisher: "outline-pages"}, a); err != nil {
		t.Fatal(err)
	}
	service := &graphservice.Service{Store: s, Backend: &graphquery.Service{Store: s}, Files: &repository.Service{Store: s, GitHub: &inspectionGitHub{t: t, commit: a.Commit}}}
	request := graphservice.InspectFileRequest{Repo: api.GraphRepositorySelector{ID: 101}, Path: "core.ts", Limit: 1}
	p := authn.Principal{InstallationID: 10, RepositoryIDs: []int64{101}}
	first, err := service.InspectFile(t.Context(), p, request)
	if err != nil || first.Complete || len(first.Entities) != 1 || first.NextCursor == "" || !slices.Contains(first.Boundaries, "outline_page") {
		t.Fatalf("first outline page=%+v err=%v", first, err)
	}
	request.Cursor = first.NextCursor
	last, err := service.InspectFile(t.Context(), p, request)
	if err != nil || last.Complete || last.NextCursor != "" || !slices.Contains(last.Boundaries, "outline_page") {
		t.Fatalf("last outline page: complete=%v next=%q boundaries=%v err=%v", last.Complete, last.NextCursor, last.Boundaries, err)
	}
	if len(last.Entities) != 1 || last.Entities[0].ID == first.Entities[0].ID || !proto.Equal(last.Entities[0].Fact, a.Nodes[1]) || !proto.Equal(last.File.Fact, a.Files[0]) || len(last.Sources) != 1 || last.Sources[0].Status != "ok" || last.Sources[0].Content != first.Sources[0].Content || last.Sources[0].Content == "" {
		t.Fatalf("continuation lost facts/source: %+v", last)
	}
}

func TestGraphInspectionFileExtractionErrors(t *testing.T) {
	for _, mode := range []string{"file_only", "entity"} {
		t.Run(mode, func(t *testing.T) {
			s, id := readyGraphStore(t, testSHA('a'))
			a := storageV2Artifact()
			a.ContentHash, a.Unresolved, a.Diagnostics, a.Edges = nil, nil, nil, nil
			a.Files[0].Path = "core.ts"
			a.Files[0].Errors = &graphv2.Extension{Namespace: "codegraph.extraction-errors", Json: []byte(`[{"message":"parse failed","severity":"error","native":{"detail":true}}]`)}
			if mode == "file_only" {
				a.Nodes = nil
			} else {
				a.Nodes = a.Nodes[:1]
				a.Nodes[0].Path = proto.String("core.ts")
				a.Nodes[0].Location = &graphv2.Location{Start: &graphv2.Position{Line: proto.Int32(0), Character: proto.Int32(0)}, End: &graphv2.Position{Line: proto.Int32(0), Character: proto.Int32(1)}}
			}
			if _, err := s.ReplaceGraphV2(t.Context(), id, GraphPublication{Publisher: "extraction-errors"}, a); err != nil {
				t.Fatal(err)
			}
			service := &graphservice.Service{Store: s, Backend: &graphquery.Service{Store: s}, Files: &repository.Service{Store: s, GitHub: &inspectionGitHub{t: t, commit: a.Commit}}}
			p := authn.Principal{InstallationID: 10, RepositoryIDs: []int64{101}}
			var got graphservice.InspectionResponse
			var err error
			if mode == "file_only" {
				got, err = service.InspectFile(t.Context(), p, graphservice.InspectFileRequest{Repo: api.GraphRepositorySelector{ID: 101}, Path: "core.ts"})
			} else {
				got, err = service.InspectEntity(t.Context(), p, graphservice.InspectEntityRequest{Repo: api.GraphRepositorySelector{ID: 101}, Selector: graphprotocol.EntitySelector{Occurrence: &a.Nodes[0].Occurrence}})
			}
			if err != nil || got.Complete || !slices.Contains(got.Boundaries, "file_extraction_errors") || len(got.Generations) != 1 || got.Generations[0].DiagnosticCount != 0 || got.Generations[0].UnresolvedCount != 0 || len(got.Sources) != 1 || got.Sources[0].Status != "ok" || got.Sources[0].Content == "" {
				t.Fatalf("file errors independent of diagnostics: %+v err=%v", got, err)
			}
			if mode == "file_only" {
				if len(got.Entities) != 0 || !proto.Equal(got.File.Fact, a.Files[0]) {
					t.Fatal("lost original failed file")
				}
			} else if !proto.Equal(got.Sources[0].FileErrors, a.Files[0].Errors) {
				t.Fatal("lost original source file errors")
			}
		})
	}
}

func (g *inspectionGitHub) ReadContents(_ context.Context, _ int64, owner, name, path, ref string, _ int64) (githubapp.Content, error) {
	g.t.Helper()
	if ref != g.commit || owner != "acme" || name != "repo-101" {
		g.t.Fatalf("source authority=%s/%s@%s expected=%s", owner, name, ref, g.commit)
	}
	g.reads++
	data, err := os.ReadFile(filepath.Join("../../test/fixtures/codegraph/source", path))
	if err != nil {
		return githubapp.Content{}, err
	}
	if g.after != nil {
		g.after()
	}
	return githubapp.Content{Type: "file", Encoding: "base64", Content: base64.StdEncoding.EncodeToString(data), Size: int64(len(data)), SHA: "fixture-blob"}, nil
}

func TestGraphInspectionRealOracle(t *testing.T) {
	fixture := os.Getenv("GRAPHNEST_TEST_CODEGRAPH_V2_FIXTURE")
	if fixture == "" {
		t.Fatal("required real fixture: set GRAPHNEST_TEST_CODEGRAPH_V2_FIXTURE")
	}
	data, err := os.ReadFile(fixture)
	if err != nil {
		t.Fatal(err)
	}
	a, err := graphartifact.ParseV2(data, graphartifact.Limits{})
	if err != nil {
		t.Fatal(err)
	}
	s, id := readyGraphStore(t, a.Commit)
	publication, err := s.ReplaceGraphV2(t.Context(), id, GraphPublication{Publisher: "inspection-oracle"}, a)
	if err != nil {
		t.Fatal(err)
	}
	// A selected v2 repository remains usable alongside an authorized v1 upload.
	legacy := seedReadyRepository(t, s, 202, a.Commit)
	if _, err := s.ReplaceGraph(t.Context(), legacy, GraphSourceManaged, artifactFor(legacy, a.Commit, "legacy")); err != nil {
		t.Fatal(err)
	}
	p := authn.Principal{InstallationID: 10, RepositoryIDs: []int64{101, 202}}
	gateway := &inspectionGitHub{t: t, commit: a.Commit}
	service := &graphservice.Service{Store: s, Backend: &graphquery.Service{Store: s}, Files: &repository.Service{Store: s, GitHub: gateway}}
	repo := api.GraphRepositorySelector{ID: 101}
	oracleData, err := os.ReadFile("../../test/fixtures/codegraph/library-expected.json")
	if err != nil {
		t.Fatal(err)
	}
	var oracle map[string]json.RawMessage
	if err := json.Unmarshal(oracleData, &oracle); err != nil {
		t.Fatal(err)
	}
	var expectedCode string
	if err := json.Unmarshal(oracle["lib-getCode"], &expectedCode); err != nil {
		t.Fatal(err)
	}
	var expectedContext struct {
		Focal struct {
			ID string `json:"id"`
		} `json:"focal"`
	}
	if err := json.Unmarshal(oracle["lib-getContext"], &expectedContext); err != nil {
		t.Fatal(err)
	}
	files := map[string]*graphv2.File{}
	for _, file := range a.Files {
		files[file.Path] = file
	}
	count := 0
	cursor := ""
	for {
		page, err := service.ListFiles(t.Context(), p, graphservice.ListFilesRequest{Repo: repo, Limit: 3, Cursor: cursor})
		if err != nil {
			t.Fatal(err)
		}
		for _, file := range page.Files {
			if !proto.Equal(file.Fact, files[file.Fact.Path]) {
				t.Fatalf("file differs from producer: %+v", file)
			}
			count++
		}
		if page.NextCursor == "" {
			break
		}
		cursor = page.NextCursor
	}
	if count != len(a.Files) {
		t.Fatalf("inventory=%d expected=%d", count, len(a.Files))
	}
	for _, name := range []string{"core.ts", "consumer.ts", "unicode.ts"} {
		got, err := service.InspectFile(t.Context(), p, graphservice.InspectFileRequest{Repo: repo, Path: "./" + name, Limit: 100})
		if err != nil {
			t.Fatal(err)
		}
		want, err := os.ReadFile(filepath.Join("../../test/fixtures/codegraph/source", name))
		if err != nil {
			t.Fatal(err)
		}
		if got.Status != "ok" || len(got.Sources) != 1 || got.Sources[0].Content != string(want) {
			t.Fatalf("verbatim %s=%+v", name, got)
		}
		if got.Structure == nil || len(got.Structure.Edges) == 0 {
			t.Fatalf("missing file containment structure: %s", name)
		}
		nodes := map[string]*graphv2.Node{}
		for _, n := range a.Nodes {
			if n.GetPath() == name {
				nodes[n.Occurrence] = n
			}
		}
		if len(got.Entities) != len(nodes) {
			t.Fatalf("outline %s count=%d want=%d", name, len(got.Entities), len(nodes))
		}
		for _, n := range got.Entities {
			if !proto.Equal(n.Fact, nodes[n.Fact.Occurrence]) {
				t.Fatalf("outline lost original occurrence: %+v", n)
			}
		}
	}
	var sourceOracle struct {
		From, To int
		Lines    []string
	}
	if err := json.Unmarshal(oracle["ui-source-verbatim"], &sourceOracle); err != nil {
		t.Fatal(err)
	}
	rangeResult, err := service.InspectFile(t.Context(), p, graphservice.InspectFileRequest{Repo: repo, Path: "consumer.ts", StartLine: sourceOracle.From, EndLine: sourceOracle.To})
	if err != nil || rangeResult.Sources[0].Content != strings.Join(sourceOracle.Lines, "\n") {
		t.Fatalf("ui-source-verbatim=%+v err=%v", rangeResult, err)
	}
	root, err := service.InspectEntity(t.Context(), p, graphservice.InspectEntityRequest{Repo: repo, Selector: graphprotocol.EntitySelector{Occurrence: &expectedContext.Focal.ID}})
	if err != nil || root.Status != "ok" || len(root.Sources) == 0 || root.Sources[0].Content != expectedCode {
		t.Fatalf("lib-getCode/getContext=%+v err=%v", root, err)
	}
	for index, graph := range []*graphprotocol.TraverseResponse{root.Callers, root.Callees} {
		wantEdges := 0
		for _, edge := range a.Edges {
			if edge.Kind == graphv2.EdgeKind_EDGE_KIND_CALLS && (index == 0 && edge.Target == expectedContext.Focal.ID || index == 1 && edge.Source == expectedContext.Focal.ID) {
				wantEdges++
			}
		}
		if len(graph.Edges) != wantEdges {
			t.Fatalf("caller/callee occurrence recall=%d want=%d direction=%d", len(graph.Edges), wantEdges, index)
		}
		for _, edge := range graph.Edges {
			found := false
			for _, original := range a.Edges {
				if proto.Equal(edge.Fact, original) {
					found = true
					break
				}
			}
			if !found {
				t.Fatalf("invented caller/callee evidence=%+v", edge)
			}
		}
	}
	for _, source := range root.Sources {
		if source.Status != "ok" {
			t.Fatalf("real source unavailable: %+v", source)
		}
		data, err := os.ReadFile(filepath.Join("../../test/fixtures/codegraph/source", source.Path))
		if err != nil {
			t.Fatal(err)
		}
		lines := strings.Split(string(data), "\n")
		if source.Content != strings.Join(lines[source.StartLine-1:source.EndLine], "\n") {
			t.Fatalf("nonverbatim entity context: %+v", source)
		}
		from, e1 := graphartifact.SourceOffset(string(data), source.Range.Start)
		to, e2 := graphartifact.SourceOffset(string(data), source.Range.End)
		if e1 != nil || e2 != nil || source.Selection == nil || source.Content[source.Selection.StartByte:source.Selection.EndByte] != string(data[from:to]) {
			t.Fatalf("lost original UTF-16 coordinates: %+v", source)
		}
	}
	for _, mode := range []string{"generation", "sha", "grant"} {
		t.Run(mode, func(t *testing.T) {
			gateway.after = func() {
				gateway.after = nil
				switch mode {
				case "generation":
					next := proto.Clone(a).(*graphv2.Artifact)
					next.ContentHash = nil
					next.Producer.Version = "replacement"
					if _, err := s.ReplaceGraphV2(t.Context(), id, GraphPublication{Publisher: "inspection-oracle", ExpectedActiveID: publication.Upload.ID}, next); err != nil {
						t.Fatal(err)
					}
				case "sha":
					if _, err := s.pool.Exec(t.Context(), "update repositories set indexed_sha=$2 where id=$1", id, testSHA('b')); err != nil {
						t.Fatal(err)
					}
				case "grant":
					if _, err := s.pool.Exec(t.Context(), "update repositories set enabled=false where id=$1", id); err != nil {
						t.Fatal(err)
					}
				}
			}
			got, err := service.InspectFile(t.Context(), p, graphservice.InspectFileRequest{Repo: repo, Path: "consumer.ts"})
			if err == nil || len(got.Sources) != 0 || got.File != nil || len(got.Entities) != 0 {
				t.Fatalf("buffered data after %s: %+v err=%v", mode, got, err)
			}
			if _, err := s.pool.Exec(t.Context(), "update repositories set indexed_sha=$2,enabled=true where id=$1", id, a.Commit); err != nil {
				t.Fatal(err)
			}
		})
	}
	t.Logf("real oracle: %d original file facts; 3 complete source files/ outlines; lib-getCode/getContext occurrence %s; ui-source-verbatim; %d exact-SHA reads", count, expectedContext.Focal.ID, gateway.reads)
}
