package graphquery

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"testing"

	"github.com/balcsida/graphnest/internal/graphprotocol"
	"google.golang.org/protobuf/proto"
)

type relevantContextStore struct {
	entityTestStore
	matches []graphprotocol.DiscoveryMatch
	err     error
}

func (s *relevantContextStore) QueryDiscovery(ctx context.Context, _ DiscoverySearch) ([]graphprotocol.DiscoveryMatch, error) {
	if s.err != nil {
		return nil, s.err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return append([]graphprotocol.DiscoveryMatch(nil), s.matches...), nil
}

func TestRelevantContextPrefersConciseExactNameCandidate(t *testing.T) {
	serviceMethod := testEntity("service-greet")
	serviceMethod.Fact.Name = "greet"
	serviceMethod.Fact.Signature = proto.String("(name: string): string")
	parentMethod := testEntity("parent-greet")
	parentMethod.Fact.Name = "greet"
	store := &relevantContextStore{matches: []graphprotocol.DiscoveryMatch{
		{Entity: serviceMethod, Score: 10},
		{Entity: parentMethod, Score: 1},
	}}
	store.lookup = func(_ context.Context, q EntityQuery) ([]graphprotocol.Entity, error) {
		return []graphprotocol.Entity{testEntity(*q.Selector.Occurrence)}, nil
	}
	limit, nodes, depth := 1, 1, 0
	seeds := []string{}
	got, err := (&Service{Store: store}).RelevantContext(t.Context(), graphprotocol.RelevantContextRequest{
		Scope: entityTestScope(), Query: "greet",
		Options: graphprotocol.RelevantContextOptions{SearchLimit: &limit, MaxNodes: &nodes, TraversalDepth: &depth, SeedNames: &seeds},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Nodes) != 1 || got.Nodes[0].Fact.Occurrence != "parent-greet" {
		t.Fatalf("exact-name ranking lost concise candidate: %+v", got.Nodes)
	}
}

func TestRelevantContextRanksRepeatedQueryEvidence(t *testing.T) {
	match := func(occurrence, name, qualified string, score float64) graphprotocol.DiscoveryMatch {
		entity := testEntity(occurrence)
		entity.Fact.Name = name
		entity.Fact.QualifiedName = qualified
		return graphprotocol.DiscoveryMatch{Entity: entity, Score: score}
	}
	store := &relevantContextStore{matches: []graphprotocol.DiscoveryMatch{
		match("process", "processGreeting", "processGreeting", 40),
		match("service", "Service", "Service", 10),
		match("normalize", "normalize", "normalize", 9),
		match("greeter", "Greeter", "Greeter", 8),
		match("service-greet", "greet", "Service::greet", 7),
	}}
	store.lookup = func(_ context.Context, q EntityQuery) ([]graphprotocol.Entity, error) {
		return []graphprotocol.Entity{testEntity(*q.Selector.Occurrence)}, nil
	}
	limit, nodes, depth := 3, 3, 0
	got, err := (&Service{Store: store}).RelevantContext(t.Context(), graphprotocol.RelevantContextRequest{
		Scope: entityTestScope(), Query: "Normalize greeting: Trace Service and processGreeting",
		Options: graphprotocol.RelevantContextOptions{SearchLimit: &limit, MaxNodes: &nodes, TraversalDepth: &depth},
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, occurrence := range []string{"process", "greeter", "service-greet"} {
		if !slices.ContainsFunc(got.Nodes, func(entity graphprotocol.Entity) bool { return entity.Fact.Occurrence == occurrence }) {
			t.Fatalf("repeated query evidence lost %s: %+v", occurrence, got.Nodes)
		}
	}
}

func TestRelevantContextTraversalCanChangeDirection(t *testing.T) {
	store := &relevantContextStore{matches: []graphprotocol.DiscoveryMatch{{Entity: testEntity("root"), Score: 10}}}
	store.lookup = func(_ context.Context, q EntityQuery) ([]graphprotocol.Entity, error) {
		return []graphprotocol.Entity{testEntity(*q.Selector.Occurrence)}, nil
	}
	store.neighbors = func(_ context.Context, q EntityNeighborQuery) ([]EntityNeighbor, error) {
		switch {
		case q.Occurrence == "root" && q.Direction == "outgoing":
			row := testNeighbor("root", "middle", "root-middle")
			row.Entity = testEntity("middle")
			return []EntityNeighbor{row}, nil
		case q.Occurrence == "middle" && q.Direction == "incoming":
			row := testNeighbor("leaf", "middle", "leaf-middle")
			row.Entity = testEntity("leaf")
			return []EntityNeighbor{row}, nil
		default:
			return nil, nil
		}
	}
	depth, nodes := 2, 3
	edges := []string{"calls"}
	got, err := (&Service{Store: store}).RelevantContext(t.Context(), graphprotocol.RelevantContextRequest{
		Scope: entityTestScope(), Query: "root",
		Options: graphprotocol.RelevantContextOptions{TraversalDepth: &depth, MaxNodes: &nodes, EdgeKinds: edges},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Nodes) != 3 || len(got.Edges) != 2 || !slices.ContainsFunc(got.Nodes, func(entity graphprotocol.Entity) bool { return entity.Fact.Occurrence == "leaf" }) {
		t.Fatalf("mixed-direction path was lost: %+v", got)
	}
}

func TestRelevantContextSupportsNavigatesAndReportsHiddenNodes(t *testing.T) {
	store := &relevantContextStore{matches: []graphprotocol.DiscoveryMatch{{Entity: testEntity("root"), Score: 10}}}
	store.lookup = func(_ context.Context, q EntityQuery) ([]graphprotocol.Entity, error) {
		return []graphprotocol.Entity{testEntity(*q.Selector.Occurrence)}, nil
	}
	store.neighbors = func(_ context.Context, q EntityNeighborQuery) ([]EntityNeighbor, error) {
		if q.Relation != "navigates" || q.Direction != "outgoing" || q.Occurrence != "root" {
			return nil, nil
		}
		first := testNeighbor("root", "screen-a", "navigate-a")
		first.Entity = testEntity("screen-a")
		second := testNeighbor("root", "screen-b", "navigate-b")
		second.Entity = testEntity("screen-b")
		return []EntityNeighbor{first, second}, nil
	}
	depth, nodes := 1, 2
	edges := []string{"navigates"}
	got, err := (&Service{Store: store}).RelevantContext(t.Context(), graphprotocol.RelevantContextRequest{
		Scope: entityTestScope(), Query: "root",
		Options: graphprotocol.RelevantContextOptions{TraversalDepth: &depth, MaxNodes: &nodes, EdgeKinds: edges},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Nodes) != 2 || len(got.Edges) != 1 || !got.Partial || !slices.ContainsFunc(got.Boundaries, func(boundary graphprotocol.Boundary) bool { return boundary.Reason == "node_limit" }) {
		t.Fatalf("navigates/node ceiling changed: %+v", got)
	}
}

func TestRelevantContextPreservesCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err := (&Service{Store: &relevantContextStore{}}).RelevantContext(ctx, graphprotocol.RelevantContextRequest{Scope: entityTestScope(), Query: "root"})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation changed: %v", err)
	}
}

func TestRelevantContextReportsDiscoveryCeiling(t *testing.T) {
	store := &relevantContextStore{}
	for index := range 101 {
		store.matches = append(store.matches, graphprotocol.DiscoveryMatch{Entity: testEntity(fmt.Sprintf("candidate-%03d", index)), Score: 1})
	}
	depth, nodes, limit := 0, 1, 1
	got, err := (&Service{Store: store}).RelevantContext(t.Context(), graphprotocol.RelevantContextRequest{
		Scope: entityTestScope(), Query: "candidate",
		Options: graphprotocol.RelevantContextOptions{TraversalDepth: &depth, MaxNodes: &nodes, SearchLimit: &limit},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !got.Partial || !slices.ContainsFunc(got.Boundaries, func(boundary graphprotocol.Boundary) bool { return boundary.Reason == "discovery_limit" }) {
		t.Fatalf("discovery ceiling was not reported: %+v", got)
	}
}

func TestRelevantContextExpandsBothDirectionsAndRetainsEdgeOccurrences(t *testing.T) {
	store := &relevantContextStore{matches: []graphprotocol.DiscoveryMatch{{Entity: testEntity("root"), Score: 10}}}
	store.lookup = func(_ context.Context, q EntityQuery) ([]graphprotocol.Entity, error) {
		return []graphprotocol.Entity{testEntity(*q.Selector.Occurrence)}, nil
	}
	store.neighbors = func(_ context.Context, q EntityNeighborQuery) ([]EntityNeighbor, error) {
		if q.Relation != "calls" || q.Occurrence != "root" {
			return nil, nil
		}
		if q.Direction == "incoming" {
			row := testNeighbor("caller", "root", "incoming")
			row.Entity = testEntity("caller")
			return []EntityNeighbor{row}, nil
		}
		return []EntityNeighbor{
			testNeighbor("root", "callee", "outgoing-one"),
			testNeighbor("root", "callee", "outgoing-two"),
		}, nil
	}

	depth, nodes := 1, 3
	edges := []string{"calls"}
	kinds := []string{"function"}
	got, err := (&Service{Store: store}).RelevantContext(t.Context(), graphprotocol.RelevantContextRequest{
		Scope: entityTestScope(), Query: "root",
		Options: graphprotocol.RelevantContextOptions{TraversalDepth: &depth, MaxNodes: &nodes, EdgeKinds: edges, NodeKinds: &kinds},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != graphprotocol.StatusOK || got.Partial || len(got.Roots) != 1 || len(got.Nodes) != 3 || len(got.Edges) != 3 {
		t.Fatalf("unexpected context: %+v", got)
	}
	wantOccurrences := map[string]bool{"incoming": true, "outgoing-one": true, "outgoing-two": true}
	for _, edge := range got.Edges {
		if edge.Fact == nil || !wantOccurrences[edge.Fact.Occurrence] {
			t.Fatalf("original edge occurrence lost: %+v", edge)
		}
		delete(wantOccurrences, edge.Fact.Occurrence)
	}
	if len(wantOccurrences) != 0 {
		t.Fatalf("missing edge occurrences: %v", wantOccurrences)
	}
}

func TestRelevantContextPreservesExplicitZeroAndEmptyNodeKinds(t *testing.T) {
	variable := testEntity("variable")
	variable.Fact.Kind = "variable"
	store := &relevantContextStore{matches: []graphprotocol.DiscoveryMatch{{Entity: variable, Score: 1, UsageCount: 1}}}
	store.lookup = func(_ context.Context, q EntityQuery) ([]graphprotocol.Entity, error) {
		return []graphprotocol.Entity{testEntity(*q.Selector.Occurrence)}, nil
	}
	store.neighbors = func(_ context.Context, q EntityNeighborQuery) ([]EntityNeighbor, error) {
		return []EntityNeighbor{testNeighbor(q.Occurrence, "neighbor", "unexpected")}, nil
	}
	depth, nodes := 0, 1
	emptyKinds := []string{}
	got, err := (&Service{Store: store}).RelevantContext(t.Context(), graphprotocol.RelevantContextRequest{
		Scope: entityTestScope(), Query: "variable",
		Options: graphprotocol.RelevantContextOptions{TraversalDepth: &depth, MaxNodes: &nodes, NodeKinds: &emptyKinds},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Nodes) != 1 || got.Nodes[0].Fact.Kind != "variable" || len(got.Edges) != 0 {
		t.Fatalf("explicit zero/empty options changed: %+v", got)
	}
}

func TestRelevantContextRejectsInvalidOptions(t *testing.T) {
	service := &Service{Store: &relevantContextStore{}}
	negative, excessive := -1, 101
	for _, options := range []graphprotocol.RelevantContextOptions{
		{TraversalDepth: &negative},
		{MaxNodes: &excessive},
		{EdgeKinds: []string{"not-a-relation"}},
		{NodeKinds: &[]string{"not-a-kind"}},
	} {
		if _, err := service.RelevantContext(t.Context(), graphprotocol.RelevantContextRequest{Scope: entityTestScope(), Query: "x", Options: options}); err == nil {
			t.Fatalf("accepted invalid options: %+v", options)
		}
	}
}
