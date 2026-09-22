package policy

import (
	"strings"
	"testing"

	"github.com/balcsida/graphnest/internal/supplychain/spdxexpr"
)

func evaluate(t *testing.T, policy Policy, expression string) Result {
	t.Helper()
	parsed := spdxexpr.Parse(expression)
	if parsed.Status != spdxexpr.StatusParsed && parsed.Status != spdxexpr.StatusUnknownTerms {
		return policy.Evaluate(nil)
	}
	return policy.Evaluate(parsed.Expression)
}

func TestExamplePolicyTruthTable(t *testing.T) {
	policy := Example()
	cases := map[string]struct {
		verdict  Verdict
		branches []string
	}{
		// leaves
		"MIT":                      {verdict: VerdictApproved},
		"GPL-3.0-only":             {verdict: VerdictProhibited},
		"LGPL-2.1-only":            {verdict: VerdictReviewRequired},
		"Custom-Corp-1.0":          {verdict: VerdictReviewRequired}, // unknown term -> unknown handling
		"LicenseRef-Acme-Internal": {verdict: VerdictReviewRequired}, // "*" wildcard
		// AND: worst operand wins
		"MIT AND Apache-2.0":       {verdict: VerdictApproved},
		"MIT AND GPL-3.0-only":     {verdict: VerdictProhibited},
		"MIT AND LGPL-2.1-only":    {verdict: VerdictReviewRequired},
		"MIT AND Custom-Corp-1.0":  {verdict: VerdictReviewRequired},
		"GPL-3.0-only AND MPL-2.0": {verdict: VerdictProhibited},
		// OR: best branch wins, acceptable branches reported
		"MIT OR GPL-3.0-only":                     {verdict: VerdictApproved, branches: []string{"MIT"}},
		"GPL-3.0-only OR MIT":                     {verdict: VerdictApproved, branches: []string{"MIT"}},
		"GPL-3.0-only OR LGPL-2.1-only":           {verdict: VerdictReviewRequired},
		"GPL-3.0-only OR AGPL-3.0-only":           {verdict: VerdictProhibited},
		"MIT OR Apache-2.0":                       {verdict: VerdictApproved, branches: []string{"MIT", "Apache-2.0"}},
		"Custom-Corp-1.0 OR MIT":                  {verdict: VerdictApproved, branches: []string{"MIT"}},
		"(MIT AND GPL-3.0-only) OR ISC":           {verdict: VerdictApproved, branches: []string{"ISC"}},
		"MIT AND (GPL-3.0-only OR ISC)":           {verdict: VerdictApproved, branches: []string{"ISC"}},
		"MIT AND (GPL-3.0-only OR AGPL-3.0-only)": {verdict: VerdictProhibited},
		// WITH: the pair is its own term
		"GPL-2.0-only WITH Classpath-exception-2.0": {verdict: VerdictApproved},
		"GPL-2.0-only": {verdict: VerdictProhibited},
		"GPL-3.0-only WITH Classpath-exception-2.0": {verdict: VerdictReviewRequired}, // unlisted pair is not approved by its base
		"MIT WITH Made-Up-Exception":                {verdict: VerdictReviewRequired},
		// or-later handling off: exact identifiers only
		"GPL-2.0-or-later": {verdict: VerdictProhibited},
		"LGPL-2.1+":        {verdict: VerdictReviewRequired},
		"Apache-2.0+":      {verdict: VerdictReviewRequired}, // "+" form is not listed and allow_or_later is false
	}
	for expression, want := range cases {
		t.Run(expression, func(t *testing.T) {
			got := evaluate(t, policy, expression)
			if got.Verdict != want.verdict {
				t.Fatalf("verdict = %s (%s), want %s", got.Verdict, got.Explanation, want.verdict)
			}
			if strings.Join(got.AcceptableBranches, ",") != strings.Join(want.branches, ",") {
				t.Fatalf("branches = %v, want %v", got.AcceptableBranches, want.branches)
			}
			if got.Explanation == "" || !strings.Contains(got.Explanation, "graphnest-example v1") {
				t.Fatalf("explanation = %q", got.Explanation)
			}
		})
	}
}

func TestUnknownHandlingNeverApproves(t *testing.T) {
	strict := Example()
	strict.UnknownHandling = VerdictProhibited
	for _, expression := range []string{"Custom-1.0", "MIT AND Custom-1.0", "NOASSERTION", "", "SEE LICENSE IN LICENSE"} {
		got := evaluate(t, strict, expression)
		if got.Verdict == VerdictApproved {
			t.Fatalf("%q approved: %s", expression, got.Explanation)
		}
	}
	if got := strict.Evaluate(nil); got.Verdict != VerdictProhibited || !strings.Contains(got.Explanation, "no license expression") {
		t.Fatalf("nil expression = %+v", got)
	}
	lenient := Example()
	if got := lenient.Evaluate(nil); got.Verdict != VerdictReviewRequired {
		t.Fatalf("nil with review_required handling = %+v", got)
	}
}

func TestAllowOrLaterUsesBaseClassification(t *testing.T) {
	policy := Example()
	policy.Rules.AllowOrLater = true
	policy.Rules.Approved = append(policy.Rules.Approved, "Apache-2.0")
	if got := evaluate(t, policy, "Apache-2.0+"); got.Verdict != VerdictApproved {
		t.Fatalf("Apache-2.0+ = %s", got.Verdict)
	}
	// GPL-2.0-or-later is explicitly prohibited regardless.
	if got := evaluate(t, policy, "GPL-2.0-or-later"); got.Verdict != VerdictProhibited {
		t.Fatalf("GPL-2.0-or-later = %s", got.Verdict)
	}
	// An unlisted or-later maps to its -only base when allowed.
	policy.Rules.Prohibited = []string{"GPL-2.0-only"}
	if got := evaluate(t, policy, "GPL-2.0-or-later"); got.Verdict != VerdictProhibited {
		t.Fatalf("GPL-2.0-or-later via base = %s", got.Verdict)
	}
}

func TestParseRulesValidation(t *testing.T) {
	for name, body := range map[string]string{
		"unknown field": `{"approved":["MIT"],"extra":true}`,
		"unknown id":    `{"approved":["Not-A-License-9"]}`,
		"compound":      `{"approved":["MIT OR ISC"]}`,
		"duplicate":     `{"approved":["MIT"],"prohibited":["mit"]}`,
		"not json":      `{"approved":`,
	} {
		if _, err := ParseRules([]byte(body)); err == nil {
			t.Fatalf("%s accepted", name)
		}
	}
	rules, err := ParseRules([]byte(`{"approved":["mit"],"review_required":["LicenseRef-Acme","*"],"prohibited":["GPL-2.0-only WITH Classpath-exception-2.0"]}`))
	if err != nil || len(rules.Approved) != 1 || len(rules.ReviewRequired) != 2 {
		t.Fatalf("rules = %+v %v", rules, err)
	}
	// Example rules parse and the example is labelled as such.
	if Example().Kind != "example" || !strings.Contains(Example().Description, "EXAMPLE FIXTURE") {
		t.Fatalf("example = %+v", Example())
	}
}

func TestDifferentGroupingsGetDifferentVerdicts(t *testing.T) {
	policy := Example()
	left := evaluate(t, policy, "MIT AND (GPL-3.0-only OR ISC)")
	right := evaluate(t, policy, "(MIT AND GPL-3.0-only) OR ISC")
	// Both approved via ISC, but the explanations differ and neither flattens.
	if left.Verdict != VerdictApproved || right.Verdict != VerdictApproved {
		t.Fatalf("%s / %s", left.Verdict, right.Verdict)
	}
	strictGrouping := evaluate(t, policy, "MIT AND (GPL-3.0-only OR AGPL-3.0-only)")
	flat := evaluate(t, policy, "MIT OR GPL-3.0-only OR AGPL-3.0-only")
	if strictGrouping.Verdict != VerdictProhibited || flat.Verdict != VerdictApproved {
		t.Fatalf("grouping changed nothing: %s vs %s", strictGrouping.Verdict, flat.Verdict)
	}
}
