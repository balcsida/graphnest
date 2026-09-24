//go:build integration

package postgres

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/balcsida/graphnest/internal/graphartifact"
	graphv2 "github.com/balcsida/graphnest/internal/graphartifact/v2"
	"github.com/balcsida/graphnest/internal/graphprotocol"
	"github.com/balcsida/graphnest/internal/graphquery"
	"google.golang.org/protobuf/proto"
)

type hierarchyCapture struct {
	Nodes          []positiveNode        `json:"nodes"`
	Edges          []positiveEdge        `json:"edges"`
	Files          []hierarchyFile       `json:"files"`
	UnresolvedRefs []hierarchyUnresolved `json:"unresolvedRefs"`
}

type hierarchyFile struct {
	Path        string          `json:"path"`
	ContentHash string          `json:"content_hash"`
	Language    string          `json:"language"`
	Size        int64           `json:"size"`
	ModifiedAt  float64         `json:"modified_at"`
	IndexedAt   int64           `json:"indexed_at"`
	NodeCount   int64           `json:"node_count"`
	Errors      json.RawMessage `json:"errors"`
	Generated   int             `json:"generated"`
}

type hierarchyUnresolved struct {
	ID         int64           `json:"id"`
	Source     string          `json:"from_node_id"`
	Name       string          `json:"reference_name"`
	Kind       string          `json:"reference_kind"`
	Line       *int32          `json:"line"`
	Column     *int32          `json:"col"`
	Candidates json.RawMessage `json:"candidates"`
	Path       *string         `json:"file_path"`
	Language   *string         `json:"language"`
	Status     *string         `json:"status"`
	NameTail   *string         `json:"name_tail"`
}

func loadHierarchyCapture(t *testing.T) (*graphv2.Artifact, hierarchyCapture) {
	t.Helper()
	path := "../../test/fixtures/codegraph/type-hierarchy-facts.json"
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := fmt.Sprintf("%x", sha256.Sum256(raw)); got != "652e54bf26e606eb35ea65004b13fd6a5a4572c43a91b6385753d5dab959c904" {
		t.Fatalf("hierarchy fact capture hash=%s", got)
	}
	var capture hierarchyCapture
	if err = json.Unmarshal(raw, &capture); err != nil {
		t.Fatal(err)
	}
	if len(capture.Nodes) != 37 || len(capture.Edges) != 53 || len(capture.Files) != 3 || len(capture.UnresolvedRefs) != 5 {
		t.Fatalf("incomplete hierarchy capture nodes=%d edges=%d files=%d unresolved=%d", len(capture.Nodes), len(capture.Edges), len(capture.Files), len(capture.UnresolvedRefs))
	}
	for name, want := range map[string]string{
		"go.mod":         "b54d66f2b7960919ede3cfc12752e99416429a7f3d2b8a8e3d5d1ba31042a94a",
		"src/shapes.ts":  "f9354f8bfd3e6068f91aae9abfa83e6c3a68537db84c3a8f34a9369777ad282b",
		"src/plugins.ts": "e36b5726c5596007b9cb678adb54d807426dd5b5de2d6153c8a75d6aab6647b0",
		"src/clock.go":   "96137f6407c45c31afe9a498b3d333f8cfc63ea33e1b6a704f57f184fa2748a3",
	} {
		data, readErr := os.ReadFile(filepath.Join("../../test/fixtures/codegraph/type-hierarchy-source", name))
		if readErr != nil || fmt.Sprintf("%x", sha256.Sum256(data)) != want {
			t.Fatalf("hierarchy source %s hash=%x err=%v", name, sha256.Sum256(data), readErr)
		}
	}

	artifact := storageV2Artifact()
	artifact.ContentHash = nil
	artifact.Commit = strings.Repeat("7", 40)
	artifact.Nodes, artifact.Edges, artifact.Files, artifact.Unresolved, artifact.Diagnostics = nil, nil, nil, nil, nil
	for _, node := range capture.Nodes {
		path := node.FilePath
		artifact.Nodes = append(artifact.Nodes, &graphv2.Node{
			SourceId: node.ID, Occurrence: node.ID, Kind: node.Kind, Name: node.Name,
			QualifiedName: node.QualifiedName, Path: &path, Language: node.Language,
			Location: &graphv2.Location{
				Start: positivePoint(proto.Int32(node.StartLine), proto.Int32(node.StartColumn)),
				End:   positivePoint(proto.Int32(node.EndLine), proto.Int32(node.EndColumn)),
			},
			Documentation: node.Documentation, Signature: node.Signature, Visibility: node.Visibility,
			IsExported: proto.Bool(node.IsExported != 0), IsAsync: proto.Bool(node.IsAsync != 0),
			IsStatic: proto.Bool(node.IsStatic != 0), IsAbstract: proto.Bool(node.IsAbstract != 0),
			Decorators: positiveStringList(t, node.Decorators), TypeParameters: positiveStringList(t, node.TypeParameters),
			ReturnType: node.ReturnType, UpdatedAt: proto.Int64(node.UpdatedAt),
		})
	}
	for _, edge := range capture.Edges {
		relation, ok := graphartifact.ParseRelationship(edge.Kind)
		if !ok {
			t.Fatal(edge.Kind)
		}
		fact := &graphv2.Edge{
			SourceId: strconv.FormatInt(edge.ID, 10), Occurrence: "edge:" + strconv.FormatInt(edge.ID, 10),
			Source: edge.Source, Target: edge.Target, Kind: relation.WireKind(), Provenance: edge.Provenance,
		}
		if point := positivePoint(edge.Line, edge.Col); point != nil {
			fact.Location = &graphv2.Location{Start: point}
		}
		if edge.Metadata != nil {
			fact.Extensions = []*graphv2.Extension{{Namespace: "codegraph.edge-metadata", Json: []byte(*edge.Metadata)}}
			var metadata struct {
				Confidence *float64 `json:"confidence"`
				ResolvedBy *string  `json:"resolvedBy"`
			}
			if err = json.Unmarshal([]byte(*edge.Metadata), &metadata); err != nil {
				t.Fatal(err)
			}
			fact.Confidence, fact.ResolutionReason = metadata.Confidence, metadata.ResolvedBy
		}
		artifact.Edges = append(artifact.Edges, fact)
	}
	for _, file := range capture.Files {
		artifact.Files = append(artifact.Files, &graphv2.File{
			Path: file.Path, ContentHash: file.ContentHash, Language: file.Language, Size: file.Size,
			ModifiedAt: proto.Int64(int64(file.ModifiedAt)), IndexedAt: proto.Int64(file.IndexedAt),
			NodeCount: proto.Int64(file.NodeCount), Generated: proto.Bool(file.Generated != 0),
			Errors: positiveExtension("codegraph.extraction-errors", file.Errors),
		})
	}
	for _, unresolved := range capture.UnresolvedRefs {
		fact := &graphv2.UnresolvedReference{
			SourceId: strconv.FormatInt(unresolved.ID, 10), Occurrence: "unresolved:" + strconv.FormatInt(unresolved.ID, 10),
			Source: unresolved.Source, Name: unresolved.Name, Kind: unresolved.Kind,
			Candidates: positiveStringList(t, unresolved.Candidates), Path: unresolved.Path, Language: unresolved.Language,
			Status: unresolved.Status, NameTail: unresolved.NameTail,
		}
		if point := positivePoint(unresolved.Line, unresolved.Column); point != nil {
			fact.Location = &graphv2.Location{Path: unresolved.Path, Start: point}
		}
		artifact.Unresolved = append(artifact.Unresolved, fact)
	}
	return artifact, capture
}

func captureNode(t *testing.T, artifact *graphv2.Artifact, name, kind string) *graphv2.Node {
	t.Helper()
	var found *graphv2.Node
	for _, node := range artifact.Nodes {
		if node.Name == name && node.Kind == kind {
			if found != nil {
				t.Fatalf("ambiguous captured node %s/%s", name, kind)
			}
			found = node
		}
	}
	if found == nil {
		t.Fatalf("missing captured node %s/%s", name, kind)
	}
	return found
}

func hierarchyNames(entries []graphprotocol.HierarchyEntry) []string {
	result := make([]string, len(entries))
	for i, entry := range entries {
		result[i] = entry.Entity.Fact.Name
	}
	return result
}

func TestGraphTypeHierarchyProducerOracle(t *testing.T) {
	artifact, _ := loadHierarchyCapture(t)
	store, service, scope := fileDependencyService(t, artifact)
	originalNodes, originalEdges := entityImpactFacts(artifact)

	all, err := service.Entities(t.Context(), graphprotocol.EntitiesRequest{Scope: scope, Limit: 100})
	if err != nil || len(all.Entities) != 37 {
		t.Fatalf("complete nodes=%d err=%v", len(all.Entities), err)
	}
	assertEntityFacts(t, all.Entities, originalNodes)
	occurrences := make([]string, len(artifact.Nodes))
	for i, node := range artifact.Nodes {
		occurrences[i] = node.Occurrence
	}
	slices.Sort(occurrences)
	evidence, err := store.QueryAnalysisEvidence(t.Context(), graphquery.AnalysisEvidenceQuery{Snapshot: graphquery.QuerySnapshot{RepositoryID: scope.SelectedRepositoryID, UploadID: all.Generations[0].UploadID, Commit: artifact.Commit}, Occurrences: occurrences, Limit: 54})
	if err != nil || len(evidence) != 53 {
		t.Fatalf("complete edges=%d err=%v", len(evidence), err)
	}
	for _, edge := range evidence {
		// Store rows carry the internal repository ID; the service maps it to the public one.
		if edge.RepositoryID != scope.SelectedRepositoryID || !proto.Equal(edge.Fact, originalEdges[edge.Fact.GetOccurrence()]) {
			t.Fatalf("lost evidence fact=%+v", edge)
		}
	}
	if all.Generations[0].NodeCount != 37 || all.Generations[0].EdgeCount != 53 || all.Generations[0].UnresolvedCount != 5 {
		t.Fatalf("complete generation=%+v", all.Generations[0])
	}

	for _, test := range []struct {
		name, kind           string
		ancestors            []string
		descendants          []string
		direct, implementers int
		polymorphic          bool
	}{
		{"Tile", "class", []string{"Square", "Shape", "Drawable"}, nil, 0, 0, false},
		{"Shape", "class", []string{"Drawable"}, []string{"Square", "Tile"}, 1, 0, false},
		{"Plugin", "interface", nil, []string{"AlphaPlugin", "BravoPlugin", "CharliePlugin", "DeltaPlugin", "EchoPlugin", "FoxtrotPlugin", "GolfPlugin", "HotelPlugin", "IndiaPlugin"}, 9, 9, true},
		{"Clock", "interface", nil, []string{"Fixed", "System"}, 2, 2, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			focus := captureNode(t, artifact, test.name, test.kind)
			got, callErr := service.TypeHierarchy(t.Context(), graphprotocol.TypeHierarchyRequest{Scope: scope, Occurrence: focus.Occurrence})
			if callErr != nil || !slices.Equal(hierarchyNames(got.Ancestors.Items), test.ancestors) || !slices.Equal(hierarchyNames(got.Descendants.Items), test.descendants) || got.DirectSubtypes != test.direct || got.DirectImplementers != test.implementers || got.Polymorphic != test.polymorphic || got.Bounded {
				t.Fatalf("hierarchy ancestors=%v descendants=%v direct=%d/%d bounded=%v err=%v", hierarchyNames(got.Ancestors.Items), hierarchyNames(got.Descendants.Items), got.DirectSubtypes, got.DirectImplementers, got.Bounded, callErr)
			}
			if got.Analysis == nil || got.Analysis.Unresolved != 5 || got.Analysis.Coverage != graphprotocol.AnalysisCoverageNotAssessed || !got.Analysis.Complete {
				t.Fatalf("hierarchy analysis=%+v", got.Analysis)
			}
			for _, entry := range append(slices.Clone(got.Ancestors.Items), got.Descendants.Items...) {
				if originalEdges[entry.Edge.Fact.Occurrence] == nil || !proto.Equal(entry.Edge.Fact, originalEdges[entry.Edge.Fact.Occurrence]) {
					t.Fatalf("lost hierarchy edge evidence=%+v", entry.Edge.Fact)
				}
			}
			if test.name == "Clock" {
				for _, entry := range got.Descendants.Items {
					if !entry.Synthesized || entry.Via != "go-implements" || entry.RegisteredAt == "" || entry.Edge.Fact.GetProvenance() != "heuristic" {
						t.Fatalf("implicit hierarchy=%+v", entry)
					}
				}
			}
		})
	}

	square := captureNode(t, artifact, "Square", "class")
	squareHierarchy, err := service.TypeHierarchy(t.Context(), graphprotocol.TypeHierarchyRequest{Scope: scope, Occurrence: square.Occurrence})
	if err != nil || len(squareHierarchy.DerivedOverrides) != 1 || squareHierarchy.DerivedOverrides[0].Member.Fact.Name != "draw" || squareHierarchy.DerivedOverrides[0].BaseMember.Fact.QualifiedName != "Shape::draw" || !squareHierarchy.DerivedOverrides[0].SignatureUncertain {
		t.Fatalf("square overrides=%+v err=%v", squareHierarchy.DerivedOverrides, err)
	}
	tile := captureNode(t, artifact, "Tile", "class")
	tileHierarchy, err := service.TypeHierarchy(t.Context(), graphprotocol.TypeHierarchyRequest{Scope: scope, Occurrence: tile.Occurrence})
	if err != nil || len(tileHierarchy.DerivedOverrides) != 0 {
		t.Fatalf("tile overrides=%+v err=%v", tileHierarchy.DerivedOverrides, err)
	}
}

func TestGraphTypeHierarchyOriginalServiceAndBaseOracle(t *testing.T) {
	raw, err := os.ReadFile(os.Getenv("GRAPHNEST_TEST_CODEGRAPH_V2_FIXTURE"))
	if err != nil {
		t.Fatal(err)
	}
	artifact, err := graphartifact.ParseV2(raw, graphartifact.Limits{})
	if err != nil {
		t.Fatal(err)
	}
	verifyAggregateSources(t)
	_, service, scope := fileDependencyService(t, artifact)
	base := captureNode(t, artifact, "Base", "class")
	serviceType := captureNode(t, artifact, "Service", "class")
	baseHierarchy, err := service.TypeHierarchy(t.Context(), graphprotocol.TypeHierarchyRequest{Scope: scope, Occurrence: base.Occurrence})
	if err != nil || !slices.Equal(hierarchyNames(baseHierarchy.Descendants.Items), []string{"Service"}) || baseHierarchy.DirectSubtypes != 1 || baseHierarchy.Bounded {
		t.Fatalf("Base descendants=%+v err=%v", baseHierarchy, err)
	}
	entry := baseHierarchy.Descendants.Items[0]
	if entry.Depth != 1 || entry.ParentID != baseHierarchy.Focus.ID || entry.Relation != "extends" || entry.Edge.Fact.GetLocation().GetStart().GetLine() != 5 || entry.Edge.Fact.GetLocation().GetStart().GetCharacter() != 29 || entry.Edge.Fact.GetConfidence() != 0.9 || entry.Edge.Fact.GetResolutionReason() != "exact-match" {
		t.Fatalf("Base descendant evidence=%+v", entry)
	}
	serviceHierarchy, err := service.TypeHierarchy(t.Context(), graphprotocol.TypeHierarchyRequest{Scope: scope, Occurrence: serviceType.Occurrence})
	if err != nil || !slices.Equal(hierarchyNames(serviceHierarchy.Ancestors.Items), []string{"Base", "Greeter"}) || len(serviceHierarchy.DerivedOverrides) != 1 || serviceHierarchy.DerivedOverrides[0].Member.Fact.QualifiedName != "Service::greet" || serviceHierarchy.DerivedOverrides[0].BaseMember.Fact.QualifiedName != "Base::greet" {
		t.Fatalf("Service hierarchy=%+v err=%v", serviceHierarchy, err)
	}
	relations, err := service.TypeRelations(t.Context(), graphprotocol.TypeRelationsRequest{Scope: scope, Occurrence: serviceType.Occurrence})
	if err != nil || relations.TypeKnowledge != graphprotocol.TypeKnowledgeUnknown || len(relations.Types) != 0 || len(relations.ReferencedTypes) != 0 {
		t.Fatalf("portable producer type relations=%+v err=%v", relations, err)
	}
}

func TestGraphTypeRelationsSyntheticDirectionAndOccurrences(t *testing.T) {
	artifact := storageV2Artifact()
	artifact.ContentHash = nil
	artifact.Commit = strings.Repeat("8", 40)
	artifact.Nodes, artifact.Edges, artifact.Files, artifact.Unresolved, artifact.Diagnostics = nil, nil, nil, nil, nil
	for _, value := range []struct{ occurrence, name, kind string }{
		{"root", "makeService", "function"}, {"service", "Service", "class"}, {"greeter", "Greeter", "interface"},
		{"user", "serviceVariable", "variable"}, {"returner", "returnService", "function"}, {"base", "baseMethod", "method"}, {"helper", "helper", "function"},
	} {
		artifact.Nodes = append(artifact.Nodes, &graphv2.Node{SourceId: value.occurrence, Occurrence: value.occurrence, Name: value.name, QualifiedName: value.name, Kind: value.kind})
	}
	for i, value := range []struct{ source, target, kind string }{
		{"root", "service", "type_of"}, {"root", "service", "type_of"}, {"root", "greeter", "returns"},
		{"user", "root", "type_of"}, {"returner", "root", "returns"}, {"root", "service", "references"},
		{"root", "helper", "references"}, {"root", "base", "overrides"},
	} {
		relation, _ := graphartifact.ParseRelationship(value.kind)
		artifact.Edges = append(artifact.Edges, &graphv2.Edge{SourceId: strconv.Itoa(i + 1), Occurrence: fmt.Sprintf("synthetic-edge-%d", i+1), Source: value.source, Target: value.target, Kind: relation.WireKind()})
	}
	_, service, scope := fileDependencyService(t, artifact)
	got, err := service.TypeRelations(t.Context(), graphprotocol.TypeRelationsRequest{Scope: scope, Occurrence: "root"})
	if err != nil || got.TypeKnowledge != graphprotocol.TypeKnowledgeRecorded || len(got.Types) != 2 || len(got.Types[0].Edges) != 2 || len(got.Users) != 1 || len(got.Returners) != 1 || len(got.ReferencedTypes) != 1 || len(got.RecordedOverrides) != 1 {
		t.Fatalf("synthetic type relations=%+v err=%v", got, err)
	}
	if got.Types[0].Edges[0].Fact.Occurrence == got.Types[0].Edges[1].Fact.Occurrence || got.Users[0].Edges[0].Fact.Source != "user" || got.Returners[0].Edges[0].Fact.Source != "returner" || got.ReferencedTypes[0].Entity.Fact.Occurrence != "service" {
		t.Fatalf("synthetic direction/evidence=%+v", got)
	}
}

func TestGraphHierarchyChildCountScopeAndBounds(t *testing.T) {
	artifact := storageV2Artifact()
	artifact.ContentHash = nil
	artifact.Commit = strings.Repeat("9", 40)
	artifact.Nodes, artifact.Edges, artifact.Files, artifact.Unresolved, artifact.Diagnostics = nil, nil, nil, nil, nil
	for _, occurrence := range []string{"base", "both", "impl", "sub", "self"} {
		artifact.Nodes = append(artifact.Nodes, &graphv2.Node{SourceId: occurrence, Occurrence: occurrence, Name: occurrence, QualifiedName: occurrence, Kind: "class"})
	}
	for i, value := range []struct{ source, target, kind string }{
		{"both", "base", "implements"}, {"both", "base", "extends"}, {"impl", "base", "implements"},
		{"impl", "base", "implements"}, {"sub", "base", "extends"}, {"base", "base", "extends"}, {"self", "base", "references"},
	} {
		relation, _ := graphartifact.ParseRelationship(value.kind)
		artifact.Edges = append(artifact.Edges, &graphv2.Edge{SourceId: strconv.Itoa(i + 1), Occurrence: fmt.Sprintf("count-edge-%d", i+1), Source: value.source, Target: value.target, Kind: relation.WireKind()})
	}
	store, service, scope := fileDependencyService(t, artifact)
	all, err := service.Entities(t.Context(), graphprotocol.EntitiesRequest{Scope: scope, Limit: 10})
	if err != nil || len(all.Generations) != 1 {
		t.Fatalf("generations=%+v err=%v", all.Generations, err)
	}
	snapshot := graphquery.QuerySnapshot{RepositoryID: scope.SelectedRepositoryID, UploadID: all.Generations[0].UploadID, Commit: artifact.Commit}

	// Distinct subtypes only (extends rows list first, as upstream sortLevel): duplicate occurrences count once, extends wins over
	// implements, self edges and other relations are not subtypes.
	got, err := store.CountHierarchyChildren(t.Context(), graphquery.HierarchyCountQuery{Snapshot: snapshot, Occurrence: "base"})
	if err != nil || got != (graphquery.HierarchyCount{Subtypes: 3, Implementers: 1}) {
		t.Fatalf("base count=%+v err=%v", got, err)
	}
	hierarchy, err := service.TypeHierarchy(t.Context(), graphprotocol.TypeHierarchyRequest{Scope: scope, Occurrence: "base"})
	if err != nil || !slices.Equal(hierarchyNames(hierarchy.Descendants.Items), []string{"both", "sub", "impl"}) || hierarchy.DirectSubtypes != 3 || hierarchy.DirectImplementers != 1 {
		t.Fatalf("base hierarchy descendants=%v direct=%d/%d err=%v", hierarchyNames(hierarchy.Descendants.Items), hierarchy.DirectSubtypes, hierarchy.DirectImplementers, err)
	}
	for _, entry := range hierarchy.Descendants.Items {
		if entry.Entity.Fact.Occurrence == "both" && entry.Relation != "extends" {
			t.Fatalf("duplicate relation preference=%+v", entry)
		}
	}
	for name, query := range map[string]graphquery.HierarchyCountQuery{
		"leaf":       {Snapshot: snapshot, Occurrence: "sub"},
		"missing":    {Snapshot: snapshot, Occurrence: "missing"},
		"commit":     {Snapshot: graphquery.QuerySnapshot{RepositoryID: snapshot.RepositoryID, UploadID: snapshot.UploadID, Commit: strings.Repeat("8", 40)}, Occurrence: "base"},
		"upload":     {Snapshot: graphquery.QuerySnapshot{RepositoryID: snapshot.RepositoryID, UploadID: snapshot.UploadID + 1000, Commit: snapshot.Commit}, Occurrence: "base"},
		"repository": {Snapshot: graphquery.QuerySnapshot{RepositoryID: snapshot.RepositoryID + 1000, UploadID: snapshot.UploadID, Commit: snapshot.Commit}, Occurrence: "base"},
	} {
		if got, err := store.CountHierarchyChildren(t.Context(), query); err != nil || got != (graphquery.HierarchyCount{}) {
			t.Fatalf("%s count=%+v err=%v", name, got, err)
		}
	}
	for name, query := range map[string]graphquery.HierarchyCountQuery{
		"occurrence": {Snapshot: snapshot},
		"snapshot":   {Snapshot: graphquery.QuerySnapshot{RepositoryID: snapshot.RepositoryID, UploadID: 0, Commit: snapshot.Commit}, Occurrence: "base"},
	} {
		if _, err := store.CountHierarchyChildren(t.Context(), query); !errors.Is(err, graphquery.ErrInvalidRequest) {
			t.Fatalf("%s invalid count err=%v", name, err)
		}
	}
	canceled, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := store.CountHierarchyChildren(canceled, graphquery.HierarchyCountQuery{Snapshot: snapshot, Occurrence: "base"}); err == nil {
		t.Fatal("canceled count succeeded")
	}

	// The descendant walk needs one row of lookahead beyond its 400-row page.
	neighbors := graphquery.EntityNeighborQuery{Snapshot: snapshot, Occurrence: "base", Relation: "extends", Direction: "incoming", Limit: 401}
	if rows, err := store.EntityNeighbors(t.Context(), neighbors); err != nil || len(rows) != 3 { // both, sub and the raw self edge
		t.Fatalf("401 lookahead rows=%d err=%v", len(rows), err)
	}
	neighbors.Limit = 402
	if _, err := store.EntityNeighbors(t.Context(), neighbors); !errors.Is(err, graphquery.ErrInvalidRequest) {
		t.Fatalf("402 lookahead err=%v", err)
	}
}
