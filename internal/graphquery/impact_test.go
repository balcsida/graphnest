package graphquery

import (
	"math"
	"testing"

	"github.com/grepnest/grepnest/internal/graphartifact"
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

func TestImpactFiltersAndReturnsStoredConfidence(t *testing.T) {
	artifact := callChain("A", "B", "C")
	for index := range artifact.Edges {
		if artifact.Edges[index].Kind != graphartifact.EdgeCalls {
			continue
		}
		artifact.Edges[index].Confidence = 0.4
		if artifact.Edges[index].TargetUID == "C" {
			artifact.Edges[index].SourceUID = "A"
			artifact.Edges[index].Confidence = 0.8
			artifact.Edges[index].Path = "main.go"
			artifact.Edges[index].Range = graphartifact.Range{StartLine: 7, StartCharacter: 1, EndLine: 7, EndCharacter: 2}
			artifact.Edges[index].ResolutionReason = "exact"
		}
	}
	got, err := seededQueryService(t, artifact).Impact(t.Context(), graphprotocol.ImpactRequest{
		Scope: scope(testCommit), TargetUID: "A", Direction: "downstream",
		Relations: []string{"calls"}, MinConfidence: 0.5, MaxDepth: 1, IncludeTests: true,
	})
	if err != nil || len(got.ByDepth[1]) != 1 || got.ByDepth[1][0].UID != "C" || len(got.Edges) != 1 {
		t.Fatalf("Impact()=%#v,%v", got, err)
	}
	edge := got.Edges[0]
	if math.Abs(edge.Confidence-0.8) > 1e-6 || edge.Path != "main.go" || edge.Range.StartLine != 7 || edge.ResolutionReason != "exact" {
		t.Fatalf("edge = %#v", edge)
	}
}

func TestImpactRejectsNegativeBounds(t *testing.T) {
	service := seededQueryService(t, callChain("A", "B"))
	base := graphprotocol.ImpactRequest{Scope: scope(testCommit), TargetUID: "A", Direction: "downstream"}
	for name, mutate := range map[string]func(*graphprotocol.ImpactRequest){
		"depth":  func(request *graphprotocol.ImpactRequest) { request.MaxDepth = -1 },
		"limit":  func(request *graphprotocol.ImpactRequest) { request.Limit = -1 },
		"offset": func(request *graphprotocol.ImpactRequest) { request.Offset = -1 },
	} {
		t.Run(name, func(t *testing.T) {
			request := base
			mutate(&request)
			if _, err := service.Impact(t.Context(), request); err == nil {
				t.Fatal("negative bound unexpectedly succeeded")
			}
		})
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
