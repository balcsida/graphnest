package supplychain

import "time"

// PortfolioOverview counts inventory state across the authorized set.
type PortfolioOverview struct {
	Repositories        int
	WithInventory       int
	Stale               int
	Failed              int
	NeverCollected      int
	OptedOut            int
	Occurrences         int
	UniqueCoordinates   int
	WithoutPURL         int
	WithoutVersion      int
	AssessmentCounts    map[string]int
	UnassessedComponent int
	WarningTotal        int
	OldestCollectedAt   *time.Time
	NewestCollectedAt   *time.Time
	Ecosystems          []FacetCount
}

type FacetCount struct {
	Value string
	Count int
}

// PortfolioFilter bounds a cross-repository component query. RepositoryIDs
// is the authorized scope, already intersected with any requested selection.
type PortfolioFilter struct {
	RepositoryIDs     []int64
	StreamKey         string
	Ecosystem         string
	Search            string
	LicenseExpression string
	AssessmentStatus  string
	Scope             string
	AfterKey          string
	Limit             int
}

// PortfolioComponent is one unique coordinate across the authorized set.
type PortfolioComponent struct {
	Key                 string
	Ecosystem           string
	Namespace           string
	Name                string
	Version             string
	PURL                string
	RepositoryCount     int
	OccurrenceCount     int
	AssessmentStatuses  []string
	Expression          string
	DeclaredRaw         []string
	Scopes              []string
	NewestCollectedAt   time.Time
	OldestCollectedAt   time.Time
	RepositoryGitHubIDs []int64
}

// PortfolioOccurrence is one repository's occurrence of a coordinate.
type PortfolioOccurrence struct {
	RepositoryGitHubID int64
	Repository         string
	SnapshotID         int64
	CollectedAt        time.Time
	ElementID          string
	Scope              string
	DeclaredRaw        *string
	AssessmentStatus   string
	Expression         string
}

// ExportRow is one CSV export line.
type ExportRow struct {
	ElementID, Name, Version, PURL, Ecosystem, DeclaredRaw, ConcludedRaw string
	IsRoot                                                               bool
	AssessmentStatus, Expression                                         string
}
