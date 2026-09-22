package license

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/balcsida/graphnest/internal/supplychain/spdxexpr"
)

// ResolverVersion is recorded on every registry evidence row. Bump it when
// resolver behavior changes so old rows are distinguishable.
const ResolverVersion = 1

// NegativeTTL bounds how long a not-found / no-metadata / unavailable result
// is trusted before the coordinates are looked up again.
const NegativeTTL = 24 * time.Hour

type Source string

const (
	SourceProducerDeclared  Source = "producer_declared"
	SourceProducerConcluded Source = "producer_concluded"
	SourceRegistryNPM       Source = "registry_npm"
	SourceRegistryNuGet     Source = "registry_nuget"
	SourceRegistryMaven     Source = "registry_maven"
	SourceImport            Source = "import"
	SourceHuman             Source = "human"
)

type RawKind string

const (
	RawExpression       RawKind = "expression"
	RawExpressionOrFile RawKind = "expression_or_file"
	RawLicenseFile      RawKind = "license_file"
	RawLicenseURL       RawKind = "license_url"
	RawLicenseName      RawKind = "license_name"
	RawLegacyObject     RawKind = "legacy_object"
	RawMissing          RawKind = "missing"
	RawSentinel         RawKind = "sentinel"
)

type Outcome string

const (
	OutcomeResolved          Outcome = "resolved"
	OutcomeNotFound          Outcome = "not_found"
	OutcomeNoLicenseMetadata Outcome = "no_license_metadata"
	OutcomeUnavailable       Outcome = "unavailable"
	OutcomeRejected          Outcome = "rejected"
	OutcomeTooLarge          Outcome = "too_large"
	OutcomeMalformed         Outcome = "malformed"
)

// Coordinates identify the exact package version evidence applies to.
type Coordinates struct {
	Ecosystem, Namespace, Name, Version string
}

// Evidence is one immutable observation about a package version's license.
// Parsed fields come from spdxexpr; the raw value is always kept.
type Evidence struct {
	ID                   int64
	Source               Source
	Route                string
	Coordinates          Coordinates
	ArtifactSHA256       string
	RawValue             string
	RawKind              RawKind
	ParseStatus          spdxexpr.Status
	NormalizedExpression string
	ExpressionTree       *spdxexpr.Node
	UnknownTerms         []string
	LicenseURL           string
	LicenseFileName      string
	Detail               map[string]any
	ResolverVersion      int
	LicenseListVersion   string
	ContentSHA256        []byte
	FetchedAt            time.Time
	ExpiresAt            *time.Time
	Outcome              Outcome
	HTTPStatus           *int
	Message              string
}

// Fingerprint hashes the fields that make evidence materially different, so
// a re-fetch that produced the same facts is recognizable without comparing
// rows field by field.
func (evidence Evidence) Fingerprint() []byte {
	hash := sha256.New()
	for _, part := range []string{string(evidence.Source), evidence.Route, evidence.Coordinates.Ecosystem, evidence.Coordinates.Namespace, evidence.Coordinates.Name, evidence.Coordinates.Version,
		evidence.ArtifactSHA256, evidence.RawValue, string(evidence.RawKind), string(evidence.ParseStatus), evidence.NormalizedExpression, evidence.LicenseURL, evidence.LicenseFileName, string(evidence.Outcome)} {
		hash.Write([]byte(part))
		hash.Write([]byte{0})
	}
	return hash.Sum(nil)
}

// Resolver resolves one exact version through one configured route.
type Resolver interface {
	Ecosystem() string
	Resolve(ctx context.Context, coordinates Coordinates) (Evidence, error)
}

// NotApplicable is the parse status of evidence that carries no expression
// (a license file, URL, or a missing field).
const NotApplicable spdxexpr.Status = "not_applicable"

// classify parses a raw expression candidate into evidence fields.
func classify(evidence *Evidence, raw string, kind RawKind) {
	evidence.RawValue, evidence.RawKind = raw, kind
	evidence.LicenseListVersion = spdxexpr.ListVersion
	evidence.ResolverVersion = ResolverVersion
	if kind != RawExpression && kind != RawExpressionOrFile && kind != RawLicenseName {
		evidence.ParseStatus = NotApplicable
		return
	}
	parsed := spdxexpr.Parse(raw)
	evidence.ParseStatus = parsed.Status
	evidence.NormalizedExpression = parsed.Normalized
	evidence.ExpressionTree = parsed.Expression
	evidence.UnknownTerms = parsed.UnknownTerms
	if parsed.Status == spdxexpr.StatusInvalid {
		evidence.Message = parsed.Problem
	}
}

func negative(evidence Evidence, outcome Outcome, status *int, message string) Evidence {
	evidence.Outcome = outcome
	evidence.HTTPStatus = status
	evidence.Message = message
	evidence.RawKind = RawMissing
	evidence.ParseStatus = NotApplicable
	evidence.ResolverVersion = ResolverVersion
	evidence.LicenseListVersion = spdxexpr.ListVersion
	expires := evidence.FetchedAt.Add(NegativeTTL)
	evidence.ExpiresAt = &expires
	return evidence
}

// fromFetchError maps route errors to negative evidence outcomes.
func fromFetchError(evidence Evidence, err error) Evidence {
	switch {
	case errors.Is(err, ErrRouteRejected), errors.Is(err, ErrNamespaceDeny):
		return negative(evidence, OutcomeRejected, nil, "request rejected by the registry route policy")
	case errors.Is(err, ErrResponseLarge):
		return negative(evidence, OutcomeTooLarge, nil, "registry response exceeds the configured limit")
	default:
		return negative(evidence, OutcomeUnavailable, nil, "registry unavailable")
	}
}

func statusOutcome(evidence Evidence, response Response) (Evidence, bool) {
	status := response.Status
	switch {
	case status == http.StatusOK:
		return evidence, true
	case status == http.StatusNotFound || status == http.StatusGone:
		return negative(evidence, OutcomeNotFound, &status, "package version not found at the configured route"), false
	case status == http.StatusUnauthorized || status == http.StatusForbidden:
		return negative(evidence, OutcomeUnavailable, &status, "registry refused the configured credentials"), false
	default:
		return negative(evidence, OutcomeUnavailable, &status, "registry returned an unexpected status"), false
	}
}

func contentHash(body []byte) []byte {
	sum := sha256.Sum256(body)
	return sum[:]
}

// boundedDetail keeps a small JSON-serializable excerpt for the evidence view.
func boundedDetail(values map[string]any) map[string]any {
	data, err := json.Marshal(values)
	if err != nil || len(data) > 4096 {
		return map[string]any{"truncated": true}
	}
	return values
}

func truncate(value string, max int) string {
	value = strings.TrimSpace(value)
	if len(value) > max {
		return value[:max]
	}
	return value
}

// ListVersion exposes the pinned license list release for evidence rows.
func ListVersion() string { return spdxexpr.ListVersion }

// Now is replaceable in tests.
var Now = time.Now

func now() time.Time { return Now().UTC() }
