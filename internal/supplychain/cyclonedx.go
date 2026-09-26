package supplychain

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"
)

// FormatCycloneDX16JSON is the CycloneDX 1.6 JSON document format.
const FormatCycloneDX16JSON Format = "cyclonedx-1.6-json"

// cdxLicenseChoice decodes the CycloneDX licenses array, which is either a list
// of {license:{id|name,url}} objects or a single {expression} object.
type cdxLicenseChoice struct {
	License *struct {
		ID   string `json:"id"`
		Name string `json:"name"`
		URL  string `json:"url"`
		Text *struct {
			Content string `json:"content"`
		} `json:"text"`
		Acknowledgement string `json:"acknowledgement"`
	} `json:"license"`
	Expression      string `json:"expression"`
	Acknowledgement string `json:"acknowledgement"`
}

type cdxComponent struct {
	Type     string                 `json:"type"`
	BOMRef   string                 `json:"bom-ref"`
	Group    string                 `json:"group"`
	Name     string                 `json:"name"`
	Version  *string                `json:"version"`
	PURL     string                 `json:"purl"`
	Supplier *struct{ Name string } `json:"supplier"`
	Hashes   []struct {
		Alg     string `json:"alg"`
		Content string `json:"content"`
	} `json:"hashes"`
	Licenses   []cdxLicenseChoice `json:"licenses"`
	Components []cdxComponent     `json:"components"`
	Evidence   json.RawMessage    `json:"evidence"`
}

type cdxDocument struct {
	BOMFormat    string `json:"bomFormat"`
	SpecVersion  string `json:"specVersion"`
	SerialNumber string `json:"serialNumber"`
	Version      int    `json:"version"`
	Metadata     struct {
		Timestamp string          `json:"timestamp"`
		Tools     json.RawMessage `json:"tools"`
		Component *cdxComponent   `json:"component"`
	} `json:"metadata"`
	Components   []cdxComponent `json:"components"`
	Dependencies []struct {
		Ref       string   `json:"ref"`
		DependsOn []string `json:"dependsOn"`
		Provides  []string `json:"provides"`
	} `json:"dependencies"`
}

// NormalizeCycloneDX16 turns a CycloneDX 1.6 JSON BOM into snapshot content.
// The metadata.component (if any) is the root; nested components are
// flattened into occurrences with a CONTAINS edge from their parent;
// dependencies become DEPENDS_ON edges; license choices are recorded verbatim
// (an expression, an SPDX id, or a name/url) so nothing is invented.
func NormalizeCycloneDX16(document []byte, limits Limits) (Normalized, error) {
	limits = limits.withDefaults()
	decoder := json.NewDecoder(bytes.NewReader(document))
	var parsed cdxDocument
	if err := decoder.Decode(&parsed); err != nil {
		return Normalized{}, fmt.Errorf("%w: %v", ErrMalformed, safeJSONError(err))
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return Normalized{}, fmt.Errorf("%w: trailing content", ErrMalformed)
	}
	if parsed.BOMFormat != "CycloneDX" {
		return Normalized{}, fmt.Errorf("%w: bomFormat is not CycloneDX", ErrMalformed)
	}
	if parsed.SpecVersion != "1.6" {
		if parsed.SpecVersion == "" {
			return Normalized{}, fmt.Errorf("%w: missing specVersion", ErrMalformed)
		}
		return Normalized{}, fmt.Errorf("%w: CycloneDX %s", ErrUnsupportedVersion, sanitize(parsed.SpecVersion, 16))
	}
	result := Normalized{SPDXVersion: "CycloneDX-1.6", DocumentNamespace: parsed.SerialNumber, DocumentSPDXID: "CycloneDX-BOM"}
	if parsed.Version > 0 {
		result.DocumentNamespace = strings.TrimSpace(parsed.SerialNumber + "#" + strconv.Itoa(parsed.Version))
	}
	warn := func(code, element, detail string) {
		result.WarningCount++
		if len(result.Warnings) < limits.MaxWarnings {
			result.Warnings = append(result.Warnings, Warning{Code: code, Element: sanitize(element, 200), Detail: sanitize(detail, 200)})
		}
	}
	if parsed.Metadata.Timestamp != "" {
		if created, err := time.Parse(time.RFC3339, parsed.Metadata.Timestamp); err == nil {
			utc := created.UTC()
			result.CreatedAtClaimed = &utc
		} else {
			warn("created_invalid", "", "metadata.timestamp is not RFC 3339")
		}
	} else {
		warn("created_missing", "", "metadata.timestamp is missing")
	}
	result.ProducerTool = cdxTool(parsed.Metadata.Tools)
	if result.ProducerTool == "" {
		warn("creator_tool_missing", "", "metadata.tools names no tool")
	}

	elements := map[string]int{}
	var flatten func(component cdxComponent, parent string, depth int)
	ordinal := 0
	overflow := false
	flatten = func(component cdxComponent, parent string, depth int) {
		if len(result.Components) >= limits.MaxComponents {
			overflow = true
			return
		}
		elementID := component.BOMRef
		if elementID == "" {
			elementID = "graphnest-bomref-missing-" + strconv.Itoa(ordinal)
			warn("package_id_missing", elementID, "component has no bom-ref; a synthetic document-local ID was assigned")
		}
		if _, duplicate := elements[elementID]; duplicate {
			original := elementID
			elementID = elementID + "#graphnest-duplicate-" + strconv.Itoa(ordinal)
			warn("package_id_duplicate", original, "duplicate bom-ref kept as a separate occurrence under "+elementID)
		}
		occurrence := Component{Ordinal: ordinal, ElementID: elementID, Name: component.Name, Version: component.Version, Qualifiers: map[string]string{}}
		if component.Group != "" {
			occurrence.Name = component.Group + "/" + component.Name
		}
		if component.Name == "" {
			occurrence.Name = elementID
			warn("package_name_missing", elementID, "component has no name; its bom-ref is shown instead")
		}
		if component.Version == nil || *component.Version == "" {
			warn("package_version_missing", elementID, "component has no version")
		}
		if component.Type != "" && component.Type != "library" && component.Type != "application" && component.Type != "framework" {
			occurrence.Qualifiers["cdx:type"] = component.Type
		}
		if component.Supplier != nil && component.Supplier.Name != "" {
			supplier := component.Supplier.Name
			occurrence.Supplier = &supplier
		}
		for _, hash := range component.Hashes {
			if hash.Alg != "" && hash.Content != "" {
				occurrence.Checksums = append(occurrence.Checksums, Checksum{Algorithm: hash.Alg, Value: hash.Content})
			}
		}
		if component.PURL != "" {
			locator := component.PURL
			occurrence.PURL = &locator
			if purl, err := ParsePURL(component.PURL); err == nil {
				occurrence.Ecosystem, occurrence.PURLNamespace, occurrence.PURLName, occurrence.PURLVersion = purl.Type, purl.Namespace, purl.Name, purl.Version
				for key, value := range purl.Qualifiers {
					occurrence.Qualifiers[key] = value
				}
				if occurrence.Version != nil && purl.Version != "" && *occurrence.Version != purl.Version {
					warn("package_version_mismatch", elementID, "version differs from the purl version; both are kept")
				}
			} else {
				warn("package_purl_invalid", elementID, "purl could not be parsed and is kept verbatim")
			}
		} else {
			warn("package_purl_missing", elementID, "component has no purl")
		}
		if declared, ok := cdxLicenses(component.Licenses, elementID, warn); ok {
			occurrence.LicenseDeclaredRaw = &declared
		}
		if len(component.Evidence) > 0 {
			warn("evidence_not_carried", elementID, "component evidence (license/copyright/occurrences) is retained only in the stored original document")
		}
		elements[elementID] = len(result.Components)
		result.Components = append(result.Components, occurrence)
		ordinal++
		for _, child := range component.Components {
			if depth >= 32 {
				warn("nesting_depth_exceeded", elementID, "nested components beyond depth 32 were not flattened")
				break
			}
			childBefore := len(result.Components)
			flatten(child, elementID, depth+1)
			if len(result.Components) > childBefore {
				result.Relationships = append(result.Relationships, Relationship{FromElement: elementID, Type: "CONTAINS", ToElement: result.Components[childBefore].ElementID, Resolved: true})
			}
		}
	}
	if parsed.Metadata.Component != nil {
		flatten(*parsed.Metadata.Component, "", 0)
		if len(result.Components) > 0 {
			result.Components[0].IsRoot = true
			result.RootElementIDs = append(result.RootElementIDs, result.Components[0].ElementID)
			result.DocumentName = result.Components[0].Name
		}
	}
	if len(parsed.Components) > limits.MaxComponents {
		return Normalized{}, fmt.Errorf("%w: %d components exceed the %d limit", ErrTooLarge, len(parsed.Components), limits.MaxComponents)
	}
	for _, component := range parsed.Components {
		flatten(component, "", 0)
	}
	if overflow || len(result.Components) > limits.MaxComponents {
		return Normalized{}, fmt.Errorf("%w: flattened components exceed the %d limit", ErrTooLarge, limits.MaxComponents)
	}
	if len(parsed.Dependencies) > limits.MaxRelationships {
		return Normalized{}, fmt.Errorf("%w: %d dependencies exceed the %d limit", ErrTooLarge, len(parsed.Dependencies), limits.MaxRelationships)
	}
	edgeCount := len(result.Relationships)
	for _, dependency := range parsed.Dependencies {
		_, fromKnown := elements[dependency.Ref]
		if !fromKnown {
			warn("relationship_unresolved", dependency.Ref, "dependencies entry references a bom-ref that is not a component")
		}
		for _, target := range dependency.DependsOn {
			_, toKnown := elements[target]
			resolved := fromKnown && toKnown
			if !resolved && fromKnown {
				warn("relationship_unresolved", dependency.Ref, "dependsOn references a bom-ref that is not a component")
			}
			result.Relationships = append(result.Relationships, Relationship{FromElement: dependency.Ref, Type: "DEPENDS_ON", ToElement: target, Resolved: resolved})
			edgeCount++
			if edgeCount > limits.MaxRelationships {
				return Normalized{}, fmt.Errorf("%w: dependency edges exceed the %d limit", ErrTooLarge, limits.MaxRelationships)
			}
		}
		for _, target := range dependency.Provides {
			_, toKnown := elements[target]
			result.Relationships = append(result.Relationships, Relationship{FromElement: dependency.Ref, Type: "PROVIDES", ToElement: target, Resolved: fromKnown && toKnown})
		}
	}
	if len(result.RootElementIDs) == 0 {
		warn("root_missing", "", "metadata.component is absent; dependency scope (direct vs transitive) is unavailable")
	}
	if result.Components == nil {
		result.Components = []Component{}
	}
	if result.Relationships == nil {
		result.Relationships = []Relationship{}
	}
	return result, nil
}

// cdxLicenses renders the licenses array as one raw declaration string with
// only the semantics the format carries: a single expression or SPDX id is
// kept as written. CycloneDX defines no conjunction for several license
// objects, so a list is kept semicolon-separated (which the expression parser
// reports as invalid) with a warning naming the ambiguity. Names and URLs stay
// names and URLs.
func cdxLicenses(choices []cdxLicenseChoice, element string, warn func(code, element, detail string)) (string, bool) {
	if len(choices) == 0 {
		return "", false
	}
	var parts []string
	for _, choice := range choices {
		switch {
		case choice.Expression != "":
			parts = append(parts, choice.Expression)
		case choice.License != nil && choice.License.ID != "":
			parts = append(parts, choice.License.ID)
		case choice.License != nil && choice.License.Name != "":
			name := choice.License.Name
			if choice.License.URL != "" {
				name += " (" + choice.License.URL + ")"
			}
			parts = append(parts, name)
			warn("license_name_not_spdx", element, "license is declared by name/url rather than an SPDX id or expression")
		case choice.License != nil && choice.License.URL != "":
			parts = append(parts, choice.License.URL)
			warn("license_url_only", element, "license is declared by URL only")
		case choice.License != nil && choice.License.Text != nil:
			parts = append(parts, "LicenseRef-graphnest-inline-text")
			warn("license_text_inline", element, "license is declared by inline text; the text is retained only in the stored original document")
		}
		if (choice.License != nil && choice.License.Acknowledgement != "") || choice.Acknowledgement != "" {
			acknowledgement := choice.Acknowledgement
			if acknowledgement == "" {
				acknowledgement = choice.License.Acknowledgement
			}
			warn("license_acknowledgement", element, "license acknowledgement is "+acknowledgement)
		}
	}
	if len(parts) == 0 {
		return "", false
	}
	if len(parts) > 1 {
		warn("license_list_ambiguous", element, "several license objects have no defined AND/OR meaning in CycloneDX; the list is kept without invented structure")
		return strings.Join(parts, "; "), true
	}
	return parts[0], true
}

// cdxTool extracts the first tool name/version from either the 1.5+ object
// form or the legacy array form.
func cdxTool(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var object struct {
		Components []struct {
			Name    string `json:"name"`
			Version string `json:"version"`
		} `json:"components"`
	}
	if err := json.Unmarshal(raw, &object); err == nil && len(object.Components) > 0 {
		return strings.TrimSpace(object.Components[0].Name + " " + object.Components[0].Version)
	}
	var legacy []struct {
		Vendor  string `json:"vendor"`
		Name    string `json:"name"`
		Version string `json:"version"`
	}
	if err := json.Unmarshal(raw, &legacy); err == nil && len(legacy) > 0 {
		return strings.TrimSpace(strings.TrimSpace(legacy[0].Vendor+" "+legacy[0].Name) + " " + legacy[0].Version)
	}
	return ""
}
