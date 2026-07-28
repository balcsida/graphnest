package graphquery

import (
	"testing"

	"github.com/grepnest/grepnest/internal/graphartifact"
	"github.com/grepnest/grepnest/internal/graphprotocol"
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
