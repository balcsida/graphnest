// Package policy evaluates SPDX license expression trees against a
// versioned organization policy. It walks the tree: AND requires every
// operand to be acceptable, OR is acceptable when any branch is acceptable
// (and reports which), WITH is evaluated as the license+exception pair.
// Unknown terms and unparseable evidence never become approved. It ships no
// company legal policy; the embedded policy is an example fixture.
package policy

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/balcsida/graphnest/internal/supplychain/spdxexpr"
)

// Verdict is the outcome of evaluating one expression against a policy.
type Verdict string

const (
	VerdictApproved       Verdict = "approved"
	VerdictProhibited     Verdict = "prohibited"
	VerdictReviewRequired Verdict = "review_required"
	VerdictUnknown        Verdict = "unknown"
)

// Rules is the JSON policy body. Identifiers are SPDX license IDs in list
// casing; "id WITH exception" keys match WITH nodes exactly; a "*" key in a
// class applies to any LicenseRef-/DocumentRef- reference. Anything not
// listed follows UnknownHandling.
type Rules struct {
	Approved       []string `json:"approved"`
	ReviewRequired []string `json:"review_required"`
	Prohibited     []string `json:"prohibited"`
	// AllowOrLater: when true, "GPL-2.0-or-later"/"GPL-2.0+" is classified by
	// the base identifier when the exact or-later form is not listed.
	AllowOrLater bool `json:"allow_or_later"`
}

// Policy is one immutable version.
type Policy struct {
	ID              int64
	Name            string
	Version         int
	Kind            string
	Rules           Rules
	UnknownHandling Verdict
	Description     string
	CreatedBy       string
	Active          bool
}

// Result explains a verdict. Branches records, for OR nodes, which branch
// would be acceptable; a recorded choice is a separate decision, not part of
// evaluation.
type Result struct {
	Verdict     Verdict
	Explanation string
	// AcceptableBranches lists the OR alternatives that evaluate as approved,
	// in written order, when the top-level verdict depends on a choice.
	AcceptableBranches []string
	// Terms lists each leaf with its classification, for display.
	Terms []TermResult
}

type TermResult struct {
	Term    string  `json:"term"`
	Verdict Verdict `json:"verdict"`
}

// ParseRules decodes and validates a policy body: every identifier must be
// an SPDX list ID, "id WITH exception", a LicenseRef-/DocumentRef- pattern,
// or "*"; no identifier may appear in two classes.
func ParseRules(data []byte) (Rules, error) {
	var rules Rules
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&rules); err != nil {
		return Rules{}, fmt.Errorf("invalid policy rules: %w", err)
	}
	seen := map[string]string{}
	for class, terms := range map[string][]string{"approved": rules.Approved, "review_required": rules.ReviewRequired, "prohibited": rules.Prohibited} {
		for _, term := range terms {
			canonical, err := canonicalTerm(term)
			if err != nil {
				return Rules{}, fmt.Errorf("%s: %w", class, err)
			}
			if previous, duplicate := seen[canonical]; duplicate && previous != class {
				return Rules{}, fmt.Errorf("%q is listed as both %s and %s", term, previous, class)
			}
			seen[canonical] = class
		}
	}
	return rules, nil
}

func canonicalTerm(term string) (string, error) {
	term = strings.TrimSpace(term)
	if term == "*" {
		return "*", nil
	}
	lower := strings.ToLower(term)
	if strings.HasPrefix(lower, "licenseref-") || strings.HasPrefix(lower, "documentref-") {
		return term, nil
	}
	parsed := spdxexpr.Parse(term)
	if parsed.Status != spdxexpr.StatusParsed || parsed.Expression == nil {
		return "", fmt.Errorf("%q is not a known SPDX license, exception pair, or reference", term)
	}
	if parsed.Expression.Kind != spdxexpr.KindLicense && parsed.Expression.Kind != spdxexpr.KindWith {
		return "", fmt.Errorf("%q must be a single license or license WITH exception, not a compound expression", term)
	}
	return parsed.Normalized, nil
}

// Evaluate classifies a parsed expression against the policy.
func (policy Policy) Evaluate(expression *spdxexpr.Node) Result {
	if expression == nil {
		return Result{Verdict: policy.UnknownHandling, Explanation: "no license expression is available; " + string(policy.UnknownHandling) + " by policy for unknown licensing"}
	}
	index := policy.index()
	result := Result{}
	verdict := policy.evaluate(expression, index, &result)
	result.Verdict = verdict
	result.Explanation = policy.explain(expression, verdict, result)
	return result
}

type ruleIndex struct {
	byTerm   map[string]Verdict
	wildcard Verdict
	hasWild  bool
}

func (policy Policy) index() ruleIndex {
	index := ruleIndex{byTerm: map[string]Verdict{}}
	add := func(terms []string, verdict Verdict) {
		for _, term := range terms {
			canonical, err := canonicalTerm(term)
			if err != nil {
				continue
			}
			if canonical == "*" {
				index.wildcard, index.hasWild = verdict, true
				continue
			}
			index.byTerm[strings.ToLower(canonical)] = verdict
		}
	}
	add(policy.Rules.Prohibited, VerdictProhibited)
	add(policy.Rules.ReviewRequired, VerdictReviewRequired)
	add(policy.Rules.Approved, VerdictApproved)
	return index
}

func (policy Policy) classifyLeaf(node *spdxexpr.Node, index ruleIndex) Verdict {
	term := strings.ToLower(node.String())
	if verdict, ok := index.byTerm[term]; ok {
		return verdict
	}
	switch node.Kind {
	case spdxexpr.KindReference:
		if index.hasWild {
			return index.wildcard
		}
		return policy.UnknownHandling
	case spdxexpr.KindLicense:
		if !node.Known {
			return policy.UnknownHandling
		}
		if node.OrLater && policy.Rules.AllowOrLater {
			if verdict, ok := index.byTerm[strings.ToLower(node.ID)]; ok {
				return verdict
			}
		}
		// "-or-later" identifiers may be listed by their "-only" base when allowed.
		if policy.Rules.AllowOrLater && strings.HasSuffix(node.ID, "-or-later") {
			if verdict, ok := index.byTerm[strings.ToLower(strings.TrimSuffix(node.ID, "-or-later")+"-only")]; ok {
				return verdict
			}
		}
		return policy.UnknownHandling
	case spdxexpr.KindWith:
		// An unlisted WITH pair is never approved by its base license alone:
		// the exception changes the terms, so it needs its own listing.
		if !node.Known {
			return policy.UnknownHandling
		}
		return VerdictReviewRequired
	}
	return policy.UnknownHandling
}

func (policy Policy) evaluate(node *spdxexpr.Node, index ruleIndex, result *Result) Verdict {
	switch node.Kind {
	case spdxexpr.KindAnd:
		worst := VerdictApproved
		for _, child := range node.Children {
			worst = worse(worst, policy.evaluate(child, index, result))
		}
		return worst
	case spdxexpr.KindOr:
		best := VerdictProhibited
		first := true
		for _, child := range node.Children {
			verdict := policy.evaluate(child, index, result)
			if verdict == VerdictApproved {
				result.AcceptableBranches = append(result.AcceptableBranches, child.String())
			}
			if first {
				best, first = verdict, false
				continue
			}
			best = better(best, verdict)
		}
		return best
	default:
		verdict := policy.classifyLeaf(node, index)
		result.Terms = append(result.Terms, TermResult{Term: node.String(), Verdict: verdict})
		return verdict
	}
}

var severity = map[Verdict]int{VerdictApproved: 0, VerdictReviewRequired: 1, VerdictUnknown: 2, VerdictProhibited: 3}

func worse(left, right Verdict) Verdict {
	if severity[right] > severity[left] {
		return right
	}
	return left
}

func better(left, right Verdict) Verdict {
	if severity[right] < severity[left] {
		return right
	}
	return left
}

func (policy Policy) explain(expression *spdxexpr.Node, verdict Verdict, result Result) string {
	var parts []string
	for _, term := range result.Terms {
		parts = append(parts, term.Term+": "+string(term.Verdict))
	}
	sort.Strings(parts)
	explanation := fmt.Sprintf("%s under %s v%d; terms: %s", verdict, policy.Name, policy.Version, strings.Join(parts, ", "))
	if expression.Kind == spdxexpr.KindOr && len(result.AcceptableBranches) > 0 && len(result.AcceptableBranches) < len(expression.Children) {
		explanation += "; acceptable only by choosing " + strings.Join(result.AcceptableBranches, " or ") + " (a recorded choice is a separate decision)"
	}
	return explanation
}

// ExampleRules is a clearly labeled fixture, not legal advice or a company
// policy. Operators author their own organization policy.
const ExampleRules = `{
  "approved": ["MIT", "ISC", "BSD-2-Clause", "BSD-3-Clause", "Apache-2.0", "0BSD", "Unlicense", "CC0-1.0", "Zlib", "GPL-2.0-only WITH Classpath-exception-2.0"],
  "review_required": ["LGPL-2.1-only", "LGPL-2.1-or-later", "LGPL-3.0-only", "LGPL-3.0-or-later", "MPL-2.0", "EPL-1.0", "EPL-2.0", "CDDL-1.0", "*"],
  "prohibited": ["GPL-2.0-only", "GPL-2.0-or-later", "GPL-3.0-only", "GPL-3.0-or-later", "AGPL-3.0-only", "AGPL-3.0-or-later", "SSPL-1.0", "BUSL-1.1"],
  "allow_or_later": false
}`

// Example returns the example policy (version 1, kind "example").
func Example() Policy {
	rules, err := ParseRules([]byte(ExampleRules))
	if err != nil {
		panic(err)
	}
	return Policy{Name: "graphnest-example", Version: 1, Kind: "example", Rules: rules, UnknownHandling: VerdictReviewRequired,
		Description: "EXAMPLE FIXTURE for demonstration and tests. Not legal advice and not an organization policy; replace with your own before relying on verdicts."}
}

// ErrNoPolicy: no active organization policy exists.
var ErrNoPolicy = errors.New("no active policy")
