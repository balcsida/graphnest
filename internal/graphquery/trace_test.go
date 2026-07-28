package graphquery

import (
	"fmt"
	"testing"

	"github.com/grepnest/grepnest/internal/graphartifact"
	"github.com/grepnest/grepnest/internal/graphprotocol"
	"github.com/grepnest/grepnest/internal/ladybug"
)

func TestTraceReturnsOneShortestDirectedPath(t *testing.T) {
	artifact := callChain("A", "B", "C")
	artifact.Edges = append(artifact.Edges,
		graphartifact.Edge{SourceUID: "A", TargetUID: "C", Kind: graphartifact.EdgeCalls, Confidence: 1},
	)
	got, err := seededQueryService(t, artifact).Trace(t.Context(), graphprotocol.TraceRequest{
		Scope: scope(testCommit), SourceUID: "A", TargetUID: "C", MaxDepth: 10,
	})
	if err != nil || got.Status != graphprotocol.StatusOK || len(got.Nodes) != 2 ||
		got.Nodes[0].UID != "A" || got.Nodes[1].UID != "C" || len(got.Edges) != 1 {
		t.Fatalf("Trace()=%#v,%v", got, err)
	}
}

func TestTraceUsesConfiguredDefaultDepth(t *testing.T) {
	service := seededQueryService(t, callChain("A", "B", "C"))
	service.Limits = Limits{DefaultTraceDepth: 1, MaxTraceDepth: 9}
	got, err := service.Trace(t.Context(), graphprotocol.TraceRequest{
		Scope: scope(testCommit), SourceUID: "A", TargetUID: "C",
	})
	if err != nil || got.Status != graphprotocol.StatusNoPath {
		t.Fatalf("Trace()=%#v,%v", got, err)
	}
}

func TestTraceReportsFanoutBoundary(t *testing.T) {
	artifact := callChain("A", "B", "C")
	artifact.Nodes = append(artifact.Nodes, graphartifact.Node{
		UID: "D", Kind: graphartifact.NodeSymbol, Path: "main.go", Language: "go", SymbolKind: "func", QualifiedName: "D",
	})
	artifact.Edges = append(artifact.Edges,
		graphartifact.Edge{SourceUID: "file:main.go", TargetUID: "D", Kind: graphartifact.EdgeContains, Confidence: 1},
		graphartifact.Edge{SourceUID: "A", TargetUID: "D", Kind: graphartifact.EdgeCalls, Confidence: 1},
	)
	service := seededQueryService(t, artifact)
	service.Limits = Limits{MaxDepth: 10, MaxNodes: 10, MaxEdges: 10, MaxFanout: 1}
	got, err := service.Trace(t.Context(), graphprotocol.TraceRequest{
		Scope: scope(testCommit), SourceUID: "A", TargetUID: "D", MaxDepth: 10,
	})
	if err != nil || got.Status != graphprotocol.StatusNoPath || len(got.Boundaries) == 0 {
		t.Fatalf("Trace()=%#v,%v", got, err)
	}
}

func TestTraceDoesNotStitchSameUIDAcrossRepositories(t *testing.T) {
	first := callChain("A", "B")
	second := repositoryCallChain(202, "B", "C")
	service := seededQueryServiceWithArtifacts(t, first, second)
	got, err := service.Trace(t.Context(), graphprotocol.TraceRequest{
		Scope: graphprotocol.Scope{SelectedRepositoryID: 101, Repositories: []graphprotocol.RepositorySnapshot{
			{ID: 101, Name: "acme/one", Commit: testCommit},
			{ID: 202, Name: "acme/two", Commit: testCommit},
		}},
		SourceUID: "A", TargetUID: "C", MaxDepth: 3,
	})
	if err != nil || got.Status != graphprotocol.StatusNoPath || len(got.Nodes) != 0 {
		t.Fatalf("Trace()=%#v,%v", got, err)
	}
}

func TestTraceAnchorsDuplicateEndpointsToSelectedRepository(t *testing.T) {
	service := seededQueryServiceWithArtifacts(t, callChain("A", "B"), repositoryCallChain(202, "A", "B"))
	got, err := service.Trace(t.Context(), graphprotocol.TraceRequest{
		Scope: graphprotocol.Scope{SelectedRepositoryID: 101, Repositories: []graphprotocol.RepositorySnapshot{
			{ID: 101, Name: "acme/one", Commit: testCommit},
			{ID: 202, Name: "acme/two", Commit: testCommit},
		}},
		SourceUID: "A", TargetUID: "B",
	})
	if err != nil || got.Status != graphprotocol.StatusOK || len(got.Candidates) != 0 || len(got.Nodes) != 2 {
		t.Fatalf("Trace()=%#v,%v", got, err)
	}
	for _, node := range got.Nodes {
		if node.RepositoryID != 101 {
			t.Fatalf("Trace() selected repository %d: %#v", node.RepositoryID, got)
		}
	}
}

func TestTraceTraversesOnlyAuthorizedRepositoriesAfterAnchoring(t *testing.T) {
	service := seededQueryServiceWithArtifacts(t,
		repositorySymbols(101, "A", "Z"),
		repositorySymbols(202, "A", "B", "Z"),
		repositorySymbols(303, "C"),
	)
	addCrossRepositoryCall(t, service, 101, "A", 202, "B")
	addCrossRepositoryCall(t, service, 202, "B", 101, "Z")
	addCrossRepositoryCall(t, service, 101, "A", 303, "C")
	addCrossRepositoryCall(t, service, 303, "C", 101, "Z")

	got, err := service.Trace(t.Context(), graphprotocol.TraceRequest{
		Scope: graphprotocol.Scope{SelectedRepositoryID: 101, Repositories: []graphprotocol.RepositorySnapshot{
			{ID: 101, Name: "acme/one", Commit: testCommit},
			{ID: 202, Name: "acme/two", Commit: testCommit},
		}},
		SourceUID: "A", TargetUID: "Z", MaxDepth: 2,
	})
	if err != nil || got.Status != graphprotocol.StatusOK || len(got.Candidates) != 0 || len(got.Nodes) != 3 ||
		got.Nodes[0].RepositoryID != 101 || got.Nodes[0].UID != "A" ||
		got.Nodes[1].RepositoryID != 202 || got.Nodes[1].UID != "B" ||
		got.Nodes[2].RepositoryID != 101 || got.Nodes[2].UID != "Z" {
		t.Fatalf("Trace()=%#v,%v", got, err)
	}
}

func TestTraceRejectsNegativeDepth(t *testing.T) {
	service := seededQueryService(t, callChain("A", "B"))
	if _, err := service.Trace(t.Context(), graphprotocol.TraceRequest{
		Scope: scope(testCommit), SourceUID: "A", TargetUID: "B", MaxDepth: -1,
	}); err == nil {
		t.Fatal("negative depth unexpectedly succeeded")
	}
}

func TestTraceChoosesStableLowestRepositoryPath(t *testing.T) {
	higher := repositoryCallChain(10, "X", "B", "Y")
	lower := repositoryCallChain(2, "A", "B", "Z")
	service := seededQueryServiceWithArtifacts(t, higher, lower)
	request := graphprotocol.TraceRequest{
		Scope: graphprotocol.Scope{SelectedRepositoryID: 2, Repositories: []graphprotocol.RepositorySnapshot{
			{ID: 10, Name: "acme/ten", Commit: testCommit},
			{ID: 2, Name: "acme/two", Commit: testCommit},
		}},
		SourceUID: "A", TargetUID: "Z", MaxDepth: 3,
	}
	for range 20 {
		got, err := service.Trace(t.Context(), request)
		if err != nil || got.Status != graphprotocol.StatusOK || len(got.Nodes) != 3 {
			t.Fatalf("Trace()=%#v,%v", got, err)
		}
		for _, node := range got.Nodes {
			if node.RepositoryID != 2 {
				t.Fatalf("Trace() chose repository %d: %#v", node.RepositoryID, got)
			}
		}
	}
}

func repositoryCallChain(repositoryID int64, names ...string) graphartifact.Artifact {
	artifact := callChain(names...)
	artifact.RepositoryID = repositoryID
	repositoryUID := fmt.Sprintf("repository:%d", repositoryID)
	artifact.Nodes[0].UID = repositoryUID
	artifact.Nodes[0].QualifiedName = fmt.Sprintf("acme/repo-%d", repositoryID)
	for index := range artifact.Edges {
		if artifact.Edges[index].SourceUID == "repository:101" {
			artifact.Edges[index].SourceUID = repositoryUID
		}
	}
	return artifact
}

func repositorySymbols(repositoryID int64, names ...string) graphartifact.Artifact {
	artifact := repositoryCallChain(repositoryID, names...)
	edges := artifact.Edges[:0]
	for _, edge := range artifact.Edges {
		if edge.Kind != graphartifact.EdgeCalls {
			edges = append(edges, edge)
		}
	}
	artifact.Edges = edges
	return artifact
}

func addCrossRepositoryCall(t *testing.T, service *Service, sourceRepositoryID int64, sourceUID string, targetRepositoryID int64, targetUID string) {
	t.Helper()
	err := service.Database.Update(t.Context(), func(session *ladybug.Session) error {
		_, err := session.Execute(t.Context(), `MATCH (a:Symbol {uid: $source}), (b:Symbol {uid: $target}) CREATE (a)-[:CALLS {path: "", start_line: 0, start_character: 0, end_line: 0, end_character: 0, confidence: 1.0, resolution_reason: "test"}]->(b)`, map[string]any{
			"source": fmt.Sprintf("%d:%s", sourceRepositoryID, sourceUID),
			"target": fmt.Sprintf("%d:%s", targetRepositoryID, targetUID),
		}, ladybug.QueryLimits{MaxRows: 1})
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
}
