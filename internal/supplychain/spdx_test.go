package supplychain

import (
	"errors"
	"os"
	"strings"
	"testing"
	"time"
)

func fixture(t *testing.T) []byte {
	t.Helper()
	data, err := os.ReadFile("../../test/fixtures/supplychain/ghes-spdx-2.3.json")
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func warningCodes(warnings []Warning) map[string]int {
	codes := map[string]int{}
	for _, warning := range warnings {
		codes[warning.Code]++
	}
	return codes
}

func TestNormalizeSPDX23PreservesOccurrencesRootsAndEdges(t *testing.T) {
	got, err := NormalizeSPDX23(fixture(t), Limits{})
	if err != nil {
		t.Fatal(err)
	}
	if got.SPDXVersion != "SPDX-2.3" || got.DataLicense != "CC0-1.0" || got.ProducerTool != "GitHub.com-Dependency-Graph" || got.DocumentName != "com.github.acme/widgets" {
		t.Fatalf("header = %+v", got)
	}
	if got.CreatedAtClaimed == nil || !got.CreatedAtClaimed.Equal(time.Date(2026, 9, 20, 8, 15, 30, 0, time.UTC)) {
		t.Fatalf("created = %v", got.CreatedAtClaimed)
	}
	if len(got.Components) != 7 {
		t.Fatalf("components = %d, want 7 (PURL-less and version-less packages must survive)", len(got.Components))
	}
	if got.RootElementIDs == nil || len(got.RootElementIDs) != 1 || got.RootElementIDs[0] != "SPDXRef-com.github.acme-widgets" || !got.Components[0].IsRoot {
		t.Fatalf("roots = %v", got.RootElementIDs)
	}
	scoped := got.Components[1]
	if scoped.Ecosystem != "npm" || scoped.PURLNamespace != "@scope" || scoped.PURLName != "left-pad" || scoped.PURLVersion != "1.3.0" || scoped.Version == nil || *scoped.Version != "1.3.0" {
		t.Fatalf("scoped npm component = %+v", scoped)
	}
	if scoped.LicenseDeclaredRaw == nil || *scoped.LicenseDeclaredRaw != "NOASSERTION" {
		t.Fatalf("NOASSERTION must be preserved verbatim, got %v", scoped.LicenseDeclaredRaw)
	}
	maven := got.Components[2]
	if maven.Ecosystem != "maven" || maven.PURLNamespace != "org.example" || maven.Qualifiers["type"] != "jar" {
		t.Fatalf("maven component = %+v", maven)
	}
	vendored := got.Components[6]
	if vendored.PURL != nil || vendored.Version != nil || vendored.Ecosystem != "" || vendored.Name != "vendored:legacy-widget-lib" {
		t.Fatalf("PURL-less component = %+v", vendored)
	}
	if len(got.Relationships) != 8 {
		t.Fatalf("relationships = %d", len(got.Relationships))
	}
	resolved := 0
	for _, edge := range got.Relationships {
		if edge.Resolved {
			resolved++
		}
	}
	if resolved != 7 || got.Relationships[7].Resolved || got.Relationships[7].ToElement != "SPDXRef-missing-transitive" {
		t.Fatalf("unresolved edge must stay visible as unresolved: %+v", got.Relationships[7])
	}
	codes := warningCodes(got.Warnings)
	if codes["relationship_unresolved"] != 1 || codes["package_purl_missing"] != 1 || codes["package_version_missing"] != 2 {
		t.Fatalf("warnings = %v", codes)
	}
	if got.WarningCount != len(got.Warnings) {
		t.Fatalf("warning count %d != %d", got.WarningCount, len(got.Warnings))
	}
}

func TestNormalizeSPDX23RootsFromRelationshipsOnly(t *testing.T) {
	document := `{"spdxVersion":"SPDX-2.3","SPDXID":"SPDXRef-DOCUMENT","creationInfo":{"created":"2026-01-01T00:00:00Z","creators":["Tool: x"]},
	"packages":[{"SPDXID":"SPDXRef-a","name":"a"},{"SPDXID":"SPDXRef-b","name":"b"}],
	"relationships":[{"spdxElementId":"SPDXRef-a","relationshipType":"DESCRIBED_BY","relatedSpdxElement":"SPDXRef-DOCUMENT"},{"spdxElementId":"SPDXRef-a","relationshipType":"depends_on","relatedSpdxElement":"SPDXRef-b"}]}`
	got, err := NormalizeSPDX23([]byte(document), Limits{})
	if err != nil {
		t.Fatal(err)
	}
	if len(got.RootElementIDs) != 1 || got.RootElementIDs[0] != "SPDXRef-a" {
		t.Fatalf("roots = %v", got.RootElementIDs)
	}
	if got.Relationships[1].Type != "DEPENDS_ON" || !got.Relationships[1].Resolved {
		t.Fatalf("relationship type must be uppercased and resolved: %+v", got.Relationships[1])
	}
}

func TestNormalizeSPDX23EmptyInventoryIsValid(t *testing.T) {
	document := `{"spdxVersion":"SPDX-2.3","SPDXID":"SPDXRef-DOCUMENT","dataLicense":"CC0-1.0","creationInfo":{"created":"2026-01-01T00:00:00Z","creators":["Tool: GitHub.com-Dependency-Graph"]},"packages":[],"relationships":[]}`
	got, err := NormalizeSPDX23([]byte(document), Limits{})
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Components) != 0 || len(got.Relationships) != 0 || warningCodes(got.Warnings)["root_missing"] != 1 {
		t.Fatalf("empty inventory = %+v", got)
	}
}

func TestNormalizeSPDX23DuplicateAndMissingIDsStayVisible(t *testing.T) {
	document := `{"spdxVersion":"SPDX-2.3","SPDXID":"SPDXRef-DOCUMENT","creationInfo":{"created":"2026-01-01T00:00:00Z"},
	"packages":[{"SPDXID":"SPDXRef-a","name":"a","versionInfo":"1"},{"SPDXID":"SPDXRef-a","name":"a","versionInfo":"2"},{"name":"nameless"}]}`
	got, err := NormalizeSPDX23([]byte(document), Limits{})
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Components) != 3 {
		t.Fatalf("components = %d", len(got.Components))
	}
	if got.Components[1].ElementID == "SPDXRef-a" || !strings.HasPrefix(got.Components[1].ElementID, "SPDXRef-a#graphnest-duplicate-") {
		t.Fatalf("duplicate id = %q", got.Components[1].ElementID)
	}
	if !strings.HasPrefix(got.Components[2].ElementID, "SPDXRef-graphnest-missing-") {
		t.Fatalf("missing id = %q", got.Components[2].ElementID)
	}
	codes := warningCodes(got.Warnings)
	if codes["package_id_duplicate"] != 1 || codes["package_id_missing"] != 1 || codes["creator_tool_missing"] != 1 {
		t.Fatalf("warnings = %v", codes)
	}
}

func TestNormalizeSPDX23Rejects(t *testing.T) {
	cases := map[string]struct {
		document string
		limits   Limits
		want     error
	}{
		"not json":            {document: `{"spdxVersion":`, want: ErrMalformed},
		"trailing":            {document: `{"spdxVersion":"SPDX-2.3","packages":[]} {}`, want: ErrMalformed},
		"missing version":     {document: `{"packages":[]}`, want: ErrMalformed},
		"unsupported version": {document: `{"spdxVersion":"SPDX-2.2","packages":[]}`, want: ErrUnsupportedVersion},
		"wrong type":          {document: `{"spdxVersion":"SPDX-2.3","packages":"nope"}`, want: ErrMalformed},
		"too many packages": {document: `{"spdxVersion":"SPDX-2.3","packages":[{"SPDXID":"a"},{"SPDXID":"b"}]}`,
			limits: Limits{MaxComponents: 1}, want: ErrTooLarge},
	}
	for name, test := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := NormalizeSPDX23([]byte(test.document), test.limits)
			if !errors.Is(err, test.want) {
				t.Fatalf("error = %v, want %v", err, test.want)
			}
			if strings.Contains(err.Error(), "nope") {
				t.Fatalf("diagnostic echoes document content: %v", err)
			}
		})
	}
}

func TestNormalizeSPDX23BoundsWarnings(t *testing.T) {
	var packages []string
	for range 10 {
		packages = append(packages, `{"name":"x"}`)
	}
	document := `{"spdxVersion":"SPDX-2.3","SPDXID":"SPDXRef-DOCUMENT","creationInfo":{"created":"2026-01-01T00:00:00Z","creators":["Tool: t"]},"packages":[` + strings.Join(packages, ",") + `]}`
	got, err := NormalizeSPDX23([]byte(document), Limits{MaxWarnings: 5})
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Warnings) != 5 || got.WarningCount <= 5 {
		t.Fatalf("warnings kept %d of %d", len(got.Warnings), got.WarningCount)
	}
}

func TestParsePURL(t *testing.T) {
	cases := map[string]PURL{
		"pkg:npm/%40scope/left-pad@1.3.0":                              {Type: "npm", Namespace: "@scope", Name: "left-pad", Version: "1.3.0"},
		"pkg:maven/org.example/core@2.1.0?type=jar&classifier=sources": {Type: "maven", Namespace: "org.example", Name: "core", Version: "2.1.0", Qualifiers: map[string]string{"type": "jar", "classifier": "sources"}},
		"pkg:golang/golang.org/x/text@0.14.0":                          {Type: "golang", Namespace: "golang.org/x", Name: "text", Version: "0.14.0"},
		"pkg:nuget/Newtonsoft.Json":                                    {Type: "nuget", Name: "Newtonsoft.Json"},
		"pkg:github/acme/widgets#sub/path":                             {Type: "github", Namespace: "acme", Name: "widgets"},
		"PKG:PyPI/requests@2.31.0":                                     {Type: "pypi", Name: "requests", Version: "2.31.0"},
	}
	for input, want := range cases {
		got, err := ParsePURL(input)
		if err != nil {
			t.Fatalf("%s: %v", input, err)
		}
		if got.Type != want.Type || got.Namespace != want.Namespace || got.Name != want.Name || got.Version != want.Version || len(got.Qualifiers) != len(want.Qualifiers) {
			t.Fatalf("%s = %+v, want %+v", input, got, want)
		}
		for key, value := range want.Qualifiers {
			if got.Qualifiers[key] != value {
				t.Fatalf("%s qualifier %s = %q", input, key, got.Qualifiers[key])
			}
		}
	}
	for _, invalid := range []string{"", "npm/left-pad", "pkg:", "pkg:npm", "pkg:npm/", "pkg:npm/%zz@1", "pkg:npm/a?=v"} {
		if _, err := ParsePURL(invalid); err == nil {
			t.Fatalf("%q parsed", invalid)
		}
	}
}

func FuzzNormalizeSPDX23(f *testing.F) {
	data, err := os.ReadFile("../../test/fixtures/supplychain/ghes-spdx-2.3.json")
	if err != nil {
		f.Fatal(err)
	}
	f.Add(string(data))
	f.Add(`{"spdxVersion":"SPDX-2.3","packages":[]}`)
	f.Fuzz(func(t *testing.T, document string) {
		got, err := NormalizeSPDX23([]byte(document), Limits{MaxComponents: 200, MaxRelationships: 2000, MaxWarnings: 50})
		if err != nil {
			return
		}
		if len(got.Components) > 200 || len(got.Warnings) > 50 {
			t.Fatalf("limits exceeded: %d components, %d warnings", len(got.Components), len(got.Warnings))
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
