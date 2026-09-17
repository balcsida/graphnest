package graphartifact

import (
	"bytes"
	"strings"
	"testing"

	"github.com/balcsida/graphnest/internal/scipgraph"
)

func TestFromSCIPBuildsDeterministicExplicitGraph(t *testing.T) {
	repository := SCIPRepository{ID: 101, Commit: strings.Repeat("a", 40)}
	occurrences := []scipgraph.Occurrence{
		{Path: "b.go", Symbol: "scip go B#", EndCharacter: 1},
		{Path: "a.go", Symbol: "scip go A#", EndCharacter: 1},
		{Path: "a.go", Symbol: "scip go A#", EndCharacter: 1},
	}
	relationships := []scipgraph.Relationship{
		{Path: "a.go", Source: "scip go A#", Target: "scip go B#", Reference: true},
		{Path: "a.go", Source: "scip go A#", Target: "scip go B#", Implementation: true, TypeDefinition: true},
	}
	first, err := FromSCIP(repository, occurrences, relationships)
	if err != nil {
		t.Fatal(err)
	}
	second, err := FromSCIP(repository, occurrences, relationships)
	if err != nil || !bytes.Equal(first.ContentHash, second.ContentHash) || len(first.Nodes) != len(second.Nodes) {
		t.Fatalf("second=%#v err=%v", second, err)
	}
	var references, extends, implements, calls int
	for _, edge := range first.Edges {
		switch edge.Kind {
		case EdgeReferences:
			references++
		case EdgeExtends:
			extends++
		case EdgeImplements:
			implements++
		case EdgeCalls:
			calls++
		}
	}
	if len(first.Nodes) != 5 || references != 1 || extends != 1 || implements != 1 || calls != 0 || Validate(first, Limits{}) != nil {
		t.Fatalf("artifact=%#v", first)
	}
}

// Context/impact/trace look symbols up by the name a developer types, so the
// fallback graph must expose the SCIP descriptor name and kind rather than the
// raw symbol string, which is never what a caller has in hand.
func TestFromSCIPDerivesNameAndKindFromSymbol(t *testing.T) {
	repository := SCIPRepository{ID: 101, Commit: strings.Repeat("a", 40)}
	for _, test := range []struct {
		symbol, name, kind string
	}{
		{"scip-go gomod example.com/acme v1 `example.com/acme`/Config#", "Config", "type"},
		{"scip-go gomod example.com/acme v1 `example.com/acme`/Config#GitHubAppID.", "GitHubAppID", "field"},
		{"scip-go gomod example.com/acme v1 `example.com/acme`/LoadConfig().", "LoadConfig", "function"},
		{"scip-go gomod example.com/acme v1 `example.com/acme`/Config#Validate().", "Validate", "method"},
		{"scip-go gomod example.com/acme v1 `example.com/acme`/ParseDiff().(rev)", "rev", "parameter"},
		{"scip-go gomod example.com/acme v1 `example.com/acme`/", "example.com/acme", "namespace"},
		{"scip-typescript npm pkg 1.0.0 src/`index.ts`/Foo#[T]", "T", "type_parameter"},
		{"scip-go gomod example.com/acme v1 `example.com/acme`/Kind:", "Kind", "meta"},
		{"local 42", "42", "local"},
		// Unparsable symbols keep the raw string so they stay addressable.
		{"scip go A#", "scip go A#", ""},
	} {
		artifact, err := FromSCIP(repository, []scipgraph.Occurrence{{Path: "a.go", Symbol: test.symbol, EndCharacter: 1}}, nil)
		if err != nil {
			t.Fatalf("%s: %v", test.symbol, err)
		}
		var found *Node
		for index := range artifact.Nodes {
			if artifact.Nodes[index].Kind == NodeSymbol {
				found = &artifact.Nodes[index]
			}
		}
		if found == nil || found.QualifiedName != test.name || found.SymbolKind != test.kind || found.SCIPSymbol != test.symbol {
			t.Fatalf("%s => %#v, want name=%q kind=%q", test.symbol, found, test.name, test.kind)
		}
	}
}
