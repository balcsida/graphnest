package graphartifact

import graphv2 "github.com/balcsida/graphnest/internal/graphartifact/v2"

// Relationship is the common wire, API-name and display contract. Every relation
// is directed source -> target; Incoming labels describe the reverse view.
type Relationship struct {
	Kind                     EdgeKind
	Name, Outgoing, Incoming string
}

var relationships = [...]Relationship{
	{EdgeContains, "contains", "contains", "contained by"},
	{EdgeImports, "imports", "imports", "imported by"},
	{EdgeReferences, "references", "references", "referenced by"},
	{EdgeCalls, "calls", "calls", "called by"},
	{EdgeExtends, "extends", "extends", "extended by"},
	{EdgeImplements, "implements", "implements", "implemented by"},
	{EdgeExports, "exports", "exports", "exported by"},
	{EdgeTypeOf, "type_of", "has type", "type of"},
	{EdgeReturns, "returns", "returns", "returned by"},
	{EdgeInstantiates, "instantiates", "instantiates", "instantiated by"},
	{EdgeOverrides, "overrides", "overrides", "overridden by"},
	{EdgeDecorates, "decorates", "decorates", "decorated by"},
	{EdgeNavigates, "navigates", "navigates to", "navigated from"},
}

// Relationships returns a copy so callers cannot mutate the validation registry.
func Relationships() []Relationship { return append([]Relationship(nil), relationships[:]...) }

// ParseRelationship validates an API or producer name without aliases.
func ParseRelationship(name string) (Relationship, bool) {
	for _, r := range relationships {
		if r.Name == name {
			return r, true
		}
	}
	return Relationship{}, false
}

// RelationshipFromWire checks the full enum width before narrowing.
func RelationshipFromWire(kind graphv2.EdgeKind) (Relationship, bool) {
	if kind < 1 || int64(kind) > int64(len(relationships)) {
		return Relationship{}, false
	}
	return relationships[int(kind)-1], true
}

func (r Relationship) WireKind() graphv2.EdgeKind { return graphv2.EdgeKind(r.Kind) }

func validV2NodeKind(kind string) bool {
	switch kind {
	case "repository", "symbol", "file", "module", "class", "struct", "interface", "trait", "protocol", "function", "method", "property", "field", "variable", "constant", "enum", "enum_member", "type_alias", "namespace", "parameter", "import", "export", "route", "component", "union":
		return true
	}
	return false
}
