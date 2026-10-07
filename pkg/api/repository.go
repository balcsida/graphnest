package api

import "time"

type RepositorySummary struct {
	ID            int64      `json:"id"`
	GitHubID      int64      `json:"github_id"`
	Name          string     `json:"name"`
	Branch        string     `json:"branch"`
	DesiredSHA    string     `json:"desired_sha"`
	IndexedSHA    string     `json:"indexed_sha"`
	WebURL        string     `json:"web_url"`
	Status        string     `json:"status"`
	ErrorCode     string     `json:"error_code"`
	SearchNode    string     `json:"search_node"`
	LastIndexedAt *time.Time `json:"last_indexed_at,omitempty"`
	// SCIPStatus reports whether code navigation is usable: "current", "stale",
	// "absent", or "unknown". Search being healthy says nothing about SCIP, so
	// callers need this to tell why navigation tools fail on an indexed repository.
	SCIPStatus string `json:"scip_status"`
	SCIPCommit string `json:"scip_commit,omitempty"`
	// GraphStatus reports whether the v2 graph generation the tools would use (the
	// current published one, else the current SCIP-derived one) is usable:
	// "current", "stale", "absent", or "unknown". The graph tools fail unless it
	// is "current", so callers need this to tell why they fail on an indexed repository.
	GraphStatus   string `json:"graph_status"`
	GraphCommit   string `json:"graph_commit,omitempty"`
	GraphProducer string `json:"graph_producer,omitempty"`
}

const (
	SCIPStatusCurrent = "current"
	SCIPStatusStale   = "stale"
	SCIPStatusAbsent  = "absent"
	SCIPStatusUnknown = "unknown"
)

const (
	GraphStatusCurrent = "current"
	GraphStatusStale   = "stale"
	GraphStatusAbsent  = "absent"
	GraphStatusUnknown = "unknown"
)
