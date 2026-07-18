package api

import "time"

type SearchRequest struct {
	Query            string        `json:"query"`
	Repositories     []string      `json:"repositories,omitempty"`
	Limit            int           `json:"limit,omitempty"`
	ContextLines     int           `json:"context_lines,omitempty"`
	Timeout          time.Duration `json:"timeout,omitempty"`
	MaxResponseBytes int64         `json:"max_response_bytes,omitempty"`
}

type Repository struct {
	ID         int64  `json:"id"`
	Name       string `json:"name"`
	Branch     string `json:"branch"`
	IndexedSHA string `json:"indexed_sha"`
	WebURL     string `json:"web_url"`
}

type SearchMatch struct {
	Repository Repository `json:"repository"`
	Path       string     `json:"path"`
	SHA        string     `json:"sha"`
	LineNumber int        `json:"line_number"`
	LineStart  int        `json:"line_start"`
	LineEnd    int        `json:"line_end"`
	Preview    string     `json:"preview"`
	Score      float64    `json:"score"`
	ZoektID    uint32     `json:"-"`
}

type SearchResponse struct {
	Matches []SearchMatch `json:"matches"`
}
