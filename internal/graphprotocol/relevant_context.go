package graphprotocol

// Pointer fields retain the difference between an omitted option and an
// explicit zero or empty list.
type RelevantContextOptions struct {
	SearchLimit    *int      `json:"search_limit,omitempty"`
	TraversalDepth *int      `json:"traversal_depth,omitempty"`
	MaxNodes       *int      `json:"max_nodes,omitempty"`
	MinScore       *float64  `json:"min_score,omitempty"`
	EdgeKinds      []string  `json:"edge_kinds,omitempty"`
	NodeKinds      *[]string `json:"node_kinds,omitempty"`
	SeedNames      *[]string `json:"seed_names,omitempty"`
}

type AppliedRelevantContextOptions struct {
	SearchLimit    int      `json:"search_limit"`
	TraversalDepth int      `json:"traversal_depth"`
	MaxNodes       int      `json:"max_nodes"`
	MinScore       float64  `json:"min_score"`
	EdgeKinds      []string `json:"edge_kinds"`
	NodeKinds      []string `json:"node_kinds"`
	SeedNames      []string `json:"seed_names"`
}

type RelevantContextRequest struct {
	Scope   Scope                  `json:"scope"`
	Query   string                 `json:"query"`
	Options RelevantContextOptions `json:"options,omitempty"`
}

// RelevantContextResponse is the structured task-level graph result. Facts and
// evidence are the original producer messages; Roots refer to Entity IDs.
type RelevantContextResponse struct {
	Status      string                        `json:"status"`
	Confidence  string                        `json:"confidence"`
	EntryPoints []DiscoveryMatch              `json:"entry_points"`
	Nodes       []Entity                      `json:"nodes"`
	Edges       []Evidence                    `json:"edges"`
	Roots       []string                      `json:"roots"`
	Generations []Generation                  `json:"generations"`
	Options     AppliedRelevantContextOptions `json:"options"`
	Boundaries  []Boundary                    `json:"boundaries,omitempty"`
	Partial     bool                          `json:"partial"`
}
