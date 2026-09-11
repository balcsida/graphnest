package graphquery

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	graphv2 "github.com/balcsida/graphnest/internal/graphartifact/v2"
	"github.com/balcsida/graphnest/internal/graphprotocol"
)

type aggregateTestStore struct {
	entityTestStore
	counts     []graphprotocol.AggregateCount
	top        []graphprotocol.DependedOn
	files      []graphprotocol.Entity
	moduleRows []AggregateModuleRow
}

func (s *aggregateTestStore) QueryFan(_ context.Context, _ AggregateFanQuery) ([]graphprotocol.AggregateCount, error) {
	return s.counts, nil
}

func (*aggregateTestStore) AggregateStats(context.Context, QuerySnapshot) (graphprotocol.GraphStats, error) {
	return graphprotocol.GraphStats{}, nil
}
func (*aggregateTestStore) QueryNodeMetrics(context.Context, QuerySnapshot, string) (graphprotocol.NodeMetrics, error) {
	return graphprotocol.NodeMetrics{}, nil
}
func (*aggregateTestStore) QueryAggregateNames(context.Context, AggregateNamesQuery) ([]string, error) {
	return nil, nil
}
func (s *aggregateTestStore) QueryTopDependedOn(context.Context, AggregateLimitQuery) ([]graphprotocol.DependedOn, error) {
	return s.top, nil
}
func (*aggregateTestStore) QueryTopCallingFiles(context.Context, AggregateLimitQuery) ([]graphprotocol.CallingFile, error) {
	return nil, nil
}
func (s *aggregateTestStore) QueryFileNodes(context.Context, QuerySnapshot, []string) ([]graphprotocol.Entity, error) {
	return s.files, nil
}
func (s *aggregateTestStore) QueryModuleAggregation(context.Context, AggregateModuleQuery) ([]AggregateModuleRow, error) {
	return s.moduleRows, nil
}
func (*aggregateTestStore) QueryUnresolvedReferences(context.Context, AggregateUnresolvedQuery) ([]graphprotocol.UnresolvedReference, error) {
	return nil, nil
}

func TestFanInPreservesObservedCountsAndGeneration(t *testing.T) {
	store := &aggregateTestStore{counts: []graphprotocol.AggregateCount{{ID: "called", Count: 8}}}
	got, err := (&Service{Store: store}).FanIn(t.Context(), graphprotocol.AggregateValuesRequest{
		Scope: entityTestScope(), Values: []string{"called", "missing"},
	})
	if err != nil || !reflect.DeepEqual(got.Counts, store.counts) || len(got.Generations) != 1 {
		t.Fatalf("fan-in=%+v err=%v", got, err)
	}
}

func TestFoldModuleRowsKeepsNULSeparatedPairsDistinct(t *testing.T) {
	rows := []AggregateModuleRow{
		{Source: "source", Target: "target", Kind: "calls", From: "a\x00b", To: "c", Count: 1},
		{Source: "source", Target: "target", Kind: "calls", From: "a", To: "b\x00c", Count: 1},
		{Source: "source", Target: "target", Kind: "calls", From: "a\x00b", To: "c", Count: 1},
	}

	_, got := foldModuleRows(rows, 2, []int16{int16(graphv2.EdgeKind_EDGE_KIND_CALLS)})
	want := []graphprotocol.ModulePair{
		{Source: "source", Target: "target", From: "a\x00b", To: "c", Count: 2},
		{Source: "source", Target: "target", From: "a", To: "b\x00c", Count: 1},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("pairs=%+v, want %+v", got, want)
	}
}

func TestAggregateQueriesRejectStaleHiddenCanceledAndOversizedResults(t *testing.T) {
	stale := &aggregateTestStore{entityTestStore: entityTestStore{changed: true}}
	if got, err := (&Service{Store: stale}).FanIn(t.Context(), graphprotocol.AggregateValuesRequest{Scope: entityTestScope(), Values: []string{"a"}}); !errors.Is(err, ErrGenerationChanged) || !reflect.DeepEqual(got, graphprotocol.AggregateCountsResponse{}) {
		t.Fatalf("stale fan-in=%+v err=%v", got, err)
	}

	canceled, cancel := context.WithCancel(t.Context())
	cancel()
	if got, err := (&Service{Store: &aggregateTestStore{}}).FanOut(canceled, graphprotocol.AggregateValuesRequest{Scope: entityTestScope(), Values: []string{"a"}}); !errors.Is(err, context.Canceled) || !reflect.DeepEqual(got, graphprotocol.AggregateCountsResponse{}) {
		t.Fatalf("canceled fan-out=%+v err=%v", got, err)
	}

	hidden := testEntity("hidden")
	hidden.RepositoryID = 99
	if got, err := (&Service{Store: &aggregateTestStore{files: []graphprotocol.Entity{hidden}}}).FileNodes(t.Context(), graphprotocol.FileNodesRequest{Scope: entityTestScope(), Paths: []string{"hidden.ts"}}); !errors.Is(err, ErrGenerationChanged) || !reflect.DeepEqual(got, graphprotocol.EntitiesResponse{}) {
		t.Fatalf("hidden file nodes=%+v err=%v", got, err)
	}

	large := strings.Repeat("x", MaxEntityQueryBytes)
	oversized := &Service{Store: &aggregateTestStore{moduleRows: []AggregateModuleRow{{Source: large, Target: "b", Kind: "calls"}}}}
	if got, err := oversized.ModuleAggregation(t.Context(), graphprotocol.ModuleAggregationRequest{Scope: entityTestScope(), Assignments: []graphprotocol.ModuleAssignment{{FilePath: "a.ts", Module: "a"}}, Kinds: []string{"calls"}}); !errors.Is(err, ErrQuerySize) || !reflect.DeepEqual(got, graphprotocol.ModuleAggregationResponse{}) {
		t.Fatalf("oversized module=%+v err=%v", got, err)
	}

	bounded, err := (&Service{Store: &aggregateTestStore{top: []graphprotocol.DependedOn{{NodeID: "a", Dependents: 2}, {NodeID: "b", Dependents: 1}}}, Limits: Limits{MaxRows: 1}}).TopDependedOn(t.Context(), graphprotocol.AggregateLimitRequest{Scope: entityTestScope(), Limit: 5})
	if err != nil || len(bounded.Nodes) != 1 || !bounded.Partial || len(bounded.Boundaries) != 1 || bounded.Boundaries[0].Reason != "row_limit" {
		t.Fatalf("bounded top depended=%+v err=%v", bounded, err)
	}
}
