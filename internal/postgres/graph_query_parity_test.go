//go:build integration

package postgres

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/balcsida/graphnest/internal/graphartifact"
	"github.com/balcsida/graphnest/internal/graphprotocol"
	"github.com/balcsida/graphnest/internal/graphquery"
)

func TestGraphQueryStoresMatchGolden(t *testing.T) {
	postgresStore, repositoryID := readyGraphStore(t, testSHA('a'))
	artifact := parityArtifact(repositoryID, testSHA('a'))
	replacement, err := postgresStore.ReplaceGraph(t.Context(), repositoryID, GraphSourceManaged, artifact)
	if err != nil {
		t.Fatal(err)
	}
	assertGraphQueryPlan(t, postgresStore, repositoryID, replacement.Upload.ID, artifact.Commit)
	hiddenID := seedReadyRepository(t, postgresStore, 102, testSHA('c'))
	hiddenArtifact := parityHiddenArtifact(hiddenID, testSHA('c'))
	_, err = postgresStore.ReplaceGraph(t.Context(), hiddenID, GraphSourceManaged, hiddenArtifact)
	if err != nil {
		t.Fatal(err)
	}
	limits := graphquery.Limits{PerCategory: 2, DefaultImpactDepth: 3, MaxDepth: 4, DefaultTraceDepth: 4, MaxTraceDepth: 4, MaxNodes: 20, MaxEdges: 20, MaxFanout: 20}
	postgresService := &graphquery.Service{Store: postgresStore, Limits: limits}
	scope := graphprotocol.Scope{SelectedRepositoryID: repositoryID, Repositories: []graphprotocol.RepositorySnapshot{{ID: repositoryID, Name: "acme/renamed", Commit: artifact.Commit}}}
	staleScope := scope
	staleScope.Repositories = append([]graphprotocol.RepositorySnapshot(nil), scope.Repositories...)
	staleScope.Repositories[0].Commit = testSHA('b')
	missingScope := scope
	missingScope.Repositories = append(append([]graphprotocol.RepositorySnapshot(nil), scope.Repositories...), graphprotocol.RepositorySnapshot{ID: 999, Name: "acme/missing", Commit: testSHA('d')})

	type results struct {
		Context       graphprotocol.ContextResponse `json:"context"`
		Impact        graphprotocol.ImpactResponse  `json:"impact"`
		ImpactMinimum graphprotocol.ImpactResponse  `json:"impact_minimum"`
		Trace         graphprotocol.TraceResponse   `json:"trace"`
		Ambiguous     graphprotocol.ContextResponse `json:"ambiguous"`
		Stale         graphprotocol.ContextResponse `json:"stale"`
		Missing       graphprotocol.ContextResponse `json:"missing"`
		Unauthorized  graphprotocol.ContextResponse `json:"unauthorized"`
	}
	run := func(t *testing.T, service *graphquery.Service) results {
		t.Helper()
		var got results
		var err error
		got.Context, err = service.Context(t.Context(), graphprotocol.ContextRequest{Scope: scope, UID: "next", Relations: []string{"calls", "references"}, PerCategoryLimit: 2})
		if err != nil {
			t.Fatal(err)
		}
		got.Impact, err = service.Impact(t.Context(), graphprotocol.ImpactRequest{Scope: scope, TargetUID: "root", Direction: "downstream", Relations: []string{"calls"}, MaxDepth: 4, IncludeTests: true})
		if err != nil {
			t.Fatal(err)
		}
		got.ImpactMinimum, err = service.Impact(t.Context(), graphprotocol.ImpactRequest{Scope: scope, TargetUID: "root", Direction: "downstream", Relations: []string{"calls"}, MaxDepth: 4, MinConfidence: .8, IncludeTests: true})
		if err != nil {
			t.Fatal(err)
		}
		got.Trace, err = service.Trace(t.Context(), graphprotocol.TraceRequest{Scope: scope, SourceUID: "root", TargetUID: "last", MaxDepth: 4})
		if err != nil {
			t.Fatal(err)
		}
		got.Ambiguous, err = service.Context(t.Context(), graphprotocol.ContextRequest{Scope: scope, Name: "Duplicate"})
		if err != nil {
			t.Fatal(err)
		}
		got.Stale, err = service.Context(t.Context(), graphprotocol.ContextRequest{Scope: staleScope, UID: "root"})
		if err != nil {
			t.Fatal(err)
		}
		got.Missing, err = service.Context(t.Context(), graphprotocol.ContextRequest{Scope: missingScope, UID: "root"})
		if err != nil {
			t.Fatal(err)
		}
		got.Unauthorized, err = service.Context(t.Context(), graphprotocol.ContextRequest{Scope: scope, UID: "secret"})
		if err != nil {
			t.Fatal(err)
		}
		return got
	}

	got := run(t, postgresService)
	type golden struct {
		ContextIncomingCalls, ContextIncomingReferences, ContextOutgoingCalls []string
		ImpactDepth1, ImpactDepth2, ImpactMinimumDepth1                       []string
		Trace, Ambiguous, StaleBoundaries, MissingBoundaries                  []string
		Commit, UnauthorizedStatus                                            string
	}
	symbolUIDs := func(symbols []graphprotocol.Symbol) []string {
		result := make([]string, 0, len(symbols))
		for _, symbol := range symbols {
			result = append(result, symbol.UID)
		}
		return result
	}
	boundaryReasons := func(boundaries []graphprotocol.Boundary) []string {
		result := make([]string, 0, len(boundaries))
		for _, boundary := range boundaries {
			result = append(result, boundary.Reason)
		}
		return result
	}
	normalized := golden{
		ContextIncomingCalls:      symbolUIDs(got.Context.Incoming["calls"]),
		ContextIncomingReferences: symbolUIDs(got.Context.Incoming["references"]),
		ContextOutgoingCalls:      symbolUIDs(got.Context.Outgoing["calls"]),
		ImpactDepth1:              symbolUIDs(got.Impact.ByDepth[1]),
		ImpactDepth2:              symbolUIDs(got.Impact.ByDepth[2]),
		ImpactMinimumDepth1:       symbolUIDs(got.ImpactMinimum.ByDepth[1]),
		Trace:                     symbolUIDs(got.Trace.Nodes),
		Ambiguous:                 symbolUIDs(got.Ambiguous.Candidates),
		StaleBoundaries:           boundaryReasons(got.Stale.Boundaries),
		MissingBoundaries:         boundaryReasons(got.Missing.Boundaries),
		Commit:                    got.Context.Commits["acme/renamed"],
		UnauthorizedStatus:        got.Unauthorized.Status,
	}
	data, err := json.MarshalIndent(normalized, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	data = append(data, '\n')
	want, err := os.ReadFile(filepath.Join("..", "..", "test", "fixtures", "graph", "query", "parity.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(data, want) {
		t.Fatalf("golden mismatch; got:\n%s", data)
	}
}

func assertGraphQueryPlan(t *testing.T, store *Store, repositoryID, uploadID int64, commit string) {
	t.Helper()
	rows, err := store.pool.Query(t.Context(), "explain (analyze, buffers) "+outgoingNeighborsSQL,
		[]int64{repositoryID}, []int64{uploadID}, []string{commit}, []int64{repositoryID}, []string{"root"},
		int16(graphartifact.EdgeCalls), float64(0), 0, 21)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var lines []string
	for rows.Next() {
		var line string
		if err := rows.Scan(&line); err != nil {
			t.Fatal(err)
		}
		lines = append(lines, line)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	plan := strings.Join(lines, "\n")
	if !strings.Contains(plan, "Limit") || !strings.Contains(plan, "actual") || !strings.Contains(plan, "Buffers") {
		t.Fatalf("unbounded or incomplete graph plan:\n%s", plan)
	}
	t.Logf("PostgreSQL graph frontier plan:\n%s", plan)
}

func parityHiddenArtifact(repositoryID int64, commit string) graphartifact.Artifact {
	return graphartifact.Artifact{
		SchemaVersion: 1, RepositoryID: repositoryID, Commit: commit,
		Analyzer: graphartifact.Analyzer{Name: "parity", Version: "1"}, ContentHash: bytes.Repeat([]byte{9}, 32),
		Nodes: []graphartifact.Node{
			{UID: "repository", Kind: graphartifact.NodeRepository, QualifiedName: "acme/hidden"},
			{UID: "file", Kind: graphartifact.NodeFile, Path: "secret.go"},
			{UID: "secret", Kind: graphartifact.NodeSymbol, Path: "secret.go", Language: "go", SymbolKind: "func", QualifiedName: "Secret"},
		},
		Edges: []graphartifact.Edge{
			{SourceUID: "repository", TargetUID: "file", Kind: graphartifact.EdgeContains, Confidence: 1},
			{SourceUID: "file", TargetUID: "secret", Kind: graphartifact.EdgeContains, Confidence: 1},
		},
	}
}

func parityArtifact(repositoryID int64, commit string) graphartifact.Artifact {
	artifact := graphartifact.Artifact{
		SchemaVersion: 1, RepositoryID: repositoryID, Commit: commit,
		Analyzer: graphartifact.Analyzer{Name: "parity", Version: "1"}, ContentHash: bytes.Repeat([]byte{7}, 32),
		Nodes: []graphartifact.Node{
			{UID: "repository", Kind: graphartifact.NodeRepository, QualifiedName: "acme/renamed"},
			{UID: "file", Kind: graphartifact.NodeFile, Path: "main.go"},
			{UID: "root", Kind: graphartifact.NodeSymbol, Path: "main.go", Language: "go", SymbolKind: "func", QualifiedName: "Root"},
			{UID: "next", Kind: graphartifact.NodeSymbol, Path: "main.go", Language: "go", SymbolKind: "func", QualifiedName: "Next"},
			{UID: "last", Kind: graphartifact.NodeSymbol, Path: "main_test.go", Language: "go", SymbolKind: "func", QualifiedName: "Last"},
			{UID: "duplicate-a", Kind: graphartifact.NodeSymbol, Path: "a.go", Language: "go", SymbolKind: "func", QualifiedName: "Duplicate"},
			{UID: "duplicate-b", Kind: graphartifact.NodeSymbol, Path: "b.go", Language: "go", SymbolKind: "func", QualifiedName: "Duplicate"},
		},
	}
	for _, uid := range []string{"root", "next", "last", "duplicate-a", "duplicate-b"} {
		artifact.Edges = append(artifact.Edges, graphartifact.Edge{SourceUID: "file", TargetUID: uid, Kind: graphartifact.EdgeContains, Confidence: 1})
	}
	artifact.Edges = append(artifact.Edges,
		graphartifact.Edge{SourceUID: "repository", TargetUID: "file", Kind: graphartifact.EdgeContains, Confidence: 1},
		graphartifact.Edge{SourceUID: "root", TargetUID: "next", Kind: graphartifact.EdgeCalls, Path: "main.go", Confidence: .9, ResolutionReason: "exact"},
		graphartifact.Edge{SourceUID: "next", TargetUID: "last", Kind: graphartifact.EdgeCalls, Path: "main.go", Confidence: .7, ResolutionReason: "heuristic"},
		graphartifact.Edge{SourceUID: "last", TargetUID: "root", Kind: graphartifact.EdgeCalls, Path: "main_test.go", Confidence: .6, ResolutionReason: "cycle"},
		graphartifact.Edge{SourceUID: "duplicate-a", TargetUID: "next", Kind: graphartifact.EdgeReferences, Path: "a.go", Confidence: .8, ResolutionReason: "reference"},
	)
	return artifact
}
