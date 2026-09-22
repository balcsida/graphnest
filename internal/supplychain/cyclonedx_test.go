package supplychain

import (
	"errors"
	"os"
	"strings"
	"testing"
	"time"
)

func cdxFixture(t *testing.T) []byte {
	t.Helper()
	data, err := os.ReadFile("../../test/fixtures/supplychain/syft-cyclonedx-1.6.json")
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func TestNormalizeCycloneDX16PreservesWhatTheFormatCarries(t *testing.T) {
	got, err := NormalizeCycloneDX16(cdxFixture(t), Limits{})
	if err != nil {
		t.Fatal(err)
	}
	if got.SPDXVersion != "CycloneDX-1.6" || got.ProducerTool != "syft 1.20.0" || got.DocumentNamespace != "urn:uuid:3e671687-395b-41f5-a30f-a58921a69b79#1" || got.DocumentName != "registry.example.internal/acme/widgets" {
		t.Fatalf("header = %+v", got)
	}
	if got.CreatedAtClaimed == nil || !got.CreatedAtClaimed.Equal(time.Date(2026, 9, 21, 14, 3, 11, 0, time.UTC)) {
		t.Fatalf("created = %v", got.CreatedAtClaimed)
	}
	// root + 6 top-level + 1 nested = 8 occurrences; the file component without bom-ref survives with a synthetic ID.
	if len(got.Components) != 8 || !got.Components[0].IsRoot || got.RootElementIDs[0] != "graphnest-fixture-root" || got.Components[0].Qualifiers["cdx:type"] != "container" {
		t.Fatalf("components = %d roots=%v", len(got.Components), got.RootElementIDs)
	}
	byElement := map[string]Component{}
	for _, component := range got.Components {
		byElement[component.ElementID] = component
	}
	leftPad := byElement["pkg:npm/%40scope/left-pad@1.3.0?package-id=1a2b3c"]
	if leftPad.Ecosystem != "npm" || leftPad.PURLNamespace != "@scope" || leftPad.PURLName != "left-pad" || *leftPad.LicenseDeclaredRaw != "MIT" || len(leftPad.Checksums) != 1 || leftPad.Checksums[0].Algorithm != "SHA-512" || leftPad.Name != "@scope/left-pad" {
		t.Fatalf("left-pad = %+v", leftPad)
	}
	if *byElement["pkg:npm/dual@2.0.0?package-id=2b3c4d"].LicenseDeclaredRaw != "(MIT OR Apache-2.0)" {
		t.Fatalf("expression must be kept verbatim: %+v", byElement["pkg:npm/dual@2.0.0?package-id=2b3c4d"])
	}
	libssl := byElement["pkg:deb/debian/libssl3@3.0.11-1~deb12u2?arch=amd64&distro=debian-12&package-id=3c4d5e"]
	if *libssl.LicenseDeclaredRaw != "OpenSSL; Apache-2.0" || libssl.Qualifiers["arch"] != "amd64" || libssl.Qualifiers["distro"] != "debian-12" || libssl.Supplier == nil {
		t.Fatalf("libssl = %+v", libssl)
	}
	nested := byElement["pkg:npm/nested-helper@0.1.0?package-id=5e6f7a"]
	if *nested.LicenseDeclaredRaw != "Custom Acme License (https://acme.example.internal/license)" {
		t.Fatalf("nested = %+v", nested)
	}
	var synthetic *Component
	for _, component := range got.Components {
		if strings.HasPrefix(component.ElementID, "graphnest-bomref-missing-") {
			synthetic = &component
		}
	}
	if synthetic == nil || synthetic.PURL != nil || synthetic.Name != "/app/config/settings.yaml" || synthetic.Qualifiers["cdx:type"] != "file" {
		t.Fatalf("bom-ref-less component = %+v", synthetic)
	}
	// Edges: 1 CONTAINS (bundle -> nested), 4 + 1 + 2 DEPENDS_ON = 8 total; the missing target is unresolved.
	edges := map[string]int{}
	unresolved := 0
	for _, edge := range got.Relationships {
		edges[edge.Type]++
		if !edge.Resolved {
			unresolved++
		}
	}
	if edges["CONTAINS"] != 1 || edges["DEPENDS_ON"] != 7 || unresolved != 1 {
		t.Fatalf("edges = %v unresolved=%d", edges, unresolved)
	}
	codes := warningCodes(got.Warnings)
	if codes["license_list_ambiguous"] != 1 || codes["license_name_not_spdx"] != 2 || codes["evidence_not_carried"] != 1 || codes["relationship_unresolved"] != 1 || codes["package_id_missing"] != 1 || codes["license_acknowledgement"] != 1 || codes["package_purl_missing"] != 3 {
		t.Fatalf("warnings = %v", codes)
	}
}

func TestNormalizeCycloneDX16Rejects(t *testing.T) {
	cases := map[string]struct {
		document string
		limits   Limits
		want     error
	}{
		"not json":        {document: `{"bomFormat"`, want: ErrMalformed},
		"wrong format":    {document: `{"bomFormat":"SPDX","specVersion":"1.6"}`, want: ErrMalformed},
		"old version":     {document: `{"bomFormat":"CycloneDX","specVersion":"1.4"}`, want: ErrUnsupportedVersion},
		"missing version": {document: `{"bomFormat":"CycloneDX"}`, want: ErrMalformed},
		"trailing":        {document: `{"bomFormat":"CycloneDX","specVersion":"1.6"} x`, want: ErrMalformed},
		"too many":        {document: `{"bomFormat":"CycloneDX","specVersion":"1.6","components":[{"type":"library","name":"a"},{"type":"library","name":"b"}]}`, limits: Limits{MaxComponents: 1}, want: ErrTooLarge},
		"nested too many": {document: `{"bomFormat":"CycloneDX","specVersion":"1.6","components":[{"type":"library","name":"a","components":[{"type":"library","name":"b"},{"type":"library","name":"c"}]}]}`, limits: Limits{MaxComponents: 2}, want: ErrTooLarge},
	}
	for name, test := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := NormalizeCycloneDX16([]byte(test.document), test.limits)
			if !errors.Is(err, test.want) {
				t.Fatalf("error = %v, want %v", err, test.want)
			}
		})
	}
}

func TestNormalizeCycloneDX16EmptyAndLegacyTools(t *testing.T) {
	got, err := NormalizeCycloneDX16([]byte(`{"bomFormat":"CycloneDX","specVersion":"1.6","metadata":{"timestamp":"2026-01-01T00:00:00Z","tools":[{"vendor":"OSS Review Toolkit","name":"ort","version":"45.0.0"}]},"components":[],"dependencies":[]}`), Limits{})
	if err != nil {
		t.Fatal(err)
	}
	if got.ProducerTool != "OSS Review Toolkit ort 45.0.0" || len(got.Components) != 0 || len(got.Relationships) != 0 || warningCodes(got.Warnings)["root_missing"] != 1 {
		t.Fatalf("legacy tools = %+v", got)
	}
}

func FuzzNormalizeCycloneDX16(f *testing.F) {
	f.Add(string(mustRead(f, "../../test/fixtures/supplychain/syft-cyclonedx-1.6.json")))
	f.Add(`{"bomFormat":"CycloneDX","specVersion":"1.6"}`)
	f.Fuzz(func(t *testing.T, document string) {
		got, err := NormalizeCycloneDX16([]byte(document), Limits{MaxComponents: 200, MaxRelationships: 2000})
		if err != nil {
			return
		}
		if len(got.Components) > 200 {
			t.Fatalf("components exceeded the limit: %d", len(got.Components))
		}
		seen := map[string]bool{}
		for _, component := range got.Components {
			if seen[component.ElementID] {
				t.Fatalf("duplicate element id %q", component.ElementID)
			}
			seen[component.ElementID] = true
		}
	})
}

func mustRead(f *testing.F, path string) []byte {
	data, err := os.ReadFile(path)
	if err != nil {
		f.Fatal(err)
	}
	return data
}
