package license

import (
	"context"
	"errors"
	"time"
)

// ErrNoJob: no runnable enrichment job.
var ErrNoJob = errors.New("no enrichment job available")

// ErrFenced: the job's lease was lost; nothing was written.
var ErrFenced = errors.New("enrichment job lease lost")

// Job is one exact-coordinates lookup through one route.
type Job struct {
	ID             int64
	Coordinates    Coordinates
	Route          string
	State          string
	Attempt        int
	MaxAttempts    int
	RunAfter       time.Time
	LeaseOwner     string
	LeaseExpiresAt *time.Time
	Fence          int64
	ErrorCode      string
}

// Assessment is the derived, per-occurrence view of the evidence that applies
// to one snapshot component. It is rebuilt when evidence changes; evidence
// rows themselves are never edited.
type Assessment struct {
	ComponentID          int64
	SnapshotID           int64
	Status               AssessmentStatus
	NormalizedExpression string
	EvidenceIDs          []int64
	ConflictDetail       string
	AssessedAt           time.Time
	EvidenceFingerprint  []byte
}

type AssessmentStatus string

const (
	// AssessmentUnknown: no usable expression from any source.
	AssessmentUnknown AssessmentStatus = "unknown"
	// AssessmentDeclared: only the producer's declaration parsed (no registry evidence).
	AssessmentDeclared AssessmentStatus = "declared"
	// AssessmentResolved: registry evidence parsed and agrees with any declaration.
	AssessmentResolved AssessmentStatus = "resolved"
	// AssessmentConflict: two parsed expressions disagree structurally.
	AssessmentConflict AssessmentStatus = "conflict"
	// AssessmentUnlicensed: a source asserts UNLICENSED/NONE.
	AssessmentUnlicensed AssessmentStatus = "unlicensed"
	// AssessmentNotApplicable: the component has no exact coordinates to resolve.
	AssessmentNotApplicable AssessmentStatus = "not_applicable"
	// AssessmentPending: a lookup is queued or running and nothing parsed yet.
	AssessmentPending AssessmentStatus = "pending"
)

// Store is the persistence the enrichment worker and assessor need.
type Store interface {
	// InsertLicenseEvidence appends an immutable evidence row and returns its ID.
	// When an identical fingerprint already exists as the newest row for the
	// same coordinates/route/source, it still inserts (a new observation) but
	// reports duplicate=true so callers can avoid re-assessing.
	InsertLicenseEvidence(ctx context.Context, evidence Evidence) (id int64, duplicate bool, err error)
	// LatestLicenseEvidence returns, per (source, route), the newest row and,
	// when that row is a negative outcome, also the newest resolved row, so an
	// outage does not erase earlier evidence (it is shown with its age).
	LatestLicenseEvidence(ctx context.Context, coordinates Coordinates) ([]Evidence, error)
	// EnqueueEnrichment queues a lookup unless one is queued/running or fresh
	// evidence (resolved, or unexpired negative) exists for the route.
	EnqueueEnrichment(ctx context.Context, coordinates Coordinates, route string) (created bool, err error)
	ClaimEnrichment(ctx context.Context, owner string) (Job, error)
	RenewEnrichment(ctx context.Context, id int64, owner string, fence int64) error
	CompleteEnrichment(ctx context.Context, id int64, owner string, fence int64, outcome Outcome, errorCode string) error
	ReapExpiredEnrichment(ctx context.Context, limit int) (int64, error)
	// ComponentsForCoordinates returns (component_id, snapshot_id) pairs of
	// latest-stream occurrences matching the coordinates, bounded.
	ComponentsForCoordinates(ctx context.Context, coordinates Coordinates, limit int) ([][2]int64, error)
	UpsertAssessment(ctx context.Context, assessment Assessment) error
	// SnapshotCoordinates lists distinct resolvable coordinates in a snapshot.
	SnapshotCoordinates(ctx context.Context, snapshotID int64) ([]Coordinates, error)
}
