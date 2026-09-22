package supplychain

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"regexp"
	"strings"
	"time"

	"github.com/balcsida/graphnest/internal/authn"
	"github.com/balcsida/graphnest/pkg/api"
	"github.com/jackc/pgx/v5"
)

var (
	ErrUnsupportedFormat = errors.New("unsupported_format")
	ErrQuotaExceeded     = errors.New("quota_exceeded")
	ErrImportTooLarge    = errors.New("import_too_large")
)

// ImportStore is the persistence the importer needs beyond Store.
type ImportStore interface {
	SupplyChainUploadAllowed(ctx context.Context, repositoryID int64, subject string) (bool, error)
	SupplyChainImportCount(ctx context.Context, repositoryID int64, since time.Time) (int, error)
	SupplyChainImportByDigest(ctx context.Context, repositoryID int64, streamKey string, digest []byte) (ImportRecord, bool, error)
	RecordSupplyChainImport(ctx context.Context, record ImportRecord) (int64, error)
	PublishSupplyChainSnapshot(ctx context.Context, publication Publication) (Collection, error)
	ProjectSupplyChainPackages(ctx context.Context, repositoryID, snapshotID int64) (int, error)
}

// ImportRecord is one upload attempt.
type ImportRecord struct {
	ID             int64
	RepositoryID   int64
	StreamKey      string
	DocumentSHA256 []byte
	Format         Format
	UploadedBy     string
	UploadLabel    string
	ByteSize       int64
	Outcome        string
	SnapshotID     *int64
	ErrorCode      string
	Message        string
	CreatedAt      time.Time
}

// ImportRequest is an authenticated upload of an SBOM produced elsewhere.
type ImportRequest struct {
	RepositoryID int64 // GitHub ID
	// Subject is "source" or "artifact"; Label distinguishes streams of the
	// same subject (e.g. "ort", "syft-image"). Stream key: import:<subject>:<label>.
	Subject Subject
	Label   string
	// SubjectRevision is the uploader's claim of the source commit the document
	// describes. It is recorded as producer_asserted, never verified.
	SubjectRevision string
	ContentType     string
	Document        []byte
}

// Importer accepts SPDX 2.3 JSON and CycloneDX 1.6 JSON uploads into
// import streams. Documents are preserved byte-for-byte; publication reuses
// the same atomic path as GitHub collection.
type Importer struct {
	Store            ImportStore
	Authorizer       Authorizer
	Enricher         Enricher
	MaxDocumentBytes int64
	Limits           Limits
	// QuotaPerRepository bounds imports per repository per QuotaWindow.
	QuotaPerRepository int
	QuotaWindow        time.Duration
	Now                func() time.Time
}

var labelPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,63}$`)
var shaPattern = regexp.MustCompile(`^[0-9a-f]{40}$`)

func (importer *Importer) now() time.Time {
	if importer.Now != nil {
		return importer.Now().UTC()
	}
	return time.Now().UTC()
}

// ImportStreamKey builds the stream key for an import subject and label.
func ImportStreamKey(subject Subject, label string) string {
	return "import:" + string(subject) + ":" + label
}

// Import validates, authorizes, deduplicates, normalizes, and publishes.
func (importer *Importer) Import(ctx context.Context, principal authn.Principal, request ImportRequest) (api.SupplyChainImportResponse, error) {
	if request.RepositoryID < 1 || (request.Subject != SubjectSource && request.Subject != SubjectArtifact) || !labelPattern.MatchString(request.Label) {
		return api.SupplyChainImportResponse{}, ErrInvalidRequest
	}
	if request.SubjectRevision != "" && !shaPattern.MatchString(request.SubjectRevision) {
		return api.SupplyChainImportResponse{}, ErrInvalidRequest
	}
	maxBytes := importer.MaxDocumentBytes
	if maxBytes <= 0 {
		maxBytes = 16 << 20
	}
	if int64(len(request.Document)) > maxBytes {
		return api.SupplyChainImportResponse{}, ErrImportTooLarge
	}
	if len(request.Document) == 0 {
		return api.SupplyChainImportResponse{}, ErrInvalidRequest
	}
	repo, err := importer.Authorizer.AuthorizedRepository(ctx, principal, request.RepositoryID)
	if errors.Is(err, pgx.ErrNoRows) {
		return api.SupplyChainImportResponse{}, ErrNotFound
	}
	if err != nil {
		return api.SupplyChainImportResponse{}, err
	}
	if !principal.Administrator {
		allowed, err := importer.Store.SupplyChainUploadAllowed(ctx, repo.ID, principal.Subject)
		if err != nil {
			return api.SupplyChainImportResponse{}, err
		}
		if !allowed {
			return api.SupplyChainImportResponse{}, ErrForbidden
		}
	}
	streamKey := ImportStreamKey(request.Subject, request.Label)
	digest := sha256.Sum256(request.Document)
	// Idempotency: the same bytes into the same stream return the earlier result.
	if previous, ok, err := importer.Store.SupplyChainImportByDigest(ctx, repo.ID, streamKey, digest[:]); err != nil {
		return api.SupplyChainImportResponse{}, err
	} else if ok && previous.Outcome != "rejected" {
		return api.SupplyChainImportResponse{ImportID: previous.ID, Outcome: "unchanged", SnapshotID: previous.SnapshotID, Stream: streamKey, Repeated: true, Format: string(previous.Format)}, nil
	}
	window := importer.QuotaWindow
	if window <= 0 {
		window = 24 * time.Hour
	}
	quota := importer.QuotaPerRepository
	if quota <= 0 {
		quota = 100
	}
	count, err := importer.Store.SupplyChainImportCount(ctx, repo.ID, importer.now().Add(-window))
	if err != nil {
		return api.SupplyChainImportResponse{}, err
	}
	if count >= quota {
		return api.SupplyChainImportResponse{}, ErrQuotaExceeded
	}
	record := ImportRecord{RepositoryID: repo.ID, StreamKey: streamKey, DocumentSHA256: digest[:], UploadedBy: principal.Subject, UploadLabel: request.Label, ByteSize: int64(len(request.Document)), CreatedAt: importer.now()}
	format, normalized, err := importer.normalize(request.ContentType, request.Document)
	record.Format = format
	if err != nil {
		record.Outcome = "rejected"
		record.ErrorCode, record.Message = importErrorCode(err), sanitize(err.Error(), 200)
		if record.Format == "" {
			record.Format = FormatSPDX23JSON
		}
		if _, recordErr := importer.Store.RecordSupplyChainImport(ctx, record); recordErr != nil {
			return api.SupplyChainImportResponse{}, recordErr
		}
		return api.SupplyChainImportResponse{}, err
	}
	assurance := AssuranceUnknown
	if request.SubjectRevision != "" {
		assurance = AssuranceProducerAsserted
	}
	mediaType := request.ContentType
	if mediaType == "" {
		mediaType = "application/json"
	}
	collection, err := importer.Store.PublishSupplyChainSnapshot(ctx, Publication{
		RepositoryID: repo.ID, Producer: ProducerImport, Subject: request.Subject, StreamKey: streamKey, Format: format, MediaType: mediaType, Document: request.Document, Normalized: normalized,
		CollectedAt: importer.now(), StartedAt: importer.now(), SubjectRevision: request.SubjectRevision, SubjectAssurance: assurance, UploadedBy: principal.Subject, UploadLabel: request.Label,
	})
	if err != nil {
		return api.SupplyChainImportResponse{}, err
	}
	record.Outcome, record.SnapshotID = string(collection.Outcome), collection.SnapshotID
	importID, err := importer.Store.RecordSupplyChainImport(ctx, record)
	if err != nil {
		return api.SupplyChainImportResponse{}, err
	}
	response := api.SupplyChainImportResponse{ImportID: importID, Outcome: string(collection.Outcome), SnapshotID: collection.SnapshotID, Stream: streamKey, Format: string(format),
		ComponentCount: len(normalized.Components), EdgeCount: len(normalized.Relationships), WarningCount: normalized.WarningCount, Warnings: make([]api.SupplyChainWarning, 0, len(normalized.Warnings)),
		SubjectAssurance: string(assurance), Notes: []string{}}
	for _, warning := range normalized.Warnings {
		response.Warnings = append(response.Warnings, api.SupplyChainWarning{Code: warning.Code, Element: warning.Element, Detail: warning.Detail})
	}
	if request.SubjectRevision != "" {
		response.Notes = append(response.Notes, "The subject revision is the uploader's assertion (producer_asserted); GraphNest did not verify it.")
	}
	if collection.Outcome == OutcomePublished && collection.SnapshotID != nil && importer.Enricher != nil {
		if _, err := importer.Enricher.EnqueueSnapshot(ctx, *collection.SnapshotID); err != nil {
			response.Notes = append(response.Notes, "License enrichment could not be queued; the next publication will retry.")
		}
	}
	return response, nil
}

// normalize detects the format from content (never from the producer's
// self-description alone) and normalizes it.
func (importer *Importer) normalize(contentType string, document []byte) (Format, Normalized, error) {
	var probe struct {
		BOMFormat   string `json:"bomFormat"`
		SpecVersion string `json:"spdxVersion"`
	}
	trimmed := bytes.TrimSpace(document)
	if len(trimmed) == 0 || trimmed[0] != '{' {
		return "", Normalized{}, ErrMalformed
	}
	if err := json.Unmarshal(trimmed, &probe); err != nil {
		return "", Normalized{}, errors.Join(ErrMalformed, errors.New(safeJSONError(err)))
	}
	switch {
	case probe.BOMFormat == "CycloneDX":
		if strings.Contains(contentType, "spdx") {
			return FormatCycloneDX16JSON, Normalized{}, errors.Join(ErrUnsupportedFormat, errors.New("content type says SPDX but the document is CycloneDX"))
		}
		normalized, err := NormalizeCycloneDX16(document, importer.Limits)
		return FormatCycloneDX16JSON, normalized, err
	case probe.SpecVersion != "":
		if strings.Contains(contentType, "cyclonedx") {
			return FormatSPDX23JSON, Normalized{}, errors.Join(ErrUnsupportedFormat, errors.New("content type says CycloneDX but the document is SPDX"))
		}
		normalized, err := NormalizeSPDX23(document, importer.Limits)
		return FormatSPDX23JSON, normalized, err
	default:
		return "", Normalized{}, errors.Join(ErrUnsupportedFormat, errors.New("document is neither SPDX 2.3 JSON (spdxVersion) nor CycloneDX 1.6 JSON (bomFormat)"))
	}
}

func importErrorCode(err error) string {
	switch {
	case errors.Is(err, ErrUnsupportedFormat):
		return "unsupported_format"
	case errors.Is(err, ErrUnsupportedVersion):
		return "unsupported_version"
	case errors.Is(err, ErrTooLarge):
		return "too_large"
	default:
		return "malformed"
	}
}
