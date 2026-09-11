package graphprotocol

import graphv2 "github.com/balcsida/graphnest/internal/graphartifact/v2"

type AggregateValuesRequest struct {
	Scope  Scope    `json:"scope"`
	Values []string `json:"values"`
}

type AggregateLimitRequest struct {
	Scope Scope `json:"scope"`
	Limit int   `json:"limit"`
}

type NodeMetricsRequest struct {
	Scope      Scope  `json:"scope"`
	Occurrence string `json:"occurrence"`
}

type FileNodesRequest struct {
	Scope Scope    `json:"scope"`
	Paths []string `json:"paths"`
}

type ModuleAssignment struct {
	FilePath string `json:"file_path"`
	Module   string `json:"module"`
}

type ModuleAggregationRequest struct {
	Scope           Scope              `json:"scope"`
	Assignments     []ModuleAssignment `json:"assignments"`
	Kinds           []string           `json:"kinds"`
	MinConfidence   float64            `json:"min_confidence"`
	TopPairsPerLink int                `json:"top_pairs_per_link"`
	PairKinds       []string           `json:"pair_kinds"`
}

type UnresolvedReferencesRequest struct {
	Scope      Scope  `json:"scope"`
	Occurrence string `json:"occurrence,omitempty"`
	Path       string `json:"path,omitempty"`
	Limit      *int   `json:"limit,omitempty"`
}

type AggregateCount struct {
	ID    string `json:"id"`
	Count int    `json:"count"`
}

type AggregateCountsResponse struct {
	Counts      []AggregateCount `json:"counts"`
	Generations []Generation     `json:"generations"`
}

type AggregateNamesResponse struct {
	Names       []string     `json:"names"`
	Generations []Generation `json:"generations"`
}

type GraphStats struct {
	NodeCount       int            `json:"node_count"`
	EdgeCount       int            `json:"edge_count"`
	FileCount       int            `json:"file_count"`
	UnresolvedCount int            `json:"unresolved_count"`
	NodesByKind     map[string]int `json:"nodes_by_kind"`
	EdgesByKind     map[string]int `json:"edges_by_kind"`
	FilesByLanguage map[string]int `json:"files_by_language"`
}

type GraphStatsResponse struct {
	Stats       GraphStats   `json:"stats"`
	Generations []Generation `json:"generations"`
}

type NodeMetrics struct {
	IncomingEdgeCount int `json:"incoming_edge_count"`
	OutgoingEdgeCount int `json:"outgoing_edge_count"`
	CallCount         int `json:"call_count"`
	CallerCount       int `json:"caller_count"`
	ChildCount        int `json:"child_count"`
	Depth             int `json:"depth"`
}

type NodeMetricsResponse struct {
	Metrics     NodeMetrics  `json:"metrics"`
	Generations []Generation `json:"generations"`
}

type DependedOn struct {
	NodeID     string `json:"node_id"`
	Dependents int    `json:"dependents"`
}

type TopDependedOnResponse struct {
	Nodes       []DependedOn `json:"nodes"`
	Generations []Generation `json:"generations"`
	Boundaries  []Boundary   `json:"boundaries,omitempty"`
	Partial     bool         `json:"partial"`
}

type CallingFile struct {
	NodeID   string `json:"node_id"`
	FilePath string `json:"file_path"`
	Calls    int    `json:"calls"`
	Reaches  int    `json:"reaches"`
	Score    int    `json:"score"`
}

type TopCallingFilesResponse struct {
	Files       []CallingFile `json:"files"`
	Generations []Generation  `json:"generations"`
	Boundaries  []Boundary    `json:"boundaries,omitempty"`
	Partial     bool          `json:"partial"`
}

type ModuleLink struct {
	Source    string `json:"source"`
	Target    string `json:"target"`
	Kind      string `json:"kind"`
	Count     int    `json:"count"`
	Declared  int    `json:"declared"`
	Uncertain int    `json:"uncertain"`
}

type ModulePair struct {
	Source   string `json:"source"`
	Target   string `json:"target"`
	From     string `json:"from"`
	To       string `json:"to"`
	Count    int    `json:"count"`
	Declared int    `json:"declared"`
}

type ModuleAggregationResponse struct {
	Links       []ModuleLink `json:"links"`
	Pairs       []ModulePair `json:"pairs"`
	Generations []Generation `json:"generations"`
}

type UnresolvedReference struct {
	RepositoryID int64                        `json:"repository_id"`
	ID           string                       `json:"id"`
	Fact         *graphv2.UnresolvedReference `json:"fact"`
}

type UnresolvedReferencesResponse struct {
	References  []UnresolvedReference `json:"references"`
	Generations []Generation          `json:"generations"`
}
