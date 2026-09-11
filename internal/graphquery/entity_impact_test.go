package graphquery

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"

	graphv2 "github.com/balcsida/graphnest/internal/graphartifact/v2"
	"github.com/balcsida/graphnest/internal/graphprotocol"
	"google.golang.org/protobuf/proto"
)

type entityImpactTestStore struct {
	entityTestStore
	analysis func(context.Context, AnalysisEntityQuery) ([]graphprotocol.Entity, error)
	evidence func(context.Context, AnalysisEvidenceQuery) ([]graphprotocol.Evidence, error)
}

func (s *entityImpactTestStore) QueryAnalysisEntities(ctx context.Context, query AnalysisEntityQuery) ([]graphprotocol.Entity, error) {
	if s.analysis != nil {
		return s.analysis(ctx, query)
	}
	return nil, ctx.Err()
}

func (s *entityImpactTestStore) QueryAnalysisEvidence(ctx context.Context, query AnalysisEvidenceQuery) ([]graphprotocol.Evidence, error) {
	if s.evidence != nil {
		return s.evidence(ctx, query)
	}
	return nil, ctx.Err()
}

func impactEntity(occurrence, kind, path string) graphprotocol.Entity {
	return graphprotocol.Entity{RepositoryID: 1, ID: occurrence, Fact: &graphv2.Node{
		SourceId: occurrence, Occurrence: occurrence, Kind: kind, Name: occurrence,
		QualifiedName: occurrence, Path: proto.String(path), IsExported: proto.Bool(true),
	}}
}

func impactNeighbor(source graphprotocol.Entity, target, occurrence, kind string) EntityNeighbor {
	return EntityNeighbor{Entity: source, Edge: graphprotocol.Evidence{
		RepositoryID: 1, SourceID: source.ID, TargetID: target,
		Fact: &graphv2.Edge{Occurrence: occurrence, Source: source.Fact.Occurrence, Target: target, Kind: graphv2.EdgeKind(graphRelationKindForTest(kind))},
	}}
}

func graphRelationKindForTest(name string) int32 {
	for index, relation := range []string{"contains", "imports", "references", "calls", "extends", "implements", "exports", "type_of", "returns", "instantiates", "overrides", "decorates", "navigates"} {
		if relation == name {
			return int32(index + 1)
		}
	}
	return 0
}

func TestImpactRadiusExpandsContainersAndRetainsConvergentEvidence(t *testing.T) {
	depth := 3
	serviceNode := impactEntity("service", "class", "core.ts")
	greet := impactEntity("greet", "method", "core.ts")
	caller := impactEntity("caller", "function", "consumer.test.ts")
	route := impactEntity("route", "route", "app/index.tsx")
	store := &entityImpactTestStore{}
	store.lookup = func(_ context.Context, query EntityQuery) ([]graphprotocol.Entity, error) {
		if query.Selector.Occurrence != nil && *query.Selector.Occurrence == "service" {
			return []graphprotocol.Entity{serviceNode}, nil
		}
		return nil, nil
	}
	store.neighbors = func(_ context.Context, query EntityNeighborQuery) ([]EntityNeighbor, error) {
		switch {
		case query.Direction == "outgoing" && query.Relation == "contains" && query.Occurrence == "service":
			row := impactNeighbor(greet, "service", "member", "contains")
			row.Edge.SourceID, row.Edge.TargetID = "service", "greet"
			row.Edge.Fact.Source, row.Edge.Fact.Target = "service", "greet"
			return []EntityNeighbor{row}, nil
		case query.Direction == "incoming" && query.Relation == "calls" && query.Occurrence == "service":
			return []EntityNeighbor{impactNeighbor(caller, "service", "call-service", "calls")}, nil
		case query.Direction == "incoming" && query.Relation == "references" && query.Occurrence == "service":
			return []EntityNeighbor{impactNeighbor(route, "service", "route-service", "references")}, nil
		case query.Direction == "incoming" && query.Relation == "references" && query.Occurrence == "greet":
			return []EntityNeighbor{impactNeighbor(caller, "greet", "call-greet", "references")}, nil
		case query.Direction == "incoming" && query.Relation == "calls" && query.Occurrence == "caller":
			return []EntityNeighbor{impactNeighbor(serviceNode, "caller", "cycle", "calls")}, nil
		default:
			return nil, nil
		}
	}

	got, err := (&Service{Store: store}).ImpactRadius(t.Context(), graphprotocol.EntityImpactRequest{
		Scope: entityTestScope(), Occurrence: "service", MaxDepth: &depth,
	})
	if err != nil || got.Status != graphprotocol.StatusOK || len(got.Entities) != 4 || len(got.Edges) != 5 || got.Partial {
		t.Fatalf("impact=%+v err=%v", got, err)
	}
	if got.Entities[1].Fact.Occurrence != "greet" || got.Entities[1].Depth != 0 {
		t.Fatalf("same-depth member=%+v", got.Entities[1])
	}
	wantBlast := graphprotocol.BlastSummary{Direct: 2, WithinHops: 3, Hops: 3, Files: 3, TestFiles: 1, Routes: 1, TopFiles: []graphprotocol.BlastFile{{File: "app/index.tsx", Symbols: 1}, {File: "consumer.test.ts", Symbols: 1, Test: true}, {File: "core.ts", Symbols: 1}}}
	if !reflect.DeepEqual(got.Blast, &wantBlast) {
		t.Fatalf("blast=%+v want=%+v", got.Blast, wantBlast)
	}
	assertAnalysisState(t, got, true, 0)
	for _, edge := range got.Edges {
		if edge.Fact.GetKind() == graphv2.EdgeKind(1) && edge.Fact.Occurrence != "member" {
			t.Fatalf("incoming contains leaked: %+v", edge)
		}
	}
}

func TestCallGraphAndUsagesPreserveDirectionsAndOccurrences(t *testing.T) {
	depth := 1
	root := impactEntity("root", "function", "root.ts")
	caller := impactEntity("caller", "function", "caller.ts")
	callee := impactEntity("callee", "function", "callee.ts")
	container := impactEntity("file", "file", "root.ts")
	store := &entityImpactTestStore{}
	store.lookup = func(_ context.Context, query EntityQuery) ([]graphprotocol.Entity, error) {
		if query.Selector.Occurrence != nil && *query.Selector.Occurrence == "root" {
			return []graphprotocol.Entity{root}, nil
		}
		return nil, nil
	}
	store.neighbors = func(_ context.Context, query EntityNeighborQuery) ([]EntityNeighbor, error) {
		switch {
		case query.Direction == "incoming" && query.Relation == "calls" && query.Occurrence == "root":
			return []EntityNeighbor{impactNeighbor(caller, "root", "call-1", "calls"), impactNeighbor(caller, "root", "call-2", "calls")}, nil
		case query.Direction == "incoming" && query.Relation == "contains" && query.Occurrence == "root":
			return []EntityNeighbor{impactNeighbor(container, "root", "contains-root", "contains")}, nil
		case query.Direction == "outgoing" && query.Relation == "imports" && query.Occurrence == "root":
			row := impactNeighbor(callee, "root", "root-imports", "imports")
			row.Edge.SourceID, row.Edge.TargetID = "root", "callee"
			row.Edge.Fact.Source, row.Edge.Fact.Target = "root", "callee"
			return []EntityNeighbor{row}, nil
		case query.Direction == "outgoing" && query.Relation == "references" && query.Occurrence == "root":
			row := impactNeighbor(caller, "root", "root-caller", "references")
			row.Edge.SourceID, row.Edge.TargetID = "root", "caller"
			row.Edge.Fact.Source, row.Edge.Fact.Target = "root", "caller"
			return []EntityNeighbor{row}, nil
		default:
			return nil, nil
		}
	}
	service := &Service{Store: store}
	graph, err := service.CallGraph(t.Context(), graphprotocol.EntityImpactRequest{Scope: entityTestScope(), Occurrence: "root", MaxDepth: &depth})
	if err != nil || len(graph.Entities) != 3 || len(graph.Edges) != 3 {
		t.Fatalf("call graph=%+v err=%v", graph, err)
	}
	wantEdges := map[string][2]string{"call-1": {"caller", "root"}, "root-caller": {"root", "caller"}, "root-imports": {"root", "callee"}}
	for _, edge := range graph.Edges {
		if wantEdges[edge.Fact.Occurrence] != [2]string{edge.Fact.Source, edge.Fact.Target} {
			t.Fatalf("call graph edge=%+v", edge.Fact)
		}
		delete(wantEdges, edge.Fact.Occurrence)
	}
	if len(wantEdges) != 0 {
		t.Fatalf("missing call graph edges=%v", wantEdges)
	}
	usages, err := service.Usages(t.Context(), graphprotocol.EntityRequest{Scope: entityTestScope(), Occurrence: "root"})
	if err != nil || len(usages.Usages) != 3 || usages.Usages[0].Entity.Fact.Occurrence != "file" || usages.Usages[1].Edge.Fact.Occurrence == usages.Usages[2].Edge.Fact.Occurrence {
		t.Fatalf("usages=%+v err=%v", usages, err)
	}
}

func TestEntityGraphProjectionsUseBoundedFiltersAndInducedEdges(t *testing.T) {
	a := impactEntity("a", "function", "core.ts")
	b := impactEntity("b", "class", "core.ts")
	file := impactEntity("file", "file", "app/index.tsx")
	store := &entityImpactTestStore{}
	store.analysis = func(_ context.Context, query AnalysisEntityQuery) ([]graphprotocol.Entity, error) {
		switch {
		case query.QualifiedPattern != "":
			if query.QualifiedPattern != `^Service::gree.$` {
				t.Fatalf("qualified regexp=%q", query.QualifiedPattern)
			}
			return []graphprotocol.Entity{a}, nil
		case reflect.DeepEqual(query.Kinds, []string{"file"}):
			return []graphprotocol.Entity{file}, nil
		default:
			return []graphprotocol.Entity{a, b}, nil
		}
	}
	store.evidence = func(_ context.Context, query AnalysisEvidenceQuery) ([]graphprotocol.Evidence, error) {
		if !reflect.DeepEqual(query.Occurrences, []string{"a", "b"}) {
			t.Fatalf("edge endpoints=%v", query.Occurrences)
		}
		return []graphprotocol.Evidence{{RepositoryID: 1, SourceID: "a", TargetID: "b", Fact: &graphv2.Edge{Occurrence: "a-b", Source: "a", Target: "b"}}}, nil
	}
	service := &Service{Store: store}
	exported, err := service.ExportedSymbols(t.Context(), graphprotocol.FileEntityRequest{Scope: entityTestScope(), Path: "core.ts"})
	if err != nil || len(exported.Entities) != 2 {
		t.Fatalf("exports=%+v err=%v", exported, err)
	}
	qualified, err := service.FindByQualifiedName(t.Context(), graphprotocol.QualifiedNameRequest{Scope: entityTestScope(), Pattern: "Service::gree?"})
	if err != nil || len(qualified.Entities) != 1 {
		t.Fatalf("qualified=%+v err=%v", qualified, err)
	}
	modules, err := service.ModuleStructure(t.Context(), graphprotocol.ScopeRequest{Scope: entityTestScope()})
	if err != nil || !reflect.DeepEqual(modules.Modules, []graphprotocol.ModuleDirectory{{Directory: "app", Files: []string{"app/index.tsx"}}}) {
		t.Fatalf("modules=%+v err=%v", modules, err)
	}
	withEdges := true
	filtered, err := service.FilteredSubgraph(t.Context(), graphprotocol.FilteredSubgraphRequest{Scope: entityTestScope(), Filter: graphprotocol.EntityFilter{Paths: []string{"core.ts"}, Exported: proto.Bool(true)}, IncludeEdges: &withEdges})
	if err != nil || len(filtered.Entities) != 2 || len(filtered.Edges) != 1 {
		t.Fatalf("filtered=%+v err=%v", filtered, err)
	}
}

func TestEntityImpactRejectsStaleCanceledInvalidAndBoundedResults(t *testing.T) {
	depth := 3
	request := graphprotocol.EntityImpactRequest{Scope: entityTestScope(), Occurrence: "a", MaxDepth: &depth}
	stale := &entityImpactTestStore{entityTestStore: entityTestStore{changed: true}}
	if got, err := (&Service{Store: stale}).ImpactRadius(t.Context(), request); !errors.Is(err, ErrGenerationChanged) || !reflect.DeepEqual(got, graphprotocol.SubgraphResponse{}) {
		t.Fatalf("stale impact=%+v err=%v", got, err)
	}
	canceled, cancel := context.WithCancel(t.Context())
	cancel()
	if got, err := (&Service{Store: &entityImpactTestStore{}}).CallGraph(canceled, request); !errors.Is(err, context.Canceled) || !reflect.DeepEqual(got, graphprotocol.SubgraphResponse{}) {
		t.Fatalf("canceled call graph=%+v err=%v", got, err)
	}
	depth = 1
	store := &entityImpactTestStore{}
	store.neighbors = func(_ context.Context, query EntityNeighborQuery) ([]EntityNeighbor, error) {
		if query.Direction == "incoming" && query.Relation == "calls" {
			return []EntityNeighbor{impactNeighbor(impactEntity(query.Occurrence+"x", "function", "x.ts"), query.Occurrence, query.Occurrence+"-edge", "calls")}, nil
		}
		return nil, nil
	}
	got, err := (&Service{Store: store, Limits: Limits{MaxNodes: 1}}).ImpactRadius(t.Context(), request)
	if err != nil || !got.Partial || got.Blast == nil || len(got.Boundaries) == 0 || got.Boundaries[0].Reason != "node_limit" {
		t.Fatalf("bounded impact=%+v err=%v", got, err)
	}
	assertAnalysisState(t, got, true, 0)
	if _, err = (&Service{Store: store}).FindByQualifiedName(t.Context(), graphprotocol.QualifiedNameRequest{Scope: entityTestScope(), Pattern: ""}); !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("empty qualified pattern=%v", err)
	}
}

func TestImpactRadiusAdmitsNodesAndEdgesAtomically(t *testing.T) {
	depth := 2
	root := impactEntity("root", "function", "root.ts")
	caller := impactEntity("caller", "function", "caller.ts")
	descendant := impactEntity("descendant", "function", "descendant.ts")
	store := &entityImpactTestStore{}
	store.lookup = func(_ context.Context, query EntityQuery) ([]graphprotocol.Entity, error) {
		if query.Selector.Occurrence != nil && *query.Selector.Occurrence == "root" {
			return []graphprotocol.Entity{root}, nil
		}
		return nil, nil
	}
	store.neighbors = func(_ context.Context, query EntityNeighborQuery) ([]EntityNeighbor, error) {
		if query.Direction != "incoming" || query.Relation != "calls" {
			return nil, nil
		}
		switch query.Occurrence {
		case "root":
			return []EntityNeighbor{impactNeighbor(caller, "root", "caller-root", "calls")}, nil
		case "caller":
			return []EntityNeighbor{impactNeighbor(descendant, "caller", "descendant-caller", "calls")}, nil
		}
		return nil, nil
	}
	got, err := (&Service{Store: store, Limits: Limits{MaxNodes: 1}}).ImpactRadius(t.Context(), graphprotocol.EntityImpactRequest{Scope: entityTestScope(), Occurrence: "root", MaxDepth: &depth})
	if err != nil || !got.Partial || len(got.Entities) != 1 || len(got.Edges) != 0 {
		t.Fatalf("atomic bounded impact=%+v err=%v", got, err)
	}
	for _, edge := range got.Edges {
		if edge.Fact.Source != "root" || edge.Fact.Target != "root" {
			t.Fatalf("edge endpoint absent from retained entities: %+v", edge.Fact)
		}
	}
}

func TestImpactRadiusReexpandsShorterContainerPath(t *testing.T) {
	depth := 3
	root := impactEntity("root", "function", "root.ts")
	longA := impactEntity("long-a", "function", "long.ts")
	longB := impactEntity("long-b", "function", "long.ts")
	container := impactEntity("container", "class", "container.ts")
	converged := impactEntity("converged", "method", "container.ts")
	descendant := impactEntity("descendant", "function", "descendant.ts")
	store := &entityImpactTestStore{}
	store.lookup = func(_ context.Context, query EntityQuery) ([]graphprotocol.Entity, error) {
		if query.Selector.Occurrence != nil && *query.Selector.Occurrence == "root" {
			return []graphprotocol.Entity{root}, nil
		}
		return nil, nil
	}
	store.neighbors = func(_ context.Context, query EntityNeighborQuery) ([]EntityNeighbor, error) {
		switch {
		case query.Direction == "incoming" && query.Relation == "calls" && query.Occurrence == "root":
			return []EntityNeighbor{impactNeighbor(longA, "root", "long-a-root", "calls"), impactNeighbor(container, "root", "container-root", "calls")}, nil
		case query.Direction == "incoming" && query.Relation == "calls" && query.Occurrence == "long-a":
			return []EntityNeighbor{impactNeighbor(longB, "long-a", "long-b-long-a", "calls")}, nil
		case query.Direction == "incoming" && query.Relation == "calls" && query.Occurrence == "long-b":
			return []EntityNeighbor{impactNeighbor(converged, "long-b", "converged-long-b", "calls")}, nil
		case query.Direction == "outgoing" && query.Relation == "contains" && query.Occurrence == "container":
			row := impactNeighbor(converged, "container", "container-converged", "contains")
			row.Edge.SourceID, row.Edge.TargetID = "container", "converged"
			row.Edge.Fact.Source, row.Edge.Fact.Target = "container", "converged"
			return []EntityNeighbor{row}, nil
		case query.Direction == "incoming" && query.Relation == "calls" && query.Occurrence == "converged":
			return []EntityNeighbor{impactNeighbor(descendant, "converged", "descendant-converged", "calls")}, nil
		}
		return nil, nil
	}
	got, err := (&Service{Store: store}).ImpactRadius(t.Context(), graphprotocol.EntityImpactRequest{Scope: entityTestScope(), Occurrence: "root", MaxDepth: &depth})
	if err != nil {
		t.Fatal(err)
	}
	depths := map[string]int{}
	for _, entity := range got.Entities {
		depths[entity.Fact.Occurrence] = entity.Depth
	}
	if depths["converged"] != 1 || depths["descendant"] != 2 {
		t.Fatalf("shortest impact depths=%v result=%+v", depths, got)
	}
}

func assertAnalysisState(t *testing.T, response any, complete bool, unresolved int) {
	t.Helper()
	raw, err := json.Marshal(response)
	if err != nil {
		t.Fatal(err)
	}
	var value struct {
		Analysis *struct {
			Freshness  string `json:"freshness"`
			Coverage   string `json:"coverage"`
			Unresolved int    `json:"unresolved"`
			Complete   bool   `json:"complete"`
		} `json:"analysis"`
	}
	if err = json.Unmarshal(raw, &value); err != nil {
		t.Fatal(err)
	}
	if value.Analysis == nil || value.Analysis.Freshness != "current" || value.Analysis.Coverage != "not_assessed" || value.Analysis.Unresolved != unresolved || value.Analysis.Complete != complete {
		t.Fatalf("analysis state=%+v raw=%s", value.Analysis, raw)
	}
}
