package supplychain

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/balcsida/graphnest/internal/authn"
	"github.com/balcsida/graphnest/internal/supplychain/license"
	"github.com/jackc/pgx/v5"
)

// DerivedSPDXVersion identifies the GraphNest-produced document shape.
const DerivedSPDXVersion = "SPDX-2.3"

var spdxIDUnsafe = regexp.MustCompile(`[^A-Za-z0-9.-]`)

// ExportSPDX writes a derived SPDX 2.3 JSON document for one authorized
// snapshot with GraphNest as the creator. It never mutates or re-serves the
// original: the original stays downloadable as-is, and this document names
// it (snapshot ID, SHA-256, producer, collected time) as an external
// reference so the two are never confused. Assessments are emitted as
// licenseComments, not as licenseConcluded: GraphNest concludes nothing on a
// publisher's behalf, and review decisions live outside this document.
func (service *Service) ExportSPDX(ctx context.Context, principal authn.Principal, githubID int64, streamKey string, snapshotID int64, publicOrigin string) ([]byte, Snapshot, error) {
	streamKey, err := normalizeStream(streamKey)
	if err != nil {
		return nil, Snapshot{}, err
	}
	repo, err := service.authorizedRepository(ctx, principal, githubID)
	if err != nil {
		return nil, Snapshot{}, err
	}
	if snapshotID == 0 {
		stream, err := service.Store.SupplyChainStream(ctx, repo.ID, streamKey)
		if errors.Is(err, pgx.ErrNoRows) || err == nil && stream.LatestSnapshotID == nil {
			return nil, Snapshot{}, ErrNoInventory
		}
		if err != nil {
			return nil, Snapshot{}, err
		}
		snapshotID = *stream.LatestSnapshotID
	}
	snapshot, err := service.Store.SupplyChainSnapshot(ctx, snapshotID, []int64{repo.ID})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, Snapshot{}, ErrNotFound
	}
	if err != nil {
		return nil, Snapshot{}, err
	}
	components, err := service.Store.SupplyChainComponents(ctx, snapshot.ID, -1, DefaultMaxComponents*2, "")
	if err != nil {
		return nil, Snapshot{}, err
	}
	edges, err := service.Store.SupplyChainRelationships(ctx, snapshot.ID, "", snapshot.EdgeCount+1)
	if err != nil {
		return nil, Snapshot{}, err
	}
	assessments, err := service.assessments(ctx, snapshot.ID, components)
	if err != nil {
		return nil, Snapshot{}, err
	}
	now := service.now()
	digest := hex.EncodeToString(snapshot.DocumentSHA256)
	namespace := strings.TrimSuffix(publicOrigin, "/") + "/spdxdocs/graphnest/repository/" + strconv.FormatInt(repo.GitHubID, 10) + "/snapshot/" + strconv.FormatInt(snapshot.ID, 10) + "-" + digest[:16]
	type spdxPackage struct {
		SPDXID           string `json:"SPDXID"`
		Name             string `json:"name"`
		VersionInfo      string `json:"versionInfo,omitempty"`
		DownloadLocation string `json:"downloadLocation"`
		FilesAnalyzed    bool   `json:"filesAnalyzed"`
		Supplier         string `json:"supplier,omitempty"`
		LicenseConcluded string `json:"licenseConcluded"`
		LicenseDeclared  string `json:"licenseDeclared"`
		LicenseComments  string `json:"licenseComments,omitempty"`
		Checksums        []struct {
			Algorithm string `json:"algorithm"`
			Value     string `json:"checksumValue"`
		} `json:"checksums,omitempty"`
		ExternalRefs []struct {
			Category string `json:"referenceCategory"`
			Type     string `json:"referenceType"`
			Locator  string `json:"referenceLocator"`
		} `json:"externalRefs,omitempty"`
	}
	type spdxRelationship struct {
		SPDXElementID      string `json:"spdxElementId"`
		Type               string `json:"relationshipType"`
		RelatedSPDXElement string `json:"relatedSpdxElement"`
	}
	ids := map[string]string{}
	used := map[string]bool{"SPDXRef-DOCUMENT": true}
	spdxID := func(element string) string {
		if id, ok := ids[element]; ok {
			return id
		}
		candidate := "SPDXRef-" + spdxIDUnsafe.ReplaceAllString(element, "-")
		if strings.HasPrefix(element, "SPDXRef-") {
			candidate = spdxIDUnsafe.ReplaceAllString(element, "-")
		}
		base := candidate
		for index := 2; used[candidate]; index++ {
			candidate = base + "-" + strconv.Itoa(index)
		}
		used[candidate] = true
		ids[element] = candidate
		return candidate
	}
	packages := make([]spdxPackage, 0, len(components))
	var describes []string
	for _, component := range components {
		pkg := spdxPackage{SPDXID: spdxID(component.ElementID), Name: component.Name, DownloadLocation: "NOASSERTION", LicenseConcluded: "NOASSERTION", LicenseDeclared: "NOASSERTION"}
		if component.Version != nil {
			pkg.VersionInfo = *component.Version
		}
		if component.DownloadLocation != nil && *component.DownloadLocation != "" {
			pkg.DownloadLocation = *component.DownloadLocation
		}
		if component.Supplier != nil && *component.Supplier != "" {
			pkg.Supplier = *component.Supplier
		}
		// Only a value that is a valid SPDX expression (or the sentinels) may
		// stand in licenseDeclared; anything else is described in comments.
		var comments []string
		if component.LicenseDeclaredRaw != nil {
			parsed := license.ParseForExport(*component.LicenseDeclaredRaw)
			if parsed != "" {
				pkg.LicenseDeclared = parsed
			} else {
				comments = append(comments, "producer declared (not an SPDX expression): "+sanitize(*component.LicenseDeclaredRaw, 300))
			}
		}
		if component.LicenseConcludedRaw != nil {
			if parsed := license.ParseForExport(*component.LicenseConcludedRaw); parsed != "" && parsed != "NOASSERTION" {
				comments = append(comments, "producer concluded: "+parsed)
			}
		}
		if assessment, ok := assessments[component.ID]; ok && assessment.Status != "" {
			line := "GraphNest assessment: " + string(assessment.Status)
			if assessment.NormalizedExpression != "" {
				line += " (" + assessment.NormalizedExpression + ")"
			}
			if assessment.ConflictDetail != "" {
				line += "; conflict: " + sanitize(assessment.ConflictDetail, 300)
			}
			line += "; evidence fingerprint " + hex.EncodeToString(assessment.EvidenceFingerprint)[:16] + "; evidence is not approval"
			comments = append(comments, line)
		}
		pkg.LicenseComments = strings.Join(comments, " | ")
		for _, checksum := range component.Checksums {
			if algorithm := spdxChecksumAlgorithm(checksum.Algorithm); algorithm != "" {
				pkg.Checksums = append(pkg.Checksums, struct {
					Algorithm string `json:"algorithm"`
					Value     string `json:"checksumValue"`
				}{algorithm, strings.ToLower(checksum.Value)})
			}
		}
		if component.PURL != nil {
			pkg.ExternalRefs = append(pkg.ExternalRefs, struct {
				Category string `json:"referenceCategory"`
				Type     string `json:"referenceType"`
				Locator  string `json:"referenceLocator"`
			}{"PACKAGE-MANAGER", "purl", *component.PURL})
		}
		if component.IsRoot {
			describes = append(describes, pkg.SPDXID)
		}
		packages = append(packages, pkg)
	}
	relationships := []spdxRelationship{}
	for _, root := range describes {
		relationships = append(relationships, spdxRelationship{"SPDXRef-DOCUMENT", "DESCRIBES", root})
	}
	for _, edge := range edges {
		if !edge.Resolved || edge.Type == "DESCRIBES" || edge.Type == "DESCRIBED_BY" {
			continue
		}
		relationships = append(relationships, spdxRelationship{spdxID(edge.FromElement), edge.Type, spdxID(edge.ToElement)})
	}
	document := map[string]any{
		"spdxVersion":       DerivedSPDXVersion,
		"dataLicense":       "CC0-1.0",
		"SPDXID":            "SPDXRef-DOCUMENT",
		"name":              "graphnest-derived-inventory-" + strconv.FormatInt(repo.GitHubID, 10) + "-" + strconv.FormatInt(snapshot.ID, 10),
		"documentNamespace": namespace,
		"creationInfo": map[string]any{
			"created":  now.Format(time.RFC3339),
			"creators": []string{"Tool: GraphNest-supply-chain-" + strconv.Itoa(ParserVersion), "Organization: GraphNest deployment"},
			"comment": fmt.Sprintf("Derived by GraphNest from snapshot %d of repository %s (%s stream, producer %s, tool %q, collected %s, subject assurance %s). "+
				"The original document (%s, sha256 %s, %d bytes) is preserved unchanged and is the authoritative source; this document adds GraphNest normalization and license assessments as comments only. Review decisions are not part of this document.",
				snapshot.ID, repo.Name, snapshot.StreamKey, snapshot.Producer, snapshot.ProducerTool, snapshot.CollectedAt.Format(time.RFC3339), snapshot.SubjectAssurance,
				snapshot.DocumentFormat, digest, snapshot.DocumentBytes),
		},
		"externalDocumentRefs": []map[string]any{{
			"externalDocumentId": "DocumentRef-graphnest-original-snapshot-" + strconv.FormatInt(snapshot.ID, 10),
			"spdxDocument":       strings.TrimSuffix(publicOrigin, "/") + "/v1/supply-chain/snapshots/" + strconv.FormatInt(snapshot.ID, 10) + "/document",
			"checksum":           map[string]string{"algorithm": "SHA256", "checksumValue": digest},
		}},
		"documentDescribes": nonNilStrings(describes),
		"packages":          packages,
		"relationships":     relationships,
	}
	if snapshot.SubjectRevision != "" {
		document["comment"] = "Subject revision " + snapshot.SubjectRevision + " is " + string(snapshot.SubjectAssurance) + "; GraphNest did not independently verify it."
	}
	data, err := json.MarshalIndent(document, "", "  ")
	if err != nil {
		return nil, Snapshot{}, err
	}
	// Validate what we produced with our own reader before serving it.
	if _, err := NormalizeSPDX23(data, Limits{}); err != nil {
		return nil, Snapshot{}, fmt.Errorf("derived document failed validation: %w", err)
	}
	return append(data, '\n'), snapshot, nil
}

func nonNilStrings(values []string) []string {
	if values == nil {
		return []string{}
	}
	return values
}

func spdxChecksumAlgorithm(value string) string {
	switch strings.ToUpper(strings.ReplaceAll(value, "-", "")) {
	case "SHA1":
		return "SHA1"
	case "SHA256":
		return "SHA256"
	case "SHA384":
		return "SHA384"
	case "SHA512":
		return "SHA512"
	case "MD5":
		return "MD5"
	case "SHA3256":
		return "SHA3-256"
	case "SHA3384":
		return "SHA3-384"
	case "SHA3512":
		return "SHA3-512"
	case "BLAKE2B256":
		return "BLAKE2b-256"
	case "BLAKE2B384":
		return "BLAKE2b-384"
	case "BLAKE2B512":
		return "BLAKE2b-512"
	case "BLAKE3":
		return "BLAKE3"
	}
	return ""
}
