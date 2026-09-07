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
}

type FilesResponse struct {
	Files       []IndexedFile `json:"files"`
	Generations []Generation  `json:"generations"`
	NextCursor  string        `json:"next_cursor,omitempty"`
}
