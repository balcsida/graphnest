package githubapp

import (
	"context"
	"errors"
	"net/http"
	"strings"
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

func (c *Client) DependencySBOM(ctx context.Context, installationID int64, owner, name string) (SBOM, bool, error) {
	var response struct {
		SBOM struct {
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
		} `json:"sbom"`
	}
	_, err := c.doInstallationJSON(ctx, "dependency_sbom", installationID, c.apiURL("repos", owner, name, "dependency-graph", "sbom"), c.maxBytes, &response)
	var statusError HTTPStatusError
	if errors.As(err, &statusError) && (statusError.StatusCode == http.StatusForbidden || statusError.StatusCode == http.StatusNotFound) {
		return SBOM{}, false, nil
	}
	if err != nil {
		return SBOM{}, false, err
	}

	result := SBOM{DocumentSPDXID: response.SBOM.SPDXID, DocumentDescribes: response.SBOM.DocumentDescribes}
	for _, item := range response.SBOM.Packages {
		pkg := SBOMPackage{SPDXID: item.SPDXID}
		for _, ref := range item.ExternalRefs {
			if strings.EqualFold(ref.Type, "purl") {
				pkg.PURLs = append(pkg.PURLs, ref.Locator)
			}
		}
		result.Packages = append(result.Packages, pkg)
	}
	for _, relationship := range response.SBOM.Relationships {
		result.Relationships = append(result.Relationships, SBOMRelationship{
			SPDXElementID: relationship.SPDXElementID, Type: relationship.Type, RelatedSPDXElement: relationship.RelatedSPDXElement,
		})
	}
	return result, true, nil
}
