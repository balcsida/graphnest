package license

import (
	"strings"
	"testing"
	"time"

	"github.com/balcsida/graphnest/internal/supplychain/spdxexpr"
)

func ptr(value string) *string { return &value }

func registryEvidence(id int64, route, raw string, outcome Outcome) Evidence {
	evidence := Evidence{ID: id, Source: SourceRegistryNPM, Route: route, Coordinates: Coordinates{Ecosystem: "npm", Name: "a", Version: "1"}, Outcome: outcome, FetchedAt: time.Now()}
	if outcome == OutcomeResolved {
		classify(&evidence, raw, RawExpression)
	} else {
		evidence = negative(evidence, outcome, nil, "x")
	}
	return evidence
}

func TestAssessCombinesDeclarationsAndRegistryEvidence(t *testing.T) {
	now := time.Date(2026, 9, 22, 12, 0, 0, 0, time.UTC)
	cases := map[string]struct {
		declared, concluded *string
		registry            []Evidence
		status              AssessmentStatus
		normalized          string
		conflictIn          string
		evidence            int
	}{
		"nothing":                       {declared: ptr("NOASSERTION"), concluded: ptr("NOASSERTION"), status: AssessmentUnknown},
		"declared only":                 {declared: ptr("MIT"), concluded: ptr("NOASSERTION"), status: AssessmentDeclared, normalized: "MIT"},
		"registry only":                 {declared: ptr("NOASSERTION"), registry: []Evidence{registryEvidence(1, "npm:a", "MIT", OutcomeResolved)}, status: AssessmentResolved, normalized: "MIT", evidence: 1},
		"agree":                         {declared: ptr("mit"), registry: []Evidence{registryEvidence(1, "npm:a", "MIT", OutcomeResolved)}, status: AssessmentResolved, normalized: "MIT", evidence: 1},
		"agree structurally":            {declared: ptr("(MIT OR Apache-2.0)"), registry: []Evidence{registryEvidence(1, "npm:a", "MIT OR Apache-2.0", OutcomeResolved)}, status: AssessmentResolved, normalized: "MIT OR Apache-2.0", evidence: 1},
		"disagree":                      {declared: ptr("MIT"), registry: []Evidence{registryEvidence(1, "npm:a", "ISC", OutcomeResolved)}, status: AssessmentConflict, conflictIn: "producer_declared: MIT | registry_npm@npm:a: ISC", evidence: 1},
		"grouping disagrees":            {declared: ptr("MIT AND (ISC OR Apache-2.0)"), registry: []Evidence{registryEvidence(1, "npm:a", "(MIT AND ISC) OR Apache-2.0", OutcomeResolved)}, status: AssessmentConflict, conflictIn: "|", evidence: 1},
		"or branch vs choice":           {declared: ptr("MIT OR Apache-2.0"), concluded: ptr("MIT"), status: AssessmentConflict, conflictIn: "producer_concluded: MIT | producer_declared: MIT OR Apache-2.0"},
		"routes disagree":               {registry: []Evidence{registryEvidence(1, "npm:public", "MIT", OutcomeResolved), registryEvidence(2, "npm:private", "LicenseRef-Acme", OutcomeResolved)}, status: AssessmentConflict, conflictIn: "npm:private: LicenseRef-Acme", evidence: 2},
		"unlicensed registry":           {declared: ptr("NOASSERTION"), registry: []Evidence{registryEvidence(1, "npm:a", "UNLICENSED", OutcomeResolved)}, status: AssessmentUnlicensed, evidence: 1},
		"unlicensed vs declared":        {declared: ptr("MIT"), registry: []Evidence{registryEvidence(1, "npm:a", "UNLICENSED", OutcomeResolved)}, status: AssessmentConflict, conflictIn: "UNLICENSED", evidence: 1},
		"none declared":                 {declared: ptr("NONE"), status: AssessmentUnlicensed},
		"outage keeps unknown":          {declared: ptr("NOASSERTION"), registry: []Evidence{registryEvidence(1, "npm:a", "", OutcomeUnavailable)}, status: AssessmentUnknown, evidence: 1},
		"outage keeps earlier evidence": {registry: []Evidence{registryEvidence(1, "npm:a", "MIT", OutcomeResolved), registryEvidence(2, "npm:a", "", OutcomeUnavailable)}, status: AssessmentResolved, normalized: "MIT", evidence: 2},
		"not found keeps declared":      {declared: ptr("Apache-2.0"), registry: []Evidence{registryEvidence(1, "npm:a", "", OutcomeNotFound)}, status: AssessmentDeclared, normalized: "Apache-2.0", evidence: 1},
		"unknown terms still count":     {registry: []Evidence{registryEvidence(1, "npm:a", "Custom-1.0", OutcomeResolved)}, status: AssessmentResolved, normalized: "Custom-1.0", evidence: 1},
		"free text is not evidence":     {declared: ptr("see LICENSE"), registry: []Evidence{registryEvidence(1, "npm:a", "Copyright Acme", OutcomeResolved)}, status: AssessmentUnknown, evidence: 1},
	}
	for name, test := range cases {
		t.Run(name, func(t *testing.T) {
			got := Assess(7, 3, test.declared, test.concluded, test.registry, now)
			if got.Status != test.status || got.NormalizedExpression != test.normalized || len(got.EvidenceIDs) != test.evidence || got.ComponentID != 7 || got.SnapshotID != 3 {
				t.Fatalf("assessment = %+v", got)
			}
			if test.conflictIn != "" && !strings.Contains(got.ConflictDetail, test.conflictIn) {
				t.Fatalf("conflict detail = %q, want it to contain %q", got.ConflictDetail, test.conflictIn)
			}
			if test.status != AssessmentConflict && got.ConflictDetail != "" {
				t.Fatalf("unexpected conflict detail %q", got.ConflictDetail)
			}
			if len(got.EvidenceFingerprint) != 32 {
				t.Fatalf("fingerprint = %x", got.EvidenceFingerprint)
			}
		})
	}
}

func TestAssessFingerprintTracksMaterialChange(t *testing.T) {
	now := time.Now()
	base := Assess(1, 1, ptr("MIT"), nil, []Evidence{registryEvidence(1, "npm:a", "MIT", OutcomeResolved)}, now)
	same := Assess(1, 1, ptr("MIT"), nil, []Evidence{registryEvidence(1, "npm:a", "MIT", OutcomeResolved)}, now.Add(time.Hour))
	if string(base.EvidenceFingerprint) != string(same.EvidenceFingerprint) {
		t.Fatal("assessment time must not change the evidence fingerprint")
	}
	changed := Assess(1, 1, ptr("MIT"), nil, []Evidence{registryEvidence(1, "npm:a", "ISC", OutcomeResolved)}, now)
	if string(base.EvidenceFingerprint) == string(changed.EvidenceFingerprint) {
		t.Fatal("new registry evidence must change the fingerprint so reviews can detect it")
	}
	if spdxexpr.Parse("MIT").Status != spdxexpr.StatusParsed {
		t.Fatal("sanity")
	}
}
