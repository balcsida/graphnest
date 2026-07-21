package scipgraph

import "testing"

func TestParsePackageURL(t *testing.T) {
	tests := []struct {
		name  string
		value string
		want  Package
	}{
		{"golang", "pkg:golang/example.com/acme/lib@v1.2.3", Package{PURL: "pkg:golang/example.com/acme/lib@v1.2.3", Manager: "gomod", Name: "example.com/acme/lib", Version: "v1.2.3"}},
		{"npm scoped", "pkg:npm/%40scope/name@1.2.3", Package{PURL: "pkg:npm/@scope/name@1.2.3", Manager: "npm", Name: "@scope/name", Version: "1.2.3"}},
		{"normalized type and escapes", "pkg:PyPI/acme%2Dlib@V1", Package{PURL: "pkg:pypi/acme-lib@V1", Manager: "pip", Name: "acme-lib", Version: "V1"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := ParsePackageURL(test.value)
			if err != nil || got != test.want {
				t.Fatalf("ParsePackageURL() = %#v, %v, want %#v", got, err, test.want)
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
