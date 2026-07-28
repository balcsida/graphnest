package graphquery

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"

	lbug "github.com/LadybugDB/go-ladybug"
	"github.com/grepnest/grepnest/internal/graphartifact"
	"github.com/grepnest/grepnest/internal/graphprotocol"
	"github.com/grepnest/grepnest/internal/ladybug"
)

func TestLimitsPreserveConfiguredDefaultsAndRows(t *testing.T) {
	got := (&Service{Limits: Limits{
		DefaultImpactDepth: 2, MaxDepth: 7,
		DefaultTraceDepth: 4, MaxTraceDepth: 9, MaxRows: 321,
	}}).limits()
	if got.DefaultImpactDepth != 2 || got.DefaultTraceDepth != 4 || got.MaxRows != 321 {
		t.Fatalf("limits = %#v", got)
	}
}

const testCommit = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

func TestContextRequiresReadyScope(t *testing.T) {
	service := seededQueryService(t, callChain("A", "B"))
	if _, err := service.Context(t.Context(), graphprotocol.ContextRequest{UID: "A"}); err == nil {
		t.Fatal("empty scope unexpectedly succeeded")
	}
	got, err := service.Context(t.Context(), graphprotocol.ContextRequest{
		Scope: scope("bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"), UID: "A",
	})
	if err != nil || got.Status != graphprotocol.StatusNotFound || len(got.Boundaries) != 1 {
		t.Fatalf("Context()=%#v,%v", got, err)
	}
}

func TestContextDoesNotResolveSelectedUIDFromAnotherRepository(t *testing.T) {
	service := seededQueryServiceWithArtifacts(t, callChain("A"), repositoryCallChain(202, "A"))
	got, err := service.Context(t.Context(), graphprotocol.ContextRequest{
		Scope: graphprotocol.Scope{
			SelectedRepositoryID: 101,
			Repositories: []graphprotocol.RepositorySnapshot{
				{ID: 101, Name: "acme/one", Commit: testCommit},
				{ID: 202, Name: "acme/two", Commit: testCommit},
			},
		},
		UID: "A",
	})
	if err != nil || got.Status != graphprotocol.StatusFound || got.Symbol.RepositoryID != 101 {
		t.Fatalf("Context()=%#v,%v", got, err)
	}
}

func TestContextReportsAmbiguityAndCategories(t *testing.T) {
	artifact := callChain("A", "B")
	artifact.Nodes = append(artifact.Nodes,
		graphartifact.Node{UID: "A2", Kind: graphartifact.NodeSymbol, Path: "other.go", Language: "go", SymbolKind: "func", QualifiedName: "A"},
	)
	artifact.Edges = append(artifact.Edges,
		graphartifact.Edge{SourceUID: "A2", TargetUID: "B", Kind: graphartifact.EdgeReferences, Confidence: 1},
	)
	service := seededQueryService(t, artifact)
	ambiguous, err := service.Context(t.Context(), graphprotocol.ContextRequest{Scope: scope(testCommit), Name: "A"})
	if err != nil || ambiguous.Status != graphprotocol.StatusAmbiguous || len(ambiguous.Candidates) != 2 {
		t.Fatalf("Context()=%#v,%v", ambiguous, err)
	}
	found, err := service.Context(t.Context(), graphprotocol.ContextRequest{Scope: scope(testCommit), UID: "B", PerCategoryLimit: 1})
	if err != nil || found.Status != graphprotocol.StatusFound || len(found.Incoming["calls"]) != 1 ||
		len(found.Incoming["references"]) != 1 || strings.HasPrefix(found.Symbol.UID, "101:") {
		t.Fatalf("Context()=%#v,%v", found, err)
	}
}

func TestContextRejectsNegativeBounds(t *testing.T) {
	service := seededQueryService(t, callChain("A"))
	for name, request := range map[string]graphprotocol.ContextRequest{
		"limit":  {Scope: scope(testCommit), UID: "A", PerCategoryLimit: -1},
		"offset": {Scope: scope(testCommit), UID: "A", PerCategoryOffset: -1},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := service.Context(t.Context(), request); err == nil {
				t.Fatal("negative bound unexpectedly succeeded")
			}
		})
	}
}

func seededQueryService(t *testing.T, artifact graphartifact.Artifact) *Service {
	return seededQueryServiceWithArtifacts(t, artifact)
}

func seededQueryServiceWithArtifacts(t *testing.T, artifacts ...graphartifact.Artifact) *Service {
	t.Helper()
	path := filepath.Join(t.TempDir(), "graph")
	handle, err := lbug.OpenDatabase(path, lbug.DefaultSystemConfig())
	if err != nil {
		t.Fatal(err)
	}
	connection, err := lbug.OpenConnection(handle)
	if err != nil {
		handle.Close()
		t.Fatal(err)
	}
	if err := ladybug.EnsureSchema(t.Context(), connection); err != nil {
		connection.Close()
		handle.Close()
		t.Fatal(err)
	}
	connection.Close()
	handle.Close()
	db, err := ladybug.Open(ladybug.Options{Path: path})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	for index, artifact := range artifacts {
		manifest := graphartifact.Manifest{
			RepositoryID: artifact.RepositoryID, UploadID: int64(index + 1), Commit: artifact.Commit, Source: "managed",
			SchemaVersion: artifact.SchemaVersion, ContentHash: bytes.Clone(artifact.ContentHash),
		}
		if err := db.ReplaceRepository(t.Context(), manifest, artifact); err != nil {
			t.Fatal(err)
		}
	}
	return &Service{Database: db}
}

func scope(commit string) graphprotocol.Scope {
	return graphprotocol.Scope{Repositories: []graphprotocol.RepositorySnapshot{{
		ID: 101, GitHubID: 1001, Name: "acme/repo", Branch: "main", Commit: commit,
	}}}
}

func callChain(names ...string) graphartifact.Artifact {
	nodes := []graphartifact.Node{
		{UID: "repository:101", Kind: graphartifact.NodeRepository, QualifiedName: "acme/repo"},
		{UID: "file:main.go", Kind: graphartifact.NodeFile, Path: "main.go"},
	}
	edges := []graphartifact.Edge{{
		SourceUID: "repository:101", TargetUID: "file:main.go", Kind: graphartifact.EdgeContains, Confidence: 1,
	}}
	for _, name := range names {
		nodes = append(nodes, graphartifact.Node{
			UID: name, Kind: graphartifact.NodeSymbol, Path: "main.go", Language: "go",
			SymbolKind: "func", QualifiedName: name,
		})
		edges = append(edges, graphartifact.Edge{
			SourceUID: "file:main.go", TargetUID: name, Kind: graphartifact.EdgeContains, Confidence: 1,
		})
	}
	for i := 1; i < len(names); i++ {
		edges = append(edges, graphartifact.Edge{
			SourceUID: names[i-1], TargetUID: names[i], Kind: graphartifact.EdgeCalls, Confidence: 1,
		})
	}
	return graphartifact.Artifact{
		SchemaVersion: 1, Analyzer: graphartifact.Analyzer{Name: "test", Version: "1"},
		RepositoryID: 101, Commit: testCommit, ContentHash: bytes.Repeat([]byte{1}, 32),
		Nodes: nodes, Edges: edges,
	}
}
