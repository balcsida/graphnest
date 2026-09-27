package graphprotocol

// SymbolCallsRequest addresses a symbol by name, the way CodeGraph's callers
// and callees tools do. Direction is "incoming" (callers) or "outgoing"
// (callees). File narrows same-named definitions by path or path suffix.
type SymbolCallsRequest struct {
	Scope     Scope  `json:"scope"`
	Symbol    string `json:"symbol"`
	File      string `json:"file,omitempty"`
	Direction string `json:"direction"`
	Limit     int    `json:"limit,omitempty"`
}

// SymbolImpactRequest is the name-addressed impact radius.
type SymbolImpactRequest struct {
	Scope  Scope  `json:"scope"`
	Symbol string `json:"symbol"`
	File   string `json:"file,omitempty"`
	Depth  int    `json:"depth,omitempty"`
}

// SymbolRelated is one neighbor with the first edge that reached it.
type SymbolRelated struct {
	Entity Entity   `json:"entity"`
	Edge   Evidence `json:"edge"`
}

// SymbolDefinition is one distinct definition: every match sharing a file and
// qualified name (same-file overloads stay together).
type SymbolDefinition struct {
	Definitions []Entity        `json:"definitions"`
	Related     []SymbolRelated `json:"related,omitempty"`
	Truncated   bool            `json:"truncated,omitempty"`
	// Impact fields; empty for callers and callees.
	Entities []Entity   `json:"entities,omitempty"`
	Edges    []Evidence `json:"edges,omitempty"`
}

// SymbolResponse reports status ok or not_found. FileFilter is "matched" when
// File narrowed the definitions and "unmatched" when no definition matched
// File, so every definition is returned instead.
type SymbolResponse struct {
	Status      string             `json:"status"`
	FileFilter  string             `json:"file_filter,omitempty"`
	Definitions []SymbolDefinition `json:"definitions"`
	Generations []Generation       `json:"generations"`
	Boundaries  []Boundary         `json:"boundaries,omitempty"`
	Partial     bool               `json:"partial"`
}
