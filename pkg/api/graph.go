package api

type GraphState string

const (
	GraphStateNotIndexed GraphState = "not_indexed"
	GraphStatePending    GraphState = "pending"
	GraphStateFallback   GraphState = "fallback"
	GraphStateDegraded   GraphState = "degraded"
	GraphStateReady      GraphState = "ready"
)

type GraphSource string

const (
	GraphSourceManaged  GraphSource = "managed"
	GraphSourceExternal GraphSource = "external"
)

type GraphJobState string

const (
	GraphJobStateQueued     GraphJobState = "queued"
	GraphJobStateRunning    GraphJobState = "running"
	GraphJobStateSucceeded  GraphJobState = "succeeded"
	GraphJobStateFailed     GraphJobState = "failed"
	GraphJobStateSuperseded GraphJobState = "superseded"
)

type GraphStatus struct {
	RepositoryID int64               `json:"repository_id"`
	Commit       string              `json:"commit,omitempty"`
	State        GraphState          `json:"state"`
	Source       GraphSource         `json:"source,omitempty"`
	JobState     GraphJobState       `json:"job_state,omitempty"`
	ErrorCode    string              `json:"error_code,omitempty"`
	SCIPFallback *SCIPFallbackStatus `json:"scip_fallback,omitempty"`
}

type SCIPFallbackStatus struct {
	Commit string `json:"commit"`
}
