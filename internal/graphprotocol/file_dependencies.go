package graphprotocol

type FileDependencyRequest struct {
	Scope         Scope    `json:"scope"`
	Paths         []string `json:"paths,omitempty"`
	MinConfidence float64  `json:"min_confidence,omitempty"`
	Limit         int      `json:"limit,omitempty"`
}

type FileDependencyPair struct {
	Source     string `json:"source"`
	Target     string `json:"target"`
	References int    `json:"references"`
}

type FileDependencyCount struct {
	Path       string `json:"path"`
	Dependents int    `json:"dependents,omitempty"`
	Reaches    int    `json:"reaches,omitempty"`
	References int    `json:"references,omitempty"`
}

type FileDependencyResponse struct {
	Files       []string              `json:"files,omitempty"`
	Counts      []FileDependencyCount `json:"counts,omitempty"`
	Pairs       []FileDependencyPair  `json:"pairs,omitempty"`
	Cycles      [][]string            `json:"cycles,omitempty"`
	Generations []Generation          `json:"generations"`
	Boundaries  []Boundary            `json:"boundaries,omitempty"`
	Partial     bool                  `json:"partial"`
}

type AffectedTestsRequest struct {
	Scope        Scope    `json:"scope"`
	ChangedFiles []string `json:"changed_files"`
	MaxDepth     int      `json:"max_depth,omitempty"`
	TestGlob     string   `json:"test_glob,omitempty"`
	Limit        int      `json:"limit,omitempty"`
}

type AffectedTestsResponse struct {
	ChangedFiles             []string     `json:"changed_files"`
	AffectedTests            []string     `json:"affected_tests"`
	TotalDependentsTraversed int          `json:"total_dependents_traversed"`
	Generations              []Generation `json:"generations"`
	Boundaries               []Boundary   `json:"boundaries,omitempty"`
	Partial                  bool         `json:"partial"`
}
