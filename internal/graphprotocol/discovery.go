package graphprotocol

import graphv2 "github.com/balcsida/graphnest/internal/graphartifact/v2"

// DiscoveryConfig is request-local policy; project terms come from indexed
// repository manifests, not files from the caller's machine.
type DiscoveryConfig struct {
	ProjectTerms []string `json:"project_terms,omitempty"`
	Deprioritize []string `json:"deprioritize,omitempty"`
}

type DiscoverRequest struct {
	Scope          Scope           `json:"scope"`
	Query          string          `json:"query"`
	Limit          int             `json:"limit,omitempty"`
	CandidateLimit int             `json:"candidate_limit,omitempty"`
	Symbols        []string        `json:"symbols,omitempty"`
	Files          []string        `json:"files,omitempty"`
	Config         DiscoveryConfig `json:"config,omitempty"`
}

type DiscoveryMatch struct {
	Entity        Entity        `json:"entity"`
	File          *graphv2.File `json:"file,omitempty"`
	Fuzzy         bool          `json:"fuzzy"`
	EditDistance  int           `json:"edit_distance"`
	Score         float64       `json:"score"`
	Fields        []string      `json:"fields"`
	MatchedTerms  int           `json:"matched_terms"`
	UsageCount    int           `json:"usage_count"`
	Pinned        bool          `json:"pinned"`
	Generated     bool          `json:"generated"`
	Ambient       bool          `json:"ambient"`
	Test          bool          `json:"test"`
	Deprioritized bool          `json:"deprioritized"`
}

type DiscoverResponse struct {
	Status             string           `json:"status"`
	Confidence         string           `json:"confidence"`
	Matches            []DiscoveryMatch `json:"matches"`
	Generations        []Generation     `json:"generations"`
	CandidateLimit     int              `json:"candidate_limit"`
	CandidatesExamined int              `json:"candidates_examined"`
	CandidateTruncated bool             `json:"candidate_truncated"`
	ResultTruncated    bool             `json:"result_truncated"`
	// Discovery coverage never claims complete source or flow analysis.
	Coverage string `json:"coverage"`
}
