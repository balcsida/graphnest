// Package supplychain implements the Dependencies & Licenses inventory
// accepted in ADR-0017: preserved SBOM observations, immutable snapshots,
// component occurrences, and their collection lifecycle.
package supplychain

import (
	"errors"
	"time"
)

// ParserVersion is recorded on every snapshot so a normalization change can be
// told apart from a change in the observed document.
const ParserVersion = 1

// Format identifies a stored document's syntax; only formats the normalizer
// understands are accepted.
type Format string

const FormatSPDX23JSON Format = "spdx-2.3-json"

// Producer is who produced the document; Subject is what it describes.
type (
	Producer  string
	Subject   string
	Assurance string
)

const (
	ProducerGitHub Producer = "github"
	ProducerImport Producer = "import"

	SubjectSource   Subject = "source"
	SubjectArtifact Subject = "artifact"

	// AssuranceUnknown: the document names no subject revision GraphNest can
	// bind. GHES exports are always unknown; HEAD is never copied in.
	AssuranceUnknown Assurance = "unknown"
	// AssuranceProducerAsserted: the producer claimed a revision; nothing
	// verified it.
	AssuranceProducerAsserted Assurance = "producer_asserted"
	AssuranceVerified         Assurance = "verified"
)

// StreamGitHubSource is the stream key of GHES dependency-graph observations.
const StreamGitHubSource = "github:source"

var (
	ErrMalformed          = errors.New("malformed_document")
	ErrUnsupportedVersion = errors.New("unsupported_version")
	ErrTooLarge           = errors.New("too_large")
	// ErrNoJob: no runnable job is queued.
	ErrNoJob = errors.New("no supply chain job available")
	// ErrFenced: a publication or completion lost its lease; another worker
	// holds a newer lease or the job was cancelled. Nothing was written.
	ErrFenced = errors.New("supply chain job lease lost")
)

// Publication is the atomic input of a successful collection: the original
// document bytes, the normalized content, and the attempt metadata.
type Publication struct {
	RepositoryID     int64
	JobID            *int64
	JobOwner         string
	JobFence         int64
	Producer         Producer
	Subject          Subject
	StreamKey        string
	Format           Format
	MediaType        string
	Document         []byte
	Normalized       Normalized
	CollectedAt      time.Time
	StartedAt        time.Time
	HTTPStatus       *int
	SubjectRevision  string
	SubjectAssurance Assurance
	// UploadedBy and UploadLabel identify an import's uploader separately
	// from the document's claimed producer.
	UploadedBy  string
	UploadLabel string
}

// Failure records an attempt that produced no new snapshot.
type Failure struct {
	RepositoryID      int64
	JobID             *int64
	JobOwner          string
	JobFence          int64
	Producer          Producer
	Subject           Subject
	StreamKey         string
	StartedAt         time.Time
	FinishedAt        time.Time
	Outcome           Outcome
	HTTPStatus        *int
	RetryAfterSeconds *int
	ErrorCode         string
	Message           string
}

type Snapshot struct {
	ID, RepositoryID, DocumentID int64
	Producer                     Producer
	Subject                      Subject
	StreamKey                    string
	CollectedAt                  time.Time
	CreatedAtClaimed             *time.Time
	ProducerTool                 string
	DocumentNamespace            string
	DocumentName                 string
	SPDXVersion                  string
	DataLicense                  string
	SubjectRevision              string
	SubjectAssurance             Assurance
	RootElementIDs               []string
	ParserVersion                int
	ComponentCount               int
	EdgeCount                    int
	WarningCount                 int
	Warnings                     []Warning
	PublishedAt                  time.Time
	DocumentSHA256               []byte
	DocumentFormat               Format
	DocumentBytes                int64
	UploadedBy                   string
	UploadLabel                  string
}

// Component is one occurrence in one snapshot. Identity is document-scoped:
// the same package in two snapshots is two occurrences.
type Component struct {
	ID, SnapshotID      int64
	Ordinal             int
	ElementID           string
	Name                string
	Version             *string
	PURL                *string
	Ecosystem           string
	PURLNamespace       string
	PURLName            string
	PURLVersion         string
	Qualifiers          map[string]string
	LicenseDeclaredRaw  *string
	LicenseConcludedRaw *string
	DownloadLocation    *string
	Supplier            *string
	Checksums           []Checksum
	IsRoot              bool
}

type Checksum struct {
	Algorithm string `json:"algorithm"`
	Value     string `json:"value"`
}

// Relationship preserves the document's edge direction and type verbatim.
// Resolved is false when either end is not a component (or the document) in
// the snapshot; such an edge is a diagnostic, not a dependency path.
type Relationship struct {
	FromElement string
	Type        string
	ToElement   string
	Resolved    bool
}

type Warning struct {
	Code    string `json:"code"`
	Element string `json:"element,omitempty"`
	Detail  string `json:"detail,omitempty"`
}

// Normalized is the parser output that a store publishes as a snapshot.
type Normalized struct {
	SPDXVersion       string
	DataLicense       string
	DocumentSPDXID    string
	DocumentName      string
	DocumentNamespace string
	CreatedAtClaimed  *time.Time
	ProducerTool      string
	RootElementIDs    []string
	Components        []Component
	Relationships     []Relationship
	Warnings          []Warning
	WarningCount      int
}

// Limits bound normalization so a hostile or huge document cannot exhaust
// the worker.
type Limits struct {
	MaxComponents    int
	MaxRelationships int
	MaxWarnings      int
}

const (
	DefaultMaxComponents    = 50000
	DefaultMaxRelationships = 500000
	DefaultMaxWarnings      = 200
)

func (limits Limits) withDefaults() Limits {
	if limits.MaxComponents <= 0 {
		limits.MaxComponents = DefaultMaxComponents
	}
	if limits.MaxRelationships <= 0 {
		limits.MaxRelationships = DefaultMaxRelationships
	}
	if limits.MaxWarnings <= 0 {
		limits.MaxWarnings = DefaultMaxWarnings
	}
	return limits
}

// Outcome classifies one collection attempt. Only Published and Unchanged
// count as success; none of the others may move a stream's latest snapshot.
type Outcome string

const (
	OutcomePublished   Outcome = "published"
	OutcomeUnchanged   Outcome = "unchanged"
	OutcomeUnavailable Outcome = "unavailable"
	OutcomeForbidden   Outcome = "forbidden"
	OutcomeRateLimited Outcome = "rate_limited"
	OutcomeNotFound    Outcome = "not_found"
	OutcomeMalformed   Outcome = "malformed"
	OutcomeTooLarge    Outcome = "too_large"
	OutcomeTransient   Outcome = "transient"
	OutcomeCancelled   Outcome = "cancelled"
	OutcomeError       Outcome = "error"
)

// Success reports whether the outcome leaves the stream with a current
// inventory produced by this attempt.
func (outcome Outcome) Success() bool {
	return outcome == OutcomePublished || outcome == OutcomeUnchanged
}

// Retryable reports whether a job may attempt collection again after this
// outcome; permanent document or access failures are not retried.
func (outcome Outcome) Retryable() bool {
	switch outcome {
	case OutcomeRateLimited, OutcomeTransient, OutcomeError:
		return true
	}
	return false
}

type Collection struct {
	ID, RepositoryID  int64
	JobID             *int64
	Producer          Producer
	StreamKey         string
	StartedAt         time.Time
	FinishedAt        time.Time
	Outcome           Outcome
	HTTPStatus        *int
	RetryAfterSeconds *int
	SnapshotID        *int64
	DocumentID        *int64
	ErrorCode         string
	Message           string
	ProjectionError   string
}

type Stream struct {
	RepositoryID       int64
	StreamKey          string
	Producer           Producer
	Subject            Subject
	LatestSnapshotID   *int64
	LatestCollectionID *int64
	LastSuccessAt      *time.Time
	LastAttemptAt      *time.Time
	LastOutcome        Outcome
	OptOut             bool
}

type JobState string

const (
	JobQueued     JobState = "queued"
	JobRunning    JobState = "running"
	JobSucceeded  JobState = "succeeded"
	JobFailed     JobState = "failed"
	JobCancelled  JobState = "cancelled"
	JobSuperseded JobState = "superseded"
)

type Job struct {
	ID, RepositoryID int64
	StreamKey        string
	Reason           string
	State            JobState
	Priority         int
	Attempt          int
	MaxAttempts      int
	RunAfter         time.Time
	LeaseOwner       string
	LeaseExpiresAt   *time.Time
	Fence            int64
	RequestedBy      string
	ErrorCode        string
	CreatedAt        time.Time
	UpdatedAt        time.Time
}
