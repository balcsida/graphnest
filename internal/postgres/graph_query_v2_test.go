//go:build integration

package postgres

import (
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/balcsida/graphnest/internal/graphartifact"
	graphv2 "github.com/balcsida/graphnest/internal/graphartifact/v2"
	"github.com/balcsida/graphnest/internal/graphprotocol"
	"github.com/balcsida/graphnest/internal/graphquery"
	"github.com/balcsida/graphnest/internal/scipgraph"
	"google.golang.org/protobuf/proto"
)

// TestGraphQueryManifestsSelectGeneration pins which generation answers
// context, impact and trace: at the indexed commit, a published v2 generation,
// then a managed or external v1 graph, then the SCIP-derived v2 generation,
// then the SCIP-derived v1 graph.
func TestGraphQueryManifestsSelectGeneration(t *testing.T) {
	a, b := testSHA('a'), testSHA('b')
	type step func(t *testing.T, store *Store, repositoryID int64)
	scip := func(commit string) step {
		return func(t *testing.T, store *Store, repositoryID int64) {
			if err := store.ReplaceSCIP(t.Context(), repositoryID, commit, uploadWith("a.go", globalSymbol, definitionRole)); err != nil {
				t.Fatal(err)
			}
		}
	}
	managed := func(commit string) step {
		return func(t *testing.T, store *Store, repositoryID int64) {
			if replacement, err := store.ReplaceGraph(t.Context(), repositoryID, GraphSourceManaged, artifactFor(repositoryID, commit, "managed")); err != nil || !replacement.Applied {
				t.Fatalf("ReplaceGraph()=%#v err=%v", replacement, err)
			}
		}
	}
	published := func(commit string) step {
		return func(t *testing.T, store *Store, repositoryID int64) {
			if replacement, err := store.ReplaceGraphV2(t.Context(), repositoryID, GraphPublication{Publisher: "api_token:1"}, publishedCallGraph(commit)); err != nil || !replacement.Applied {
				t.Fatalf("ReplaceGraphV2()=%#v err=%v", replacement, err)
			}
		}
	}
	advance := func(commit string) step {
		return func(t *testing.T, store *Store, repositoryID int64) {
			if _, err := store.pool.Exec(t.Context(), `update repositories set indexed_sha=$2, desired_sha=$2 where id=$1`, repositoryID, commit); err != nil {
				t.Fatal(err)
			}
		}
	}
	for _, test := range []struct {
		name        string
		steps       []step
		wantVersion uint32
		wantSource  string
		wantCommit  string
	}{
		{"SCIP-derived v2 answers instead of SCIP-derived v1", []step{scip(a)}, 2, "scip", a},
		{"managed v1 keeps answering beside SCIP-derived v2", []step{managed(a), scip(a)}, 1, "managed", a},
		{"published v2 answers instead of managed v1", []step{managed(a), published(a)}, 2, "external", a},
		{"published v2 answers instead of SCIP-derived v2", []step{scip(a), published(a)}, 2, "external", a},
		{"stale v2 falls back to current v1", []step{published(a), advance(b), managed(b)}, 1, "managed", b},
		{"current v2 answers when v1 is stale", []step{managed(a), advance(b), published(b)}, 2, "external", b},
		{"v1 alone", []step{managed(a)}, 1, "managed", a},
		{"nothing at the indexed commit", []step{scip(a), advance(b)}, 0, "", ""},
	} {
		t.Run(test.name, func(t *testing.T) {
			store, repositoryID := readyGraphStore(t, a)
			for _, apply := range test.steps {
				apply(t, store, repositoryID)
			}
			manifests, err := store.Manifests(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			manifest, ok := manifests[repositoryID]
			if test.wantVersion == 0 {
				if ok {
					t.Fatalf("manifest=%#v, want none", manifest)
				}
				return
			}
			if !ok || manifest.SchemaVersion != test.wantVersion || manifest.Source != test.wantSource || manifest.Commit != test.wantCommit || manifest.UploadID <= 0 {
				t.Fatalf("manifest=%#v ok=%v, want schema %d source %s commit %s", manifest, ok, test.wantVersion, test.wantSource, test.wantCommit)
			}
		})
	}
}

// TestLegacyGraphQueriesAnswerFromSCIPGenerations runs the same context,
// impact and trace requests against the generation a SCIP upload derives and
// against the v1 graph it derives instead when the generation is dropped. The
// answers must agree except where the generations record
// different facts, which the per-generation expectations name.
func TestLegacyGraphQueriesAnswerFromSCIPGenerations(t *testing.T) {
	const (
		alpha = "scip go example.com/acme v1 pkg/Alpha()."
		beta  = "scip go example.com/acme v1 pkg/Beta()."
		gamma = "scip go example.com/acme v1 pkg/Gamma()."
		shape = "scip go example.com/acme v1 pkg/Shape#"
		thing = "scip go example.com/acme v1 pkg/Thing#"
		// Referenced but defined in another module's index.
		helper = "scip go example.com/dep v1 dep/Helper()."
	)
	definition := func(symbol string, line int32) scipgraph.Occurrence {
		return scipgraph.Occurrence{Path: "pkg/a.go", Symbol: symbol, StartLine: line, StartCharacter: 5, EndLine: line, EndCharacter: 10, PositionEncoding: 1, Roles: definitionRole, Enclosing: &scipgraph.Range{StartLine: line, EndLine: line + 4, EndCharacter: 1}}
	}
	reference := func(symbol string, line int32) scipgraph.Occurrence {
		return scipgraph.Occurrence{Path: "pkg/a.go", Symbol: symbol, StartLine: line, StartCharacter: 1, EndLine: line, EndCharacter: 6, PositionEncoding: 1}
	}
	upload := scipgraph.Upload{
		ProjectRoot: "file:///src", IndexerName: "test", IndexerVersion: "1",
		Documents: []scipgraph.Document{{Path: "pkg/a.go", Language: "go", PositionEncoding: 1}},
		Occurrences: []scipgraph.Occurrence{
			definition(alpha, 8), reference(thing, 9), reference(thing, 10), reference(beta, 11),
			definition(beta, 20), reference(gamma, 21),
			definition(gamma, 30), reference(helper, 31),
			definition(shape, 40),
			definition(thing, 50),
		},
		Relationships: []scipgraph.Relationship{
			{Path: "pkg/a.go", Source: thing, Target: shape, Implementation: true},
			{Path: "pkg/a.go", Source: thing, Target: shape, TypeDefinition: true},
		},
	}
	for _, generation := range []struct {
		name                 string
		limits               graphartifact.Limits
		language, typeKind   string
		referenceReason      string
		relationshipReason   string
		wantManifestVersion  uint32
		undefined            graphprotocol.Symbol
		wantTypeKindAccepted []string
	}{
		{name: "v2", language: "go", typeKind: "class", referenceReason: "scip_enclosing_range", relationshipReason: "scip_relationship", wantManifestVersion: 2, wantTypeKindAccepted: []string{"type", "class"},
			undefined: graphprotocol.Symbol{Language: "go"}},
		{name: "v1 fallback", limits: graphartifact.Limits{MaxNodes: 1}, typeKind: "type", referenceReason: "scip", relationshipReason: "scip", wantManifestVersion: 1, wantTypeKindAccepted: []string{"type"},
			// v1 places a symbol it never saw defined at its first reference.
			undefined: graphprotocol.Symbol{FilePath: "pkg/a.go", Range: graphprotocol.Position{StartLine: 31, StartCharacter: 1, EndLine: 31, EndCharacter: 6}}},
	} {
		t.Run(generation.name, func(t *testing.T) {
			store, repositoryID := readyGraphStore(t, testSHA('a'))
			store.SCIPGraphLimits = generation.limits
			if err := store.ReplaceSCIP(t.Context(), repositoryID, testSHA('a'), upload); err != nil {
				t.Fatal(err)
			}
			if manifests, err := store.Manifests(t.Context()); err != nil || manifests[repositoryID].SchemaVersion != generation.wantManifestVersion {
				t.Fatalf("manifests=%#v err=%v", manifests, err)
			}
			service := &graphquery.Service{Store: store}
			scope := graphprotocol.Scope{SelectedRepositoryID: repositoryID, Repositories: []graphprotocol.RepositorySnapshot{{ID: repositoryID, GitHubID: 101, Name: "acme/repo-101", Commit: testSHA('a')}}}
			symbol := func(scipSymbol, name, kind string, line int32) graphprotocol.Symbol {
				return graphprotocol.Symbol{UID: "symbol:" + scipSymbol, Name: name, Kind: kind, FilePath: "pkg/a.go", Language: generation.language, RepositoryID: repositoryID, Range: graphprotocol.Position{StartLine: line, StartCharacter: 5, EndLine: line, EndCharacter: 10}}
			}
			referenceEdge := func(source, target string, line int32) graphprotocol.Relationship {
				return graphprotocol.Relationship{SourceRepositoryID: repositoryID, TargetRepositoryID: repositoryID, SourceUID: "symbol:" + source, TargetUID: "symbol:" + target, Kind: "references", Path: "pkg/a.go", Range: graphprotocol.Position{StartLine: line, StartCharacter: 1, EndLine: line, EndCharacter: 6}, Confidence: 1, ResolutionReason: generation.referenceReason}
			}
			commits := map[string]string{"acme/repo-101": testSHA('a')}

			byUID, err := service.Context(t.Context(), graphprotocol.ContextRequest{Scope: scope, UID: "symbol:" + beta, Relations: []string{"references"}})
			if err != nil {
				t.Fatal(err)
			}
			wantBeta := symbol(beta, "Beta", "function", 20)
			if byUID.Status != graphprotocol.StatusFound || byUID.Symbol == nil || *byUID.Symbol != wantBeta || !reflect.DeepEqual(byUID.Commits, commits) || len(byUID.Boundaries) != 0 ||
				!reflect.DeepEqual(byUID.Incoming, map[string][]graphprotocol.Symbol{"references": {symbol(alpha, "Alpha", "function", 8)}}) ||
				!reflect.DeepEqual(byUID.Outgoing, map[string][]graphprotocol.Symbol{"references": {symbol(gamma, "Gamma", "function", 30)}}) ||
				!reflect.DeepEqual(byUID.IncomingEdges, map[string][]graphprotocol.Relationship{"references": {referenceEdge(alpha, beta, 11)}}) ||
				!reflect.DeepEqual(byUID.OutgoingEdges, map[string][]graphprotocol.Relationship{"references": {referenceEdge(beta, gamma, 21)}}) {
				t.Fatalf("Context(uid)=%#v symbol=%#v", byUID, byUID.Symbol)
			}

			// Several reference sites between one pair of symbols read in file order.
			sites, err := service.Context(t.Context(), graphprotocol.ContextRequest{Scope: scope, UID: "symbol:" + alpha, Relations: []string{"references"}})
			if err != nil || !reflect.DeepEqual(sites.OutgoingEdges["references"], []graphprotocol.Relationship{referenceEdge(alpha, beta, 11), referenceEdge(alpha, thing, 9), referenceEdge(alpha, thing, 10)}) {
				t.Fatalf("Context(reference sites)=%#v err=%v", sites.OutgoingEdges, err)
			}

			for _, kind := range generation.wantTypeKindAccepted {
				byName, err := service.Context(t.Context(), graphprotocol.ContextRequest{Scope: scope, Name: "Thing", Kind: kind, FilePath: "pkg/a.go", Relations: []string{"implements", "extends"}})
				if err != nil {
					t.Fatal(err)
				}
				relationship := func(kind string) graphprotocol.Relationship {
					return graphprotocol.Relationship{SourceRepositoryID: repositoryID, TargetRepositoryID: repositoryID, SourceUID: "symbol:" + thing, TargetUID: "symbol:" + shape, Kind: kind, Path: "pkg/a.go", Confidence: 1, ResolutionReason: generation.relationshipReason}
				}
				// SCIP's is_type_definition has always been reported as "extends".
				if byName.Status != graphprotocol.StatusFound || byName.Symbol == nil || *byName.Symbol != symbol(thing, "Thing", generation.typeKind, 50) ||
					!reflect.DeepEqual(byName.OutgoingEdges, map[string][]graphprotocol.Relationship{"implements": {relationship("implements")}, "extends": {relationship("extends")}}) ||
					len(byName.IncomingEdges) != 0 {
					t.Fatalf("Context(name, kind=%s)=%#v symbol=%#v", kind, byName, byName.Symbol)
				}
			}
			if missing, err := service.Context(t.Context(), graphprotocol.ContextRequest{Scope: scope, Name: "Thing", Kind: "function"}); err != nil || missing.Status != graphprotocol.StatusNotFound {
				t.Fatalf("Context(wrong kind)=%#v err=%v", missing, err)
			}
			// A file is not a symbol in either generation.
			for _, request := range []graphprotocol.ContextRequest{{Scope: scope, Name: "a.go"}, {Scope: scope, UID: "symbol:file:pkg/a.go"}, {Scope: scope, UID: alpha}} {
				if missing, err := service.Context(t.Context(), request); err != nil || missing.Status != graphprotocol.StatusNotFound {
					t.Fatalf("Context(%#v)=%#v err=%v", request, missing, err)
				}
			}

			undefined, err := service.Context(t.Context(), graphprotocol.ContextRequest{Scope: scope, UID: "symbol:" + helper, Relations: []string{"references"}})
			if err != nil {
				t.Fatal(err)
			}
			wantUndefined := generation.undefined
			wantUndefined.UID, wantUndefined.Name, wantUndefined.Kind, wantUndefined.RepositoryID = "symbol:"+helper, "Helper", "function", repositoryID
			if undefined.Status != graphprotocol.StatusFound || *undefined.Symbol != wantUndefined ||
				!reflect.DeepEqual(undefined.IncomingEdges, map[string][]graphprotocol.Relationship{"references": {referenceEdge(gamma, helper, 31)}}) {
				t.Fatalf("Context(undefined)=%#v symbol=%#v", undefined, undefined.Symbol)
			}

			downstream, err := service.Impact(t.Context(), graphprotocol.ImpactRequest{Scope: scope, TargetUID: "symbol:" + alpha, Direction: "downstream", Relations: []string{"references"}, MaxDepth: 2})
			if err != nil {
				t.Fatal(err)
			}
			if downstream.Status != graphprotocol.StatusFound || downstream.Partial ||
				!reflect.DeepEqual(downstream.ByDepth, map[int][]graphprotocol.Symbol{1: {symbol(beta, "Beta", "function", 20), symbol(thing, "Thing", generation.typeKind, 50)}, 2: {symbol(gamma, "Gamma", "function", 30)}}) ||
				!reflect.DeepEqual(downstream.Edges, []graphprotocol.Relationship{referenceEdge(alpha, beta, 11), referenceEdge(alpha, thing, 9), referenceEdge(beta, gamma, 21)}) {
				t.Fatalf("Impact(downstream)=%#v", downstream)
			}
			upstream, err := service.Impact(t.Context(), graphprotocol.ImpactRequest{Scope: scope, TargetUID: "symbol:" + gamma, Direction: "upstream", Relations: []string{"references"}, MaxDepth: 3})
			if err != nil || !reflect.DeepEqual(upstream.ByDepth, map[int][]graphprotocol.Symbol{1: {symbol(beta, "Beta", "function", 20)}, 2: {symbol(alpha, "Alpha", "function", 8)}}) {
				t.Fatalf("Impact(upstream)=%#v err=%v", upstream, err)
			}

			// SCIP records no call expressions, so neither generation has a call path.
			trace, err := service.Trace(t.Context(), graphprotocol.TraceRequest{Scope: scope, SourceUID: "symbol:" + alpha, TargetUID: "symbol:" + gamma})
			if err != nil || trace.Status != graphprotocol.StatusNoPath || !reflect.DeepEqual(trace.Commits, commits) {
				t.Fatalf("Trace()=%#v err=%v", trace, err)
			}

			// Replacing the generation that answered invalidates the captured snapshot.
			if err := service.ValidateContextSnapshots(t.Context(), byUID.Snapshots); err != nil {
				t.Fatal(err)
			}
			if err := store.ReplaceSCIP(t.Context(), repositoryID, testSHA('a'), uploadWith("b.go", globalSymbol, definitionRole)); err != nil {
				t.Fatal(err)
			}
			if err := service.ValidateContextSnapshots(t.Context(), byUID.Snapshots); !errors.Is(err, graphquery.ErrGenerationChanged) {
				t.Fatalf("ValidateContextSnapshots() after replacement=%v", err)
			}
		})
	}
}

// TestLegacyGraphQueriesAnswerFromPublishedGeneration covers what only a
// published generation carries: call edges, confidences and tests.
func TestLegacyGraphQueriesAnswerFromPublishedGeneration(t *testing.T) {
	store, repositoryID := readyGraphStore(t, testSHA('a'))
	if replacement, err := store.ReplaceGraphV2(t.Context(), repositoryID, GraphPublication{Publisher: "api_token:1"}, publishedCallGraph(testSHA('a'))); err != nil || !replacement.Applied {
		t.Fatalf("ReplaceGraphV2()=%#v err=%v", replacement, err)
	}
	service := &graphquery.Service{Store: store}
	scope := graphprotocol.Scope{SelectedRepositoryID: repositoryID, Repositories: []graphprotocol.RepositorySnapshot{{ID: repositoryID, GitHubID: 101, Name: "acme/repo-101", Commit: testSHA('a')}}}
	uids := func(symbols []graphprotocol.Symbol) []string {
		result := make([]string, 0, len(symbols))
		for _, symbol := range symbols {
			result = append(result, symbol.UID)
		}
		return result
	}

	trace, err := service.Trace(t.Context(), graphprotocol.TraceRequest{Scope: scope, SourceUID: "symbol:decl:a", TargetUID: "symbol:decl:c"})
	if err != nil || trace.Status != graphprotocol.StatusOK || !reflect.DeepEqual(uids(trace.Nodes), []string{"symbol:decl:a", "symbol:decl:b", "symbol:decl:c"}) || len(trace.Edges) != 2 {
		t.Fatalf("Trace()=%#v err=%v", trace, err)
	}
	wantCall := graphprotocol.Relationship{SourceRepositoryID: repositoryID, TargetRepositoryID: repositoryID, SourceUID: "symbol:decl:a", TargetUID: "symbol:decl:b", Kind: "calls", Path: "pkg/graph.go", Range: graphprotocol.Position{StartLine: 3, StartCharacter: 2, EndLine: 3, EndCharacter: 8}, Confidence: 0.9, ResolutionReason: "import"}
	if trace.Edges[0] != wantCall {
		t.Fatalf("Trace() edge=%#v want=%#v", trace.Edges[0], wantCall)
	}
	wantA := graphprotocol.Symbol{UID: "symbol:decl:a", Name: "A", Kind: "function", FilePath: "pkg/graph.go", Language: "go", Signature: "func A()", RepositoryID: repositoryID, Range: graphprotocol.Position{StartLine: 2, StartCharacter: 5, EndLine: 2, EndCharacter: 6}}
	if trace.Nodes[0] != wantA {
		t.Fatalf("Trace() node=%#v want=%#v", trace.Nodes[0], wantA)
	}

	// Relations are listed by neighbor UID and page deterministically.
	context, err := service.Context(t.Context(), graphprotocol.ContextRequest{Scope: scope, UID: "symbol:decl:b"})
	if err != nil || context.Status != graphprotocol.StatusFound ||
		!reflect.DeepEqual(uids(context.Incoming["calls"]), []string{"symbol:decl:a", "symbol:decl:t"}) ||
		!reflect.DeepEqual(uids(context.Outgoing["calls"]), []string{"symbol:decl:c", "symbol:decl:d"}) ||
		// A published type_of edge is not an extends edge, and file entities are not symbols.
		len(context.Outgoing["extends"]) != 1 || context.Outgoing["extends"][0].UID != "symbol:decl:c" || len(context.Incoming["references"]) != 0 {
		t.Fatalf("Context()=%#v err=%v", context, err)
	}
	if !context.Incoming["calls"][1].Test || context.Incoming["calls"][0].Test {
		t.Fatalf("Context() test flags=%#v", context.Incoming["calls"])
	}
	page, err := service.Context(t.Context(), graphprotocol.ContextRequest{Scope: scope, UID: "symbol:decl:b", Relations: []string{"calls"}, PerCategoryLimit: 1, PerCategoryOffset: 1})
	if err != nil || !reflect.DeepEqual(uids(page.Incoming["calls"]), []string{"symbol:decl:t"}) || !reflect.DeepEqual(uids(page.Outgoing["calls"]), []string{"symbol:decl:d"}) || len(page.Boundaries) != 0 {
		t.Fatalf("Context(page)=%#v err=%v", page, err)
	}
	first, err := service.Context(t.Context(), graphprotocol.ContextRequest{Scope: scope, UID: "symbol:decl:b", Relations: []string{"calls"}, PerCategoryLimit: 1})
	if err != nil || !reflect.DeepEqual(uids(first.Outgoing["calls"]), []string{"symbol:decl:c"}) || len(first.Boundaries) != 2 || first.Boundaries[0].Reason != "category_limit" {
		t.Fatalf("Context(first page)=%#v err=%v", first, err)
	}

	// An edge without a recorded confidence passes every threshold, as in the
	// v2 tools, and reports 1; tests are skipped unless asked for.
	impact, err := service.Impact(t.Context(), graphprotocol.ImpactRequest{Scope: scope, TargetUID: "symbol:decl:a", Direction: "downstream", Relations: []string{"calls"}, MinConfidence: 0.5, MaxDepth: 3})
	if err != nil || !reflect.DeepEqual(uids(impact.ByDepth[1]), []string{"symbol:decl:b"}) || !reflect.DeepEqual(uids(impact.ByDepth[2]), []string{"symbol:decl:c"}) || len(impact.Edges) != 2 || impact.Edges[1].Confidence != 1 {
		t.Fatalf("Impact(min confidence)=%#v err=%v", impact, err)
	}
	for includeTests, want := range map[bool][]string{false: {"symbol:decl:a"}, true: {"symbol:decl:a", "symbol:decl:t"}} {
		upstream, err := service.Impact(t.Context(), graphprotocol.ImpactRequest{Scope: scope, TargetUID: "symbol:decl:b", Direction: "upstream", Relations: []string{"calls"}, IncludeTests: includeTests, MaxDepth: 1})
		if err != nil || !reflect.DeepEqual(uids(upstream.ByDepth[1]), want) {
			t.Fatalf("Impact(include_tests=%v)=%#v err=%v", includeTests, upstream, err)
		}
	}
}

// TestLegacyGraphStoreMergesGenerationsAcrossRepositories covers a scope that
// holds a v1 graph for one repository and a v2 generation for another.
func TestLegacyGraphStoreMergesGenerationsAcrossRepositories(t *testing.T) {
	store, legacyID := readyGraphStore(t, testSHA('a'))
	publishedID := seedReadyRepository(t, store, 102, testSHA('a'))
	legacy := artifactFor(legacyID, testSHA('a'), "managed")
	legacy.Nodes = append(legacy.Nodes, graphartifact.Node{UID: "caller", Kind: graphartifact.NodeSymbol, Path: "a.go", QualifiedName: "Caller"})
	legacy.Edges = append(legacy.Edges, graphartifact.Edge{SourceUID: "caller", TargetUID: "symbol", Kind: graphartifact.EdgeCalls, Path: "a.go", Confidence: 1})
	if _, err := store.ReplaceGraph(t.Context(), legacyID, GraphSourceManaged, legacy); err != nil {
		t.Fatal(err)
	}
	artifact := publishedCallGraph(testSHA('a'))
	artifact.Repository = "102"
	if _, err := store.ReplaceGraphV2(t.Context(), publishedID, GraphPublication{Publisher: "api_token:1"}, artifact); err != nil {
		t.Fatal(err)
	}
	service := &graphquery.Service{Store: store}
	scope := graphprotocol.Scope{Repositories: []graphprotocol.RepositorySnapshot{
		{ID: legacyID, GitHubID: 101, Name: "acme/repo-101", Commit: testSHA('a')},
		{ID: publishedID, GitHubID: 102, Name: "acme/repo-102", Commit: testSHA('a')},
	}}
	for uid, repositoryID := range map[string]int64{"symbol": legacyID, "symbol:decl:b": publishedID} {
		found, err := service.Context(t.Context(), graphprotocol.ContextRequest{Scope: scope, UID: uid, Relations: []string{"calls"}})
		if err != nil || found.Status != graphprotocol.StatusFound || found.Symbol.RepositoryID != repositoryID || len(found.Incoming["calls"]) == 0 ||
			!reflect.DeepEqual(found.Commits, map[string]string{"acme/repo-101": testSHA('a'), "acme/repo-102": testSHA('a')}) {
			t.Fatalf("Context(%s)=%#v err=%v", uid, found, err)
		}
	}

	manifests, err := store.Manifests(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	snapshots := []graphquery.QuerySnapshot{
		{RepositoryID: legacyID, UploadID: manifests[legacyID].UploadID, Commit: testSHA('a'), SchemaVersion: manifests[legacyID].SchemaVersion},
		{RepositoryID: publishedID, UploadID: manifests[publishedID].UploadID, Commit: testSHA('a'), SchemaVersion: manifests[publishedID].SchemaVersion},
	}
	symbols, err := store.Symbols(t.Context(), graphquery.SymbolQuery{Snapshots: snapshots, Limit: 3})
	if err != nil || len(symbols) != 3 || symbols[0].UID != "caller" || symbols[1].UID != "symbol" || symbols[2].UID != "symbol:decl:a" {
		t.Fatalf("Symbols()=%#v err=%v", symbols, err)
	}
	frontier := []graphquery.SymbolRef{{RepositoryID: legacyID, UID: "symbol"}, {RepositoryID: publishedID, UID: "symbol:decl:b"}}
	neighbors, err := store.Neighbors(t.Context(), graphquery.NeighborQuery{Snapshots: snapshots, Frontier: frontier, Relation: "calls", Direction: "incoming", Offset: 1, Limit: 2})
	if err != nil || len(neighbors) != 2 || neighbors[0].Symbol.UID != "symbol:decl:a" || neighbors[1].Symbol.UID != "symbol:decl:t" || neighbors[0].Parent != frontier[1] {
		t.Fatalf("Neighbors()=%#v err=%v", neighbors, err)
	}
}

// TestLegacyV2NeighborQueryUsesEdgeIndexes keeps the frontier lookup on the
// per-generation edge indexes in both directions.
func TestLegacyV2NeighborQueryUsesEdgeIndexes(t *testing.T) {
	store, repositoryID := readyGraphStore(t, testSHA('a'))
	artifact := publishedCallGraph(testSHA('a'))
	for i := range 2000 {
		id := fmt.Sprintf("n%04d", i)
		artifact.Nodes = append(artifact.Nodes, &graphv2.Node{SourceId: id, Occurrence: "decl:" + id, Kind: "function", Name: id})
		artifact.Edges = append(artifact.Edges, &graphv2.Edge{SourceId: "e" + id, Occurrence: "edge:e" + id, Source: "decl:" + id, Target: "decl:a", Kind: graphv2.EdgeKind_EDGE_KIND_REFERENCES})
	}
	replacement, err := store.ReplaceGraphV2(t.Context(), repositoryID, GraphPublication{Publisher: "api_token:1"}, artifact)
	if err != nil || !replacement.Applied {
		t.Fatalf("ReplaceGraphV2()=%#v err=%v", replacement, err)
	}
	if _, err := store.pool.Exec(t.Context(), `analyze graph_v2_edges; analyze graph_v2_nodes`); err != nil {
		t.Fatal(err)
	}
	for direction, index := range map[string]string{"outgoing": "graph_v2_edges_outgoing", "incoming": "graph_v2_edges_incoming"} {
		rows, err := store.pool.Query(t.Context(), "explain (analyze, buffers) "+legacyV2NeighborsSQL(direction),
			[]int64{repositoryID}, []int64{replacement.Upload.ID}, []string{testSHA('a')}, []int64{repositoryID}, [][]byte{[]byte("decl:b")},
			int16(graphartifact.EdgeCalls), float64(0), 0, 101, graphquery.MaxEntityQueryBytes, int16(graphartifact.EdgeExtends), int16(graphartifact.EdgeTypeOf))
		if err != nil {
			t.Fatal(err)
		}
		var lines []string
		for rows.Next() {
			var line string
			if err := rows.Scan(&line); err != nil {
				t.Fatal(err)
			}
			lines = append(lines, line)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			t.Fatal(err)
		}
		plan := strings.Join(lines, "\n")
		if !strings.Contains(plan, index) || strings.Contains(plan, "Seq Scan on graph_v2_edges") || strings.Contains(plan, "Seq Scan on graph_v2_nodes") {
			t.Fatalf("%s frontier plan does not use %s:\n%s", direction, index, plan)
		}
	}
}

// publishedCallGraph is a publisher's generation for repository 101: A calls
// B, B calls C and D, and a test calls B.
func publishedCallGraph(commit string) *graphv2.Artifact {
	function := func(id, name, path string, line int32) *graphv2.Node {
		return &graphv2.Node{SourceId: id, Occurrence: "decl:" + id, Kind: "function", Name: name, QualifiedName: "pkg." + name, Path: proto.String(path), Language: "go", Signature: proto.String("func " + name + "()"),
			Location: &graphv2.Location{Path: proto.String(path), Start: &graphv2.Position{Line: proto.Int32(line), Character: proto.Int32(5)}, End: &graphv2.Position{Line: proto.Int32(line), Character: proto.Int32(6)}}}
	}
	edge := func(id, source, target string, kind graphv2.EdgeKind, confidence *float64) *graphv2.Edge {
		return &graphv2.Edge{SourceId: id, Occurrence: "edge:" + id, Source: source, Target: target, Kind: kind, Confidence: confidence, ResolutionReason: proto.String("import"),
			Location: &graphv2.Location{Path: proto.String("pkg/graph.go"), Start: &graphv2.Position{Line: proto.Int32(3), Character: proto.Int32(2)}, End: &graphv2.Position{Line: proto.Int32(3), Character: proto.Int32(8)}}}
	}
	return &graphv2.Artifact{
		SchemaVersion: 2, Repository: "101", Commit: commit,
		Producer: &graphv2.Producer{Name: "codegraph", Version: "1.6.0", Configuration: "portable"},
		Nodes: []*graphv2.Node{
			{SourceId: "file", Occurrence: "file:pkg/graph.go", Kind: "file", Name: "graph.go", QualifiedName: "pkg/graph.go", Path: proto.String("pkg/graph.go"), Language: "go"},
			function("a", "A", "pkg/graph.go", 2), function("b", "B", "pkg/graph.go", 6), function("c", "C", "pkg/graph.go", 10),
			function("d", "D", "pkg/graph.go", 14), function("t", "TestB", "pkg/graph_test.go", 4),
		},
		Edges: []*graphv2.Edge{
			edge("1", "decl:a", "decl:b", graphv2.EdgeKind_EDGE_KIND_CALLS, proto.Float64(0.9)),
			edge("2", "decl:b", "decl:c", graphv2.EdgeKind_EDGE_KIND_CALLS, nil),
			edge("3", "decl:b", "decl:d", graphv2.EdgeKind_EDGE_KIND_CALLS, proto.Float64(0.4)),
			edge("4", "decl:t", "decl:b", graphv2.EdgeKind_EDGE_KIND_CALLS, proto.Float64(1)),
			edge("5", "file:pkg/graph.go", "decl:b", graphv2.EdgeKind_EDGE_KIND_REFERENCES, proto.Float64(1)),
			edge("6", "decl:b", "decl:c", graphv2.EdgeKind_EDGE_KIND_EXTENDS, proto.Float64(1)),
			edge("7", "decl:b", "decl:d", graphv2.EdgeKind_EDGE_KIND_TYPE_OF, proto.Float64(1)),
		},
	}
}
