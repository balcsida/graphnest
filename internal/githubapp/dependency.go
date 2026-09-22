package githubapp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

type SBOM struct {
	DocumentSPDXID    string
	DocumentDescribes []string
	Packages          []SBOMPackage
	Relationships     []SBOMRelationship
}

type SBOMPackage struct {
	SPDXID string
	PURLs  []string
}

type SBOMRelationship struct {
	SPDXElementID      string
	Type               string
	RelatedSPDXElement string
}

// SBOMDocument is one lossless observation of a repository's dependency-graph
// SBOM export. Body is the exact bytes of the SPDX JSON member as GitHub sent
// them; the HTTP envelope around it is not retained. FetchedAt is GraphNest's
// clock, never the document's own creation time.
type SBOMDocument struct {
	Body       []byte
	MediaType  string
	Status     int
	FetchedAt  time.Time
	RateLimit  RateLimit
	RetryAfter time.Duration
}

// RateLimit carries GitHub's rate-limit headers when present so a 403 caused
// by throttling can be told apart from missing access.
type RateLimit struct {
	Limited   bool
	Remaining int
	ResetAt   time.Time
}

// SBOMError is a non-2xx SBOM export response. It preserves the status and
// throttling signals and carries no response body.
type SBOMError struct {
	Status     int
	RateLimit  RateLimit
	RetryAfter time.Duration
}

func (err SBOMError) Error() string { return fmt.Sprintf("GitHub SBOM export status %d", err.Status) }

// IsRateLimited reports whether the failure is throttling (429, or a 403 with
// exhausted rate-limit headers) rather than a permission or availability
// failure. A plain 403 or 404 does not prove the dependency graph is disabled.
func (err SBOMError) IsRateLimited() bool {
	return err.Status == http.StatusTooManyRequests || err.RateLimit.Limited || err.RetryAfter > 0
}

var ErrSBOMTooLarge = errors.New("GitHub SBOM export exceeds the configured limit")
var ErrSBOMMalformed = errors.New("GitHub SBOM export envelope is malformed")

// DependencySBOMDocument fetches the SPDX SBOM export losslessly. It returns
// the SPDX member bytes verbatim (no reserialization), the HTTP status, and
// throttling headers. maxBytes bounds the whole HTTP body; a larger response is
// ErrSBOMTooLarge. Non-2xx statuses are returned as SBOMError so callers can
// classify forbidden, not found, rate limited, and transient outcomes.
func (c *Client) DependencySBOMDocument(ctx context.Context, installationID int64, owner, name string, maxBytes int64) (SBOMDocument, error) {
	if maxBytes <= 0 {
		maxBytes = c.maxBytes
	}
	result := "error"
	if c.metrics != nil {
		defer func() { c.metrics.ObserveGitHub("dependency_sbom", result) }()
	}
	endpoint := c.apiURL("repos", owner, name, "dependency-graph", "sbom")
	for attempt := 0; attempt < 2; attempt++ {
		token, err := c.InstallationToken(ctx, installationID, nil)
		if err != nil {
			return SBOMDocument{}, err
		}
		request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint.String(), nil)
		if err != nil {
			return SBOMDocument{}, err
		}
		SetAPIHeaders(request.Header, c.apiVersion)
		request.Header.Set("Authorization", "Bearer "+token.Value)
		response, err := c.http.Do(request)
		if err != nil {
			return SBOMDocument{}, fmt.Errorf("GitHub API request: %w", err)
		}
		fetchedAt := c.now().UTC()
		if response.StatusCode == http.StatusUnauthorized && attempt == 0 {
			response.Body.Close()
			c.mu.Lock()
			delete(c.tokens, tokenKey(installationID, nil))
			c.mu.Unlock()
			continue
		}
		if response.StatusCode < 200 || response.StatusCode >= 300 {
			response.Body.Close()
			return SBOMDocument{Status: response.StatusCode, FetchedAt: fetchedAt}, SBOMError{
				Status: response.StatusCode, RateLimit: rateLimitFrom(response.Header, c.now()), RetryAfter: retryAfterFrom(response.Header, c.now()),
			}
		}
		data, err := io.ReadAll(io.LimitReader(response.Body, maxBytes+1))
		response.Body.Close()
		if err != nil {
			return SBOMDocument{}, errors.New("read GitHub API response")
		}
		if int64(len(data)) > maxBytes {
			return SBOMDocument{Status: response.StatusCode, FetchedAt: fetchedAt}, ErrSBOMTooLarge
		}
		body, err := sbomMember(data)
		if err != nil {
			return SBOMDocument{Status: response.StatusCode, FetchedAt: fetchedAt}, err
		}
		result = "success"
		mediaType := response.Header.Get("Content-Type")
		if mediaType == "" {
			mediaType = "application/json"
		}
		return SBOMDocument{Body: body, MediaType: mediaType, Status: response.StatusCode, FetchedAt: fetchedAt, RateLimit: rateLimitFrom(response.Header, c.now())}, nil
	}
	return SBOMDocument{}, SBOMError{Status: http.StatusUnauthorized}
}

// sbomMember slices the exact bytes of the top-level "sbom" member out of the
// envelope so the stored document is what GitHub produced, not a re-encoding.
func sbomMember(envelope []byte) ([]byte, error) {
	var outer struct {
		SBOM json.RawMessage `json:"sbom"`
	}
	decoder := json.NewDecoder(bytes.NewReader(envelope))
	if err := decoder.Decode(&outer); err != nil {
		return nil, ErrSBOMMalformed
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return nil, ErrSBOMMalformed
	}
	trimmed := bytes.TrimSpace(outer.SBOM)
	if len(trimmed) == 0 || trimmed[0] != '{' {
		return nil, ErrSBOMMalformed
	}
	return append([]byte(nil), trimmed...), nil
}

func rateLimitFrom(header http.Header, now time.Time) RateLimit {
	var limit RateLimit
	if remaining, err := strconv.Atoi(header.Get("X-RateLimit-Remaining")); err == nil {
		limit.Remaining = remaining
		limit.Limited = remaining == 0
	}
	if reset, err := strconv.ParseInt(header.Get("X-RateLimit-Reset"), 10, 64); err == nil && reset > 0 {
		limit.ResetAt = time.Unix(reset, 0).UTC()
		if limit.ResetAt.Before(now.Add(-24*time.Hour)) || limit.ResetAt.After(now.Add(24*time.Hour)) {
			limit.ResetAt = time.Time{}
		}
	}
	return limit
}

// retryAfterFrom decodes Retry-After (seconds or HTTP date) and bounds it to a
// day so a hostile header cannot park a job indefinitely.
func retryAfterFrom(header http.Header, now time.Time) time.Duration {
	value := strings.TrimSpace(header.Get("Retry-After"))
	if value == "" {
		return 0
	}
	var wait time.Duration
	if seconds, err := strconv.Atoi(value); err == nil {
		wait = time.Duration(seconds) * time.Second
	} else if at, err := http.ParseTime(value); err == nil {
		wait = at.Sub(now)
	}
	if wait < 0 {
		return 0
	}
	if wait > 24*time.Hour {
		return 24 * time.Hour
	}
	return wait
}

// DependencySBOM is the compatibility reader used by SCIP cross-repository
// package mapping. It keeps its historical contract: identifiers, PURLs and
// relationships only, with HTTP 403/404 reported as unavailable.
func (c *Client) DependencySBOM(ctx context.Context, installationID int64, owner, name string) (SBOM, bool, error) {
	document, err := c.DependencySBOMDocument(ctx, installationID, owner, name, c.maxBytes)
	var sbomError SBOMError
	if errors.As(err, &sbomError) {
		if sbomError.Status == http.StatusForbidden || sbomError.Status == http.StatusNotFound {
			return SBOM{}, false, nil
		}
		return SBOM{}, false, HTTPStatusError{StatusCode: sbomError.Status, RateLimited: sbomError.IsRateLimited()}
	}
	if errors.Is(err, ErrSBOMTooLarge) {
		return SBOM{}, false, errors.New("GitHub API response too large")
	}
	if errors.Is(err, ErrSBOMMalformed) {
		return SBOM{}, false, errors.New("decode GitHub API response")
	}
	if err != nil {
		return SBOM{}, false, err
	}
	var parsed struct {
		SPDXID            string   `json:"SPDXID"`
		DocumentDescribes []string `json:"documentDescribes"`
		Packages          []struct {
			SPDXID       string `json:"SPDXID"`
			ExternalRefs []struct {
				Type    string `json:"referenceType"`
				Locator string `json:"referenceLocator"`
			} `json:"externalRefs"`
		} `json:"packages"`
		Relationships []struct {
			SPDXElementID      string `json:"spdxElementId"`
			Type               string `json:"relationshipType"`
			RelatedSPDXElement string `json:"relatedSpdxElement"`
		} `json:"relationships"`
	}
	if err := json.Unmarshal(document.Body, &parsed); err != nil {
		return SBOM{}, false, errors.New("decode GitHub API response")
	}
	result := SBOM{DocumentSPDXID: parsed.SPDXID, DocumentDescribes: parsed.DocumentDescribes}
	for _, item := range parsed.Packages {
		pkg := SBOMPackage{SPDXID: item.SPDXID}
		for _, ref := range item.ExternalRefs {
			if strings.EqualFold(ref.Type, "purl") {
				pkg.PURLs = append(pkg.PURLs, ref.Locator)
			}
		}
		result.Packages = append(result.Packages, pkg)
	}
	for _, relationship := range parsed.Relationships {
		result.Relationships = append(result.Relationships, SBOMRelationship{
			SPDXElementID: relationship.SPDXElementID, Type: relationship.Type, RelatedSPDXElement: relationship.RelatedSPDXElement,
		})
	}
	return result, true, nil
}
