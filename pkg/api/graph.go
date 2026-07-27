package api

type GraphStatus struct {
	RepositoryID int64               `json:"repository_id"`
	Commit       string              `json:"commit,omitempty"`
	State        string              `json:"state"`
	Source       string              `json:"source,omitempty"`
	JobState     string              `json:"job_state,omitempty"`
	ErrorCode    string              `json:"error_code,omitempty"`
	SCIPFallback *SCIPFallbackStatus `json:"scip_fallback,omitempty"`
}

type SCIPFallbackStatus struct {
	Commit string `json:"commit"`
}
