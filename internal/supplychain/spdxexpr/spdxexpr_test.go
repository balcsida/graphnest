package spdxexpr

import (
	"strings"
	"testing"
)

func TestPinnedListLoads(t *testing.T) {
	licenses, exceptions := Count()
	if licenses != 699 || exceptions != 79 {
		t.Fatalf("list sizes = %d licenses, %d exceptions; the embedded list must match release %s", licenses, exceptions, ListVersion)
	}
	if id, deprecated, ok := Pinned().License("mit"); !ok || id != "MIT" || deprecated {
		t.Fatalf("MIT lookup = %q %v %v", id, deprecated, ok)
	}
	if id, deprecated, ok := Pinned().License("GPL-2.0"); !ok || id != "GPL-2.0" || !deprecated {
		t.Fatalf("GPL-2.0 must be a deprecated identifier: %q %v %v", id, deprecated, ok)
	}
	if id, _, ok := Pinned().Exception("classpath-exception-2.0"); !ok || id != "Classpath-exception-2.0" {
		t.Fatalf("exception lookup = %q %v", id, ok)
	}
}

func TestParsePreservesStructure(t *testing.T) {
	cases := map[string]struct {
		normalized string
		kind       Kind
		terms      []string
		status     Status
	}{
		"MIT":                 {normalized: "MIT", kind: KindLicense, terms: []string{"MIT"}, status: StatusParsed},
		"mit":                 {normalized: "MIT", kind: KindLicense, terms: []string{"MIT"}, status: StatusParsed},
		"MIT OR Apache-2.0":   {normalized: "MIT OR Apache-2.0", kind: KindOr, terms: []string{"Apache-2.0", "MIT"}, status: StatusParsed},
		"(MIT OR Apache-2.0)": {normalized: "MIT OR Apache-2.0", kind: KindOr, terms: []string{"Apache-2.0", "MIT"}, status: StatusParsed},
		"MIT AND Apache-2.0":  {normalized: "MIT AND Apache-2.0", kind: KindAnd, terms: []string{"Apache-2.0", "MIT"}, status: StatusParsed},
		"GPL-2.0-only WITH Classpath-exception-2.0": {normalized: "GPL-2.0-only WITH Classpath-exception-2.0", kind: KindWith, terms: []string{"GPL-2.0-only WITH Classpath-exception-2.0"}, status: StatusParsed},
		"LGPL-2.1+": {normalized: "LGPL-2.1+", kind: KindLicense, terms: []string{"LGPL-2.1+"}, status: StatusParsed},
		"MIT AND (LGPL-2.1-or-later OR BSD-3-Clause)":   {normalized: "MIT AND (LGPL-2.1-or-later OR BSD-3-Clause)", kind: KindAnd, terms: []string{"BSD-3-Clause", "LGPL-2.1-or-later", "MIT"}, status: StatusParsed},
		"(MIT AND LGPL-2.1-or-later) OR BSD-3-Clause":   {normalized: "(MIT AND LGPL-2.1-or-later) OR BSD-3-Clause", kind: KindOr, terms: []string{"BSD-3-Clause", "LGPL-2.1-or-later", "MIT"}, status: StatusParsed},
		"MIT and Apache-2.0 or ISC":                     {normalized: "(MIT AND Apache-2.0) OR ISC", kind: KindOr, terms: []string{"Apache-2.0", "ISC", "MIT"}, status: StatusParsed},
		"LicenseRef-Proprietary-Acme":                   {normalized: "LicenseRef-Proprietary-Acme", kind: KindReference, terms: []string{"LicenseRef-Proprietary-Acme"}, status: StatusParsed},
		"DocumentRef-sbom:LicenseRef-Custom AND MIT":    {normalized: "DocumentRef-sbom:LicenseRef-Custom AND MIT", kind: KindAnd, terms: []string{"DocumentRef-sbom:LicenseRef-Custom", "MIT"}, status: StatusParsed},
		"MIT OR NotARealLicense-1.0":                    {normalized: "MIT OR NotARealLicense-1.0", kind: KindOr, terms: []string{"MIT", "NotARealLicense-1.0"}, status: StatusUnknownTerms},
		"GPL-2.0-only WITH Made-Up-Exception":           {normalized: "GPL-2.0-only WITH Made-Up-Exception", kind: KindWith, terms: []string{"GPL-2.0-only WITH Made-Up-Exception"}, status: StatusUnknownTerms},
		"  Apache-2.0  ":                                {normalized: "Apache-2.0", kind: KindLicense, terms: []string{"Apache-2.0"}, status: StatusParsed},
		"MIT\tAND\nApache-2.0":                          {normalized: "MIT AND Apache-2.0", kind: KindAnd, terms: []string{"Apache-2.0", "MIT"}, status: StatusParsed},
		"MIT OR (Apache-2.0 AND (ISC OR BSD-2-Clause))": {normalized: "MIT OR (Apache-2.0 AND (ISC OR BSD-2-Clause))", kind: KindOr, terms: []string{"Apache-2.0", "BSD-2-Clause", "ISC", "MIT"}, status: StatusParsed},
		"GPL-2.0": {normalized: "GPL-2.0", kind: KindLicense, terms: []string{"GPL-2.0"}, status: StatusParsed},
	}
	for raw, want := range cases {
		t.Run(raw, func(t *testing.T) {
			got := Parse(raw)
			if got.Status != want.status || got.Normalized != want.normalized || got.Expression == nil || got.Expression.Kind != want.kind {
				t.Fatalf("Parse(%q) = %s %q kind=%v problem=%q", raw, got.Status, got.Normalized, kindOf(got.Expression), got.Problem)
			}
			if strings.Join(got.Expression.Terms(), ",") != strings.Join(want.terms, ",") {
				t.Fatalf("terms = %v, want %v", got.Expression.Terms(), want.terms)
			}
			// Round trip: the normalized form parses to an equal tree.
			again := Parse(got.Normalized)
			if !Equal(again.Expression, got.Expression) {
				t.Fatalf("normalized %q did not round-trip: %q", got.Normalized, again.Normalized)
			}
		})
	}
}

func kindOf(node *Node) Kind {
	if node == nil {
		return ""
	}
	return node.Kind
}

func TestParseKeepsOrderAndDoesNotFlatten(t *testing.T) {
	left := Parse("MIT AND (GPL-2.0-only OR Apache-2.0)")
	right := Parse("(MIT AND GPL-2.0-only) OR Apache-2.0")
	if Equal(left.Expression, right.Expression) {
		t.Fatal("different groupings must not compare equal")
	}
	if strings.Join(left.Expression.Terms(), ",") != strings.Join(right.Expression.Terms(), ",") {
		t.Fatal("the display term set is the same; the trees must carry the difference")
	}
	// Same associative operator flattens regardless of redundant parentheses;
	// mixed operators keep their grouping.
	if !Equal(Parse("(MIT OR Apache-2.0) OR ISC").Expression, Parse("MIT OR Apache-2.0 OR ISC").Expression) {
		t.Fatal("associative OR grouping must flatten")
	}
	if Equal(Parse("(MIT AND Apache-2.0) OR ISC").Expression, Parse("MIT AND (Apache-2.0 OR ISC)").Expression) {
		t.Fatal("mixed operators must keep grouping")
	}
	ordered := Parse("Apache-2.0 OR MIT")
	if ordered.Expression.Children[0].ID != "Apache-2.0" || ordered.Expression.Children[1].ID != "MIT" {
		t.Fatalf("OR branches were reordered: %+v", ordered.Expression)
	}
}

func TestParseSentinelsAreNotLicenses(t *testing.T) {
	for raw, want := range map[string]Status{
		"":            StatusNoAssertion,
		"   ":         StatusNoAssertion,
		"NOASSERTION": StatusNoAssertion,
		"noassertion": StatusNoAssertion,
		"NONE":        StatusNone,
		"UNLICENSED":  StatusUnlicensed,
	} {
		got := Parse(raw)
		if got.Status != want || got.Expression != nil || got.Normalized != "" {
			t.Fatalf("Parse(%q) = %+v, want status %s and no expression", raw, got, want)
		}
	}
}

func TestParseRejectsInvalidText(t *testing.T) {
	for _, raw := range []string{
		"SEE LICENSE IN LICENSE.txt", "MIT OR", "OR MIT", "MIT AND AND Apache-2.0", "(MIT", "MIT)", "MIT WITH", "MIT WITH (Classpath-exception-2.0)",
		"MIT/Apache-2.0", "MIT, Apache-2.0", "Copyright (c) 2020 Someone", "+MIT", "(MIT)+", "MIT OR Apache-2.0 WITH", "MIT AND ()", "()",
		strings.Repeat("(", 40) + "MIT" + strings.Repeat(")", 40), "MIT " + strings.Repeat("OR MIT ", 600), strings.Repeat("a", 5000),
		"MIT\x00", "MIT \u00e9",
	} {
		got := Parse(raw)
		if got.Status != StatusInvalid || got.Expression != nil {
			t.Fatalf("Parse(%q) = %s %q, want invalid", raw, got.Status, got.Normalized)
		}
		if got.Problem == "" || len(got.Problem) > 200 || strings.Contains(got.Problem, "LICENSE.txt") {
			t.Fatalf("problem for %q = %q", raw, got.Problem)
		}
	}
}

func TestParseReportsDeprecatedAndReferences(t *testing.T) {
	got := Parse("GPL-2.0+ AND LicenseRef-Internal OR AGPL-1.0")
	if got.Status != StatusParsed || strings.Join(got.Deprecated, ",") != "GPL-2.0,AGPL-1.0" || strings.Join(got.References, ",") != "LicenseRef-Internal" {
		t.Fatalf("result = %+v", got)
	}
	unknown := Parse("Foo-1.0 OR Bar-2.0 WITH Baz-exception")
	if unknown.Status != StatusUnknownTerms || strings.Join(unknown.UnknownTerms, ",") != "Foo-1.0,Bar-2.0,Baz-exception" {
		t.Fatalf("unknown = %+v", unknown)
	}
	// Syntactically valid but with a free-text "exception": kept as unknown
	// terms so it is visible for review, never treated as a known license.
	loose := Parse("GPL-2.0 with exceptions")
	if loose.Status != StatusUnknownTerms || loose.Normalized != "GPL-2.0 WITH exceptions" || strings.Join(loose.UnknownTerms, ",") != "exceptions" {
		t.Fatalf("loose = %+v", loose)
	}
}

func FuzzParse(f *testing.F) {
	for _, seed := range []string{"MIT", "MIT OR Apache-2.0", "(GPL-2.0-only WITH Classpath-exception-2.0) AND MIT", "LicenseRef-x", "NOASSERTION", "((", "MIT+ OR", strings.Repeat("(MIT OR ", 20)} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, raw string) {
		got := Parse(raw)
		switch got.Status {
		case StatusParsed, StatusUnknownTerms:
			if got.Expression == nil || got.Normalized == "" {
				t.Fatalf("parsed without a tree: %+v", got)
			}
			again := Parse(got.Normalized)
			if again.Status == StatusInvalid || !Equal(again.Expression, got.Expression) {
				t.Fatalf("normalized %q did not round-trip (%s)", got.Normalized, again.Problem)
			}
		case StatusInvalid:
			if got.Expression != nil || got.Problem == "" {
				t.Fatalf("invalid with a tree or no problem: %+v", got)
			}
		default:
			if got.Expression != nil {
				t.Fatalf("sentinel with a tree: %+v", got)
			}
		}
	})
}
