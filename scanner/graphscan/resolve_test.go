package graphscan

import (
	"bytes"
	"strings"
	"testing"

	"github.com/balcsida/graphnest/internal/graphartifact"
)

func TestResolveImportedCall(t *testing.T) {
	files := []File{
		{Path: "lib.go", Module: "example/lib", Language: Go, Declarations: []Declaration{{
			LocalID: "lib.F", Name: "F", QualifiedName: "example/lib.F", Kind: "Function",
			Range: Range{Start: Point{Line: 1}, End: Point{Line: 3}},
		}}},
		{Path: "main.go", Language: Go,
			Imports: []Import{{Path: "main.go", Target: "example/lib", Alias: "lib"}},
			References: []Reference{{
				Path: "main.go", FromLocalID: "main.main", Name: "lib.F",
				Candidates: []string{"example/lib.F"}, Call: true,
				Range: Range{Start: Point{Line: 4, Column: 1}, End: Point{Line: 4, Column: 6}},
			}},
			Declarations: []Declaration{{LocalID: "main.main", Name: "main", QualifiedName: "main.main", Kind: "Function"}},
		},
	}
	got, err := Resolve(101, strings.Repeat("a", 40), files)
	if err != nil || !hasEdge(got, graphartifact.EdgeCalls, "main.main", "example/lib.F") {
		t.Fatalf("Resolve() = %#v, %v", got, err)
	}
	if !hasEdge(got, graphartifact.EdgeImports, "main.go", "lib.go") {
		t.Fatalf("Resolve() imports = %#v", got)
	}
}

func TestResolveSkipsAmbiguousAndUnresolvedReferences(t *testing.T) {
	got, err := Resolve(101, strings.Repeat("a", 40), []File{{Path: "a.go", Language: Go,
		Declarations: []Declaration{
			{LocalID: "caller", Name: "caller", QualifiedName: "caller", Kind: "Function"},
			{LocalID: "one", Name: "one", QualifiedName: "duplicate", Kind: "Function"},
			{LocalID: "two", Name: "two", QualifiedName: "duplicate", Kind: "Function", Signature: "func(int)"},
		},
		References: []Reference{
			{FromLocalID: "caller", Candidates: []string{"duplicate"}},
			{FromLocalID: "caller", Candidates: []string{"missing"}},
		},
	}})
	if err != nil || countEdges(got, graphartifact.EdgeReferences) != 0 {
		t.Fatalf("Resolve() = %#v, %v", got, err)
	}
}

func TestResolveEmitsContainmentAndHeritage(t *testing.T) {
	got, err := Resolve(101, strings.Repeat("a", 40), []File{{Path: "types.go", Language: TypeScript,
		Declarations: []Declaration{
			{LocalID: "child", Name: "Child", QualifiedName: "Child", Kind: "Class"},
			{LocalID: "parent", Name: "Parent", QualifiedName: "Parent", Kind: "Class"},
		},
		Heritage: []Heritage{{ChildLocalID: "child", Candidates: []string{"Parent"}, Kind: graphartifact.EdgeExtends}},
	}})
	if err != nil || !hasEdge(got, graphartifact.EdgeContains, "repository:101", "types.go") ||
		!hasEdge(got, graphartifact.EdgeContains, "types.go", "Child") ||
		!hasEdge(got, graphartifact.EdgeExtends, "Child", "Parent") {
		t.Fatalf("Resolve() = %#v, %v", got, err)
	}
}

func TestCanonicalUIDPrefersSCIPAndIsDeterministic(t *testing.T) {
	if got := CanonicalUID(Go, "a.go", "Function", "a.F", "func()"); got == "" || got != CanonicalUID(Go, "a.go", "Function", "a.F", "func()") {
		t.Fatalf("CanonicalUID() = %q", got)
	}
	artifact, err := Resolve(101, strings.Repeat("a", 40), []File{{Path: "a.go", Language: Go, Declarations: []Declaration{
		{LocalID: "fallback", Name: "F", QualifiedName: "a.F", Kind: "Function", Signature: "func()"},
		{LocalID: "scip", Name: "S", QualifiedName: "a.S", Kind: "Function", SCIPSymbol: "scip-go gomod example.com/a v1 S#"},
	}}})
	if err != nil || nodeUID(artifact, "a.S") != "scip-go gomod example.com/a v1 S#" || nodeUID(artifact, "a.F") != CanonicalUID(Go, "a.go", "Function", "a.F", "func()") {
		t.Fatalf("Resolve() = %#v, %v", artifact, err)
	}
}

func TestResolveDeduplicatesDeclarationsAndIsDeterministic(t *testing.T) {
	files := []File{{Path: "a.go", Language: Go, Declarations: []Declaration{
		{LocalID: "first", Name: "F", QualifiedName: "a.F", Kind: "Function"},
		{LocalID: "second", Name: "F", QualifiedName: "a.F", Kind: "Function"},
	}}}
	first, err := Resolve(101, strings.Repeat("a", 40), files)
	if err != nil || countSymbols(first, "a.F") != 1 || len(first.ContentHash) != 32 {
		t.Fatalf("Resolve() = %#v, %v", first, err)
	}
	second, err := Resolve(101, strings.Repeat("a", 40), files)
	if err != nil || !bytes.Equal(first.ContentHash, second.ContentHash) || !sameArtifact(first, second) {
		t.Fatalf("Resolve() differs: %#v, %#v, %v", first, second, err)
	}
	if err := graphartifact.Validate(first, graphartifact.Limits{}); err != nil {
		t.Fatalf("Validate() = %v", err)
	}
}

func TestResolveOrdersEqualKeyDuplicateDeclarations(t *testing.T) {
	first := Declaration{LocalID: "same", Name: "F", QualifiedName: "a.F", Kind: "Function", Range: Range{Start: Point{Line: 1}, End: Point{Line: 2}}}
	second := first
	second.Range = Range{Start: Point{Line: 3}, End: Point{Line: 4}}
	left, err := Resolve(101, strings.Repeat("a", 40), []File{{Path: "a.go", Language: Go, Declarations: []Declaration{first, second}}})
	if err != nil {
		t.Fatal(err)
	}
	right, err := Resolve(101, strings.Repeat("a", 40), []File{{Path: "a.go", Language: Go, Declarations: []Declaration{second, first}}})
	if err != nil || !sameArtifact(left, right) {
		t.Fatalf("Resolve() = %#v, %#v, %v", left, right, err)
	}
}

func TestResolveBoundsReasonsAndConfidence(t *testing.T) {
	artifact, err := Resolve(101, strings.Repeat("a", 40), []File{{Path: "a.go", Language: Go, Declarations: []Declaration{
		{LocalID: "from", Name: "from", QualifiedName: "from", Kind: "Function"},
		{LocalID: "to", Name: "to", QualifiedName: "to", Kind: "Function"},
	}, References: []Reference{{FromLocalID: "from", Candidates: []string{"to"}}}}})
	if err != nil {
		t.Fatal(err)
	}
	for _, edge := range artifact.Edges {
		if edge.Kind != graphartifact.EdgeContains && (edge.ResolutionReason == "" || len(edge.ResolutionReason) > 64 || edge.Confidence < 0 || edge.Confidence > 1) {
			t.Fatalf("edge = %#v", edge)
		}
	}
}

func TestResolveDoesNotBindBareNamesAcrossLanguages(t *testing.T) {
	artifact, err := Resolve(101, strings.Repeat("a", 40), []File{
		{Path: "caller.go", Module: "main", Language: Go,
			Declarations: []Declaration{{LocalID: "main.call", QualifiedName: "main.call", Kind: "Function"}},
			References:   []Reference{{FromLocalID: "main.call", Candidates: []string{"run"}, Call: true}}},
		{Path: "service.kt", Module: "example", Language: Kotlin,
			Declarations: []Declaration{{LocalID: "example.run", Name: "run", QualifiedName: "example.run", Kind: "Function"}}},
	})
	if err != nil || countEdges(artifact, graphartifact.EdgeCalls) != 0 {
		t.Fatalf("Resolve() = %#v, %v", artifact, err)
	}
}

func TestResolveDoesNotBindBareNamesAcrossModules(t *testing.T) {
	artifact, err := Resolve(101, strings.Repeat("a", 40), []File{
		{Path: "caller.go", Module: "main", Language: Go,
			Declarations: []Declaration{{LocalID: "main.call", QualifiedName: "main.call", Kind: "Function"}},
			References:   []Reference{{FromLocalID: "main.call", Candidates: []string{"run"}, Call: true}}},
		{Path: "service.go", Module: "service", Language: Go,
			Declarations: []Declaration{{LocalID: "service.run", Name: "run", QualifiedName: "service.run", Kind: "Function"}}},
	})
	if err != nil || countEdges(artifact, graphartifact.EdgeCalls) != 0 {
		t.Fatalf("Resolve() = %#v, %v", artifact, err)
	}
}

func hasEdge(artifact graphartifact.Artifact, kind graphartifact.EdgeKind, source, target string) bool {
	for _, edge := range artifact.Edges {
		if edge.Kind == kind && nodeName(artifact, edge.SourceUID) == source && nodeName(artifact, edge.TargetUID) == target {
			return true
		}
	}
	return false
}

func countEdges(artifact graphartifact.Artifact, kind graphartifact.EdgeKind) int {
	count := 0
	for _, edge := range artifact.Edges {
		if edge.Kind == kind {
			count++
		}
	}
	return count
}

func countSymbols(artifact graphartifact.Artifact, qualifiedName string) int {
	count := 0
	for _, node := range artifact.Nodes {
		if node.Kind == graphartifact.NodeSymbol && node.QualifiedName == qualifiedName {
			count++
		}
	}
	return count
}

func nodeUID(artifact graphartifact.Artifact, qualifiedName string) string {
	for _, node := range artifact.Nodes {
		if node.QualifiedName == qualifiedName {
			return node.UID
		}
	}
	return ""
}

func nodeName(artifact graphartifact.Artifact, uid string) string {
	for _, node := range artifact.Nodes {
		if node.UID != uid {
			continue
		}
		if node.Kind == graphartifact.NodeFile {
			return node.Path
		}
		if node.Kind == graphartifact.NodeSymbol {
			return node.QualifiedName
		}
	}
	return uid
}

func sameArtifact(left, right graphartifact.Artifact) bool {
	return left.RepositoryID == right.RepositoryID && left.Commit == right.Commit && left.Analyzer == right.Analyzer &&
		len(left.Nodes) == len(right.Nodes) && len(left.Edges) == len(right.Edges) &&
		bytes.Equal(left.ContentHash, right.ContentHash) &&
		func() bool {
			for i := range left.Nodes {
				if left.Nodes[i] != right.Nodes[i] {
					return false
				}
			}
			for i := range left.Edges {
				if left.Edges[i] != right.Edges[i] {
					return false
				}
			}
			return true
		}()
}
