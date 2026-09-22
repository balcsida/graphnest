package license

import (
	"context"
	"encoding/xml"
	"strings"
)

// NuGetResolver reads the exact-version nuspec through the V3 flat container:
// GET {base}/{id-lower}/{version-lower}/{id-lower}.nuspec. It preserves the
// <license type="expression"> versus <license type="file"> distinction and
// treats a legacy <licenseUrl> alone as a URL, not a concluded license. It
// never downloads or unpacks the .nupkg.
type NuGetResolver struct {
	Route   Route
	Fetcher *Fetcher
}

func NewNuGetResolver(route Route) (*NuGetResolver, error) {
	fetcher, err := NewFetcher(route)
	if err != nil {
		return nil, err
	}
	return &NuGetResolver{Route: route, Fetcher: fetcher}, nil
}

func (resolver *NuGetResolver) Ecosystem() string { return "nuget" }

type nuspec struct {
	Metadata struct {
		ID      string `xml:"id"`
		Version string `xml:"version"`
		License struct {
			Type    string `xml:"type,attr"`
			Version string `xml:"version,attr"`
			Value   string `xml:",chardata"`
		} `xml:"license"`
		LicenseURL string `xml:"licenseUrl"`
	} `xml:"metadata"`
}

func (resolver *NuGetResolver) Resolve(ctx context.Context, coordinates Coordinates) (Evidence, error) {
	evidence := Evidence{Source: SourceRegistryNuGet, Route: resolver.Route.Name, Coordinates: coordinates, FetchedAt: now()}
	if coordinates.Version == "" || coordinates.Name == "" {
		return negative(evidence, OutcomeRejected, nil, "exact version and package id are required"), nil
	}
	if !resolver.Route.ServesNamespace(coordinates.Name) {
		return negative(evidence, OutcomeRejected, nil, "package id is not served by the configured route"), nil
	}
	id, version := strings.ToLower(coordinates.Name), strings.ToLower(coordinates.Version)
	response, err := resolver.Fetcher.Get(ctx, "application/xml", id, version, id+".nuspec")
	if err != nil {
		return fromFetchError(evidence, err), nil
	}
	evidence.FetchedAt = response.FetchedAt
	evidence, ok := statusOutcome(evidence, response)
	if !ok {
		return evidence, nil
	}
	var document nuspec
	decoder := xml.NewDecoder(strings.NewReader(string(response.Body)))
	decoder.Strict = true
	// External entities and DTDs are not resolved by encoding/xml; CharsetReader is nil so only UTF-8 is accepted.
	if err := decoder.Decode(&document); err != nil {
		return negative(evidence, OutcomeMalformed, &response.Status, "nuspec is not well-formed XML"), nil
	}
	if document.Metadata.ID != "" && !strings.EqualFold(document.Metadata.ID, coordinates.Name) || document.Metadata.Version != "" && !strings.EqualFold(strings.TrimSpace(document.Metadata.Version), coordinates.Version) {
		return negative(evidence, OutcomeRejected, &response.Status, "nuspec identifies a different package or version than requested"), nil
	}
	status := response.Status
	evidence.HTTPStatus = &status
	evidence.ContentSHA256 = contentHash(response.Body)
	evidence.Outcome = OutcomeResolved
	evidence.Detail = boundedDetail(map[string]any{"id": truncate(document.Metadata.ID, 200), "version": truncate(document.Metadata.Version, 100), "license_type": truncate(document.Metadata.License.Type, 32)})
	licenseValue := strings.TrimSpace(document.Metadata.License.Value)
	switch strings.ToLower(document.Metadata.License.Type) {
	case "expression":
		classify(&evidence, licenseValue, RawExpression)
		if document.Metadata.License.Version != "" {
			evidence.Detail["license_expression_version"] = truncate(document.Metadata.License.Version, 16)
		}
	case "file":
		classify(&evidence, licenseValue, RawLicenseFile)
		evidence.LicenseFileName = licenseValue
		evidence.Message = "license is embedded as a file in the package; the expression is unknown until the file is reviewed"
	default:
		if url := strings.TrimSpace(document.Metadata.LicenseURL); url != "" {
			classify(&evidence, url, RawLicenseURL)
			evidence.LicenseURL = url
			evidence.Message = "only a legacy licenseUrl is declared; a URL is not a concluded SPDX license"
			return evidence, nil
		}
		evidence.Outcome = OutcomeNoLicenseMetadata
		evidence.RawKind = RawMissing
		evidence.ParseStatus = NotApplicable
		evidence.ResolverVersion = ResolverVersion
		evidence.LicenseListVersion = ListVersion()
		evidence.Message = "nuspec declares no license"
		expires := evidence.FetchedAt.Add(NegativeTTL)
		evidence.ExpiresAt = &expires
	}
	if url := strings.TrimSpace(document.Metadata.LicenseURL); url != "" && evidence.LicenseURL == "" {
		evidence.LicenseURL = url
	}
	return evidence, nil
}
