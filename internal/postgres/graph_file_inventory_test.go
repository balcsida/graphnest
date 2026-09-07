//go:build integration

package postgres

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/balcsida/graphnest/internal/authn"
	"github.com/balcsida/graphnest/internal/graphartifact"
	graphv2 "github.com/balcsida/graphnest/internal/graphartifact/v2"
	"github.com/balcsida/graphnest/internal/graphprotocol"
	"github.com/balcsida/graphnest/internal/graphquery"
	"github.com/balcsida/graphnest/internal/graphservice"
	"github.com/balcsida/graphnest/pkg/api"
	"google.golang.org/protobuf/proto"
)

type inventoryAfterQuery struct {
	*graphquery.Service
	after func()
}

func (b *inventoryAfterQuery) IndexedFiles(ctx context.Context, r graphprotocol.FilesRequest) (graphprotocol.FilesResponse, error) {
	page, err := b.Service.IndexedFiles(ctx, r)
	if err == nil {
		b.after()
	}
	return page, err
}

func TestGraphFileInventoryFinalAuthority(t *testing.T) {
	for _, mode := range []string{"generation", "sha", "grant"} {
		t.Run(mode, func(t *testing.T) {
			s, id := readyGraphStore(t, testSHA('a'))
			a := storageV2Artifact()
			publication, err := s.ReplaceGraphV2(t.Context(), id, GraphPublication{Publisher: "inventory-authority"}, a)
			if err != nil {
				t.Fatal(err)
			}
			backend := &inventoryAfterQuery{Service: &graphquery.Service{Store: s}, after: func() {
				switch mode {
				case "generation":
					next := proto.Clone(a).(*graphv2.Artifact)
					next.ContentHash = nil
					next.Producer.Version = "replacement"
					if _, err := s.ReplaceGraphV2(t.Context(), id, GraphPublication{Publisher: "inventory-authority", ExpectedActiveID: publication.Upload.ID}, next); err != nil {
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
			}}
			service := &graphservice.Service{Store: s, Backend: backend}
			got, err := service.FileInventory(t.Context(), authn.Principal{InstallationID: 10, RepositoryIDs: []int64{101}}, graphservice.FileInventoryRequest{Repo: api.GraphRepositorySelector{ID: 101}})
			if err == nil || !reflect.DeepEqual(got, graphservice.FileInventoryResponse{}) {
				t.Fatalf("leaked after %s: %+v err=%v", mode, got, err)
			}
		})
	}
}

func TestGraphFileInventoryRealOracle(t *testing.T) {
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
	if _, err = s.ReplaceGraphV2(t.Context(), id, GraphPublication{Publisher: "inventory-oracle"}, a); err != nil {
		t.Fatal(err)
	}
	legacy := seedReadyRepository(t, s, 202, a.Commit)
	if _, err = s.ReplaceGraph(t.Context(), legacy, GraphSourceManaged, artifactFor(legacy, a.Commit, "legacy")); err != nil {
		t.Fatal(err)
	}
	p := authn.Principal{InstallationID: 10, RepositoryIDs: []int64{101, 202}}
	service := &graphservice.Service{Store: s, Backend: &graphquery.Service{Store: s}}
	repo := api.GraphRepositorySelector{ID: 101}
	data, err = os.ReadFile("testdata/file-inventory-reference.json")
	if err != nil {
		t.Fatal(err)
	}
	var oracle struct {
		Reference string
		Results   map[string]struct {
			Args struct {
				Path, Pattern, Format string
				MaxDepth              *int  `json:"maxDepth"`
				IncludeMetadata       *bool `json:"includeMetadata"`
			}
			Text string
		}
	}
	if err = json.Unmarshal(data, &oracle); err != nil {
		t.Fatal(err)
	}
	if oracle.Reference != "b9ca4b7981116909900368cc1686a1074cd4d4c1" || len(oracle.Results) != 9 {
		t.Fatal("wrong reference")
	}
	original := map[string]*graphv2.File{}
	for _, f := range a.Files {
		original[f.Path] = f
	}
	for name, answer := range oracle.Results {
		t.Run(name, func(t *testing.T) {
			r := graphservice.FileInventoryRequest{Repo: repo, Path: answer.Args.Path, Pattern: answer.Args.Pattern, Format: answer.Args.Format, MaxDepth: answer.Args.MaxDepth, IncludeMetadata: answer.Args.IncludeMetadata}
			got, err := service.FileInventory(t.Context(), p, r)
			if err != nil {
				t.Fatal(err)
			}
			if text := renderInventoryOracle(got); text != answer.Text {
				t.Fatalf("pinned answer differs\nGOT:\n%s\nWANT:\n%s", text, answer.Text)
			}
			if got.Complete != (name != "tree-depth-1") {
				t.Fatalf("completeness=%+v", got)
			}
			if name == "tree-depth-1" && (got.VisibleFiles != 11 || !slices.Contains(got.Boundaries, "max_depth")) {
				t.Fatalf("depth boundary=%+v", got)
			}
			for _, f := range inventoryOracleFiles(got) {
				if answer.Args.IncludeMetadata != nil && !*answer.Args.IncludeMetadata {
					if f.Metadata != nil {
						t.Fatal("metadata not omitted")
					}
					continue
				}
				if !proto.Equal(f.Metadata, original[f.Path]) {
					t.Fatalf("original metadata changed: %s", f.Path)
				}
			}
			if len(got.Generations) != 1 || got.Generations[0].RepositoryID != 101 || got.Generations[0].Commit != a.Commit {
				t.Fatalf("selected v2 provenance=%+v", got.Generations)
			}
		})
	}
	for _, format := range []string{"tree", "flat", "grouped"} {
		t.Run("paging/"+format, func(t *testing.T) {
			r := graphservice.FileInventoryRequest{Repo: repo, Format: format, Limit: 3}
			seen := map[string]bool{}
			for {
				page, err := service.FileInventory(t.Context(), p, r)
				if err != nil {
					t.Fatal(err)
				}
				if page.Complete || !slices.Contains(page.Boundaries, "file_page") || page.TotalFiles != 13 || page.PageFiles > 3 {
					t.Fatalf("page metadata=%+v", page)
				}
				for _, f := range inventoryOracleFiles(page) {
					if seen[f.Path] || !proto.Equal(f.Metadata, original[f.Path]) {
						t.Fatalf("duplicate/changed file=%s", f.Path)
					}
					seen[f.Path] = true
				}
				if page.NextCursor == "" {
					break
				}
				r.Cursor = page.NextCursor
			}
			if len(seen) != 13 {
				t.Fatalf("page union=%d", len(seen))
			}
		})
	}
	engine := service.Backend.(*graphquery.Service)
	engine.Limits.MaxRows = 1
	first, err := service.FileInventory(t.Context(), p, graphservice.FileInventoryRequest{Repo: repo, Format: "grouped"})
	if err != nil || first.PageFiles != 1 || first.TotalFiles != 13 || first.NextCursor == "" {
		t.Fatalf("lower engine limit=%+v err=%v", first, err)
	}
	changed := graphservice.FileInventoryRequest{Repo: repo, Format: "flat", Pattern: "*.ts", Cursor: first.NextCursor}
	if got, err := service.FileInventory(t.Context(), p, changed); err == nil || !reflect.DeepEqual(got, graphservice.FileInventoryResponse{}) {
		t.Fatalf("changed-filter cursor=%+v err=%v", got, err)
	}
	engine.Limits.MaxRows = 100
	// The high-level path is a literal prefix after root-spelling normalization,
	// including exact files. The low-level Directory continues to mean children.
	for _, prefix := range []string{"core.ts", "/core.ts", "./core.ts", "\\core.ts"} {
		got, err := service.FileInventory(t.Context(), p, graphservice.FileInventoryRequest{Repo: repo, Path: prefix, Format: "flat"})
		if err != nil || got.TotalFiles != 1 || len(got.Files) != 1 || got.Files[0].Path != "core.ts" {
			t.Fatalf("file prefix %q=%+v err=%v", prefix, got, err)
		}
	}
	t.Log("13 original file facts; 9 exact pinned presentation answers; 3 complete page unions; selected v2 beside v1; *.ts retains 8 answers including both TSX files")
}

// This test-only formatter compares domain projections with original pinned tool
// answers. Public transport/rendering remains outside this implementation.
func renderInventoryOracle(r graphservice.FileInventoryResponse) string {
	if r.TotalFiles == 0 {
		return "No files found matching the criteria."
	}
	file := func(f graphservice.InventoryFile, language bool) string {
		if f.Metadata == nil {
			return ""
		}
		if language {
			return fmt.Sprintf(" (%s, %d symbols)", f.Metadata.Language, f.Metadata.GetNodeCount())
		}
		return fmt.Sprintf(" (%d symbols)", f.Metadata.GetNodeCount())
	}
	lines := []string{}
	switch r.Format {
	case "flat":
		lines = append(lines, fmt.Sprintf("**Files (%d)**", r.TotalFiles), "")
		for _, f := range r.Files {
			lines = append(lines, "- "+f.Path+file(f, true))
		}
	case "grouped":
		lines = append(lines, fmt.Sprintf("**Files by Language (%d total)**", r.TotalFiles), "")
		for _, group := range r.Groups {
			lines = append(lines, fmt.Sprintf("**%s (%d)**", group.Language, len(group.Files)))
			for _, f := range group.Files {
				lines = append(lines, "- "+f.Path+file(f, false))
			}
			lines = append(lines, "")
		}
	case "tree":
		lines = append(lines, fmt.Sprintf("**Project Structure (%d files)**", r.TotalFiles), "")
		var walk func([]*graphservice.InventoryTree, string)
		walk = func(nodes []*graphservice.InventoryTree, prefix string) {
			for index, node := range nodes {
				connector, next := "├── ", "│   "
				if index == len(nodes)-1 {
					connector, next = "└── ", "    "
				}
				line := prefix + connector + node.Name
				if node.File != nil {
					line += file(*node.File, true)
				}
				lines = append(lines, line)
				walk(node.Children, prefix+next)
			}
		}
		walk(r.Tree, "")
	}
	return strings.Join(lines, "\n")
}

func inventoryOracleFiles(r graphservice.FileInventoryResponse) []graphservice.InventoryFile {
	files := append([]graphservice.InventoryFile(nil), r.Files...)
	for _, group := range r.Groups {
		files = append(files, group.Files...)
	}
	var walk func([]*graphservice.InventoryTree)
	walk = func(nodes []*graphservice.InventoryTree) {
		for _, node := range nodes {
			if node.File != nil {
				files = append(files, *node.File)
			}
			walk(node.Children)
		}
	}
	walk(r.Tree)
	return files
}

func TestGraphFileInventoryPattern(t *testing.T) {
	s, id := readyGraphStore(t, testSHA('a'))
	a := storageV2Artifact()
	a.ContentHash = nil
	a.Files = nil
	for _, path := range []string{"src/name.ts", "src/name.tsx", "src/note.txt", "outside/a.ts"} {
		a.Files = append(a.Files, &graphv2.File{Path: path, ContentHash: testSHA('a') + testSHA('a')[:24], Size: 1})
	}
	if _, err := s.ReplaceGraphV2(t.Context(), id, GraphPublication{Publisher: "inventory"}, a); err != nil {
		t.Fatal(err)
	}
	r := graphprotocol.FilesRequest{Scope: graphprotocol.Scope{SelectedRepositoryID: id, Repositories: []graphprotocol.RepositorySnapshot{{ID: id, GitHubID: 101, Commit: a.Commit}}}}
	if err := json.Unmarshal([]byte(`{"prefix":"src","pattern":"*.ts","include_count":true}`), &r); err != nil {
		t.Fatal(err)
	}
	got, err := (&graphquery.Service{Store: s}).IndexedFiles(t.Context(), r)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Files) != 2 || got.Files[0].Fact.Path != "src/name.ts" || got.Files[1].Fact.Path != "src/name.tsx" {
		t.Fatalf("required inventory *.ts answers: %+v", got.Files)
	}
	data, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	var count struct {
		TotalFiles *int64 `json:"total_files"`
	}
	if err := json.Unmarshal(data, &count); err != nil {
		t.Fatal(err)
	}
	if count.TotalFiles == nil || *count.TotalFiles != 2 {
		t.Fatalf("filtered count missing: %s", data)
	}
}

func TestGraphFileInventoryPatternUnicode(t *testing.T) {
	s, id := readyGraphStore(t, testSHA('a'))
	a := storageV2Artifact()
	a.ContentHash = nil
	a.Files = nil
	for _, path := range []string{"a😀b.ts", "aéb.ts", "a\nb.ts", "a\rb.ts", "a\u2028b.ts", "a\u2029b.ts", "literal[1].(x)+$^{}|.ts", "literal😀.ts", "src/a.ts"} {
		a.Files = append(a.Files, &graphv2.File{Path: path, ContentHash: strings.Repeat("a", 64), Size: 1})
	}
	if _, err := s.ReplaceGraphV2(t.Context(), id, GraphPublication{Publisher: "unicode-pattern"}, a); err != nil {
		t.Fatal(err)
	}
	service := &graphquery.Service{Store: s}
	scope := graphprotocol.Scope{SelectedRepositoryID: id, Repositories: []graphprotocol.RepositorySnapshot{{ID: id, GitHubID: 101, Commit: a.Commit}}}
	for _, tc := range []struct {
		pattern string
		want    []string
	}{
		{"a?b.ts", []string{"a\nb.ts", "a\rb.ts", "aéb.ts", "a\u2028b.ts", "a\u2029b.ts"}},
		{"a??b.ts", []string{"a😀b.ts"}},
		{"a**b.ts", []string{"aéb.ts", "a😀b.ts"}},
		{"literal[1].(x)+$^{}|.ts", []string{"literal[1].(x)+$^{}|.ts"}},
		{"literal[?].(x)+$^{}|.ts", []string{"literal[1].(x)+$^{}|.ts"}},
		{"literal😀.t?", []string{"literal😀.ts"}},
		{"src?a.ts", []string{}},
		{"src/**", []string{"src/a.ts"}},
	} {
		t.Run(tc.pattern, func(t *testing.T) {
			got, err := service.IndexedFiles(t.Context(), graphprotocol.FilesRequest{Scope: scope, Pattern: tc.pattern, IncludeCount: true})
			if err != nil {
				t.Fatal(err)
			}
			paths := []string{}
			for _, f := range got.Files {
				paths = append(paths, f.Fact.Path)
			}
			if !slices.Equal(paths, tc.want) {
				t.Fatalf("pinned JS wildcard=%q got=%q want=%q", tc.pattern, paths, tc.want)
			}
			if got.TotalFiles == nil || *got.TotalFiles != int64(len(tc.want)) {
				t.Fatalf("wildcard count=%+v", got.TotalFiles)
			}
		})
	}
}
