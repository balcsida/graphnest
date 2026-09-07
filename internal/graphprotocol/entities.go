package graphprotocol

import graphv2 "github.com/balcsida/graphnest/internal/graphartifact/v2"

// Entity queries are an internal v2 contract. HTTP/MCP symbol wrappers remain v1.
// Selectors use producer occurrence identity; optional strings retain presence.
type EntitySelector struct {
	Occurrence    *string `json:"occurrence,omitempty"`
	Name          *string `json:"name,omitempty"`
	QualifiedName *string `json:"qualified_name,omitempty"`
	Path          *string `json:"path,omitempty"`
	Kind          string  `json:"kind,omitempty"`
}

type Entity struct {
	RepositoryID int64         `json:"repository_id"`
	ID           string        `json:"id"`
	Depth        int           `json:"depth"`
	Fact         *graphv2.Node `json:"fact"`
}

type Evidence struct {
	RepositoryID int64         `json:"repository_id"`
	SourceID     string        `json:"source_id"`
	TargetID     string        `json:"target_id"`
	Fact         *graphv2.Edge `json:"fact"`
}

type Generation struct {
	Repository      string                   `json:"repository"`
	Publisher       string                   `json:"publisher"`
	RepositoryID    int64                    `json:"repository_id"`
	UploadID        int64                    `json:"generation"`
	Commit          string                   `json:"commit"`
	Producer        *graphv2.Producer        `json:"producer"`
	ContentHash     []byte                   `json:"content_hash"`
	Capabilities    []string                 `json:"capabilities"`
	NodeCount       int                      `json:"node_count"`
	EdgeCount       int                      `json:"edge_count"`
	UnresolvedCount int                      `json:"unresolved_count"`
	DiagnosticCount int                      `json:"diagnostic_count"`
	Metadata        []*graphv2.MetadataEntry `json:"metadata,omitempty"`
	Extensions      []*graphv2.Extension     `json:"extensions,omitempty"`
}

type EntitiesRequest struct {
	Scope    Scope          `json:"scope"`
	Selector EntitySelector `json:"selector"`
	Limit    int            `json:"limit,omitempty"`
	Cursor   string         `json:"cursor,omitempty"`
}

type EntitiesResponse struct {
	Entities    []Entity     `json:"entities"`
	Generations []Generation `json:"generations"`
	NextCursor  string       `json:"next_cursor,omitempty"`
}

type TraverseRequest struct {
	Scope         Scope          `json:"scope"`
	Root          EntitySelector `json:"root"`
	Relations     []string       `json:"relations,omitempty"`
	Direction     string         `json:"direction,omitempty"`
	MinConfidence float64        `json:"min_confidence,omitempty"`
	MaxDepth      int            `json:"max_depth,omitempty"`
}

type TraverseResponse struct {
	Status      string       `json:"status"`
	Entities    []Entity     `json:"entities"`
	Edges       []Evidence   `json:"edges"`
	Generations []Generation `json:"generations"`
	Boundaries  []Boundary   `json:"boundaries,omitempty"`
	Partial     bool         `json:"partial"`
}
