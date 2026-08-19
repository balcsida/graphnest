//go:build integration

package postgres

import (
	"context"
	"errors"
	"testing"

	"github.com/grepnest/grepnest/internal/graphartifact"
	"github.com/grepnest/grepnest/internal/graphprotocol"
	"github.com/grepnest/grepnest/internal/graphquery"
	"github.com/grepnest/grepnest/internal/scipgraph"
)

func TestPostgresGraphQueryStoreSupportsSCIPFallback(t *testing.T) {
	store, repositoryID := readyGraphStore(t, testSHA('a'))
	const (
		root = "scip go example.com/acme/root v1 Root()."
		next = "scip go example.com/acme/next v1 Next()."
		last = "scip go example.com/acme/last v1 Last()."
	)
	upload := scipgraph.Upload{
		ProjectRoot: "file:///src", IndexerName: "test", IndexerVersion: "1",
		Occurrences: []scipgraph.Occurrence{
			{Path: "root.go", Symbol: root, EndCharacter: 1, PositionEncoding: 1},
			{Path: "next.go", Symbol: next, EndCharacter: 1, PositionEncoding: 1},
			{Path: "last.go", Symbol: last, EndCharacter: 1, PositionEncoding: 1},
		},
		Relationships: []scipgraph.Relationship{
			{Path: "root.go", Source: root, Target: next, Reference: true},
			{Path: "next.go", Source: next, Target: last, Reference: true},
		},
	}
	if err := store.ReplaceSCIP(t.Context(), repositoryID, testSHA('a'), upload); err != nil {
		t.Fatal(err)
	}
	service := &graphquery.Service{Store: store, Limits: graphquery.Limits{MaxFanout: 10, MaxNodes: 10, MaxEdges: 10}}
	scope := graphprotocol.Scope{SelectedRepositoryID: repositoryID, Repositories: []graphprotocol.RepositorySnapshot{{ID: repositoryID, Name: "acme/scip", Commit: testSHA('a')}}}

	contextResult, err := service.Context(t.Context(), graphprotocol.ContextRequest{Scope: scope, UID: "symbol:" + next, Relations: []string{"references"}})
	if err != nil || contextResult.Status != graphprotocol.StatusFound || len(contextResult.Incoming["references"]) != 1 || len(contextResult.Outgoing["references"]) != 1 {
		t.Fatalf("Context()=%#v err=%v", contextResult, err)
	}
	impact, err := service.Impact(t.Context(), graphprotocol.ImpactRequest{Scope: scope, TargetUID: "symbol:" + root, Direction: "downstream", Relations: []string{"references"}, MaxDepth: 3, IncludeTests: true})
	if err != nil || impact.Status != graphprotocol.StatusFound || len(impact.ByDepth[1]) != 1 || len(impact.ByDepth[2]) != 1 {
		t.Fatalf("Impact()=%#v err=%v", impact, err)
	}
	trace, err := service.Trace(t.Context(), graphprotocol.TraceRequest{Scope: scope, SourceUID: "symbol:" + root, TargetUID: "symbol:" + last, MaxDepth: 3})
	if err != nil || trace.Status != graphprotocol.StatusNoPath || trace.Commits["acme/scip"] != testSHA('a') {
		t.Fatalf("Trace()=%#v err=%v", trace, err)
	}
}

func TestSCIPGraphMaterializationRespectsExplicitUploadPrecedence(t *testing.T) {
	store, repositoryID := readyGraphStore(t, testSHA('a'))
	if err := store.ReplaceSCIP(t.Context(), repositoryID, testSHA('a'), uploadWith("fallback.go", globalSymbol, definitionRole)); err != nil {
		t.Fatal(err)
	}
	var source GraphSource
	if err := store.pool.QueryRow(t.Context(), `select source from graph_uploads where repository_id=$1`, repositoryID).Scan(&source); err != nil || source != GraphSourceSCIP {
		t.Fatalf("initial source=%q err=%v", source, err)
	}

	explicit, err := store.ReplaceGraph(t.Context(), repositoryID, GraphSourceManaged, artifactFor(repositoryID, testSHA('a'), "explicit"))
	if err != nil || !explicit.Applied {
		t.Fatalf("ReplaceGraph()=%#v err=%v", explicit, err)
	}
	if err := store.ReplaceSCIP(t.Context(), repositoryID, testSHA('a'), uploadWith("new-fallback.go", implementationSymbol, definitionRole)); err != nil {
		t.Fatal(err)
	}
	var uploadID int64
	if err := store.pool.QueryRow(t.Context(), `select id, source from graph_uploads where repository_id=$1`, repositoryID).Scan(&uploadID, &source); err != nil || uploadID != explicit.Upload.ID || source != GraphSourceManaged {
		t.Fatalf("current explicit upload id=%d source=%q err=%v", uploadID, source, err)
	}

	if _, err := store.pool.Exec(t.Context(), `update repositories set indexed_sha=$2, desired_sha=$2 where id=$1`, repositoryID, testSHA('b')); err != nil {
		t.Fatal(err)
	}
	if err := store.ReplaceSCIP(t.Context(), repositoryID, testSHA('b'), uploadWith("current.go", globalSymbol, definitionRole)); err != nil {
		t.Fatal(err)
	}
	if err := store.pool.QueryRow(t.Context(), `select source from graph_uploads where repository_id=$1`, repositoryID).Scan(&source); err != nil || source != GraphSourceSCIP {
		t.Fatalf("replacement source=%q err=%v", source, err)
	}
	if err := store.ReplaceSCIP(t.Context(), repositoryID, testSHA('b'), uploadWith("refreshed.go", implementationSymbol, definitionRole)); err != nil {
		t.Fatal(err)
	}
	var path string
	if err := store.pool.QueryRow(t.Context(), `select path from graph_nodes join graph_uploads on graph_uploads.id=graph_nodes.upload_id
		where graph_uploads.repository_id=$1 and graph_nodes.kind=$2`, repositoryID, graphartifact.NodeFile).Scan(&path); err != nil || path != "refreshed.go" {
		t.Fatalf("refreshed path=%q err=%v", path, err)
	}
}

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
