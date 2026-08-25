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
