package graphartifact

import (
	"testing"

	graphv2 "github.com/balcsida/graphnest/internal/graphartifact/v2"
)

// FixtureV2ForOracle exposes the Python-bridge converter so graphimport can be
// proven against it from the external test package.
func FixtureV2ForOracle(t *testing.T) *graphv2.Artifact { return fixtureV2(t, fixtureRows(t)) }
