package graphprotocol

import graphv2 "github.com/balcsida/graphnest/internal/graphartifact/v2"

type IndexedFile struct {
	RepositoryID int64         `json:"repository_id"`
	Fact         *graphv2.File `json:"fact"`
}

type FilesRequest struct {
	Scope     Scope   `json:"scope"`
	Path      *string `json:"path,omitempty"`
	Directory string  `json:"directory,omitempty"`
	Glob      string  `json:"glob,omitempty"`
	Limit     int     `json:"limit,omitempty"`
	Cursor    string  `json:"cursor,omitempty"`
	// Prefix/Pattern implement the high-level inventory's literal prefix and
	// unanchored wildcard matching. Directory/Glob retain strict path semantics.
	Prefix       *string `json:"prefix,omitempty"`
	Pattern      string  `json:"pattern,omitempty"`
	IncludeCount bool    `json:"include_count,omitempty"`
}

type FilesResponse struct {
	Files       []IndexedFile `json:"files"`
	Generations []Generation  `json:"generations"`
	NextCursor  string        `json:"next_cursor,omitempty"`
	TotalFiles  *int64        `json:"total_files,omitempty"`
}

type FileClassificationRequest struct {
	Scope Scope    `json:"scope"`
	Paths []string `json:"paths"`
}

type FileClassification struct {
	Path               string `json:"path"`
	Present            bool   `json:"present"`
	PersistedGenerated *bool  `json:"persisted_generated,omitempty"`
	Generated          bool   `json:"generated"`
	Ambient            bool   `json:"ambient"`
}

type FileClassificationResponse struct {
	Files       []FileClassification `json:"files"`
	Generations []Generation         `json:"generations"`
}

type GeneratedFileCountRequest struct {
	Scope Scope `json:"scope"`
}

type GeneratedFileCountResponse struct {
	GeneratedFiles int64        `json:"generated_files"`
	TotalFiles     int64        `json:"total_files"`
	Generations    []Generation `json:"generations"`
}
