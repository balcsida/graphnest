package scipgraph

import "testing"

func TestEnclosingPicksTheInnermostDefinition(t *testing.T) {
	outer := Occurrence{Path: "a.go", Symbol: "scip go x v1 pkg/Outer().", StartLine: 1, StartCharacter: 5, EndLine: 1, EndCharacter: 10, Roles: 1, Enclosing: &Range{StartLine: 1, EndLine: 20, EndCharacter: 1}}
	inner := Occurrence{Path: "a.go", Symbol: "scip go x v1 pkg/Inner().", StartLine: 5, StartCharacter: 5, EndLine: 5, EndCharacter: 10, Roles: 1, Enclosing: &Range{StartLine: 5, EndLine: 10, EndCharacter: 1}}
	noRange := Occurrence{Path: "a.go", Symbol: "scip go x v1 pkg/Type#", StartLine: 30, EndLine: 30, EndCharacter: 4, Roles: 1}
	local := Occurrence{Path: "a.go", Symbol: "local 1", StartLine: 6, EndLine: 6, EndCharacter: 1, Roles: 1, Local: true, Enclosing: &Range{StartLine: 6, EndLine: 9}}
	other := Occurrence{Path: "b.go", Symbol: "scip go x v1 pkg/Elsewhere().", StartLine: 1, EndLine: 1, EndCharacter: 9, Roles: 1, Enclosing: &Range{StartLine: 0, EndLine: 40}}
	definitions := Upload{Occurrences: []Occurrence{outer, inner, noRange, local, other}}.Definitions()
	if len(definitions["a.go"]) != 2 || len(definitions["b.go"]) != 1 {
		t.Fatalf("definitions=%+v", definitions)
	}
	for _, test := range []struct {
		name       string
		occurrence Occurrence
		want       string
		found      bool
	}{
		{"inside both", Occurrence{Path: "a.go", StartLine: 7, StartCharacter: 2, EndLine: 7, EndCharacter: 6}, inner.Symbol, true},
		{"inside outer only", Occurrence{Path: "a.go", StartLine: 15, EndLine: 15, EndCharacter: 3}, outer.Symbol, true},
		{"on the outer boundary", Occurrence{Path: "a.go", StartLine: 20, EndLine: 20, EndCharacter: 1}, outer.Symbol, true},
		{"past the end", Occurrence{Path: "a.go", StartLine: 20, EndLine: 20, EndCharacter: 2}, "", false},
		{"inner name token", Occurrence{Path: "a.go", Symbol: inner.Symbol, StartLine: 5, StartCharacter: 5, EndLine: 5, EndCharacter: 10}, outer.Symbol, true},
		{"other document", Occurrence{Path: "b.go", StartLine: 7, EndLine: 7, EndCharacter: 1}, other.Symbol, true},
		{"unindexed document", Occurrence{Path: "c.go", StartLine: 7, EndLine: 7, EndCharacter: 1}, "", false},
	} {
		got, found := definitions.Enclosing(test.occurrence)
		if found != test.found || got.Symbol != test.want {
			t.Fatalf("%s: enclosing=%q found=%v, want %q %v", test.name, got.Symbol, found, test.want, test.found)
		}
	}
}
