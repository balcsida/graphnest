package license

import (
	"context"
	"encoding/xml"
	"fmt"
	"regexp"
	"strings"

	"github.com/balcsida/graphnest/internal/supplychain/spdxexpr"
)

// MavenResolver reads the exact-version POM from the configured repository:
// GET {base}/{group/as/path}/{artifact}/{version}/{artifact}-{version}.pom.
// Licenses are inherited from parents; resolution follows <parent> through the
// same route only, at most maxParentDepth levels, detects cycles, and bounds
// ${property} expansion. Unresolved inheritance stays unknown rather than
// guessed. Repository declarations inside POMs are never followed.
type MavenResolver struct {
	Route   Route
	Fetcher *Fetcher
}

const maxParentDepth = 8

func NewMavenResolver(route Route) (*MavenResolver, error) {
	fetcher, err := NewFetcher(route)
	if err != nil {
		return nil, err
	}
	return &MavenResolver{Route: route, Fetcher: fetcher}, nil
}

func (resolver *MavenResolver) Ecosystem() string { return "maven" }

type pom struct {
	GroupID    string `xml:"groupId"`
	ArtifactID string `xml:"artifactId"`
	Version    string `xml:"version"`
	Parent     struct {
		GroupID    string `xml:"groupId"`
		ArtifactID string `xml:"artifactId"`
		Version    string `xml:"version"`
	} `xml:"parent"`
	Licenses []struct {
		Name string `xml:"name"`
		URL  string `xml:"url"`
	} `xml:"licenses>license"`
	Properties struct {
		Entries []xmlEntry `xml:",any"`
	} `xml:"properties"`
}

type xmlEntry struct {
	XMLName xml.Name
	Value   string `xml:",chardata"`
}

var mavenCoordinate = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)

func (resolver *MavenResolver) Resolve(ctx context.Context, coordinates Coordinates) (Evidence, error) {
	evidence := Evidence{Source: SourceRegistryMaven, Route: resolver.Route.Name, Coordinates: coordinates, FetchedAt: now()}
	if coordinates.Version == "" || coordinates.Name == "" || coordinates.Namespace == "" {
		return negative(evidence, OutcomeRejected, nil, "exact groupId, artifactId, and version are required"), nil
	}
	if !resolver.Route.ServesNamespace(coordinates.Namespace) {
		return negative(evidence, OutcomeRejected, nil, "groupId is not served by the configured route"), nil
	}
	if !mavenCoordinate.MatchString(coordinates.Namespace) || !mavenCoordinate.MatchString(coordinates.Name) || !mavenCoordinate.MatchString(coordinates.Version) {
		return negative(evidence, OutcomeRejected, nil, "coordinates contain characters outside the Maven identifier alphabet"), nil
	}
	document, response, err := resolver.fetchPOM(ctx, coordinates.Namespace, coordinates.Name, coordinates.Version)
	if err != nil {
		return fromFetchError(evidence, err), nil
	}
	evidence.FetchedAt = response.FetchedAt
	evidence, ok := statusOutcome(evidence, response)
	if !ok {
		return evidence, nil
	}
	if document == nil {
		return negative(evidence, OutcomeMalformed, &response.Status, "POM is not well-formed XML"), nil
	}
	status := response.Status
	evidence.HTTPStatus = &status
	evidence.ContentSHA256 = contentHash(response.Body)
	evidence.Outcome = OutcomeResolved
	evidence.Detail = boundedDetail(map[string]any{"groupId": truncate(document.GroupID, 200), "artifactId": truncate(document.ArtifactID, 200), "version": truncate(document.Version, 100)})

	// Walk the parent chain until a <licenses> element is found.
	properties := map[string]string{}
	chain := []string{}
	current := document
	depth := 0
	for {
		collectProperties(current, properties)
		key := fmt.Sprintf("%s:%s:%s", current.GroupID, current.ArtifactID, current.Version)
		for _, seen := range chain {
			if seen == key && key != "::" {
				evidence.Message = "parent chain is cyclic; license inheritance is unresolved"
				evidence.Detail["parent_chain"] = chain
				return withoutLicense(evidence), nil
			}
		}
		chain = append(chain, key)
		if len(current.Licenses) > 0 {
			break
		}
		if current.Parent.ArtifactID == "" {
			evidence.Message = "no <licenses> element in the POM or its parents"
			evidence.Detail["parent_chain"] = chain
			return withoutLicense(evidence), nil
		}
		depth++
		if depth > maxParentDepth {
			evidence.Message = "parent chain exceeds the resolution depth; license inheritance is unresolved"
			evidence.Detail["parent_chain"] = chain
			return withoutLicense(evidence), nil
		}
		parentGroup := expand(current.Parent.GroupID, properties)
		parentArtifact := expand(current.Parent.ArtifactID, properties)
		parentVersion := expand(current.Parent.Version, properties)
		if strings.Contains(parentGroup+parentArtifact+parentVersion, "${") || !mavenCoordinate.MatchString(parentGroup) || !mavenCoordinate.MatchString(parentArtifact) || !mavenCoordinate.MatchString(parentVersion) {
			evidence.Message = "parent coordinates could not be resolved; license inheritance is unresolved"
			evidence.Detail["parent_chain"] = chain
			return withoutLicense(evidence), nil
		}
		if !resolver.Route.ServesNamespace(parentGroup) {
			evidence.Message = "parent groupId is outside the configured route; license inheritance is unresolved"
			evidence.Detail["parent_chain"] = chain
			return withoutLicense(evidence), nil
		}
		parent, parentResponse, err := resolver.fetchPOM(ctx, parentGroup, parentArtifact, parentVersion)
		if err != nil || parent == nil || parentResponse.Status != 200 {
			evidence.Message = "parent POM could not be fetched from the configured route; license inheritance is unresolved"
			evidence.Detail["parent_chain"] = chain
			return withoutLicense(evidence), nil
		}
		current = parent
	}
	evidence.Detail["parent_chain"] = chain

	// Maven licenses are names plus URLs, not SPDX expressions. Normalize only
	// unambiguous single names; several licenses have no defined AND/OR
	// semantics and stay a name list with unknown structure.
	names := make([]string, 0, len(current.Licenses))
	urls := make([]string, 0, len(current.Licenses))
	for _, item := range current.Licenses {
		name := truncate(expand(item.Name, properties), 200)
		if name != "" {
			names = append(names, name)
		}
		if url := truncate(expand(item.URL, properties), 500); url != "" {
			urls = append(urls, url)
		}
	}
	if len(urls) > 0 {
		evidence.LicenseURL = urls[0]
	}
	switch {
	case len(names) == 0 && len(urls) > 0:
		classify(&evidence, urls[0], RawLicenseURL)
		evidence.Message = "license is declared by URL only; a URL is not a concluded SPDX license"
	case len(names) == 1:
		raw := names[0]
		if canonical, ok := mavenNameToSPDX(raw); ok {
			classify(&evidence, canonical, RawLicenseName)
			evidence.RawValue = raw
			evidence.Detail["normalized_from_name"] = true
		} else {
			classify(&evidence, raw, RawLicenseName)
		}
	default:
		classify(&evidence, strings.Join(names, "; "), RawLicenseName)
		evidence.Message = "multiple <license> elements have no defined AND/OR semantics; the list is kept without invented structure"
		evidence.ParseStatus = spdxexpr.StatusInvalid
		evidence.NormalizedExpression, evidence.ExpressionTree, evidence.UnknownTerms = "", nil, nil
		evidence.Detail["license_names"] = names
	}
	return evidence, nil
}

func withoutLicense(evidence Evidence) Evidence {
	evidence.Outcome = OutcomeNoLicenseMetadata
	evidence.RawKind = RawMissing
	evidence.ParseStatus = NotApplicable
	evidence.ResolverVersion = ResolverVersion
	evidence.LicenseListVersion = ListVersion()
	expires := evidence.FetchedAt.Add(NegativeTTL)
	evidence.ExpiresAt = &expires
	return evidence
}

func (resolver *MavenResolver) fetchPOM(ctx context.Context, group, artifact, version string) (*pom, Response, error) {
	segments := strings.Split(group, ".")
	segments = append(segments, artifact, version, artifact+"-"+version+".pom")
	response, err := resolver.Fetcher.Get(ctx, "application/xml", segments...)
	if err != nil {
		return nil, Response{}, err
	}
	if response.Status != 200 {
		return nil, response, nil
	}
	var document pom
	decoder := xml.NewDecoder(strings.NewReader(string(response.Body)))
	decoder.Strict = true
	if err := decoder.Decode(&document); err != nil {
		return nil, response, nil
	}
	// Inherit missing groupId/version from the parent declaration, as Maven does.
	if document.GroupID == "" {
		document.GroupID = document.Parent.GroupID
	}
	if document.Version == "" {
		document.Version = document.Parent.Version
	}
	return &document, response, nil
}

func collectProperties(document *pom, properties map[string]string) {
	// Child values win over parent values; only set keys not yet present.
	if _, ok := properties["project.groupId"]; !ok && document.GroupID != "" {
		properties["project.groupId"] = document.GroupID
	}
	if _, ok := properties["project.version"]; !ok && document.Version != "" {
		properties["project.version"] = document.Version
	}
	for _, entry := range document.Properties.Entries {
		if _, ok := properties[entry.XMLName.Local]; !ok && len(properties) < 512 {
			properties[entry.XMLName.Local] = strings.TrimSpace(entry.Value)
		}
	}
}

var propertyPattern = regexp.MustCompile(`\$\{([A-Za-z0-9._-]+)\}`)

// expand substitutes ${property} references, bounded to a few passes so
// self-referential properties cannot loop. Unknown properties are left as
// written, which callers treat as unresolved.
func expand(value string, properties map[string]string) string {
	value = strings.TrimSpace(value)
	for pass := 0; pass < 4 && strings.Contains(value, "${"); pass++ {
		value = propertyPattern.ReplaceAllStringFunc(value, func(match string) string {
			key := match[2 : len(match)-1]
			if replacement, ok := properties[key]; ok {
				return replacement
			}
			return match
		})
	}
	return value
}

// mavenNameToSPDX maps only unambiguous, widely used Maven license names to
// SPDX identifiers. Anything else stays a name.
func mavenNameToSPDX(name string) (string, bool) {
	normalized := strings.ToLower(strings.Join(strings.Fields(strings.ReplaceAll(strings.ReplaceAll(name, ",", " "), "-", " ")), " "))
	switch normalized {
	case "apache license version 2.0", "the apache software license version 2.0", "apache 2.0", "apache license 2.0", "the apache license version 2.0", "apache software license version 2.0", "apache 2", "asl 2.0":
		return "Apache-2.0", true
	case "mit license", "the mit license", "mit":
		return "MIT", true
	case "bsd 3 clause", "bsd 3 clause license", "the bsd 3 clause license", "bsd 3 clause \"new\" or \"revised\" license", "new bsd license", "the new bsd license", "bsd new":
		return "BSD-3-Clause", true
	case "bsd 2 clause", "bsd 2 clause license", "the bsd 2 clause license", "simplified bsd license":
		return "BSD-2-Clause", true
	case "eclipse public license version 2.0", "eclipse public license v2.0", "epl 2.0", "eclipse public license 2.0":
		return "EPL-2.0", true
	case "eclipse public license version 1.0", "eclipse public license v1.0", "epl 1.0", "eclipse public license 1.0":
		return "EPL-1.0", true
	case "gnu lesser general public license version 2.1", "lgpl 2.1", "gnu lesser general public license v2.1":
		return "LGPL-2.1-only", true
	case "gnu lesser general public license version 3", "lgpl 3.0", "gnu lesser general public license v3", "gnu lesser general public license version 3.0":
		return "LGPL-3.0-only", true
	case "mozilla public license version 2.0", "mpl 2.0", "mozilla public license 2.0":
		return "MPL-2.0", true
	case "isc license", "isc":
		return "ISC", true
	case "cddl 1.0", "common development and distribution license (cddl) v1.0":
		return "CDDL-1.0", true
	case "the unlicense", "unlicense":
		return "Unlicense", true
	case "cc0 1.0 universal", "cc0 1.0", "public domain via cc0":
		return "CC0-1.0", true
	case "gnu general public license version 2 with the classpath exception", "gpl2 w/ cpe", "gplv2 with classpath exception", "gpl 2.0 with classpath exception":
		return "GPL-2.0-only WITH Classpath-exception-2.0", true
	}
	// A name that already is a valid SPDX identifier maps to itself.
	if parsed := spdxexpr.Parse(name); parsed.Status == spdxexpr.StatusParsed {
		return parsed.Normalized, true
	}
	return "", false
}
