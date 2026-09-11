//go:build integration

package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/balcsida/graphnest/internal/graphartifact"
	graphv2 "github.com/balcsida/graphnest/internal/graphartifact/v2"
	"github.com/balcsida/graphnest/internal/graphprotocol"
	"github.com/balcsida/graphnest/internal/graphquery"
	"google.golang.org/protobuf/proto"
)

func TestGraphEntitiesAndTraversal(t *testing.T) {
	s, id := readyGraphStore(t, testSHA('a'))
	a := storageV2Artifact()
	a.ContentHash = nil
	a.Nodes[0].Occurrence = "file\x00a"
	a.Nodes[0].Kind = "file"
	a.Nodes[0].Name = "ambiguous"
	a.Nodes[1].Name = "ambiguous"
	a.Nodes[1].SourceId = a.Nodes[0].SourceId
	a.Edges = nil
	a.Unresolved = nil
	for _, relation := range graphartifact.Relationships() {
		for i := 0; i < 2; i++ {
			a.Edges = append(a.Edges, &graphv2.Edge{SourceId: fmt.Sprint(i), Occurrence: fmt.Sprintf("%s-%d", relation.Name, i), Source: a.Nodes[0].Occurrence, Target: a.Nodes[1].Occurrence, Kind: relation.WireKind(), Provenance: proto.String("oracle\x00evidence")})
		}
	}
	first, err := s.ReplaceGraphV2(t.Context(), id, GraphPublication{Publisher: "query-test", Capabilities: []string{"relations"}}, a)
	if err != nil {
		t.Fatal(err)
	}
	service := &graphquery.Service{Store: s}
	scope := graphprotocol.Scope{SelectedRepositoryID: id, Repositories: []graphprotocol.RepositorySnapshot{{ID: id, GitHubID: 101, Name: "acme/visible", Commit: a.Commit}}}
	request := graphprotocol.EntitiesRequest{Scope: scope, Selector: graphprotocol.EntitySelector{Name: proto.String("ambiguous")}, Limit: 1}
	page, err := service.Entities(t.Context(), request)
	if err != nil || len(page.Entities) != 1 || page.NextCursor == "" {
		t.Fatalf("first page=%+v err=%v", page, err)
	}
	request.Cursor = page.NextCursor
	second, err := service.Entities(t.Context(), request)
	if err != nil || len(second.Entities) != 1 || second.Entities[0].ID == page.Entities[0].ID || second.NextCursor != "" {
		t.Fatalf("second page=%+v err=%v", second, err)
	}
	if second.Entities[0].RepositoryID != 101 || len(second.Generations) != 1 || !proto.Equal(second.Generations[0].Producer, a.Producer) {
		t.Fatalf("provenance=%+v", second)
	}
	for _, relation := range graphartifact.Relationships() {
		for _, direction := range []string{"outgoing", "incoming"} {
			root := a.Nodes[0].Occurrence
			if direction == "incoming" {
				root = a.Nodes[1].Occurrence
			}
			got, err := service.Traverse(t.Context(), graphprotocol.TraverseRequest{Scope: scope, Root: graphprotocol.EntitySelector{Occurrence: proto.String(root)}, Relations: []string{relation.Name}, Direction: direction, MaxDepth: 3, MinConfidence: 0.9})
			if err != nil || len(got.Edges) != 2 || len(got.Entities) != 2 {
				t.Fatalf("%s/%s=%+v err=%v", relation.Name, direction, got, err)
			}
			for _, e := range got.Edges {
				if e.Fact.Source != a.Nodes[0].Occurrence || e.Fact.Target != a.Nodes[1].Occurrence || e.Fact.Confidence != nil || e.Fact.GetProvenance() != "oracle\x00evidence" {
					t.Fatalf("lost evidence=%+v", e)
				}
			}
		}
	}
	changed := proto.Clone(a).(*graphv2.Artifact)
	changed.Producer.Version = "replacement"
	if _, err := s.ReplaceGraphV2(t.Context(), id, GraphPublication{Publisher: "query-test", ExpectedActiveID: first.Upload.ID}, changed); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Entities(t.Context(), request); !errors.Is(err, graphquery.ErrGenerationChanged) {
		t.Fatalf("stale cursor=%v", err)
	}
	canceled, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := service.Entities(canceled, graphprotocol.EntitiesRequest{Scope: scope}); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel=%v", err)
	}
	hidden := scope
	hidden.Repositories = nil
	if _, err := service.Entities(t.Context(), graphprotocol.EntitiesRequest{Scope: hidden}); !errors.Is(err, graphquery.ErrInvalidRequest) {
		t.Fatalf("missing scope=%v", err)
	}
}

// The export is produced by graphartifact's existing lossless SQLite converter.
// Its own test compares every original SQLite column, including optional evidence.
func TestGraphEntityCodeGraphOracle(t *testing.T) {
	path := os.Getenv("GRAPHNEST_TEST_CODEGRAPH_V2_FIXTURE")
	if path == "" {
		t.Skip("set GRAPHNEST_TEST_CODEGRAPH_V2_FIXTURE to the exported real oracle artifact")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	a, err := graphartifact.ParseV2(data, graphartifact.Limits{})
	if err != nil {
		t.Fatal(err)
	}
	compareEntityOracle(t, a, 20, 9)
}

func TestGraphEntitySyntheticVocabulary(t *testing.T) {
	data, err := os.ReadFile("../../test/fixtures/codegraph/synthetic-contract.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixture struct {
		Nodes []struct{ ID, Kind string }
		Edges []struct{ Source, Target, Kind string }
	}
	if err = json.Unmarshal(data, &fixture); err != nil {
		t.Fatal(err)
	}
	a := storageV2Artifact()
	a.ContentHash = nil
	a.Nodes = nil
	a.Edges = nil
	a.Files = nil
	a.Unresolved = nil
	for _, n := range fixture.Nodes {
		a.Nodes = append(a.Nodes, &graphv2.Node{SourceId: n.ID, Occurrence: n.ID, Kind: n.Kind})
	}
	for _, e := range fixture.Edges {
		r, ok := graphartifact.ParseRelationship(e.Kind)
		if !ok {
			t.Fatal(e.Kind)
		}
		a.Edges = append(a.Edges, &graphv2.Edge{Occurrence: e.Kind, Source: e.Source, Target: e.Target, Kind: r.WireKind()})
	}
	compareEntityOracle(t, a, 23, 13)
}

func compareEntityOracle(t *testing.T, a *graphv2.Artifact, wantKinds, wantRelations int) {
	t.Helper()
	s, id := readyGraphStore(t, a.Commit)
	if _, err := s.ReplaceGraphV2(t.Context(), id, GraphPublication{Publisher: "oracle", Capabilities: []string{"relations"}}, a); err != nil {
		t.Fatal(err)
	}
	service := &graphquery.Service{Store: s}
	scope := graphprotocol.Scope{SelectedRepositoryID: id, Repositories: []graphprotocol.RepositorySnapshot{{ID: id, GitHubID: 101, Commit: a.Commit}}}
	nodes := map[string]*graphv2.Node{}
	kinds := map[string]bool{}
	relations := map[graphv2.EdgeKind]bool{}
	for _, n := range a.Nodes {
		nodes[n.Occurrence] = n
		kinds[n.Kind] = true
	}
	for _, e := range a.Edges {
		relations[e.Kind] = true
	}
	if len(kinds) != wantKinds || len(relations) != wantRelations {
		t.Fatalf("fixture coverage=%d/%d want=%d/%d", len(kinds), len(relations), wantKinds, wantRelations)
	}
	for kind := range kinds {
		want := 0
		for _, n := range a.Nodes {
			if n.Kind == kind {
				want++
			}
		}
		got, err := service.Entities(t.Context(), graphprotocol.EntitiesRequest{Scope: scope, Selector: graphprotocol.EntitySelector{Kind: kind}})
		if err != nil || len(got.Entities) != want {
			t.Fatalf("kind %s lookup=%d want=%d err=%v", kind, len(got.Entities), want, err)
		}
	}
	// Every kind, file and external entity participates in exact paged lookup.
	request := graphprotocol.EntitiesRequest{Scope: scope, Limit: 7}
	occurrences := []string{}
	for {
		got, err := service.Entities(t.Context(), request)
		if err != nil {
			t.Fatal(err)
		}
		if len(got.Generations) != 1 || got.Generations[0].NodeCount != len(a.Nodes) || got.Generations[0].EdgeCount != len(a.Edges) || got.Generations[0].UnresolvedCount != len(a.Unresolved) {
			t.Fatalf("generation counts=%+v", got.Generations)
		}
		for _, n := range got.Entities {
			if !proto.Equal(n.Fact, nodes[n.Fact.Occurrence]) || n.RepositoryID != 101 {
				t.Fatalf("lost node %s", n.Fact.Occurrence)
			}
			occurrences = append(occurrences, n.Fact.Occurrence)
		}
		request.Cursor = got.NextCursor
		if request.Cursor == "" {
			break
		}
	}
	if len(occurrences) != len(a.Nodes) || !sort.StringsAreSorted(occurrences) {
		t.Fatal("lookup pagination lost nodes or ordering")
	}
	for i := 1; i < len(occurrences); i++ {
		if occurrences[i] == occurrences[i-1] {
			t.Fatal("duplicate paged entity")
		}
	}
	queries := 0
	for _, relation := range graphartifact.Relationships() {
		for _, direction := range []string{"outgoing", "incoming"} {
			// Every endpoint with an edge is a root; isolated endpoints are covered above.
			roots := map[string]bool{}
			for _, e := range a.Edges {
				if e.Kind == relation.WireKind() {
					root := e.Source
					if direction == "incoming" {
						root = e.Target
					}
					roots[root] = true
				}
			}
			for root := range roots {
				reachable := map[string]bool{root: true}
				evidence := map[string]*graphv2.Edge{}
				for changed := true; changed; {
					changed = false
					for _, e := range a.Edges {
						from, to := e.Source, e.Target
						if direction == "incoming" {
							from, to = to, from
						}
						if e.Kind != relation.WireKind() || !reachable[from] {
							continue
						}
						evidence[e.Occurrence] = e
						if !reachable[to] {
							reachable[to] = true
							changed = true
						}
					}
				}
				got, err := service.Traverse(t.Context(), graphprotocol.TraverseRequest{Scope: scope, Root: graphprotocol.EntitySelector{Occurrence: proto.String(root)}, Relations: []string{relation.Name}, Direction: direction, MaxDepth: 32})
				if err != nil || got.Partial || len(got.Entities) != len(reachable) || len(got.Edges) != len(evidence) {
					t.Fatalf("%s/%s root=%s nodes=%d/%d edges=%d/%d partial=%v err=%v", relation.Name, direction, root, len(got.Entities), len(reachable), len(got.Edges), len(evidence), got.Partial, err)
				}
				for _, n := range got.Entities {
					if !reachable[n.Fact.Occurrence] || !proto.Equal(n.Fact, nodes[n.Fact.Occurrence]) {
						t.Fatalf("unexpected node=%v", n)
					}
				}
				for _, e := range got.Edges {
					want := evidence[e.Fact.Occurrence]
					if !proto.Equal(e.Fact, want) {
						t.Fatalf("lost edge evidence %s", e.Fact.Occurrence)
					}
					source, _ := graphartifact.IdentityV2(a.Producer, a.Repository, nodes[want.Source].SourceId, want.Source)
					target, _ := graphartifact.IdentityV2(a.Producer, a.Repository, nodes[want.Target].SourceId, want.Target)
					if e.SourceID != source || e.TargetID != target || e.RepositoryID != 101 {
						t.Fatalf("reversed or nonpublic edge=%v", e)
					}
				}
				queries++
			}
		}
	}
	t.Logf("Compared %d nodes, %d edges, %d kinds, %d relations, %d incoming/outgoing reachability queries", len(a.Nodes), len(a.Edges), len(kinds), len(relations), queries)
}

func TestGraphEntityScopeAndBlockedNeighbors(t *testing.T) {
	s, id := readyGraphStore(t, testSHA('a'))
	a := storageV2Artifact()
	if _, err := s.ReplaceGraphV2(t.Context(), id, GraphPublication{Publisher: "visible"}, a); err != nil {
		t.Fatal(err)
	}
	scope := graphprotocol.Scope{SelectedRepositoryID: id, Repositories: []graphprotocol.RepositorySnapshot{{ID: id, GitHubID: 101, Commit: a.Commit}}}
	service := &graphquery.Service{Store: s}
	request := graphprotocol.TraverseRequest{Scope: scope, Root: graphprotocol.EntitySelector{Occurrence: proto.String("a")}, MaxDepth: 3}
	before, err := service.Traverse(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	threshold := request
	threshold.MinConfidence = 0.9
	filtered, err := service.Traverse(t.Context(), threshold)
	if err != nil || len(filtered.Edges) != 1 || filtered.Edges[0].Fact.Confidence != nil {
		t.Fatalf("unknown versus zero confidence=%+v err=%v", filtered, err)
	}
	hiddenID := seedReadyRepository(t, s, 202, a.Commit)
	hidden := proto.Clone(a).(*graphv2.Artifact)
	hidden.Repository = "202"
	hidden.ContentHash = nil
	hidden.Nodes[0].Name = "hidden-name"
	hidden.Nodes[1].Path = proto.String("hidden/path.ts")
	hidden.Edges[0].Provenance = proto.String("hidden-provenance")
	hidden.Metadata[0].Value = "hidden-metadata"
	if _, err = s.ReplaceGraphV2(t.Context(), hiddenID, GraphPublication{Publisher: "hidden-publisher"}, hidden); err != nil {
		t.Fatal(err)
	}
	after, err := service.Traverse(t.Context(), request)
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatalf("hidden repository altered visible output: %v", err)
	}
	missing, err := service.Entities(t.Context(), graphprotocol.EntitiesRequest{Scope: scope, Selector: graphprotocol.EntitySelector{Name: proto.String("hidden-name")}})
	if err != nil || len(missing.Entities) != 0 || len(missing.Generations) != 1 {
		t.Fatalf("hidden lookup=%+v err=%v", missing, err)
	}
	data, _ := json.Marshal(after)
	if strings.Contains(string(data), "hidden") {
		t.Fatal("hidden evidence leaked")
	}
	// A real SQL neighbor call waits on a table lock until the whole-query deadline.
	barrier, err := s.pool.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer barrier.Rollback(t.Context())
	if _, err = barrier.Exec(t.Context(), `lock table graph_v2_edges in access exclusive mode`); err != nil {
		t.Fatal(err)
	}
	service.Limits.MaxDuration = 40 * time.Millisecond
	got, err := service.Traverse(t.Context(), request)
	if !errors.Is(err, context.DeadlineExceeded) || len(got.Entities) != 0 || len(got.Generations) != 0 {
		t.Fatalf("blocked traversal=%+v err=%v", got, err)
	}
	if err = barrier.Rollback(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, err = s.pool.Exec(t.Context(), `update repositories set indexed_sha=$2 where id=$1`, id, testSHA('b')); err != nil {
		t.Fatal(err)
	}
	if _, err = service.Traverse(t.Context(), request); !errors.Is(err, graphquery.ErrGenerationChanged) {
		t.Fatalf("stale indexed SHA=%v", err)
	}
}

func TestGraphEntityStoreRejectsOversizedSets(t *testing.T) {
	for _, mode := range []string{"metadata", "nodes", "individual"} {
		t.Run(mode, func(t *testing.T) {
			s, id := readyGraphStore(t, testSHA('a'))
			a := storageV2Artifact()
			a.ContentHash = nil
			if mode == "individual" {
				a.Nodes[0].Decorators = &graphv2.StringList{Values: make([]string, 512)}
				for i := range a.Nodes[0].Decorators.Values {
					a.Nodes[0].Decorators.Values[i] = strings.Repeat("x", 16384)
				}
			}
			for i := 0; i < 80; i++ {
				if mode == "metadata" {
					a.Metadata = append(a.Metadata, &graphv2.MetadataEntry{Key: fmt.Sprint(i), Value: strings.Repeat("\x00", 16384)})
				} else if mode == "nodes" {
					a.Nodes = append(a.Nodes, &graphv2.Node{SourceId: fmt.Sprint(i), Occurrence: fmt.Sprint(i), Kind: "function", Documentation: proto.String(strings.Repeat("\x00", 16384))})
				}
			}
			published, err := s.ReplaceGraphV2(t.Context(), id, GraphPublication{Publisher: "budget"}, a)
			if err != nil {
				t.Fatal(err)
			}
			snaps := []graphquery.QuerySnapshot{{RepositoryID: id, UploadID: published.Upload.ID, Commit: a.Commit}}
			if mode == "metadata" {
				_, err = s.EntityGenerations(t.Context(), snaps)
			} else {
				_, err = s.QueryEntities(t.Context(), graphquery.EntityQuery{Snapshots: snaps, Limit: 1000})
			}
			if !errors.Is(err, graphquery.ErrQuerySize) {
				t.Fatalf("oversized %s set=%v", mode, err)
			}
		})
	}
}

func TestGraphEntitySelectorsPreserveMaxBytes(t *testing.T) {
	s, id := readyGraphStore(t, testSHA('a'))
	a := storageV2Artifact()
	a.ContentHash = nil
	n := a.Nodes[0]
	n.Occurrence = incompressibleGraphString("occurrence", 16383) + "\x00"
	n.Name = incompressibleGraphString("name", 16383) + "\x00"
	n.QualifiedName = incompressibleGraphString("qualified", 16383) + "\x00"
	n.Path = proto.String(incompressibleGraphString("path", 4096))
	a.Files[0].Path = *n.Path
	a.Unresolved[0].Source = n.Occurrence
	for _, e := range a.Edges {
		e.Source = n.Occurrence
	}
	a.Producer.Version = "version\x00one"
	if _, err := s.ReplaceGraphV2(t.Context(), id, GraphPublication{Publisher: "bytes"}, a); err != nil {
		t.Fatal(err)
	}
	service := &graphquery.Service{Store: s}
	scope := graphprotocol.Scope{SelectedRepositoryID: id, Repositories: []graphprotocol.RepositorySnapshot{{ID: id, GitHubID: 101, Commit: a.Commit}}}
	for _, selector := range []graphprotocol.EntitySelector{{Occurrence: &n.Occurrence}, {Name: &n.Name}, {QualifiedName: &n.QualifiedName}, {Path: n.Path}} {
		got, err := service.Entities(t.Context(), graphprotocol.EntitiesRequest{Scope: scope, Selector: selector})
		if err != nil || len(got.Entities) != 1 || !proto.Equal(got.Entities[0].Fact, n) {
			t.Fatalf("max-byte selector lost entity: %v", err)
		}
	}
	got, err := service.Traverse(t.Context(), graphprotocol.TraverseRequest{Scope: scope, Root: graphprotocol.EntitySelector{Occurrence: proto.String("b")}, Direction: "incoming", MinConfidence: .9})
	if err != nil || len(got.Edges) != 1 || got.Edges[0].Fact.Source != n.Occurrence {
		t.Fatalf("max-byte incoming endpoint lost: %v", err)
	}
}
