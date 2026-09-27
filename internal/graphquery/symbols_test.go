package graphquery

import (
	"errors"
	"slices"
	"testing"

	graphv2 "github.com/balcsida/graphnest/internal/graphartifact/v2"
	"github.com/balcsida/graphnest/internal/graphprotocol"
	"google.golang.org/protobuf/proto"
)

// Synthetic cases transcribed from CodeGraph b9ca4b7's matchesSymbol and
// lastQualifierPart documentation (src/graph/named-symbol-flow.ts).
func TestMatchesSymbol(t *testing.T) {
	node := func(kind, name, qualified, path string) *graphv2.Node {
		return &graphv2.Node{Kind: kind, Name: name, QualifiedName: qualified, Path: proto.String(path)}
	}
	for _, test := range []struct {
		name   string
		node   *graphv2.Node
		symbol string
		want   bool
	}{
		{"simple name", node("function", "run", "run", "main.ts"), "run", true},
		{"other name", node("function", "run", "run", "main.ts"), "walk", false},
		{"file basename", node("file", "product-card.liquid", "product-card.liquid", "sections/product-card.liquid"), "product-card", true},
		{"basename only for files", node("function", "product-card.liquid", "x", "a.ts"), "product-card", false},
		{"dotted qualified", node("method", "request", "Session::request", "session.py"), "Session.request", true},
		{"colon qualified", node("method", "request", "Session::request", "session.py"), "Session::request", true},
		{"qualified wrong tail", node("method", "send", "Session::send", "session.py"), "Session.request", false},
		{"file path module", node("function", "run", "run", "src/configurator/stage_apply.rs"), "stage_apply::run", true},
		{"rust crate prefix", node("function", "run", "run", "src/configurator/stage_apply.rs"), "crate::configurator::stage_apply::run", true},
		{"only rust prefixes", node("function", "run", "other", "src/x.rs"), "crate::run", false},
		{"missing container", node("function", "run", "run", "src/other.rs"), "stage_apply::run", false},
		{"slash qualified", node("function", "stage_apply", "stage_apply", "configurator/stage_apply.rs"), "configurator/stage_apply", true},
		{"erlang arity", node("function", "fn", "mod::fn/3", "mod.erl"), "mod.fn/3", true},
		{"erlang wrong arity", node("function", "fn", "mod::fn/3", "mod.erl"), "mod.fn/2", false},
		{"arity-less node keeps slash", node("function", "fn", "mod::fn", "mod/fn.erl"), "mod/fn", true},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := matchesSymbol(test.node, test.symbol); got != test.want {
				t.Fatalf("matchesSymbol(%q)=%v", test.symbol, got)
			}
		})
	}
	for symbol, want := range map[string]string{"run": "run", "Session.request": "request", "a::b::c": "c", "mod::fn/3": "fn", "a/b/": "b", "/3": "3"} {
		if got := lastQualifierPart(symbol); got != want {
			t.Fatalf("lastQualifierPart(%q)=%q want %q", symbol, got, want)
		}
	}
}

func TestGroupDefinitions(t *testing.T) {
	entity := func(id, path, qualified string) graphprotocol.Entity {
		return graphprotocol.Entity{ID: id, Fact: &graphv2.Node{Occurrence: id, QualifiedName: qualified, Path: proto.String(path)}}
	}
	matches := []graphprotocol.Entity{entity("a", "apps/one/user.service.ts", "UserService"), entity("b", "apps/two/user.service.ts", "UserService"), entity("c", "apps/one/user.service.ts", "UserService")}
	ids := func(groups [][]graphprotocol.Entity) [][]string {
		var out [][]string
		for _, g := range groups {
			var row []string
			for _, e := range g {
				row = append(row, e.ID)
			}
			out = append(out, row)
		}
		return out
	}
	for _, test := range []struct {
		file     string
		want     [][]string
		filtered string
	}{
		{"", [][]string{{"a", "c"}, {"b"}}, ""},
		{"apps/two/user.service.ts", [][]string{{"b"}}, "matched"},
		{"./two/user.service.ts", [][]string{{"b"}}, "matched"},
		{"missing.ts", [][]string{{"a", "c"}, {"b"}}, "unmatched"},
	} {
		groups, filtered := groupDefinitions(matches, test.file)
		if got := ids(groups); !slices.EqualFunc(got, test.want, slices.Equal) || filtered != test.filtered {
			t.Fatalf("file %q: groups=%v filter=%q", test.file, got, filtered)
		}
	}
}

func TestSymbolRequestsValidateBeforeQuerying(t *testing.T) {
	service := &Service{}
	for name, request := range map[string]graphprotocol.SymbolCallsRequest{
		"direction":    {Symbol: "run", Direction: "both"},
		"empty symbol": {Direction: "incoming"},
		"invalid utf8": {Symbol: "\xff", Direction: "outgoing"},
	} {
		if _, err := service.SymbolCalls(t.Context(), request); !errors.Is(err, ErrInvalidRequest) {
			t.Fatalf("%s: %v", name, err)
		}
	}
	if _, err := service.SymbolImpact(t.Context(), graphprotocol.SymbolImpactRequest{}); !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("impact empty symbol: %v", err)
	}
}
