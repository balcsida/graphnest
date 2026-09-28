package graphservice

import (
	"context"
	"slices"
	"time"

	"github.com/balcsida/graphnest/internal/authn"
	"github.com/balcsida/graphnest/internal/graphprotocol"
	"github.com/balcsida/graphnest/pkg/api"
)

type symbolBackend interface {
	SymbolCalls(context.Context, graphprotocol.SymbolCallsRequest) (graphprotocol.SymbolResponse, error)
	SymbolImpact(context.Context, graphprotocol.SymbolImpactRequest) (graphprotocol.SymbolResponse, error)
}

// SymbolCallers lists what calls, imports, instantiates, navigates to, or
// references each definition of a name.
func (s *Service) SymbolCallers(ctx context.Context, p authn.Principal, r api.GraphSymbolCallsRequest) (graphprotocol.SymbolResponse, error) {
	return s.symbolQuery(ctx, p, r.Repo, r.Branch, func(backend symbolBackend, scope graphprotocol.Scope) (graphprotocol.SymbolResponse, error) {
		return backend.SymbolCalls(ctx, graphprotocol.SymbolCallsRequest{Scope: scope, Symbol: r.Symbol, File: r.File, Direction: "incoming", Limit: r.Limit})
	})
}

// SymbolCallees is the outgoing counterpart of SymbolCallers.
func (s *Service) SymbolCallees(ctx context.Context, p authn.Principal, r api.GraphSymbolCallsRequest) (graphprotocol.SymbolResponse, error) {
	return s.symbolQuery(ctx, p, r.Repo, r.Branch, func(backend symbolBackend, scope graphprotocol.Scope) (graphprotocol.SymbolResponse, error) {
		return backend.SymbolCalls(ctx, graphprotocol.SymbolCallsRequest{Scope: scope, Symbol: r.Symbol, File: r.File, Direction: "outgoing", Limit: r.Limit})
	})
}

// SymbolImpact is the impact radius of each definition of a name.
func (s *Service) SymbolImpact(ctx context.Context, p authn.Principal, r api.GraphSymbolImpactRequest) (graphprotocol.SymbolResponse, error) {
	return s.symbolQuery(ctx, p, r.Repo, r.Branch, func(backend symbolBackend, scope graphprotocol.Scope) (graphprotocol.SymbolResponse, error) {
		return backend.SymbolImpact(ctx, graphprotocol.SymbolImpactRequest{Scope: scope, Symbol: r.Symbol, File: r.File, Depth: r.Depth})
	})
}

func (s *Service) symbolQuery(ctx context.Context, p authn.Principal, repo api.GraphRepositorySelector, branch string, query func(symbolBackend, graphprotocol.Scope) (graphprotocol.SymbolResponse, error)) (graphprotocol.SymbolResponse, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	i, err := s.inspectionScope(ctx, p, repo, branch)
	if err != nil {
		return graphprotocol.SymbolResponse{}, err
	}
	backend, ok := s.Backend.(symbolBackend)
	if !ok {
		return graphprotocol.SymbolResponse{}, ErrGraphNotReady
	}
	result, err := query(backend, i.scope)
	if err != nil {
		return graphprotocol.SymbolResponse{}, err
	}
	if err := i.generations(result.Generations); err != nil {
		return graphprotocol.SymbolResponse{}, err
	}
	for _, definition := range result.Definitions {
		related := make([]graphprotocol.Entity, 0, len(definition.Related))
		edges := slices.Clone(definition.Edges)
		for _, r := range definition.Related {
			related = append(related, r.Entity)
			edges = append(edges, r.Edge)
		}
		for _, edge := range edges {
			if edge.RepositoryID != i.selected.GitHubID || edge.Fact == nil {
				return graphprotocol.SymbolResponse{}, ErrGraphNotReady
			}
		}
		for _, entities := range [][]graphprotocol.Entity{definition.Definitions, related, definition.Entities} {
			if err := i.entities(entities); err != nil {
				return graphprotocol.SymbolResponse{}, err
			}
		}
	}
	if err := s.finishInspection(ctx, p, i, result.Generations, result); err != nil {
		return graphprotocol.SymbolResponse{}, err
	}
	return result, nil
}
