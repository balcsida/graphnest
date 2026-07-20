package githubapp

import "time"

type Token struct {
	Value     string
	ExpiresAt time.Time
}

type Installation struct {
	ID           int64
	AccountLogin string
	AccountType  string
	Status       string
	SuspendedAt  *time.Time
}

type Repository struct {
	ID             int64
	InstallationID int64
	SizeBytes      int64
	FullName       string
	Owner          string
	Name           string
	CloneURL       string
	HTMLURL        string
	DefaultBranch  string
	DefaultSHA     string
	Private        bool
	Archived       bool
	Disabled       bool
}

type Content struct {
	Type     string `json:"type"`
	Encoding string `json:"encoding"`
	Content  string `json:"content"`
	SHA      string `json:"sha"`
	Size     int64  `json:"size"`
}
