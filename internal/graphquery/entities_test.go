package graphquery

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	graphv2 "github.com/balcsida/graphnest/internal/graphartifact/v2"
	"github.com/balcsida/graphnest/internal/graphprotocol"
	"google.golang.org/protobuf/proto"
)

type entityTestStore struct {
	stubQueryStore
	generationCalls, lookupCalls, neighborCalls int
	lookup                                      func(context.Context, EntityQuery) ([]graphprotocol.Entity, error)
	neighbors                                   func(context.Context, EntityNeighborQuery) ([]EntityNeighbor, error)
	changed                                     bool
}

func (s *entityTestStore) EntityGenerations(ctx context.Context, _ []QuerySnapshot) ([]graphprotocol.Generation, error) {
	s.generationCalls++
	id := int64(1)
	if s.changed && s.generationCalls > 1 {
		id = 2
	}
	return []graphprotocol.Generation{{RepositoryID: 1, Repository: "101", UploadID: id, Commit: "sha", Producer: &graphv2.Producer{Name: "codegraph"}}}, ctx.Err()
}
func (s *entityTestStore) QueryEntities(ctx context.Context, q EntityQuery) ([]graphprotocol.Entity, error) {
	s.lookupCalls++
	if s.lookup != nil {
		return s.lookup(ctx, q)
	}
	return []graphprotocol.Entity{testEntity("a")}, ctx.Err()
}
func (s *entityTestStore) EntityNeighbors(ctx context.Context, q EntityNeighborQuery) ([]EntityNeighbor, error) {
	s.neighborCalls++
	if s.neighbors != nil {
		return s.neighbors(ctx, q)
	}
	return nil, ctx.Err()
}
func testEntity(id string) graphprotocol.Entity {
	return graphprotocol.Entity{RepositoryID: 1, ID: id, Fact: &graphv2.Node{SourceId: id, Occurrence: id, Kind: "function"}}
}
func testNeighbor(source, target, occurrence string) EntityNeighbor {
	return EntityNeighbor{Entity: testEntity(target), Edge: graphprotocol.Evidence{RepositoryID: 1, SourceID: source, TargetID: target, Fact: &graphv2.Edge{Source: source, Target: target, Occurrence: occurrence}}}
}
func entityTestScope() graphprotocol.Scope {
	return graphprotocol.Scope{SelectedRepositoryID: 1, Repositories: []graphprotocol.RepositorySnapshot{{ID: 1, GitHubID: 101, Commit: "sha"}}}
}
func traverseTestRequest() graphprotocol.TraverseRequest {
	return graphprotocol.TraverseRequest{Scope: entityTestScope(), Root: graphprotocol.EntitySelector{Occurrence: proto.String("a")}, MaxDepth: 3}
}

func TestEntityTraversalRetainsCycleAndParallelEvidence(t *testing.T) {
	s := &entityTestStore{neighbors: func(_ context.Context, q EntityNeighborQuery) ([]EntityNeighbor, error) {
		if q.Relation != "calls" {
			t.Fatalf("default broadened to %s", q.Relation)
		}
		if q.Occurrence == "a" {
			return []EntityNeighbor{testNeighbor("a", "a", "self"), testNeighbor("a", "b", "one"), testNeighbor("a", "b", "two")}, nil
		}
		return []EntityNeighbor{testNeighbor("b", "a", "cycle")}, nil
	}}
	result, err := (&Service{Store: s}).Traverse(t.Context(), traverseTestRequest())
	if err != nil || len(result.Entities) != 2 || len(result.Edges) != 4 || result.Partial || s.neighborCalls != 2 {
		t.Fatalf("result=%+v calls=%d err=%v", result, s.neighborCalls, err)
	}
}

func TestEntityTraversalBounds(t *testing.T) {
	for _, test := range []struct {
		name   string
		limits Limits
		depth  int
		reason string
	}{
		{"nodes", Limits{MaxNodes: 2}, 3, "node_limit"},
		{"edges", Limits{MaxEdges: 1}, 3, "edge_limit"},
		{"fanout", Limits{MaxFanout: 1}, 3, "fanout_limit"},
		{"depth", Limits{}, 1, "depth_limit"},
	} {
		t.Run(test.name, func(t *testing.T) {
			s := &entityTestStore{neighbors: func(_ context.Context, q EntityNeighborQuery) ([]EntityNeighbor, error) {
				rows := []EntityNeighbor{testNeighbor(q.Occurrence, q.Occurrence+"b", q.Occurrence+"1"), testNeighbor(q.Occurrence, q.Occurrence+"c", q.Occurrence+"2")}
				if len(rows) > q.Limit {
					rows = rows[:q.Limit]
				}
				return rows, nil
			}}
			request := traverseTestRequest()
			request.MaxDepth = test.depth
			got, err := (&Service{Store: s, Limits: test.limits}).Traverse(t.Context(), request)
			if err != nil || !got.Partial {
				t.Fatalf("bounds=%+v err=%v", got, err)
			}
			found := false
			nodes := map[string]bool{}
			for _, b := range got.Boundaries {
				found = found || b.Reason == test.reason
			}
			if !found {
				t.Fatalf("missing %s in %+v", test.reason, got.Boundaries)
			}
			for _, n := range got.Entities {
				nodes[n.ID] = true
			}
			for _, e := range got.Edges {
				if !nodes[e.SourceID] || !nodes[e.TargetID] {
					t.Fatalf("dangling evidence=%+v", e)
				}
			}
		})
	}
}

func TestEntityFanoutAcrossRelationsAndDepthLookahead(t *testing.T) {
	for _, depth := range []int{1, 3} {
		t.Run(fmt.Sprint(depth), func(t *testing.T) {
			s := &entityTestStore{neighbors: func(_ context.Context, q EntityNeighborQuery) ([]EntityNeighbor, error) {
				if q.Occurrence == "a" && q.Relation == "calls" {
					return []EntityNeighbor{testNeighbor("a", "b", "ab")}, nil
				}
				if q.Relation == "imports" {
					return []EntityNeighbor{testNeighbor(q.Occurrence, "c", q.Occurrence+"c")}, nil
				}
				return nil, nil
			}}
			request := traverseTestRequest()
			request.Relations = []string{"calls", "imports"}
			request.MaxDepth = depth
			got, err := (&Service{Store: s, Limits: Limits{MaxFanout: 1}}).Traverse(t.Context(), request)
			if err != nil || !got.Partial {
				t.Fatalf("missing cross-relation/depth truncation=%+v err=%v", got, err)
			}
		})
	}
}

func TestEntityQueriesCancellationAndFreshness(t *testing.T) {
	for _, stage := range []string{"before", "lookup", "neighbors", "deadline", "generation"} {
		t.Run(stage, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			s := &entityTestStore{}
			limits := Limits{}
			expected := context.Canceled
			switch stage {
			case "before":
				cancel()
			case "lookup":
				s.lookup = func(context.Context, EntityQuery) ([]graphprotocol.Entity, error) {
					cancel()
					return []graphprotocol.Entity{testEntity("a")}, nil
				}
			case "neighbors":
				s.neighbors = func(context.Context, EntityNeighborQuery) ([]EntityNeighbor, error) {
					cancel()
					return []EntityNeighbor{testNeighbor("a", "b", "edge")}, nil
				}
			case "deadline":
				limits.MaxDuration = 35 * time.Millisecond
				expected = context.DeadlineExceeded
				s.neighbors = func(ctx context.Context, q EntityNeighborQuery) ([]EntityNeighbor, error) {
					select {
					case <-ctx.Done():
						return nil, ctx.Err()
					case <-time.After(25 * time.Millisecond):
						return []EntityNeighbor{testNeighbor(q.Occurrence, q.Occurrence+"b", q.Occurrence)}, nil
					}
				}
			case "generation":
				s.changed = true
				expected = ErrGenerationChanged
			}
			got, err := (&Service{Store: s, Limits: limits}).Traverse(ctx, traverseTestRequest())
			if !errors.Is(err, expected) || len(got.Entities) != 0 || len(got.Edges) != 0 {
				t.Fatalf("partial output after error=%+v err=%v", got, err)
			}
			if stage == "before" && s.generationCalls != 0 || stage == "lookup" && s.neighborCalls != 0 || stage == "neighbors" && s.neighborCalls != 1 || stage == "deadline" && s.neighborCalls != 2 {
				t.Fatalf("extra store calls: %+v", s)
			}
		})
	}
}

func TestEntityQueriesRejectHiddenOutputAndOversize(t *testing.T) {
	for _, mode := range []string{"hidden", "bytes", "wrong_public_id"} {
		t.Run(mode, func(t *testing.T) {
			s := &entityTestStore{lookup: func(context.Context, EntityQuery) ([]graphprotocol.Entity, error) {
				e := testEntity("a")
				if mode == "hidden" {
					e.RepositoryID = 99
				}
				if mode == "bytes" {
					e.Fact.Documentation = proto.String(strings.Repeat("x", MaxEntityQueryBytes))
				}
				return []graphprotocol.Entity{e}, nil
			}}
			scope := entityTestScope()
			if mode == "wrong_public_id" {
				scope.Repositories[0].GitHubID = 999
			}
			got, err := (&Service{Store: s}).Entities(t.Context(), graphprotocol.EntitiesRequest{Scope: scope})
			expected := ErrGenerationChanged
			if mode == "bytes" {
				expected = ErrQuerySize
			}
			if !errors.Is(err, expected) || len(got.Entities) > 0 || len(got.Generations) > 0 {
				t.Fatalf("leaked response=%+v err=%v", got, err)
			}
		})
	}
}

func TestEntityAmbiguitySurvivesNodeLimit(t *testing.T) {
	s := &entityTestStore{lookup: func(context.Context, EntityQuery) ([]graphprotocol.Entity, error) {
		return []graphprotocol.Entity{testEntity("a"), testEntity("b")}, nil
	}}
	got, err := (&Service{Store: s, Limits: Limits{MaxNodes: 1}}).Traverse(t.Context(), traverseTestRequest())
	if err != nil || got.Status != graphprotocol.StatusAmbiguous || !got.Partial || len(got.Entities) != 1 || s.neighborCalls != 0 {
		t.Fatalf("ambiguity=%+v calls=%d err=%v", got, s.neighborCalls, err)
	}
}

func TestEntityTraversalStopsAtByteBudget(t *testing.T) {
	s := &entityTestStore{neighbors: func(_ context.Context, q EntityNeighborQuery) ([]EntityNeighbor, error) {
		row := testNeighbor(q.Occurrence, q.Occurrence+"b", q.Occurrence)
		// Escaping valid UTF-8 can make a small fact exceed the JSON output budget.
		row.Entity.Fact.Decorators = &graphv2.StringList{Values: make([]string, 64)}
		for i := range row.Entity.Fact.Decorators.Values {
			row.Entity.Fact.Decorators.Values[i] = strings.Repeat("\x00", 16384)
		}
		return []EntityNeighbor{row}, nil
	}}
	got, err := (&Service{Store: s}).Traverse(t.Context(), traverseTestRequest())
	if !errors.Is(err, ErrQuerySize) || len(got.Entities) != 0 || s.neighborCalls != 1 {
		t.Fatalf("byte budget calls=%d err=%v", s.neighborCalls, err)
	}
}

func TestEntityDepthLookaheadRejectsHiddenNeighbors(t *testing.T) {
	s := &entityTestStore{neighbors: func(_ context.Context, q EntityNeighborQuery) ([]EntityNeighbor, error) {
		row := testNeighbor(q.Occurrence, q.Occurrence+"b", q.Occurrence)
		if q.Occurrence != "a" {
			row.Entity.RepositoryID = 99
			row.Edge.RepositoryID = 99
		}
		return []EntityNeighbor{row}, nil
	}}
	request := traverseTestRequest()
	request.MaxDepth = 1
	got, err := (&Service{Store: s}).Traverse(t.Context(), request)
	if !errors.Is(err, ErrGenerationChanged) || len(got.Boundaries) != 0 || len(got.Entities) != 0 {
		t.Fatalf("hidden lookahead leaked: %+v err=%v", got, err)
	}
}
