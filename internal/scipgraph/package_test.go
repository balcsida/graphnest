package scipgraph

import "testing"

func TestParsePackageURL(t *testing.T) {
	tests := []struct {
		name  string
		value string
		want  Package
	}{
		{"golang", "pkg:golang/example.com/acme/lib@v1.2.3", Package{PURL: "pkg:golang/example.com/acme/lib@v1.2.3", Manager: "gomod", Name: "example.com/acme/lib", Version: "v1.2.3"}},
		{"npm scoped", "pkg:npm/%40scope/name@1.2.3", Package{PURL: "pkg:npm/%40scope/name@1.2.3", Manager: "npm", Name: "@scope/name", Version: "1.2.3"}},
		{"normalized type and escapes", "pkg:PyPI/acme%2Dlib@V1", Package{PURL: "pkg:pypi/acme-lib@V1", Manager: "pip", Name: "acme-lib", Version: "V1"}},
		{"encoded query delimiter", "pkg:npm/acme%3Ftools@1%3F2", Package{PURL: "pkg:npm/acme%3Ftools@1%3F2", Manager: "npm", Name: "acme?tools", Version: "1?2"}},
		{"encoded fragment delimiter", "pkg:npm/acme%23tools@1%232", Package{PURL: "pkg:npm/acme%23tools@1%232", Manager: "npm", Name: "acme#tools", Version: "1#2"}},
		{"encoded percent", "pkg:npm/acme%25tools@1%252", Package{PURL: "pkg:npm/acme%25tools@1%252", Manager: "npm", Name: "acme%tools", Version: "1%2"}},
		{"encoded separator", "pkg:npm/acme%40tools@1%402", Package{PURL: "pkg:npm/acme%40tools@1%402", Manager: "npm", Name: "acme@tools", Version: "1@2"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := ParsePackageURL(test.value)
			if err != nil || got != test.want {
				t.Fatalf("ParsePackageURL() = %#v, %v, want %#v", got, err, test.want)
			}
			roundTrip, err := ParsePackageURL(got.PURL)
			if err != nil || roundTrip != got {
				t.Fatalf("ParsePackageURL(%q) round trip = %#v, %v", got.PURL, roundTrip, err)
			}
		})
	}
}

func TestParsePackageURLRejectsUnsupportedValues(t *testing.T) {
	for _, value := range []string{
		"https:golang/example.com/acme/lib@v1",
		"pkg:golang/example.com/acme/lib",
		"pkg:golang/example.com/acme/lib@",
		"pkg:golang/@v1",
		"pkg:golang/example.com/acme/lib@v1?os=linux",
		"pkg:golang/example.com/acme/lib@v1#subpath",
		"pkg:unknown/acme/lib@v1",
	} {
		if got, err := ParsePackageURL(value); err == nil {
			t.Errorf("ParsePackageURL(%q) = %#v, want error", value, got)
		}
	}
}
