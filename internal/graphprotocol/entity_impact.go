package graphprotocol

const (
	AnalysisFreshnessCurrent    = "current"
	AnalysisCoverageNotAssessed = "not_assessed"
)

type AnalysisState struct {
	Freshness  string `json:"freshness"`
	Coverage   string `json:"coverage"`
	Unresolved int    `json:"unresolved"`
	Complete   bool   `json:"complete"`
}

type EntityRequest struct {
	Scope      Scope  `json:"scope"`
	Occurrence string `json:"occurrence"`
}

type EntityImpactRequest struct {
	Scope      Scope  `json:"scope"`
	Occurrence string `json:"occurrence"`
	MaxDepth   *int   `json:"max_depth,omitempty"`
}

type FileEntityRequest struct {
	Scope Scope  `json:"scope"`
	Path  string `json:"path"`
}

type QualifiedNameRequest struct {
	Scope   Scope  `json:"scope"`
	Pattern string `json:"pattern"`
}

type ScopeRequest struct {
	Scope Scope `json:"scope"`
}

type EntityFilter struct {
	Paths    []string `json:"paths,omitempty"`
	Kinds    []string `json:"kinds,omitempty"`
	Exported *bool    `json:"exported,omitempty"`
}

type FilteredSubgraphRequest struct {
	Scope        Scope        `json:"scope"`
	Filter       EntityFilter `json:"filter"`
	IncludeEdges *bool        `json:"include_edges,omitempty"`
}

type BlastFile struct {
	File    string `json:"file"`
	Symbols int    `json:"symbols"`
	Test    bool   `json:"test"`
}

type BlastSummary struct {
	Direct     int         `json:"direct"`
	WithinHops int         `json:"within_hops"`
	Hops       int         `json:"hops"`
	Files      int         `json:"files"`
	TestFiles  int         `json:"test_files"`
	Routes     int         `json:"routes"`
	TopFiles   []BlastFile `json:"top_files"`
}

type SubgraphResponse struct {
	Status      string         `json:"status"`
	Roots       []string       `json:"roots"`
	Entities    []Entity       `json:"entities"`
	Edges       []Evidence     `json:"edges"`
	Blast       *BlastSummary  `json:"blast,omitempty"`
	Analysis    *AnalysisState `json:"analysis,omitempty"`
	Generations []Generation   `json:"generations"`
	Boundaries  []Boundary     `json:"boundaries,omitempty"`
	Partial     bool           `json:"partial"`
}

type Usage struct {
	Entity Entity   `json:"entity"`
	Edge   Evidence `json:"edge"`
}

type UsagesResponse struct {
	Status      string         `json:"status"`
	Usages      []Usage        `json:"usages"`
	Analysis    *AnalysisState `json:"analysis,omitempty"`
	Generations []Generation   `json:"generations"`
	Boundaries  []Boundary     `json:"boundaries,omitempty"`
	Partial     bool           `json:"partial"`
}

type ModuleDirectory struct {
	Directory string   `json:"directory"`
	Files     []string `json:"files"`
}

type ModuleStructureResponse struct {
	Modules     []ModuleDirectory `json:"modules"`
	Analysis    *AnalysisState    `json:"analysis,omitempty"`
	Generations []Generation      `json:"generations"`
	Boundaries  []Boundary        `json:"boundaries,omitempty"`
	Partial     bool              `json:"partial"`
}
