package api

type GraphDiscoveryConfig struct {
	NoMultiterm  bool     `json:"no_multiterm,omitempty"`
	ProjectTerms []string `json:"project_terms,omitempty"`
	Deprioritize []string `json:"deprioritize,omitempty"`
}

type GraphDiscoverRequest struct {
	Repo           GraphRepositorySelector `json:"repo,omitzero"`
	Branch         string                  `json:"branch,omitempty"`
	Query          string                  `json:"query"`
	Limit          int                     `json:"limit,omitempty"`
	CandidateLimit int                     `json:"candidate_limit,omitempty"`
	Symbols        []string                `json:"symbols,omitempty"`
	Files          []string                `json:"files,omitempty"`
	Config         GraphDiscoveryConfig    `json:"config,omitempty"`
}

type GraphExploreConfig struct {
	GraphDiscoveryConfig
	LineNumbers *bool `json:"line_numbers,omitempty"`
	Adaptive    *bool `json:"adaptive,omitempty"`
	Dedup       *bool `json:"dedup,omitempty"`
}

type GraphExploreRequest struct {
	Repo                GraphRepositorySelector `json:"repo,omitzero"`
	Branch              string                  `json:"branch,omitempty"`
	Query               string                  `json:"query"`
	Symbols             []string                `json:"symbols,omitempty"`
	Files               []string                `json:"files,omitempty"`
	RequiredOccurrences []string                `json:"required_occurrences,omitempty"`
	Limit               int                     `json:"limit,omitempty"`
	CandidateLimit      int                     `json:"candidate_limit,omitempty"`
	MaxFiles            int                     `json:"max_files,omitempty"`
	SourceUnits         int                     `json:"source_utf16_units,omitempty"`
	SourceBytes         int                     `json:"source_bytes,omitempty"`
	Config              GraphExploreConfig      `json:"config,omitempty"`
}

type GraphFilesRequest struct {
	Repo      GraphRepositorySelector `json:"repo,omitzero"`
	Branch    string                  `json:"branch,omitempty"`
	Directory string                  `json:"directory,omitempty"`
	Glob      string                  `json:"glob,omitempty"`
	Limit     int                     `json:"limit,omitempty"`
	Cursor    string                  `json:"cursor,omitempty"`
}

type GraphCapabilitiesRequest struct {
	Repo   GraphRepositorySelector `json:"repo,omitzero"`
	Branch string                  `json:"branch,omitempty"`
}
