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

// spdxDocument is the subset of SPDX 2.3 JSON the normalizer reads. Unknown
// members are neither decoded nor lost: the stored document keeps them.
type spdxDocument struct {
	SPDXVersion       string   `json:"spdxVersion"`
	DataLicense       string   `json:"dataLicense"`
	SPDXID            string   `json:"SPDXID"`
	Name              string   `json:"name"`
	DocumentNamespace string   `json:"documentNamespace"`
	DocumentDescribes []string `json:"documentDescribes"`
	CreationInfo      struct {
		Created  string   `json:"created"`
		Creators []string `json:"creators"`
	} `json:"creationInfo"`
	Packages []struct {
		SPDXID           string  `json:"SPDXID"`
		Name             string  `json:"name"`
		VersionInfo      *string `json:"versionInfo"`
		Supplier         *string `json:"supplier"`
		DownloadLocation *string `json:"downloadLocation"`
		LicenseConcluded *string `json:"licenseConcluded"`
		LicenseDeclared  *string `json:"licenseDeclared"`
		Checksums        []struct {
			Algorithm string `json:"algorithm"`
			Value     string `json:"checksumValue"`
		} `json:"checksums"`
		ExternalRefs []struct {
			Category string `json:"referenceCategory"`
			Type     string `json:"referenceType"`
			Locator  string `json:"referenceLocator"`
		} `json:"externalRefs"`
	} `json:"packages"`
	Relationships []struct {
		SPDXElementID      string `json:"spdxElementId"`
		Type               string `json:"relationshipType"`
		RelatedSPDXElement string `json:"relatedSpdxElement"`
	} `json:"relationships"`
}

// NormalizeSPDX23 turns an SPDX 2.3 JSON document into snapshot content. It
// keeps every package as an occurrence (PURL or not, version or not), keeps
// every relationship with its direction, finds roots through both
// documentDescribes and DESCRIBES relationships, and reports structural
// problems as warnings rather than inventing data. Ambiguity that would make
// the inventory wrong (no packages array, duplicate document IDs that cannot be
// disambiguated, unsupported version) is an error.
func NormalizeSPDX23(document []byte, limits Limits) (Normalized, error) {
	limits = limits.withDefaults()
	decoder := json.NewDecoder(bytes.NewReader(document))
	var parsed spdxDocument
	if err := decoder.Decode(&parsed); err != nil {
		return Normalized{}, fmt.Errorf("%w: %v", ErrMalformed, safeJSONError(err))
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return Normalized{}, fmt.Errorf("%w: trailing content", ErrMalformed)
	}
	if !strings.EqualFold(parsed.SPDXVersion, "SPDX-2.3") {
		if parsed.SPDXVersion == "" {
			return Normalized{}, fmt.Errorf("%w: missing spdxVersion", ErrMalformed)
		}
		return Normalized{}, fmt.Errorf("%w: %s", ErrUnsupportedVersion, sanitize(parsed.SPDXVersion, 32))
	}
	if len(parsed.Packages) > limits.MaxComponents {
		return Normalized{}, fmt.Errorf("%w: %d packages exceed the %d limit", ErrTooLarge, len(parsed.Packages), limits.MaxComponents)
	}
	if len(parsed.Relationships) > limits.MaxRelationships {
		return Normalized{}, fmt.Errorf("%w: %d relationships exceed the %d limit", ErrTooLarge, len(parsed.Relationships), limits.MaxRelationships)
	}

	result := Normalized{
		SPDXVersion: "SPDX-2.3", DataLicense: parsed.DataLicense, DocumentSPDXID: parsed.SPDXID,
		DocumentName: parsed.Name, DocumentNamespace: parsed.DocumentNamespace,
	}
	warn := func(code, element, detail string) {
		result.WarningCount++
		if len(result.Warnings) < limits.MaxWarnings {
			result.Warnings = append(result.Warnings, Warning{Code: code, Element: sanitize(element, 200), Detail: sanitize(detail, 200)})
		}
	}
	if parsed.SPDXID == "" {
		warn("document_id_missing", "", "SPDXID is missing; SPDXRef-DOCUMENT is assumed for relationship resolution")
		result.DocumentSPDXID = "SPDXRef-DOCUMENT"
	}
	if parsed.CreationInfo.Created != "" {
		if created, err := time.Parse(time.RFC3339, parsed.CreationInfo.Created); err == nil {
			utc := created.UTC()
			result.CreatedAtClaimed = &utc
		} else {
			warn("created_invalid", "", "creationInfo.created is not RFC 3339")
		}
	} else {
		warn("created_missing", "", "creationInfo.created is missing")
	}
	for _, creator := range parsed.CreationInfo.Creators {
		if tool, ok := strings.CutPrefix(creator, "Tool:"); ok {
			result.ProducerTool = strings.TrimSpace(tool)
			break
		}
	}
	if result.ProducerTool == "" {
		warn("creator_tool_missing", "", "creationInfo.creators names no Tool")
	}

	elements := map[string]int{result.DocumentSPDXID: -1}
	result.Components = make([]Component, 0, len(parsed.Packages))
	for index, pkg := range parsed.Packages {
		elementID := pkg.SPDXID
		if elementID == "" {
			elementID = "SPDXRef-graphnest-missing-" + strconv.Itoa(index)
			warn("package_id_missing", elementID, "package has no SPDXID; a synthetic document-local ID was assigned")
		}
		if _, duplicate := elements[elementID]; duplicate {
			original := elementID
			elementID = elementID + "#graphnest-duplicate-" + strconv.Itoa(index)
			warn("package_id_duplicate", original, "duplicate SPDXID kept as a separate occurrence under "+elementID)
		}
		component := Component{Ordinal: index, ElementID: elementID, Name: pkg.Name, Version: pkg.VersionInfo,
			LicenseDeclaredRaw: pkg.LicenseDeclared, LicenseConcludedRaw: pkg.LicenseConcluded,
			DownloadLocation: pkg.DownloadLocation, Supplier: pkg.Supplier, Qualifiers: map[string]string{}}
		if pkg.Name == "" {
			component.Name = elementID
			warn("package_name_missing", elementID, "package has no name; its SPDXID is shown instead")
		}
		if pkg.VersionInfo == nil || *pkg.VersionInfo == "" {
			warn("package_version_missing", elementID, "package has no versionInfo")
		}
		for _, checksum := range pkg.Checksums {
			if checksum.Algorithm != "" && checksum.Value != "" {
				component.Checksums = append(component.Checksums, Checksum{Algorithm: checksum.Algorithm, Value: checksum.Value})
			}
		}
		purls := 0
		for _, ref := range pkg.ExternalRefs {
			if !strings.EqualFold(ref.Type, "purl") {
				continue
			}
			purls++
			if purls > 1 {
				warn("package_purl_multiple", elementID, "package has more than one purl; the first is used")
				continue
			}
			purl, err := ParsePURL(ref.Locator)
			if err != nil {
				warn("package_purl_invalid", elementID, "purl could not be parsed and is kept verbatim")
				locator := ref.Locator
				component.PURL = &locator
				continue
			}
			locator := ref.Locator
			component.PURL = &locator
			component.Ecosystem, component.PURLNamespace, component.PURLName, component.PURLVersion = purl.Type, purl.Namespace, purl.Name, purl.Version
			for key, value := range purl.Qualifiers {
				component.Qualifiers[key] = value
			}
			if component.Version != nil && purl.Version != "" && *component.Version != purl.Version {
				warn("package_version_mismatch", elementID, "versionInfo differs from the purl version; both are kept")
			}
		}
		if purls == 0 {
			warn("package_purl_missing", elementID, "package has no purl external reference")
		}
		elements[elementID] = len(result.Components)
		result.Components = append(result.Components, component)
	}

	roots := map[string]bool{}
	addRoot := func(id, via string) {
		if id == "" {
			return
		}
		if id == result.DocumentSPDXID {
			warn("root_is_document", id, via+" names the document itself")
			return
		}
		if _, ok := elements[id]; !ok {
			warn("root_unresolved", id, via+" names an element that is not a package")
			return
		}
		roots[id] = true
	}
	for _, id := range parsed.DocumentDescribes {
		addRoot(id, "documentDescribes")
	}
	result.Relationships = make([]Relationship, 0, len(parsed.Relationships))
	for _, relationship := range parsed.Relationships {
		relationshipType := strings.ToUpper(strings.TrimSpace(relationship.Type))
		if relationship.SPDXElementID == "" || relationship.RelatedSPDXElement == "" || relationshipType == "" {
			warn("relationship_incomplete", relationship.SPDXElementID, "relationship is missing an element or type and was kept unresolved")
		}
		_, fromKnown := elements[relationship.SPDXElementID]
		_, toKnown := elements[relationship.RelatedSPDXElement]
		toKnown = toKnown || relationship.RelatedSPDXElement == "NOASSERTION" || relationship.RelatedSPDXElement == "NONE"
		resolved := fromKnown && toKnown && relationshipType != ""
		if !resolved && relationshipType != "" {
			warn("relationship_unresolved", relationship.SPDXElementID, relationshipType+" references an element that is not in the document")
		}
		result.Relationships = append(result.Relationships, Relationship{
			FromElement: relationship.SPDXElementID, Type: relationshipType, ToElement: relationship.RelatedSPDXElement, Resolved: resolved,
		})
		if relationshipType == "DESCRIBES" && relationship.SPDXElementID == result.DocumentSPDXID {
			addRoot(relationship.RelatedSPDXElement, "DESCRIBES")
		}
		if relationshipType == "DESCRIBED_BY" && relationship.RelatedSPDXElement == result.DocumentSPDXID {
			addRoot(relationship.SPDXElementID, "DESCRIBED_BY")
		}
	}
	for index := range result.Components {
		if roots[result.Components[index].ElementID] {
			result.Components[index].IsRoot = true
			result.RootElementIDs = append(result.RootElementIDs, result.Components[index].ElementID)
		}
	}
	if len(result.RootElementIDs) == 0 {
		warn("root_missing", "", "no package is described by the document; dependency scope (direct vs transitive) is unavailable")
	}
	return result, nil
}

// safeJSONError strips offsets and quoted input from decoder errors so a
// diagnostic never echoes document content.
func safeJSONError(err error) string {
	var syntax *json.SyntaxError
	if errors.As(err, &syntax) {
		return "invalid JSON syntax"
	}
	var typeErr *json.UnmarshalTypeError
	if errors.As(err, &typeErr) {
		return "unexpected JSON type for " + sanitize(typeErr.Field, 64)
	}
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
		return "empty or truncated JSON"
	}
	return "invalid JSON"
}

func sanitize(value string, max int) string {
	value = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return ' '
		}
		return r
	}, value)
	if len(value) > max {
		return value[:max]
	}
	return value
}
