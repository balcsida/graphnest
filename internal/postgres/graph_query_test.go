//go:build integration

package postgres

import (
	"context"
	"errors"
	"testing"

	"github.com/grepnest/grepnest/internal/graphartifact"
	"github.com/grepnest/grepnest/internal/graphprotocol"
	"github.com/grepnest/grepnest/internal/graphquery"
)

func TestPostgresGraphQueryStoreSupportsBoundedTraversal(t *testing.T) {
	store, repositoryID := readyGraphStore(t, testSHA('a'))
	artifact := artifactFor(repositoryID, testSHA('a'), "query")
	artifact.Nodes = append(artifact.Nodes,
		graphartifact.Node{UID: "next", Kind: graphartifact.NodeSymbol, Path: "b.go", Language: "go", QualifiedName: "Next", Range: graphartifact.Range{EndCharacter: 1}},
		graphartifact.Node{UID: "last", Kind: graphartifact.NodeSymbol, Path: "c_test.go", Language: "go", QualifiedName: "Last", Range: graphartifact.Range{EndCharacter: 1}},
	)
	artifact.Edges = append(artifact.Edges,
		graphartifact.Edge{SourceUID: "symbol", TargetUID: "next", Kind: graphartifact.EdgeCalls, Path: "a.go", Confidence: .9, ResolutionReason: "fixture"},
		graphartifact.Edge{SourceUID: "next", TargetUID: "last", Kind: graphartifact.EdgeCalls, Path: "b.go", Confidence: .8, ResolutionReason: "fixture"},
		graphartifact.Edge{SourceUID: "last", TargetUID: "symbol", Kind: graphartifact.EdgeCalls, Path: "c_test.go", Confidence: .7, ResolutionReason: "cycle"},
	)
	artifact.ContentHash[0]++
	if _, err := store.ReplaceGraph(t.Context(), repositoryID, GraphSourceManaged, artifact); err != nil {
		t.Fatal(err)
	}
	service := &graphquery.Service{Store: store, Limits: graphquery.Limits{MaxFanout: 10, MaxNodes: 10, MaxEdges: 10}}
	scope := graphprotocol.Scope{SelectedRepositoryID: repositoryID, Repositories: []graphprotocol.RepositorySnapshot{{ID: repositoryID, Name: "acme/query", Commit: testSHA('a')}}}

	contextResult, err := service.Context(t.Context(), graphprotocol.ContextRequest{Scope: scope, UID: "next", Relations: []string{"calls"}})
	if err != nil || contextResult.Status != graphprotocol.StatusFound || len(contextResult.Incoming["calls"]) != 1 || len(contextResult.Outgoing["calls"]) != 1 {
		t.Fatalf("Context()=%#v err=%v", contextResult, err)
	}
	impact, err := service.Impact(t.Context(), graphprotocol.ImpactRequest{Scope: scope, TargetUID: "symbol", Direction: "downstream", Relations: []string{"calls"}, MaxDepth: 3, IncludeTests: true})
	if err != nil || impact.Status != graphprotocol.StatusFound || len(impact.ByDepth[1]) != 1 || len(impact.ByDepth[2]) != 1 || len(impact.ByDepth[3]) != 0 {
		t.Fatalf("Impact()=%#v err=%v", impact, err)
	}
	trace, err := service.Trace(t.Context(), graphprotocol.TraceRequest{Scope: scope, SourceUID: "symbol", TargetUID: "last", MaxDepth: 3})
	if err != nil || trace.Status != graphprotocol.StatusOK || len(trace.Nodes) != 3 || len(trace.Edges) != 2 {
		t.Fatalf("Trace()=%#v err=%v", trace, err)
	}

	canceled, cancel := context.WithCancel(t.Context())
	cancel()
	_, err = store.Symbols(canceled, graphquery.SymbolQuery{Snapshots: []graphquery.QuerySnapshot{{RepositoryID: repositoryID, Commit: testSHA('a')}}, UID: "symbol", Limit: 1})
	if !errors.Is(err, canceled.Err()) {
		t.Fatalf("Symbols() error=%v want=%v", err, canceled.Err())
	}
}
