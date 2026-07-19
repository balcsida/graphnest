package api

type ReadFileRequest struct {
	RepositoryID int64  `json:"repository_id"`
	Path         string `json:"path"`
	StartLine    int    `json:"start_line,omitempty"`
	EndLine      int    `json:"end_line,omitempty"`
}

type ReadFileResponse struct {
	RepositoryID int64  `json:"repository_id"`
	Path         string `json:"path"`
	IndexedSHA   string `json:"indexed_sha"`
	BlobSHA      string `json:"blob_sha"`
	Content      string `json:"content"`
	StartLine    int    `json:"start_line"`
	EndLine      int    `json:"end_line"`
	Truncated    bool   `json:"truncated"`
}
