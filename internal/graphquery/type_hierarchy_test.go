package graphquery

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"testing"

	graphv2 "github.com/balcsida/graphnest/internal/graphartifact/v2"
	"github.com/balcsida/graphnest/internal/graphprotocol"
	"google.golang.org/protobuf/proto"
)

type typeHierarchyTestStore struct {
	entityImpactTestStore
	counts func(context.Context, HierarchyCountQuery) (HierarchyCount, error)
}

func (s *typeHierarchyTestStore) CountHierarchyChildren(ctx context.Context, query HierarchyCountQuery) (HierarchyCount, error) {
	if s.counts != nil {
		return s.counts(ctx, query)
	}
	return HierarchyCount{}, ctx.Err()
}

func hierarchyEntity(occurrence, name, kind string) graphprotocol.Entity {
	entity := impactEntity(occurrence, kind, "types.ts")
	entity.Fact.Name = name
	entity.Fact.QualifiedName = name
	return entity
}

func hierarchyNeighbor(entity graphprotocol.Entity, parent, edgeOccurrence, relation, direction string) EntityNeighbor {
	edge := &graphv2.Edge{
		SourceId: edgeOccurrence, Occurrence: edgeOccurrence, Kind: graphv2.EdgeKind(graphRelationKindForTest(relation)),
		Source: entity.Fact.Occurrence, Target: parent,
		Location:   &graphv2.Location{Path: proto.String("types.ts"), Start: &graphv2.Position{Line: proto.Int32(7), Character: proto.Int32(11)}},
		Confidence: proto.Float64(0.9), ResolutionReason: proto.String("exact-match"),
	}
	row := EntityNeighbor{Entity: entity, Edge: graphprotocol.Evidence{RepositoryID: 1, SourceID: entity.ID, TargetID: parent, Fact: edge}}
	if direction == "outgoing" {
		row.Edge.SourceID, row.Edge.TargetID = parent, entity.ID
		row.Edge.Fact.Source, row.Edge.Fact.Target = parent, entity.Fact.Occurrence
	}
	return row
}

func TestTypeRelationsPreserveDirectionsOccurrencesAndUnknownCoverage(t *testing.T) {
	root := hierarchyEntity("root", "makeService", "function")
	service := hierarchyEntity("service", "Service", "class")
	greeter := hierarchyEntity("greeter", "Greeter", "interface")
	caller := hierarchyEntity("caller", "caller", "function")
	otherFunction := hierarchyEntity("other", "helper", "function")
	store := &typeHierarchyTestStore{}
	store.lookup = func(_ context.Context, query EntityQuery) ([]graphprotocol.Entity, error) {
		if query.Selector.Occurrence != nil && *query.Selector.Occurrence == "root" {
			return []graphprotocol.Entity{root}, nil
		}
		return nil, nil
	}
	store.neighbors = func(_ context.Context, query EntityNeighborQuery) ([]EntityNeighbor, error) {
		switch query.Direction + ":" + query.Relation {
		case "outgoing:type_of":
			return []EntityNeighbor{
				hierarchyNeighbor(service, "root", "type-site-1", "type_of", "outgoing"),
				hierarchyNeighbor(service, "root", "type-site-2", "type_of", "outgoing"),
			}, nil
		case "outgoing:returns":
			return []EntityNeighbor{hierarchyNeighbor(greeter, "root", "return-site", "returns", "outgoing")}, nil
		case "incoming:type_of":
			return []EntityNeighbor{hierarchyNeighbor(caller, "root", "user-site", "type_of", "incoming")}, nil
		case "incoming:returns":
			return []EntityNeighbor{hierarchyNeighbor(caller, "root", "returner-site", "returns", "incoming")}, nil
		case "outgoing:references":
			return []EntityNeighbor{
				hierarchyNeighbor(service, "root", "reference-type", "references", "outgoing"),
				hierarchyNeighbor(otherFunction, "root", "reference-function", "references", "outgoing"),
			}, nil
		case "outgoing:overrides":
			return []EntityNeighbor{hierarchyNeighbor(caller, "root", "recorded-override", "overrides", "outgoing")}, nil
		default:
			return nil, nil
		}
	}

	got, err := (&Service{Store: store}).TypeRelations(t.Context(), graphprotocol.TypeRelationsRequest{Scope: entityTestScope(), Occurrence: "root"})
	if err != nil || got.Status != graphprotocol.StatusOK || got.TypeKnowledge != graphprotocol.TypeKnowledgeRecorded {
		t.Fatalf("relations=%+v err=%v", got, err)
	}
	if len(got.Types) != 2 || len(got.Users) != 1 || len(got.Returners) != 1 || len(got.ReferencedTypes) != 1 || len(got.RecordedOverrides) != 1 {
		t.Fatalf("relation groups=%+v", got)
	}
	if got.Types[0].Relation != "type_of" || len(got.Types[0].Edges) != 2 || got.Types[0].Edges[0].Fact.Occurrence != "type-site-1" || got.Types[0].Edges[1].Fact.Occurrence != "type-site-2" {
		t.Fatalf("type evidence=%+v", got.Types)
	}
	if got.Types[1].Relation != "returns" || got.Types[1].Edges[0].Fact.Source != "root" || got.Users[0].Edges[0].Fact.Target != "root" || got.ReferencedTypes[0].Entity.Fact.Kind != "class" {
		t.Fatalf("relation directions=%+v", got)
	}
	assertAnalysisState(t, got, true, 0)

	empty, err := (&Service{Store: store}).TypeRelations(t.Context(), graphprotocol.TypeRelationsRequest{Scope: entityTestScope(), Occurrence: "missing"})
	if err != nil || empty.Status != graphprotocol.StatusNotFound || empty.TypeKnowledge != graphprotocol.TypeKnowledgeUnknown || len(empty.Types) != 0 {
		t.Fatalf("missing relations=%+v err=%v", empty, err)
	}
}

func TestTypeHierarchyReturnsBothDirectionsEvidenceAndDerivedOverrides(t *testing.T) {
	square := hierarchyEntity("square", "Square", "class")
	shape := hierarchyEntity("shape", "Shape", "class")
	drawable := hierarchyEntity("drawable", "Drawable", "interface")
	tile := hierarchyEntity("tile", "Tile", "class")
	squareDraw := hierarchyEntity("square-draw", "draw", "method")
	shapeDraw := hierarchyEntity("shape-draw", "draw", "method")
	shapeArea := hierarchyEntity("shape-area", "area", "method")
	store := &typeHierarchyTestStore{}
	store.lookup = func(_ context.Context, query EntityQuery) ([]graphprotocol.Entity, error) {
		if query.Selector.Occurrence != nil && *query.Selector.Occurrence == "square" {
			return []graphprotocol.Entity{square}, nil
		}
		return nil, nil
	}
	store.neighbors = func(_ context.Context, query EntityNeighborQuery) ([]EntityNeighbor, error) {
		switch query.Direction + ":" + query.Relation + ":" + query.Occurrence {
		case "outgoing:extends:square":
			return []EntityNeighbor{hierarchyNeighbor(shape, "square", "square-shape", "extends", "outgoing")}, nil
		case "outgoing:implements:shape":
			return []EntityNeighbor{hierarchyNeighbor(drawable, "shape", "shape-drawable", "implements", "outgoing")}, nil
		case "incoming:extends:square":
			return []EntityNeighbor{hierarchyNeighbor(tile, "square", "tile-square", "extends", "incoming")}, nil
		case "outgoing:contains:square":
			return []EntityNeighbor{hierarchyNeighbor(squareDraw, "square", "square-contains-draw", "contains", "outgoing")}, nil
		case "outgoing:contains:shape":
			return []EntityNeighbor{
				hierarchyNeighbor(shapeDraw, "shape", "shape-contains-draw", "contains", "outgoing"),
				hierarchyNeighbor(shapeArea, "shape", "shape-contains-area", "contains", "outgoing"),
			}, nil
		default:
			return nil, nil
		}
	}
	store.counts = func(_ context.Context, query HierarchyCountQuery) (HierarchyCount, error) {
		if query.Occurrence == "square" {
			return HierarchyCount{Subtypes: 1}, nil
		}
		return HierarchyCount{}, nil
	}

	got, err := (&Service{Store: store}).TypeHierarchy(t.Context(), graphprotocol.TypeHierarchyRequest{Scope: entityTestScope(), Occurrence: "square"})
	if err != nil || got.Status != graphprotocol.StatusOK || got.Focus.Fact.Occurrence != "square" || got.Bounded || got.Polymorphic {
		t.Fatalf("hierarchy=%+v err=%v", got, err)
	}
	wantAncestors := []string{"shape", "drawable"}
	ancestorOccurrences := []string{got.Ancestors.Items[0].Entity.Fact.Occurrence, got.Ancestors.Items[1].Entity.Fact.Occurrence}
	if !reflect.DeepEqual(ancestorOccurrences, wantAncestors) || got.Ancestors.Items[0].Depth != 1 || got.Ancestors.Items[1].Depth != 2 || got.Ancestors.Items[1].ParentID != "shape" {
		t.Fatalf("ancestors=%+v", got.Ancestors)
	}
	if len(got.Descendants.Items) != 1 || got.Descendants.Items[0].Entity.Fact.Occurrence != "tile" || got.DirectSubtypes != 1 || got.DirectImplementers != 0 {
		t.Fatalf("descendants=%+v direct=%d/%d", got.Descendants, got.DirectSubtypes, got.DirectImplementers)
	}
	if got.Ancestors.Items[0].Edge.Fact.Occurrence != "square-shape" || got.Ancestors.Items[0].Edge.Fact.GetLocation().GetStart().GetLine() != 7 {
		t.Fatalf("hierarchy evidence=%+v", got.Ancestors.Items[0].Edge)
	}
	if len(got.DerivedOverrides) != 1 || got.DerivedOverrides[0].Member.Fact.Occurrence != "square-draw" || got.DerivedOverrides[0].BaseMember.Fact.Occurrence != "shape-draw" || got.DerivedOverrides[0].BaseType.Fact.Occurrence != "shape" || !got.DerivedOverrides[0].SignatureUncertain {
		t.Fatalf("derived overrides=%+v", got.DerivedOverrides)
	}
	assertAnalysisState(t, got, true, 0)
}

func TestTypeHierarchyPreservesSynthesizedAndDuplicateEvidence(t *testing.T) {
	clock := hierarchyEntity("clock", "Clock", "interface")
	system := hierarchyEntity("system", "System", "struct")
	store := &typeHierarchyTestStore{}
	store.lookup = func(_ context.Context, query EntityQuery) ([]graphprotocol.Entity, error) {
		return []graphprotocol.Entity{clock}, nil
	}
	store.neighbors = func(_ context.Context, query EntityNeighborQuery) ([]EntityNeighbor, error) {
		if query.Direction != "incoming" || query.Occurrence != "clock" {
			return nil, nil
		}
		row := hierarchyNeighbor(system, "clock", query.Relation+"-system", query.Relation, "incoming")
		if query.Relation == "implements" {
			row.Edge.Fact.Provenance = proto.String("heuristic")
			row.Edge.Fact.Extensions = []*graphv2.Extension{{Namespace: "codegraph.edge-metadata", Json: []byte(`{"synthesizedBy":"go-implements","via":"Clock","registeredAt":"types.ts:11"}`)}}
		}
		return []EntityNeighbor{row}, nil
	}
	store.counts = func(context.Context, HierarchyCountQuery) (HierarchyCount, error) {
		return HierarchyCount{Subtypes: 1}, nil
	}

	got, err := (&Service{Store: store}).TypeHierarchy(t.Context(), graphprotocol.TypeHierarchyRequest{Scope: entityTestScope(), Occurrence: "clock"})
	if err != nil || len(got.Descendants.Items) != 1 || got.Descendants.Items[0].Relation != "extends" || got.DirectSubtypes != 1 || got.DirectImplementers != 0 {
		t.Fatalf("duplicate hierarchy=%+v err=%v", got, err)
	}
	// With only the synthesized edge, the original provenance and lifted viewer fields remain visible.
	store.neighbors = func(_ context.Context, query EntityNeighborQuery) ([]EntityNeighbor, error) {
		if query.Direction != "incoming" || query.Relation != "implements" || query.Occurrence != "clock" {
			return nil, nil
		}
		row := hierarchyNeighbor(system, "clock", "implements-system", "implements", "incoming")
		row.Edge.Fact.Provenance = proto.String("heuristic")
		row.Edge.Fact.Extensions = []*graphv2.Extension{{Namespace: "codegraph.edge-metadata", Json: []byte(`{"synthesizedBy":"go-implements","via":"Clock","registeredAt":"types.ts:11"}`)}}
		return []EntityNeighbor{row}, nil
	}
	store.counts = func(context.Context, HierarchyCountQuery) (HierarchyCount, error) {
		return HierarchyCount{Subtypes: 1, Implementers: 1}, nil
	}
	got, err = (&Service{Store: store}).TypeHierarchy(t.Context(), graphprotocol.TypeHierarchyRequest{Scope: entityTestScope(), Occurrence: "clock"})
	entry := got.Descendants.Items[0]
	if err != nil || !entry.Synthesized || entry.Via != "go-implements" || entry.RegisteredAt != "types.ts:11" || entry.Edge.Fact.GetProvenance() != "heuristic" || got.DirectImplementers != 1 {
		t.Fatalf("synthesized hierarchy=%+v err=%v", got, err)
	}
}

func TestTypeHierarchyReportsWideDepthAndViewerBounds(t *testing.T) {
	root := hierarchyEntity("root", "Root", "interface")
	store := &typeHierarchyTestStore{}
	store.lookup = func(_ context.Context, query EntityQuery) ([]graphprotocol.Entity, error) {
		return []graphprotocol.Entity{root}, nil
	}
	store.neighbors = func(_ context.Context, query EntityNeighborQuery) ([]EntityNeighbor, error) {
		if query.Direction != "incoming" || query.Relation != "implements" || query.Occurrence != "root" {
			return nil, nil
		}
		rows := make([]EntityNeighbor, 401)
		for i := range rows {
			child := hierarchyEntity(fmt.Sprintf("child-%03d", i), fmt.Sprintf("Child%03d", i), "class")
			rows[i] = hierarchyNeighbor(child, "root", fmt.Sprintf("edge-%03d", i), "implements", "incoming")
		}
		return rows, nil
	}
	store.counts = func(context.Context, HierarchyCountQuery) (HierarchyCount, error) {
		return HierarchyCount{Subtypes: 437, Implementers: 437}, nil
	}

	got, err := (&Service{Store: store}).TypeHierarchy(t.Context(), graphprotocol.TypeHierarchyRequest{Scope: entityTestScope(), Occurrence: "root"})
	if err != nil || len(got.Descendants.Items) != 400 || got.Descendants.Total != 400 || got.Descendants.Shown != 240 || !got.Descendants.Truncated || got.DirectSubtypes != 437 || got.DirectImplementers != 437 || !got.Bounded || !got.Polymorphic {
		t.Fatalf("wide hierarchy=%+v err=%v", got, err)
	}

	chain := map[string]graphprotocol.Entity{"root": root}
	for i := 1; i <= 7; i++ {
		chain[fmt.Sprintf("d%d", i)] = hierarchyEntity(fmt.Sprintf("d%d", i), fmt.Sprintf("D%d", i), "class")
	}
	store.neighbors = func(_ context.Context, query EntityNeighborQuery) ([]EntityNeighbor, error) {
		if query.Direction != "incoming" || query.Relation != "extends" {
			return nil, nil
		}
		var next string
		if query.Occurrence == "root" {
			next = "d1"
		} else {
			var index int
			if _, err := fmt.Sscanf(query.Occurrence, "d%d", &index); err == nil && index < 7 {
				next = fmt.Sprintf("d%d", index+1)
			}
		}
		if next == "" {
			return nil, nil
		}
		return []EntityNeighbor{hierarchyNeighbor(chain[next], query.Occurrence, next+"-parent", "extends", "incoming")}, nil
	}
	store.counts = func(context.Context, HierarchyCountQuery) (HierarchyCount, error) {
		return HierarchyCount{Subtypes: 1}, nil
	}
	got, err = (&Service{Store: store}).TypeHierarchy(t.Context(), graphprotocol.TypeHierarchyRequest{Scope: entityTestScope(), Occurrence: "root"})
	if err != nil || len(got.Descendants.Items) != 6 || got.Descendants.Items[5].HiddenSubtypes != 1 || !got.Bounded {
		t.Fatalf("deep hierarchy=%+v err=%v", got, err)
	}
}

func TestTypeHierarchyRejectsStaleCanceledInvalidAndHiddenResults(t *testing.T) {
	request := graphprotocol.TypeHierarchyRequest{Scope: entityTestScope(), Occurrence: "root"}
	stale := &typeHierarchyTestStore{entityImpactTestStore: entityImpactTestStore{entityTestStore: entityTestStore{changed: true}}}
	if got, err := (&Service{Store: stale}).TypeHierarchy(t.Context(), request); !errors.Is(err, ErrGenerationChanged) || !reflect.DeepEqual(got, graphprotocol.TypeHierarchyResponse{}) {
		t.Fatalf("stale hierarchy=%+v err=%v", got, err)
	}
	canceled, cancel := context.WithCancel(t.Context())
	cancel()
	if got, err := (&Service{Store: &typeHierarchyTestStore{}}).TypeHierarchy(canceled, request); !errors.Is(err, context.Canceled) || !reflect.DeepEqual(got, graphprotocol.TypeHierarchyResponse{}) {
		t.Fatalf("canceled hierarchy=%+v err=%v", got, err)
	}
	if _, err := (&Service{Store: &typeHierarchyTestStore{}}).TypeHierarchy(t.Context(), graphprotocol.TypeHierarchyRequest{Scope: entityTestScope()}); !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("empty hierarchy occurrence=%v", err)
	}
}
