package graphquery

import (
	"math"
	"testing"

	"github.com/grepnest/grepnest/internal/graphartifact"
	"github.com/grepnest/grepnest/internal/graphprotocol"
)

func TestImpactAnchorsDuplicateTargetUIDToSelectedRepository(t *testing.T) {
	service := seededQueryServiceWithArtifacts(t, callChain("A", "B"), repositoryCallChain(202, "A", "C"))
	got, err := service.Impact(t.Context(), graphprotocol.ImpactRequest{
		Scope: graphprotocol.Scope{SelectedRepositoryID: 101, Repositories: []graphprotocol.RepositorySnapshot{
			{ID: 101, Name: "acme/one", Commit: testCommit},
			{ID: 202, Name: "acme/two", Commit: testCommit},
		}},
		TargetUID: "A", Direction: "downstream",
	})
	if err != nil || got.Status != graphprotocol.StatusFound || len(got.Candidates) != 0 ||
		len(got.ByDepth[1]) != 1 || got.ByDepth[1][0].UID != "B" || got.ByDepth[1][0].RepositoryID != 101 {
		t.Fatalf("Impact()=%#v,%v", got, err)
	}
}

func TestImpactTraversesOnlyAuthorizedRepositoriesAfterAnchoring(t *testing.T) {
	service := seededQueryServiceWithArtifacts(t,
		repositorySymbols(101, "A", "Z"),
		repositorySymbols(202, "A", "B", "Z"),
		repositorySymbols(303, "C"),
	)
	addCrossRepositoryCall(t, service, 101, "A", 202, "B")
	addCrossRepositoryCall(t, service, 202, "B", 101, "Z")
	addCrossRepositoryCall(t, service, 101, "A", 303, "C")
	addCrossRepositoryCall(t, service, 303, "C", 101, "Z")

	scope := graphprotocol.Scope{SelectedRepositoryID: 101, Repositories: []graphprotocol.RepositorySnapshot{
		{ID: 101, Name: "acme/one", Commit: testCommit},
		{ID: 202, Name: "acme/two", Commit: testCommit},
	}}
	got, err := service.Impact(t.Context(), graphprotocol.ImpactRequest{
		Scope:     scope,
		TargetUID: "A", Direction: "downstream", Relations: []string{"calls"}, MaxDepth: 2,
	})
	if err != nil || got.Status != graphprotocol.StatusFound || len(got.Candidates) != 0 ||
		len(got.ByDepth[1]) != 1 || got.ByDepth[1][0].RepositoryID != 202 || got.ByDepth[1][0].UID != "B" ||
		len(got.ByDepth[2]) != 1 || got.ByDepth[2][0].RepositoryID != 101 || got.ByDepth[2][0].UID != "Z" {
		t.Fatalf("Impact(downstream)=%#v,%v", got, err)
	}
	for _, symbols := range got.ByDepth {
		for _, symbol := range symbols {
			if symbol.RepositoryID == 303 {
				t.Fatalf("Impact(downstream) leaked unauthorized repository: %#v", got)
			}
		}
	}

	got, err = service.Impact(t.Context(), graphprotocol.ImpactRequest{
		Scope:     scope,
		TargetUID: "Z", Direction: "upstream", Relations: []string{"calls"}, MaxDepth: 2,
	})
	if err != nil || got.Status != graphprotocol.StatusFound || len(got.Candidates) != 0 ||
		len(got.ByDepth[1]) != 1 || got.ByDepth[1][0].RepositoryID != 202 || got.ByDepth[1][0].UID != "B" ||
		len(got.ByDepth[2]) != 1 || got.ByDepth[2][0].RepositoryID != 101 || got.ByDepth[2][0].UID != "A" {
		t.Fatalf("Impact(upstream)=%#v,%v", got, err)
	}
	for _, symbols := range got.ByDepth {
		for _, symbol := range symbols {
			if symbol.RepositoryID == 303 {
				t.Fatalf("Impact(upstream) leaked unauthorized repository: %#v", got)
			}
		}
	}
}

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

func TestImpactUsesConfiguredDefaultDepth(t *testing.T) {
	service := seededQueryService(t, callChain("A", "B", "C", "D"))
	service.Limits = Limits{DefaultImpactDepth: 2, MaxDepth: 7}
	got, err := service.Impact(t.Context(), graphprotocol.ImpactRequest{
		Scope: scope(testCommit), TargetUID: "A", Direction: "downstream",
	})
	if err != nil || len(got.ByDepth[2]) != 1 || len(got.ByDepth[3]) != 0 {
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
