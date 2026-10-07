package scipgraph

import "github.com/scip-code/scip/bindings/go/scip"

// Definitions indexes, per document, the non-local definition occurrences that
// carry an enclosing range, so a reference can be attributed to the
// declaration whose body contains it.
type Definitions map[string][]Occurrence

// Definitions collects the upload's enclosing-range definitions by document.
func (upload Upload) Definitions() Definitions {
	definitions := Definitions{}
	for _, occurrence := range upload.Occurrences {
		if occurrence.Enclosing != nil && occurrence.Definition() && !occurrence.Local {
			definitions[occurrence.Path] = append(definitions[occurrence.Path], occurrence)
		}
	}
	return definitions
}

// Definition reports whether the occurrence carries the definition role.
func (occurrence Occurrence) Definition() bool {
	return occurrence.Roles&int32(scip.SymbolRole_Definition) != 0
}

// Enclosing returns the innermost definition whose enclosing range contains
// the occurrence, in the same document. A definition does not enclose its own
// name token.
func (definitions Definitions) Enclosing(occurrence Occurrence) (Occurrence, bool) {
	var best Occurrence
	found := false
	// ponytail: linear scan per occurrence; sort by extent and binary search if a document holds thousands of definitions.
	for _, definition := range definitions[occurrence.Path] {
		if definition.Symbol == occurrence.Symbol && definition.StartLine == occurrence.StartLine && definition.StartCharacter == occurrence.StartCharacter {
			continue
		}
		if !definition.Enclosing.contains(occurrence) || found && !best.Enclosing.contains(definitionExtent(definition)) {
			continue
		}
		best, found = definition, true
	}
	return best, found
}

func definitionExtent(definition Occurrence) Occurrence {
	return Occurrence{StartLine: definition.Enclosing.StartLine, StartCharacter: definition.Enclosing.StartCharacter, EndLine: definition.Enclosing.EndLine, EndCharacter: definition.Enclosing.EndCharacter}
}

func (r *Range) contains(occurrence Occurrence) bool {
	startsBefore := r.StartLine < occurrence.StartLine || r.StartLine == occurrence.StartLine && r.StartCharacter <= occurrence.StartCharacter
	endsAfter := r.EndLine > occurrence.EndLine || r.EndLine == occurrence.EndLine && r.EndCharacter >= occurrence.EndCharacter
	return startsBefore && endsAfter
}
