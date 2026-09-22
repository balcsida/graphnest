package api

import "time"

// SupplyChainRepositoryStatus describes one repository stream: what GraphNest
// currently serves, when it was observed, and how the latest attempt went.
// Collection, freshness, parsing coverage, and enrichment are reported as
// independent states so a retained older inventory behind a failed refresh is
// obvious.
type SupplyChainRepositoryStatus struct {
	RepositoryID int64  `json:"repository_id"`
	Repository   string `json:"repository"`
	Stream       string `json:"stream"`
	Producer     string `json:"producer"`
	Subject      string `json:"subject"`
	// Collection is "never", "current", "stale", or "failed".
	Collection string `json:"collection"`
	// Freshness is the age of the latest successful observation in seconds, or
	// null when there is none.
	FreshnessSeconds *int64                   `json:"freshness_seconds"`
	LatestSnapshot   *SupplyChainSnapshot     `json:"latest_snapshot"`
	LastCollection   *SupplyChainCollection   `json:"last_collection"`
	ActiveJob        *SupplyChainJob          `json:"active_job"`
	Enrichment       string                   `json:"enrichment"`
	OptOut           bool                     `json:"opt_out"`
	Notes            []string                 `json:"notes"`
	Documents        []SupplyChainDocumentRef `json:"documents"`
}

// SupplyChainDocumentRef points at a downloadable original document.
type SupplyChainDocumentRef struct {
	SnapshotID int64  `json:"snapshot_id"`
	SHA256     string `json:"sha256"`
	Format     string `json:"format"`
	Bytes      int64  `json:"bytes"`
	Path       string `json:"path"`
}

type SupplyChainSnapshot struct {
	ID               int64                `json:"id"`
	RepositoryID     int64                `json:"repository_id"`
	Stream           string               `json:"stream"`
	Producer         string               `json:"producer"`
	Subject          string               `json:"subject"`
	CollectedAt      time.Time            `json:"collected_at"`
	CreatedAtClaimed *time.Time           `json:"created_at_claimed"`
	ProducerTool     string               `json:"producer_tool"`
	DocumentName     string               `json:"document_name"`
	DocumentNS       string               `json:"document_namespace"`
	SPDXVersion      string               `json:"spdx_version"`
	DataLicense      string               `json:"data_license"`
	SubjectRevision  string               `json:"subject_revision,omitempty"`
	SubjectAssurance string               `json:"subject_assurance"`
	RootElementIDs   []string             `json:"root_element_ids"`
	ParserVersion    int                  `json:"parser_version"`
	ComponentCount   int                  `json:"component_count"`
	EdgeCount        int                  `json:"edge_count"`
	WarningCount     int                  `json:"warning_count"`
	Warnings         []SupplyChainWarning `json:"warnings"`
	PublishedAt      time.Time            `json:"published_at"`
	DocumentSHA256   string               `json:"document_sha256"`
	DocumentFormat   string               `json:"document_format"`
	DocumentBytes    int64                `json:"document_bytes"`
}

type SupplyChainWarning struct {
	Code    string `json:"code"`
	Element string `json:"element,omitempty"`
	Detail  string `json:"detail,omitempty"`
}

type SupplyChainComponent struct {
	ElementID           string                `json:"element_id"`
	Ordinal             int                   `json:"ordinal"`
	Name                string                `json:"name"`
	Version             *string               `json:"version"`
	PURL                *string               `json:"purl"`
	Ecosystem           string                `json:"ecosystem,omitempty"`
	Namespace           string                `json:"namespace,omitempty"`
	PackageName         string                `json:"package_name,omitempty"`
	Qualifiers          map[string]string     `json:"qualifiers,omitempty"`
	LicenseDeclaredRaw  *string               `json:"license_declared_raw"`
	LicenseConcludedRaw *string               `json:"license_concluded_raw"`
	DownloadLocation    *string               `json:"download_location,omitempty"`
	Supplier            *string               `json:"supplier,omitempty"`
	Checksums           []SupplyChainChecksum `json:"checksums,omitempty"`
	IsRoot              bool                  `json:"is_root"`
	// Scope is "root", "direct", "transitive", or "unknown" and is derived only
	// from resolved DEPENDS_ON edges from a root; a flattened list yields unknown.
	Scope string `json:"scope"`
}

type SupplyChainChecksum struct {
	Algorithm string `json:"algorithm"`
	Value     string `json:"value"`
}

type SupplyChainComponentList struct {
	SnapshotID int64                  `json:"snapshot_id"`
	Components []SupplyChainComponent `json:"components"`
	Truncated  bool                   `json:"truncated"`
	NextCursor string                 `json:"next_cursor,omitempty"`
}

type SupplyChainRelationship struct {
	From     string `json:"from"`
	Type     string `json:"type"`
	To       string `json:"to"`
	Resolved bool   `json:"resolved"`
}

type SupplyChainCollection struct {
	ID                int64     `json:"id"`
	JobID             *int64    `json:"job_id"`
	Producer          string    `json:"producer"`
	Stream            string    `json:"stream"`
	StartedAt         time.Time `json:"started_at"`
	FinishedAt        time.Time `json:"finished_at"`
	Outcome           string    `json:"outcome"`
	HTTPStatus        *int      `json:"http_status"`
	RetryAfterSeconds *int      `json:"retry_after_seconds,omitempty"`
	SnapshotID        *int64    `json:"snapshot_id"`
	ErrorCode         string    `json:"error_code,omitempty"`
	Message           string    `json:"message,omitempty"`
	ProjectionError   string    `json:"projection_error,omitempty"`
}

type SupplyChainCollectionList struct {
	Collections []SupplyChainCollection `json:"collections"`
	Truncated   bool                    `json:"truncated"`
	NextCursor  string                  `json:"next_cursor,omitempty"`
}

type SupplyChainJob struct {
	ID           int64      `json:"id"`
	RepositoryID int64      `json:"repository_id"`
	Stream       string     `json:"stream"`
	Reason       string     `json:"reason"`
	State        string     `json:"state"`
	Attempt      int        `json:"attempt"`
	MaxAttempts  int        `json:"max_attempts"`
	RunAfter     time.Time  `json:"run_after"`
	ErrorCode    string     `json:"error_code,omitempty"`
	CreatedAt    time.Time  `json:"created_at"`
	UpdatedAt    time.Time  `json:"updated_at"`
	LeaseExpires *time.Time `json:"lease_expires_at,omitempty"`
}

type SupplyChainRefreshResponse struct {
	Job     SupplyChainJob `json:"job"`
	Created bool           `json:"created"`
}
