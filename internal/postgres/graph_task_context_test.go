//go:build integration

package postgres

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"unicode/utf16"

	"github.com/balcsida/graphnest/internal/authn"
	"github.com/balcsida/graphnest/internal/graphartifact"
	graphv2 "github.com/balcsida/graphnest/internal/graphartifact/v2"
	"github.com/balcsida/graphnest/internal/graphprotocol"
	"github.com/balcsida/graphnest/internal/graphquery"
	"github.com/balcsida/graphnest/internal/graphservice"
	"github.com/balcsida/graphnest/internal/repository"
	"github.com/balcsida/graphnest/pkg/api"
	"google.golang.org/protobuf/proto"
)

type taskContextOracleOptions struct {
	SearchLimit      *int      `json:"search_limit,omitempty"`
	TraversalDepth   *int      `json:"traversal_depth,omitempty"`
	MaxNodes         *int      `json:"max_nodes,omitempty"`
	MinScore         *float64  `json:"min_score,omitempty"`
	EdgeKinds        []string  `json:"edge_kinds,omitempty"`
	NodeKinds        *[]string `json:"node_kinds,omitempty"`
	SeedNames        *[]string `json:"seed_names,omitempty"`
	MaxCodeBlocks    *int      `json:"max_code_blocks,omitempty"`
	MaxCodeBlockSize *int      `json:"max_code_block_size,omitempty"`
	IncludeCode      *bool     `json:"include_code,omitempty"`
}

func (options taskContextOracleOptions) findOptions() graphprotocol.RelevantContextOptions {
	return graphprotocol.RelevantContextOptions{
		SearchLimit: options.SearchLimit, TraversalDepth: options.TraversalDepth, MaxNodes: options.MaxNodes, MinScore: options.MinScore,
		EdgeKinds: options.EdgeKinds, NodeKinds: options.NodeKinds, SeedNames: options.SeedNames,
	}
}

func (options taskContextOracleOptions) buildOptions() graphservice.BuildTaskContextOptions {
	return graphservice.BuildTaskContextOptions{
		SearchLimit: options.SearchLimit, TraversalDepth: options.TraversalDepth, MaxNodes: options.MaxNodes, MinScore: options.MinScore,
		MaxCodeBlocks: options.MaxCodeBlocks, MaxCodeBlockSize: options.MaxCodeBlockSize, IncludeCode: options.IncludeCode,
	}
}

type taskContextOracle struct {
	ReferenceCommit       string `json:"referenceCommit"`
	CapturedResultsSHA256 string `json:"capturedResultsSha256"`
	Cases                 []struct {
		ID, Method, Query, Title, Description string
		Options                               taskContextOracleOptions
		RequiredNames, RequiredFiles          []string
		RequiredEdgeKinds                     []string
		RequiredNodes                         []struct {
			Occurrence, Name, Kind, Path string
		}
		RequiredEdges []struct {
			Occurrence, Kind, Source, Target string
		}
		RequiredSources []struct {
			Occurrence, Path, Content string
			StartLine, EndLine, UTF16 int
		}
		Original map[string]int
	} `json:"cases"`
}

func TestGraphTaskContextRealOracle(t *testing.T) {
	fixture := os.Getenv("GRAPHNEST_TEST_CODEGRAPH_V2_FIXTURE")
	if fixture == "" {
		t.Fatal("required real fixture: set GRAPHNEST_TEST_CODEGRAPH_V2_FIXTURE")
	}
	raw, err := os.ReadFile(fixture)
	if err != nil {
		t.Fatal(err)
	}
	artifact, err := graphartifact.ParseV2(raw, graphartifact.Limits{})
	if err != nil {
		t.Fatal(err)
	}
	oracleRaw, err := os.ReadFile("../../test/fixtures/codegraph/task-context.json")
	if err != nil {
		t.Fatal(err)
	}
	var oracle taskContextOracle
	if err := json.Unmarshal(oracleRaw, &oracle); err != nil {
		t.Fatal(err)
	}
	if oracle.ReferenceCommit != "b9ca4b7981116909900368cc1686a1074cd4d4c1" || oracle.CapturedResultsSHA256 != "d578525e872c5f9584ca972849658263cc976401635ea399f4a8d6d097c23007" || len(oracle.Cases) != 12 {
		t.Fatalf("task oracle provenance changed: %+v", oracle)
	}

	store, repositoryID := readyGraphStore(t, artifact.Commit)
	publication, err := store.ReplaceGraphV2(t.Context(), repositoryID, GraphPublication{Publisher: "task-context-oracle"}, artifact)
	if err != nil {
		t.Fatal(err)
	}
	principal := authn.Principal{InstallationID: 10, RepositoryIDs: []int64{101}}
	service := &graphservice.Service{
		Store: store, Backend: &graphquery.Service{Store: store},
		Files: &repository.Service{Store: store, GitHub: &inspectionGitHub{t: t, commit: artifact.Commit}},
	}
	nodes, edges := map[string]*graphv2.Node{}, map[string]*graphv2.Edge{}
	for _, node := range artifact.Nodes {
		nodes[node.Occurrence] = node
	}
	for _, edge := range artifact.Edges {
		edges[edge.Occurrence] = edge
	}

	for _, test := range oracle.Cases {
		t.Run(test.ID, func(t *testing.T) {
			var graph graphprotocol.RelevantContextResponse
			var task *graphservice.TaskContext
			if test.Method == "findRelevantContext" {
				got, err := service.FindRelevantContext(t.Context(), principal, graphservice.FindRelevantContextRequest{Repo: api.GraphRepositorySelector{ID: 101}, Query: test.Query, Options: test.Options.findOptions()})
				if err != nil {
					t.Fatal(err)
				}
				graph = got
			} else {
				got, err := service.BuildTaskContext(t.Context(), principal, graphservice.BuildTaskContextRequest{Repo: api.GraphRepositorySelector{ID: 101}, Query: test.Query, Title: test.Title, Description: test.Description, Options: test.Options.buildOptions()})
				if err != nil {
					t.Fatal(err)
				}
				task, graph = &got, got.Graph
			}
			if len(graph.Generations) != 1 || graph.Generations[0].UploadID != publication.Upload.ID || graph.Generations[0].Commit != artifact.Commit || len(graph.Nodes) > graph.Options.MaxNodes {
				t.Fatalf("generation/budget changed: %+v", graph)
			}
			gotNames, gotFiles, gotOccurrences, gotEdgeKinds := []string{}, []string{}, []string{}, []string{}
			for _, entity := range graph.Nodes {
				original := nodes[entity.Fact.GetOccurrence()]
				if original == nil || !proto.Equal(entity.Fact, original) {
					t.Fatalf("original node occurrence changed: %+v", entity)
				}
				gotNames = append(gotNames, entity.Fact.GetName())
				gotFiles = append(gotFiles, entity.Fact.GetPath())
				gotOccurrences = append(gotOccurrences, entity.Fact.GetOccurrence())
			}
			for _, evidence := range graph.Edges {
				original := edges[evidence.Fact.GetOccurrence()]
				if original == nil || !proto.Equal(evidence.Fact, original) {
					t.Fatalf("original edge occurrence changed: %+v", evidence)
				}
				gotEdgeKinds = append(gotEdgeKinds, graphartifact.Relationships()[int(evidence.Fact.Kind)-1].Name)
			}
			for _, name := range test.RequiredNames {
				if !slices.Contains(gotNames, name) {
					t.Fatalf("missing required original name %q in %v", name, gotNames)
				}
			}
			for _, path := range test.RequiredFiles {
				if !slices.Contains(gotFiles, path) {
					t.Fatalf("missing required original file %q in %v", path, gotFiles)
				}
			}
			for _, kind := range test.RequiredEdgeKinds {
				if !slices.Contains(gotEdgeKinds, kind) {
					t.Fatalf("missing required edge kind %q in %v", kind, gotEdgeKinds)
				}
			}
			for _, required := range test.RequiredNodes {
				var got *graphv2.Node
				for _, entity := range graph.Nodes {
					if entity.Fact.GetOccurrence() == required.Occurrence {
						got = entity.Fact
						break
					}
				}
				if got == nil || got.GetName() != required.Name || got.GetKind() != required.Kind || got.GetPath() != required.Path {
					t.Fatalf("missing required original node %+v: got=%+v occurrences=%v", required, got, gotOccurrences)
				}
			}
			for _, required := range test.RequiredEdges {
				found := false
				for _, evidence := range graph.Edges {
					kind := graphartifact.Relationships()[int(evidence.Fact.Kind)-1].Name
					if evidence.Fact.GetOccurrence() == required.Occurrence && kind == required.Kind && evidence.Fact.GetSource() == required.Source && evidence.Fact.GetTarget() == required.Target {
						found = true
						break
					}
				}
				if !found {
					t.Fatalf("missing required original edge %+v", required)
				}
			}
			if test.ID == "find-method-kind-normalize" {
				for _, entity := range graph.Nodes {
					if entity.Fact.Kind != "method" {
						t.Fatalf("nodeKinds admitted %s", entity.Fact.Kind)
					}
				}
			}
			if test.ID == "find-depth-zero-process-greeting" && len(graph.Edges) != 0 || test.ID == "find-high-min-score-empty" && len(graph.Nodes) != 0 || test.ID == "find-low-search-node-budget" && len(graph.Nodes) != 1 {
				t.Fatalf("option outcome changed: nodes=%d edges=%d", len(graph.Nodes), len(graph.Edges))
			}
			if task != nil {
				assertTaskContextSources(t, task, nodes)
				for _, required := range test.RequiredSources {
					found := false
					for _, block := range task.CodeBlocks {
						if block.Entity.Fact.GetOccurrence() == required.Occurrence && block.Path == required.Path && block.StartLine == required.StartLine && block.EndLine == required.EndLine && block.Content == required.Content && block.ReturnedUTF16Units == required.UTF16 {
							found = true
							break
						}
					}
					if !found {
						t.Fatalf("missing required exact source %+v: blocks=%+v", required, task.CodeBlocks)
					}
				}
				if test.ID == "build-no-code" && len(task.CodeBlocks) != 0 || test.ID == "build-empty-query" && (len(graph.Nodes) != 0 || len(task.CodeBlocks) != 0) {
					t.Fatalf("build option outcome changed: %+v", task)
				}
			}
			t.Logf("original=%v graph=%d/%d/%d files=%d blocks=%d partial=%v", test.Original, len(graph.Nodes), len(graph.Edges), len(graph.Roots), len(gotFiles), func() int {
				if task == nil {
					return 0
				}
				return len(task.CodeBlocks)
			}(), graph.Partial)
		})
	}
}

func assertTaskContextSources(t *testing.T, task *graphservice.TaskContext, nodes map[string]*graphv2.Node) {
	t.Helper()
	for _, block := range task.CodeBlocks {
		if !proto.Equal(block.Entity.Fact, nodes[block.Entity.Fact.GetOccurrence()]) || !proto.Equal(block.Range, block.Entity.Fact.Location) {
			t.Fatalf("code block lost entity/location: %+v", block)
		}
		if block.Status != "ok" || block.IndexedSHA != task.Generations[0].Commit || block.BlobSHA == "" {
			if block.Complete {
				t.Fatalf("unavailable source marked complete: %+v", block)
			}
			continue
		}
		raw, err := os.ReadFile(filepath.Join("../../test/fixtures/codegraph/source", block.Path))
		if err != nil {
			t.Fatal(err)
		}
		lines := strings.Split(string(raw), "\n")
		original := strings.Join(lines[block.StartLine-1:block.EndLine], "\n")
		if !strings.HasPrefix(original, block.Content) || block.OriginalUTF16Units == nil || *block.OriginalUTF16Units != len(utf16.Encode([]rune(original))) || block.ReturnedUTF16Units != len(utf16.Encode([]rune(block.Content))) || block.Truncated == block.Complete {
			t.Fatalf("source fidelity/truncation changed: %+v original=%q", block, original)
		}
	}
}
