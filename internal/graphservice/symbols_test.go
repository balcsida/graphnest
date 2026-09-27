package graphservice

import (
	"context"
	"errors"
	"testing"

	"github.com/balcsida/graphnest/internal/authn"
	graphv2 "github.com/balcsida/graphnest/internal/graphartifact/v2"
	"github.com/balcsida/graphnest/internal/graphprotocol"
	"github.com/balcsida/graphnest/pkg/api"
)

type symbolBackendStub struct {
	*inspectionBackend
	calls  []graphprotocol.SymbolCallsRequest
	impact []graphprotocol.SymbolImpactRequest
	result graphprotocol.SymbolResponse
}

func (b *symbolBackendStub) SymbolCalls(_ context.Context, r graphprotocol.SymbolCallsRequest) (graphprotocol.SymbolResponse, error) {
	b.calls = append(b.calls, r)
	return b.result, nil
}

func (b *symbolBackendStub) SymbolImpact(_ context.Context, r graphprotocol.SymbolImpactRequest) (graphprotocol.SymbolResponse, error) {
	b.impact = append(b.impact, r)
	return b.result, nil
}

func symbolFixture() (*Service, *symbolBackendStub) {
	s, b, _ := inspectionFixture()
	stub := &symbolBackendStub{inspectionBackend: b, result: graphprotocol.SymbolResponse{
		Status: graphprotocol.StatusOK, Generations: []graphprotocol.Generation{b.generation},
		Definitions: []graphprotocol.SymbolDefinition{{Definitions: []graphprotocol.Entity{b.entity}, Related: []graphprotocol.SymbolRelated{{Entity: b.entity, Edge: graphprotocol.Evidence{
			RepositoryID: 101, SourceID: "caller", TargetID: "identity", Fact: &graphv2.Edge{Occurrence: "call:1", Source: "decl:2", Target: "decl:1", Kind: graphv2.EdgeKind_EDGE_KIND_CALLS},
		}}}}},
	}}
	s.Backend = stub
	return s, stub
}

func TestSymbolQueriesUseSelectedScopeAndDirection(t *testing.T) {
	s, stub := symbolFixture()
	request := api.GraphSymbolCallsRequest{Repo: api.GraphRepositorySelector{ID: 101}, Symbol: "run", File: "main.ts", Limit: 3}
	if _, err := s.SymbolCallers(t.Context(), principalFor(101), request); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SymbolCallees(t.Context(), principalFor(101), request); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SymbolImpact(t.Context(), principalFor(101), api.GraphSymbolImpactRequest{Repo: api.GraphRepositorySelector{ID: 101}, Symbol: "run", Depth: 4}); err != nil {
		t.Fatal(err)
	}
	if len(stub.calls) != 2 || stub.calls[0].Direction != "incoming" || stub.calls[1].Direction != "outgoing" || stub.calls[0].Symbol != "run" || stub.calls[0].File != "main.ts" || stub.calls[0].Limit != 3 {
		t.Fatalf("calls=%+v", stub.calls)
	}
	if scope := stub.calls[0].Scope; len(scope.Repositories) != 1 || scope.Repositories[0].GitHubID != 101 || scope.SelectedRepositoryID != 1 {
		t.Fatalf("scope=%+v", scope)
	}
	if len(stub.impact) != 1 || stub.impact[0].Depth != 4 {
		t.Fatalf("impact=%+v", stub.impact)
	}
}

func TestSymbolQueriesRejectForeignFactsAndStaleCredentials(t *testing.T) {
	s, stub := symbolFixture()
	request := api.GraphSymbolCallsRequest{Repo: api.GraphRepositorySelector{ID: 101}, Symbol: "run"}
	stub.result.Definitions[0].Related[0].Entity.RepositoryID = 202
	if _, err := s.SymbolCallers(t.Context(), principalFor(101), request); !errors.Is(err, ErrGraphNotReady) {
		t.Fatalf("foreign related entity: %v", err)
	}
	stub.result.Definitions[0].Related[0].Entity.RepositoryID = 101
	stub.result.Definitions[0].Related[0].Edge.RepositoryID = 202
	if _, err := s.SymbolCallers(t.Context(), principalFor(101), request); !errors.Is(err, ErrGraphNotReady) {
		t.Fatalf("foreign edge: %v", err)
	}
	stub.result.Definitions[0].Related[0].Edge.RepositoryID = 101
	ctx := authn.WithFreshPrincipal(t.Context(), func(context.Context) (authn.Principal, error) { return authn.Principal{}, authn.ErrUnauthenticated })
	if _, err := s.SymbolCallers(ctx, principalFor(101), request); !errors.Is(err, authn.ErrUnauthenticated) {
		t.Fatalf("revoked credential: %v", err)
	}
	calls := len(stub.calls)
	request.Repo = api.GraphRepositorySelector{ID: 202}
	if _, err := s.SymbolCallers(t.Context(), principalFor(101), request); !errors.Is(err, ErrRepositoryNotFound) || len(stub.calls) != calls {
		t.Fatalf("invisible repository: %v backend calls=%d", err, len(stub.calls)-calls)
	}
}
