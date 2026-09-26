package license

import (
	"crypto/sha256"
	"sort"
	"strings"
	"time"

	"github.com/balcsida/graphnest/internal/supplychain/spdxexpr"
)

// Assess derives one component's assessment from the producer's raw
// declaration/conclusion and the latest registry evidence. Rules:
//
//   - Producer NOASSERTION/empty contributes nothing; NONE/UNLICENSED asserts
//     "no license granted" and is kept as such, never mapped.
//   - A registry expression that parsed is the resolved basis. A parsed
//     producer expression that is structurally different from a parsed
//     registry expression is a conflict, with both shown.
//   - License files, URLs, and unparseable names never become an expression;
//     they leave the status unknown (visible in the evidence detail).
//   - Two registry routes disagreeing is also a conflict.
//
// The result carries the IDs of every evidence row considered and a
// fingerprint of their material fields so a review can detect later change.
func Assess(componentID, snapshotID int64, declaredRaw, concludedRaw *string, registry []Evidence, now time.Time) Assessment {
	assessment := Assessment{ComponentID: componentID, SnapshotID: snapshotID, AssessedAt: now.UTC(), Status: AssessmentUnknown}
	var candidates []candidate
	hash := sha256.New()
	consider := func(label, raw string) {
		parsed := spdxexpr.Parse(raw)
		hash.Write([]byte(label + "\x00" + raw + "\x00" + string(parsed.Status) + "\x00"))
		switch parsed.Status {
		case spdxexpr.StatusParsed, spdxexpr.StatusUnknownTerms:
			candidates = append(candidates, candidate{label: label, expression: parsed.Expression, normalized: parsed.Normalized, status: parsed.Status})
		case spdxexpr.StatusNone, spdxexpr.StatusUnlicensed:
			candidates = append(candidates, candidate{label: label, status: parsed.Status})
		}
	}
	if concludedRaw != nil {
		consider("producer_concluded", *concludedRaw)
	}
	if declaredRaw != nil {
		consider("producer_declared", *declaredRaw)
	}
	pending := false
	sort.SliceStable(registry, func(i, j int) bool { return registry[i].ID < registry[j].ID })
	for _, evidence := range registry {
		assessment.EvidenceIDs = append(assessment.EvidenceIDs, evidence.ID)
		hash.Write(evidence.Fingerprint())
		switch evidence.Outcome {
		case OutcomeResolved:
			switch evidence.ParseStatus {
			case spdxexpr.StatusParsed, spdxexpr.StatusUnknownTerms:
				tree := evidence.ExpressionTree
				if tree == nil {
					// Re-derive the tree from the stored normalized expression so a
					// row written without one is never mistaken for a refusal.
					if parsed := spdxexpr.Parse(evidence.NormalizedExpression); parsed.Expression != nil {
						tree = parsed.Expression
					} else {
						continue
					}
				}
				candidates = append(candidates, candidate{label: string(evidence.Source) + "@" + evidence.Route, expression: tree, normalized: evidence.NormalizedExpression, status: evidence.ParseStatus})
			case spdxexpr.StatusNone, spdxexpr.StatusUnlicensed:
				candidates = append(candidates, candidate{label: string(evidence.Source) + "@" + evidence.Route, status: evidence.ParseStatus})
			}
		case OutcomeUnavailable:
			// An outage retains earlier evidence with its age; it does not
			// make the component "unlicensed". Nothing to add.
		}
	}
	if len(assessment.EvidenceIDs) == 0 {
		assessment.EvidenceIDs = []int64{}
	}
	assessment.EvidenceFingerprint = hash.Sum(nil)
	if len(candidates) == 0 {
		if pending {
			assessment.Status = AssessmentPending
		}
		return assessment
	}
	var expressions []candidate
	var refusals []candidate
	for _, item := range candidates {
		if item.expression != nil {
			expressions = append(expressions, item)
		} else {
			refusals = append(refusals, item)
		}
	}
	// Structural disagreement among parsed expressions is a conflict.
	for index := 1; index < len(expressions); index++ {
		if !spdxexpr.Equal(expressions[0].expression, expressions[index].expression) {
			assessment.Status = AssessmentConflict
			assessment.ConflictDetail = describe(expressions)
			return assessment
		}
	}
	if len(expressions) > 0 && len(refusals) > 0 {
		assessment.Status = AssessmentConflict
		assessment.ConflictDetail = describe(append(expressions, refusals...))
		return assessment
	}
	if len(expressions) == 0 {
		assessment.Status = AssessmentUnlicensed
		assessment.ConflictDetail = ""
		return assessment
	}
	assessment.NormalizedExpression = expressions[0].normalized
	registryBacked := false
	for _, item := range expressions {
		if strings.HasPrefix(item.label, "registry_") {
			registryBacked = true
		}
	}
	if registryBacked {
		assessment.Status = AssessmentResolved
	} else {
		assessment.Status = AssessmentDeclared
	}
	return assessment
}

type candidate struct {
	label      string
	expression *spdxexpr.Node
	normalized string
	status     spdxexpr.Status
}

func describe(items []candidate) string {
	parts := make([]string, 0, len(items))
	for _, item := range items {
		value := item.normalized
		if value == "" {
			value = strings.ToUpper(string(item.status))
		}
		parts = append(parts, item.label+": "+value)
	}
	return strings.Join(parts, " | ")
}

// AssessWithHuman is Assess with human conclusions given precedence: the
// newest 'human' evidence, when it parsed, becomes the assessment's basis and
// automated evidence that disagrees is reported in the conflict detail
// without changing the status. The fingerprint still covers every row, so a
// later automated change is detectable and can require re-review.
func AssessWithHuman(componentID, snapshotID int64, declaredRaw, concludedRaw *string, registry []Evidence, now time.Time) Assessment {
	var human *Evidence
	for index := range registry {
		if registry[index].Source == SourceHuman && registry[index].Outcome == OutcomeResolved && (human == nil || registry[index].ID > human.ID) {
			human = &registry[index]
		}
	}
	if human == nil {
		return Assess(componentID, snapshotID, declaredRaw, concludedRaw, registry, now)
	}
	automated := make([]Evidence, 0, len(registry))
	for _, evidence := range registry {
		if evidence.Source != SourceHuman {
			automated = append(automated, evidence)
		}
	}
	base := Assess(componentID, snapshotID, declaredRaw, concludedRaw, registry, now)
	assessment := Assessment{ComponentID: componentID, SnapshotID: snapshotID, AssessedAt: now.UTC(), EvidenceIDs: base.EvidenceIDs, EvidenceFingerprint: base.EvidenceFingerprint}
	switch human.ParseStatus {
	case spdxexpr.StatusParsed, spdxexpr.StatusUnknownTerms:
		assessment.Status = AssessmentResolved
		assessment.NormalizedExpression = human.NormalizedExpression
	case spdxexpr.StatusNone, spdxexpr.StatusUnlicensed:
		assessment.Status = AssessmentUnlicensed
	default:
		return base
	}
	without := Assess(componentID, snapshotID, declaredRaw, concludedRaw, automated, now)
	if without.Status != AssessmentUnknown && (without.NormalizedExpression != assessment.NormalizedExpression || without.Status == AssessmentConflict) {
		detail := "human conclusion (" + human.NormalizedExpression + ") overrides automated evidence"
		if without.ConflictDetail != "" {
			detail += ": " + without.ConflictDetail
		} else if without.NormalizedExpression != "" {
			detail += ": " + without.NormalizedExpression
		}
		assessment.ConflictDetail = detail
	}
	return assessment
}
