package api

// GraphSymbolCallsRequest lists the callers or callees of every definition of
// a symbol name. File narrows same-named definitions by path or path suffix.
// Limit bounds each definition's list (default 20, clamped to 1-100).
type GraphSymbolCallsRequest struct {
	Repo   GraphRepositorySelector `json:"repo,omitzero"`
	Branch string                  `json:"branch,omitempty"`
	Symbol string                  `json:"symbol"`
	File   string                  `json:"file,omitempty"`
	Limit  int                     `json:"limit,omitempty"`
}

// GraphSymbolImpactRequest is the impact radius of every definition of a
// symbol name. Depth defaults to 2 and is clamped to 1-10.
type GraphSymbolImpactRequest struct {
	Repo   GraphRepositorySelector `json:"repo,omitzero"`
	Branch string                  `json:"branch,omitempty"`
	Symbol string                  `json:"symbol"`
	File   string                  `json:"file,omitempty"`
	Depth  int                     `json:"depth,omitempty"`
}
