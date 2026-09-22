package license

import (
	"context"
	"encoding/json"
	"strings"
)

// NPMResolver reads the exact version document
// GET {base}/{name}/{version} (scoped names are one percent-encoded segment).
// It never reads dist-tags or the "latest" document, and it treats legacy
// {"type":..,"url":..} objects, arrays, "UNLICENSED", and "SEE LICENSE IN"
// conservatively: they are recorded as what they are, not as a license.
type NPMResolver struct {
	Route   Route
	Fetcher *Fetcher
}

func NewNPMResolver(route Route) (*NPMResolver, error) {
	fetcher, err := NewFetcher(route)
	if err != nil {
		return nil, err
	}
	return &NPMResolver{Route: route, Fetcher: fetcher}, nil
}

func (resolver *NPMResolver) Ecosystem() string { return "npm" }

func (resolver *NPMResolver) Resolve(ctx context.Context, coordinates Coordinates) (Evidence, error) {
	evidence := Evidence{Source: SourceRegistryNPM, Route: resolver.Route.Name, Coordinates: coordinates, FetchedAt: now()}
	if coordinates.Version == "" || coordinates.Name == "" {
		return negative(evidence, OutcomeRejected, nil, "exact version and name are required"), nil
	}
	if !resolver.Route.ServesNamespace(coordinates.Namespace) {
		return negative(evidence, OutcomeRejected, nil, "package scope is not served by the configured route"), nil
	}
	name := coordinates.Name
	if coordinates.Namespace != "" {
		// The registry expects "@scope/name" as one segment with the slash
		// percent-encoded; the fetcher escapes "%" again unless told the
		// segment is pre-encoded, so mark it.
		name = coordinates.Namespace + "%2F" + coordinates.Name
	}
	response, err := resolver.Fetcher.Get(ctx, "application/json", name, coordinates.Version)
	if err != nil {
		return fromFetchError(evidence, err), nil
	}
	evidence.FetchedAt = response.FetchedAt
	evidence, ok := statusOutcome(evidence, response)
	if !ok {
		return evidence, nil
	}
	var document struct {
		Name     string          `json:"name"`
		Version  string          `json:"version"`
		License  json.RawMessage `json:"license"`
		Licenses json.RawMessage `json:"licenses"`
		Dist     struct {
			Integrity string `json:"integrity"`
			Shasum    string `json:"shasum"`
		} `json:"dist"`
	}
	if err := json.Unmarshal(response.Body, &document); err != nil {
		return negative(evidence, OutcomeMalformed, &response.Status, "registry document is not valid JSON"), nil
	}
	if document.Version != "" && document.Version != coordinates.Version {
		// A registry that answers a different version (dist-tag resolution,
		// redirect to latest) must not become evidence for the requested one.
		return negative(evidence, OutcomeRejected, &response.Status, "registry answered a different version than requested"), nil
	}
	evidence.ContentSHA256 = contentHash(response.Body)
	evidence.Outcome = OutcomeResolved
	evidence.Detail = boundedDetail(map[string]any{"name": truncate(document.Name, 200), "version": truncate(document.Version, 100), "integrity": truncate(document.Dist.Integrity, 200)})
	status := response.Status
	evidence.HTTPStatus = &status

	raw, kind := npmLicenseValue(document.License, document.Licenses)
	switch kind {
	case RawMissing:
		evidence.Outcome = OutcomeNoLicenseMetadata
		evidence.RawKind = RawMissing
		evidence.ParseStatus = NotApplicable
		evidence.ResolverVersion = ResolverVersion
		evidence.LicenseListVersion = ListVersion()
		evidence.Message = "package.json declares no license"
		expires := evidence.FetchedAt.Add(NegativeTTL)
		evidence.ExpiresAt = &expires
		return evidence, nil
	case RawLicenseFile:
		// "SEE LICENSE IN <file>": a pointer to a file, not an expression.
		classify(&evidence, raw, RawLicenseFile)
		evidence.LicenseFileName = strings.TrimSpace(strings.TrimPrefix(raw, "SEE LICENSE IN"))
		evidence.Message = "license is declared by reference to a file in the package; the expression is unknown until the file is reviewed"
		return evidence, nil
	case RawLegacyObject:
		// Legacy {type,url} / [{type,url}] metadata: keep the type as a
		// candidate name and the URL, but do not treat the URL as a license.
		classify(&evidence, raw, RawLicenseName)
		evidence.RawKind = RawLegacyObject
		return evidence, nil
	default:
		classify(&evidence, raw, RawExpression)
		return evidence, nil
	}
}

// npmLicenseValue reduces the license/licenses members to a raw value and
// kind. Only a plain string license is an expression candidate.
func npmLicenseValue(license, licenses json.RawMessage) (string, RawKind) {
	if len(license) > 0 && string(license) != "null" {
		var text string
		if err := json.Unmarshal(license, &text); err == nil {
			text = strings.TrimSpace(text)
			switch {
			case text == "":
				return "", RawMissing
			case strings.HasPrefix(strings.ToUpper(text), "SEE LICENSE IN"):
				return text, RawLicenseFile
			default:
				return text, RawExpression
			}
		}
		var object struct {
			Type string `json:"type"`
			URL  string `json:"url"`
		}
		if err := json.Unmarshal(license, &object); err == nil && (object.Type != "" || object.URL != "") {
			return legacyValue([]struct{ Type, URL string }{{object.Type, object.URL}}), RawLegacyObject
		}
	}
	if len(licenses) > 0 && string(licenses) != "null" {
		var objects []struct {
			Type string `json:"type"`
			URL  string `json:"url"`
		}
		if err := json.Unmarshal(licenses, &objects); err == nil && len(objects) > 0 {
			items := make([]struct{ Type, URL string }, 0, len(objects))
			for _, object := range objects {
				items = append(items, struct{ Type, URL string }{object.Type, object.URL})
			}
			return legacyValue(items), RawLegacyObject
		}
		var names []string
		if err := json.Unmarshal(licenses, &names); err == nil && len(names) > 0 {
			// An array of names is ambiguous: SPDX gives it no AND/OR meaning.
			// Keep the list verbatim; it will parse as invalid and stay visible.
			return strings.Join(names, ", "), RawLegacyObject
		}
	}
	return "", RawMissing
}

func legacyValue(items []struct{ Type, URL string }) string {
	parts := make([]string, 0, len(items))
	for _, item := range items {
		part := strings.TrimSpace(item.Type)
		if url := strings.TrimSpace(item.URL); url != "" {
			if part != "" {
				part += " "
			}
			part += "(" + url + ")"
		}
		if part != "" {
			parts = append(parts, part)
		}
	}
	return strings.Join(parts, ", ")
}
