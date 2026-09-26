package api

import (
	"encoding/json"
	"time"
)

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
	FreshnessSeconds *int64                 `json:"freshness_seconds"`
	LatestSnapshot   *SupplyChainSnapshot   `json:"latest_snapshot"`
	LastCollection   *SupplyChainCollection `json:"last_collection"`
	ActiveJob        *SupplyChainJob        `json:"active_job"`
	// Enrichment is "not_configured" (no registry routes), or "configured".
	Enrichment string `json:"enrichment"`
	// EnrichmentEcosystems lists ecosystems with a configured registry route.
	EnrichmentEcosystems []string `json:"enrichment_ecosystems"`
	// LicenseSummary counts the latest snapshot's assessments by status.
	LicenseSummary map[string]int           `json:"license_summary"`
	OptOut         bool                     `json:"opt_out"`
	Notes          []string                 `json:"notes"`
	Documents      []SupplyChainDocumentRef `json:"documents"`
	// Streams lists every stream known for the repository (GitHub and imports).
	Streams []SupplyChainStreamRef `json:"streams"`
}

type SupplyChainStreamRef struct {
	Key          string `json:"key"`
	Producer     string `json:"producer"`
	Subject      string `json:"subject"`
	HasInventory bool   `json:"has_inventory"`
	LastOutcome  string `json:"last_outcome,omitempty"`
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
	// UploadedBy is the authenticated uploader of an imported document,
	// recorded separately from the producer the document claims.
	UploadedBy  string `json:"uploaded_by,omitempty"`
	UploadLabel string `json:"upload_label,omitempty"`
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
	// License is the derived assessment for this occurrence; nil until an
	// assessment exists (enrichment disabled or pending).
	License *SupplyChainLicenseAssessment `json:"license"`
}

// SupplyChainLicenseAssessment is the derived view over declarations and
// registry evidence for one occurrence. It is not an approval.
type SupplyChainLicenseAssessment struct {
	// Status is unknown, declared, resolved, conflict, unlicensed, not_applicable, or pending.
	Status string `json:"status"`
	// Expression is the normalized SPDX expression when status is declared or resolved.
	Expression string `json:"expression,omitempty"`
	// ConflictDetail lists the disagreeing sources when status is conflict.
	ConflictDetail string    `json:"conflict_detail,omitempty"`
	EvidenceCount  int       `json:"evidence_count"`
	AssessedAt     time.Time `json:"assessed_at"`
	// EvidenceFingerprint (hex) changes when any considered evidence changes;
	// reviews record it to detect stale bases.
	EvidenceFingerprint string `json:"evidence_fingerprint"`
}

// SupplyChainLicenseEvidence is one immutable evidence row as shown in the
// evidence detail view. Raw values are verbatim; nothing is mapped.
type SupplyChainLicenseEvidence struct {
	ID                 int64          `json:"id"`
	Source             string         `json:"source"`
	Route              string         `json:"route,omitempty"`
	Ecosystem          string         `json:"ecosystem"`
	Namespace          string         `json:"namespace,omitempty"`
	Name               string         `json:"name"`
	Version            string         `json:"version"`
	ArtifactSHA256     string         `json:"artifact_sha256,omitempty"`
	RawValue           string         `json:"raw_value"`
	RawKind            string         `json:"raw_kind"`
	ParseStatus        string         `json:"parse_status"`
	Expression         string         `json:"expression,omitempty"`
	UnknownTerms       []string       `json:"unknown_terms,omitempty"`
	LicenseURL         string         `json:"license_url,omitempty"`
	LicenseFileName    string         `json:"license_file_name,omitempty"`
	Detail             map[string]any `json:"detail,omitempty"`
	ResolverVersion    int            `json:"resolver_version"`
	LicenseListVersion string         `json:"license_list_version"`
	ContentSHA256      string         `json:"content_sha256,omitempty"`
	FetchedAt          time.Time      `json:"fetched_at"`
	ExpiresAt          *time.Time     `json:"expires_at,omitempty"`
	Outcome            string         `json:"outcome"`
	HTTPStatus         *int           `json:"http_status,omitempty"`
	Message            string         `json:"message,omitempty"`
}

// SupplyChainComponentDetail is the evidence detail view for one occurrence.
type SupplyChainComponentDetail struct {
	Component     SupplyChainComponent         `json:"component"`
	Snapshot      SupplyChainSnapshot          `json:"snapshot"`
	Declarations  []SupplyChainLicenseEvidence `json:"declarations"`
	Evidence      []SupplyChainLicenseEvidence `json:"evidence"`
	Relationships []SupplyChainRelationship    `json:"relationships"`
	Notes         []string                     `json:"notes"`
	Truncated     bool                         `json:"truncated"`
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

// SupplyChainOverview is the portfolio summary across the caller's authorized
// repositories. Every count names its denominator in Denominators; none is a
// compliance percentage.
type SupplyChainOverview struct {
	Stream            string                      `json:"stream"`
	GeneratedAt       time.Time                   `json:"generated_at"`
	Repositories      SupplyChainRepositoryCounts `json:"repositories"`
	Components        SupplyChainComponentCounts  `json:"components"`
	WarningTotal      int                         `json:"warning_total"`
	OldestCollectedAt *time.Time                  `json:"oldest_collected_at"`
	NewestCollectedAt *time.Time                  `json:"newest_collected_at"`
	Ecosystems        []SupplyChainFacet          `json:"ecosystems"`
	Denominators      []string                    `json:"denominators"`
}

type SupplyChainRepositoryCounts struct {
	Authorized        int `json:"authorized"`
	WithInventory     int `json:"with_inventory"`
	NeverCollected    int `json:"never_collected"`
	Stale             int `json:"stale"`
	FailedLastAttempt int `json:"failed_last_attempt"`
	OptedOut          int `json:"opted_out"`
}

type SupplyChainComponentCounts struct {
	Occurrences       int            `json:"occurrences"`
	UniqueCoordinates int            `json:"unique_coordinates"`
	WithoutPURL       int            `json:"without_purl"`
	WithoutVersion    int            `json:"without_version"`
	Unassessed        int            `json:"unassessed"`
	Assessments       map[string]int `json:"assessments"`
}

type SupplyChainFacet struct {
	Value string `json:"value"`
	Count int    `json:"count"`
}

type SupplyChainFacets struct {
	Stream      string             `json:"stream"`
	Ecosystems  []SupplyChainFacet `json:"ecosystems"`
	Assessments []SupplyChainFacet `json:"assessments"`
	Licenses    []SupplyChainFacet `json:"licenses"`
}

// SupplyChainPortfolioComponent is one unique coordinate across repositories.
type SupplyChainPortfolioComponent struct {
	Key                string                     `json:"key"`
	Ecosystem          string                     `json:"ecosystem"`
	Namespace          string                     `json:"namespace,omitempty"`
	Name               string                     `json:"name"`
	Version            string                     `json:"version"`
	PURL               string                     `json:"purl,omitempty"`
	RepositoryCount    int                        `json:"repository_count"`
	OccurrenceCount    int                        `json:"occurrence_count"`
	Assessment         string                     `json:"assessment"`
	AssessmentStatuses []string                   `json:"assessment_statuses"`
	Expression         string                     `json:"expression,omitempty"`
	DeclaredRaw        []string                   `json:"declared_raw"`
	NewestCollectedAt  time.Time                  `json:"newest_collected_at"`
	OldestCollectedAt  time.Time                  `json:"oldest_collected_at"`
	Repositories       []SupplyChainRepositoryRef `json:"repositories"`
}

type SupplyChainRepositoryRef struct {
	ID   int64  `json:"id"`
	Name string `json:"name,omitempty"`
}

type SupplyChainPortfolioComponentList struct {
	Stream              string                          `json:"stream"`
	RepositoriesInScope int                             `json:"repositories_in_scope"`
	Components          []SupplyChainPortfolioComponent `json:"components"`
	Truncated           bool                            `json:"truncated"`
	NextCursor          string                          `json:"next_cursor,omitempty"`
}

type SupplyChainPortfolioComponentDetail struct {
	Key         string                           `json:"key"`
	Stream      string                           `json:"stream"`
	Ecosystem   string                           `json:"ecosystem"`
	Namespace   string                           `json:"namespace,omitempty"`
	Name        string                           `json:"name"`
	Version     string                           `json:"version"`
	Occurrences []SupplyChainPortfolioOccurrence `json:"occurrences"`
	Truncated   bool                             `json:"truncated"`
	Notes       []string                         `json:"notes"`
}

type SupplyChainPortfolioOccurrence struct {
	RepositoryID int64     `json:"repository_id"`
	Repository   string    `json:"repository"`
	SnapshotID   int64     `json:"snapshot_id"`
	CollectedAt  time.Time `json:"collected_at"`
	ElementID    string    `json:"element_id"`
	Root         bool      `json:"root"`
	DeclaredRaw  *string   `json:"declared_raw"`
	Assessment   string    `json:"assessment,omitempty"`
	Expression   string    `json:"expression,omitempty"`
	DetailPath   string    `json:"detail_path"`
}

type SupplyChainSnapshotComparison struct {
	RepositoryID      int64                      `json:"repository_id"`
	Base              SupplyChainSnapshot        `json:"base"`
	Head              SupplyChainSnapshot        `json:"head"`
	AddedComponents   []string                   `json:"added_components"`
	RemovedComponents []string                   `json:"removed_components"`
	LicenseChanges    []SupplyChainLicenseChange `json:"license_changes"`
	EdgesAdded        int                        `json:"edges_added"`
	EdgesRemoved      int                        `json:"edges_removed"`
	MetadataChanges   []string                   `json:"metadata_changes"`
	Notes             []string                   `json:"notes"`
}

type SupplyChainLicenseChange struct {
	Component string `json:"component"`
	From      string `json:"from"`
	To        string `json:"to"`
}

// SupplyChainImportResponse reports an accepted upload.
type SupplyChainImportResponse struct {
	ImportID         int64                `json:"import_id"`
	Outcome          string               `json:"outcome"`
	Repeated         bool                 `json:"repeated"`
	SnapshotID       *int64               `json:"snapshot_id"`
	Stream           string               `json:"stream"`
	Format           string               `json:"format"`
	ComponentCount   int                  `json:"component_count"`
	EdgeCount        int                  `json:"edge_count"`
	WarningCount     int                  `json:"warning_count"`
	Warnings         []SupplyChainWarning `json:"warnings"`
	SubjectAssurance string               `json:"subject_assurance,omitempty"`
	Notes            []string             `json:"notes"`
}

// Review workflow models. Conclusions correct evidence, policy results apply
// a policy version, and decisions approve/reject usage in one repository.
type SupplyChainCoordinates struct {
	Ecosystem string `json:"ecosystem"`
	Namespace string `json:"namespace,omitempty"`
	Name      string `json:"name"`
	Version   string `json:"version"`
}

type SupplyChainReviewItem struct {
	ComponentID     int64                  `json:"component_id"`
	SnapshotID      int64                  `json:"snapshot_id"`
	RepositoryID    int64                  `json:"repository_id"`
	Repository      string                 `json:"repository"`
	ElementID       string                 `json:"element_id"`
	Name            string                 `json:"name"`
	Version         string                 `json:"version"`
	PURL            string                 `json:"purl,omitempty"`
	Coordinates     SupplyChainCoordinates `json:"coordinates"`
	Assessment      string                 `json:"assessment"`
	Expression      string                 `json:"expression,omitempty"`
	Basis           string                 `json:"basis,omitempty"`
	Verdict         string                 `json:"verdict"`
	Explanation     string                 `json:"explanation"`
	PolicyID        *int64                 `json:"policy_id"`
	StaleDecisionID *int64                 `json:"stale_decision_id"`
	Reason          string                 `json:"reason"`
}

type SupplyChainReviewQueue struct {
	Items      []SupplyChainReviewItem `json:"items"`
	Truncated  bool                    `json:"truncated"`
	NextCursor string                  `json:"next_cursor,omitempty"`
}

type SupplyChainConcludeRequest struct {
	RepositoryID     int64                  `json:"repository_id"`
	Coordinates      SupplyChainCoordinates `json:"coordinates"`
	Expression       string                 `json:"expression"`
	Reason           string                 `json:"reason"`
	BasisFingerprint string                 `json:"basis"`
}

type SupplyChainDecideRequest struct {
	RepositoryID     int64                  `json:"repository_id"`
	Coordinates      SupplyChainCoordinates `json:"coordinates"`
	Kind             string                 `json:"kind"`
	Reason           string                 `json:"reason"`
	UsageContext     string                 `json:"usage_context,omitempty"`
	ExpiresAt        *time.Time             `json:"expires_at,omitempty"`
	BasisFingerprint string                 `json:"basis"`
}

type SupplyChainConclusion struct {
	ID           int64                  `json:"id"`
	Coordinates  SupplyChainCoordinates `json:"coordinates"`
	EvidenceID   int64                  `json:"evidence_id"`
	Basis        string                 `json:"basis"`
	Reviewer     string                 `json:"reviewer"`
	Reason       string                 `json:"reason"`
	CreatedAt    time.Time              `json:"created_at"`
	SupersededBy *int64                 `json:"superseded_by"`
}

type SupplyChainDecision struct {
	ID            int64                  `json:"id"`
	Coordinates   SupplyChainCoordinates `json:"coordinates"`
	Kind          string                 `json:"kind"`
	PolicyID      *int64                 `json:"policy_id"`
	PolicyVerdict string                 `json:"policy_verdict,omitempty"`
	Basis         string                 `json:"basis"`
	Reviewer      string                 `json:"reviewer"`
	Reason        string                 `json:"reason"`
	UsageContext  string                 `json:"usage_context,omitempty"`
	ExpiresAt     *time.Time             `json:"expires_at"`
	CreatedAt     time.Time              `json:"created_at"`
	SupersededBy  *int64                 `json:"superseded_by"`
}

type SupplyChainPolicyResult struct {
	PolicyID            int64     `json:"policy_id"`
	Verdict             string    `json:"verdict"`
	Explanation         string    `json:"explanation"`
	EvaluatedAt         time.Time `json:"evaluated_at"`
	EvidenceFingerprint string    `json:"evidence_fingerprint,omitempty"`
}

type SupplyChainReviewHistory struct {
	Basis         string                    `json:"basis,omitempty"`
	Current       *SupplyChainDecision      `json:"current"`
	CurrentStale  bool                      `json:"current_stale"`
	StaleReason   string                    `json:"stale_reason,omitempty"`
	Conclusions   []SupplyChainConclusion   `json:"conclusions"`
	Decisions     []SupplyChainDecision     `json:"decisions"`
	PolicyResults []SupplyChainPolicyResult `json:"policy_results"`
}

type SupplyChainReviewEvent struct {
	ID           int64          `json:"id"`
	Kind         string         `json:"kind"`
	Actor        string         `json:"actor"`
	RepositoryID *int64         `json:"repository_id"`
	Target       string         `json:"target"`
	Detail       map[string]any `json:"detail"`
	CreatedAt    time.Time      `json:"created_at"`
}

type SupplyChainPolicy struct {
	ID              int64    `json:"id"`
	Name            string   `json:"name"`
	Version         int      `json:"version"`
	Kind            string   `json:"kind"`
	Active          bool     `json:"active"`
	UnknownHandling string   `json:"unknown_handling"`
	Description     string   `json:"description"`
	CreatedBy       string   `json:"created_by"`
	Approved        []string `json:"approved"`
	ReviewRequired  []string `json:"review_required"`
	Prohibited      []string `json:"prohibited"`
	AllowOrLater    bool     `json:"allow_or_later"`
}

type SupplyChainCreatePolicyRequest struct {
	Name        string `json:"name,omitempty"`
	Description string `json:"description,omitempty"`
	// Rules is the JSON policy body (approved/review_required/prohibited lists).
	Rules           json.RawMessage `json:"rules,omitempty"`
	UnknownHandling string          `json:"unknown_handling,omitempty"`
	Activate        bool            `json:"activate"`
	InstallExample  bool            `json:"install_example,omitempty"`
}
