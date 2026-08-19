package api

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
)

var errInvalidGraphRepositorySelector = errors.New("repo must be a positive integer or non-empty string")

type GraphRepositorySelector struct {
	ID   int64
	Name string
}

func (selector *GraphRepositorySelector) UnmarshalJSON(data []byte) error {
	*selector = GraphRepositorySelector{}
	var value any
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	if err := decoder.Decode(&value); err != nil {
		return errInvalidGraphRepositorySelector
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return errInvalidGraphRepositorySelector
	}
	switch value := value.(type) {
	case json.Number:
		id, err := value.Int64()
		if err == nil && id > 0 {
			selector.ID = id
			return nil
		}
	case string:
		if value != "" {
			selector.Name = value
			return nil
		}
	}
	return errInvalidGraphRepositorySelector
}

func (selector GraphRepositorySelector) IsZero() bool {
	return selector.ID == 0 && selector.Name == ""
}

func (selector GraphRepositorySelector) MarshalJSON() ([]byte, error) {
	switch {
	case selector.ID > 0 && selector.Name == "":
		return json.Marshal(selector.ID)
	case selector.Name != "" && selector.ID == 0:
		return json.Marshal(selector.Name)
	default:
		return nil, errInvalidGraphRepositorySelector
	}
}

type GraphSymbolSelector struct {
	UID      string `json:"uid,omitempty"`
	Name     string `json:"name,omitempty"`
	FilePath string `json:"file_path,omitempty"`
	Kind     string `json:"kind,omitempty"`
}

type GraphPosition struct {
	StartLine      int `json:"start_line"`
	StartCharacter int `json:"start_character"`
	EndLine        int `json:"end_line"`
	EndCharacter   int `json:"end_character"`
}

type GraphSymbol struct {
	UID          string        `json:"uid"`
	Name         string        `json:"name"`
	Kind         string        `json:"kind"`
	FilePath     string        `json:"file_path"`
	Language     string        `json:"language"`
	Signature    string        `json:"signature,omitempty"`
	RepositoryID int64         `json:"repository_id"`
	Range        GraphPosition `json:"range"`
	Test         bool          `json:"test"`
	Content      string        `json:"content,omitempty"`
}

type GraphCandidate struct {
	UID          string  `json:"uid"`
	Name         string  `json:"name"`
	Kind         string  `json:"kind"`
	FilePath     string  `json:"file_path"`
	RepositoryID int64   `json:"repository_id"`
	Line         int     `json:"line"`
	Score        float64 `json:"score"`
}

type GraphReference struct {
	SourceRepositoryID int64         `json:"source_repository_id"`
	TargetRepositoryID int64         `json:"target_repository_id"`
	SourceUID          string        `json:"source_uid"`
	TargetUID          string        `json:"target_uid"`
	Kind               string        `json:"kind"`
	Path               string        `json:"path,omitempty"`
	Range              GraphPosition `json:"range"`
	Confidence         float64       `json:"confidence"`
	ResolutionReason   string        `json:"resolution_reason,omitempty"`
}

type GraphBoundary struct {
	RepositoryID int64  `json:"repository_id,omitempty"`
	Repository   string `json:"repository,omitempty"`
	Reason       string `json:"reason"`
	Depth        int    `json:"depth,omitempty"`
}

type GraphContextRequest struct {
	Repo GraphRepositorySelector `json:"repo,omitzero"`
	GraphSymbolSelector
	Branch            string   `json:"branch,omitempty"`
	Relations         []string `json:"relations,omitempty"`
	PerCategoryLimit  int      `json:"per_category_limit,omitempty"`
	PerCategoryOffset int      `json:"per_category_offset,omitempty"`
	IncludeContent    bool     `json:"include_content,omitempty"`
}

type GraphContextResponse struct {
	Status     string                      `json:"status"`
	Symbol     *GraphSymbol                `json:"symbol,omitempty"`
	Candidates []GraphCandidate            `json:"candidates,omitempty"`
	Incoming   map[string][]GraphReference `json:"incoming,omitempty"`
	Outgoing   map[string][]GraphReference `json:"outgoing,omitempty"`
	Boundaries []GraphBoundary             `json:"boundaries,omitempty"`
	Commits    map[string]string           `json:"commits"`
}

type GraphImpactRequest struct {
	Repo          GraphRepositorySelector `json:"repo,omitzero"`
	Branch        string                  `json:"branch,omitempty"`
	TargetUID     string                  `json:"target_uid"`
	Direction     string                  `json:"direction"`
	Relations     []string                `json:"relations,omitempty"`
	MinConfidence float64                 `json:"min_confidence,omitempty"`
	IncludeTests  bool                    `json:"include_tests,omitempty"`
	MaxDepth      int                     `json:"max_depth,omitempty"`
	Limit         int                     `json:"limit,omitempty"`
	Offset        int                     `json:"offset,omitempty"`
	SummaryOnly   bool                    `json:"summary_only,omitempty"`
}

type GraphImpactResponse struct {
	Status     string                `json:"status"`
	Candidates []GraphCandidate      `json:"candidates,omitempty"`
	ByDepth    map[int][]GraphSymbol `json:"by_depth"`
	Relations  []GraphReference      `json:"relations,omitempty"`
	Boundaries []GraphBoundary       `json:"boundaries,omitempty"`
	Commits    map[string]string     `json:"commits"`
	Partial    bool                  `json:"partial"`
}

type GraphTraceRequest struct {
	Repo      GraphRepositorySelector `json:"repo,omitzero"`
	Branch    string                  `json:"branch,omitempty"`
	SourceUID string                  `json:"source_uid"`
	TargetUID string                  `json:"target_uid"`
	MaxDepth  int                     `json:"max_depth,omitempty"`
}

type GraphTraceResponse struct {
	Status     string            `json:"status"`
	Candidates []GraphCandidate  `json:"candidates,omitempty"`
	Nodes      []GraphSymbol     `json:"nodes,omitempty"`
	Relations  []GraphReference  `json:"relations,omitempty"`
	Boundaries []GraphBoundary   `json:"boundaries,omitempty"`
	Commits    map[string]string `json:"commits"`
}

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
