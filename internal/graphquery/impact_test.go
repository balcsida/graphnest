package graphquery

import (
	"testing"

	"github.com/grepnest/grepnest/internal/graphprotocol"
)

func TestImpactGroupsByDepth(t *testing.T) {
	service := seededQueryService(t, callChain("A", "B", "C"))
	got, err := service.Impact(t.Context(), graphprotocol.ImpactRequest{
		Scope: scope(testCommit), TargetUID: "A", Direction: "downstream", MaxDepth: 3,
	})
	if err != nil || len(got.ByDepth[1]) != 1 || got.ByDepth[1][0].UID != "B" ||
		len(got.ByDepth[2]) != 1 || got.ByDepth[2][0].UID != "C" {
		t.Fatalf("Impact()=%#v,%v", got, err)
	}
}

func TestImpactHonorsDirectionAndBounds(t *testing.T) {
	service := seededQueryService(t, callChain("A", "B", "C", "D"))
	service.Limits = Limits{MaxDepth: 2, MaxNodes: 1, MaxEdges: 1, MaxFanout: 1}
	got, err := service.Impact(t.Context(), graphprotocol.ImpactRequest{
		Scope: scope(testCommit), TargetUID: "D", Direction: "upstream", MaxDepth: 20,
		Relations: []string{"calls"}, MinConfidence: .5,
	})
	if err != nil || len(got.ByDepth[1]) != 1 || got.ByDepth[1][0].UID != "C" ||
		!got.Partial || len(got.Boundaries) == 0 {
		t.Fatalf("Impact()=%#v,%v", got, err)
	}
}
