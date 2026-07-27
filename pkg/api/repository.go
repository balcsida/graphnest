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
}
