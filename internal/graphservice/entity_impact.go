package graphservice

import (
	"context"

	"github.com/balcsida/graphnest/internal/authn"
	"github.com/balcsida/graphnest/internal/graphprotocol"
)

type EntityAnalysisRequest struct {
	AggregateScopeRequest
	Occurrence string
	MaxDepth   *int
}

type EntityFileRequest struct {
	AggregateScopeRequest
	Path string
}

type QualifiedNameRequest struct {
	AggregateScopeRequest
	Pattern string
}

type FilteredSubgraphRequest struct {
	AggregateScopeRequest
	Filter       graphprotocol.EntityFilter
	IncludeEdges *bool
}

func (s *Service) CallGraph(ctx context.Context, principal authn.Principal, request EntityAnalysisRequest) (graphprotocol.SubgraphResponse, error) {
	backend, ok := s.Backend.(interface {
		CallGraph(context.Context, graphprotocol.EntityImpactRequest) (graphprotocol.SubgraphResponse, error)
	})
	if !ok {
		return graphprotocol.SubgraphResponse{}, ErrGraphNotReady
	}
	return aggregateCall(ctx, s, principal, request.AggregateScopeRequest, func(scope graphprotocol.Scope) (graphprotocol.SubgraphResponse, error) {
		return backend.CallGraph(ctx, graphprotocol.EntityImpactRequest{Scope: scope, Occurrence: request.Occurrence, MaxDepth: request.MaxDepth})
	}, func(value graphprotocol.SubgraphResponse) []graphprotocol.Generation { return value.Generations })
}

func (s *Service) Usages(ctx context.Context, principal authn.Principal, request EntityAnalysisRequest) (graphprotocol.UsagesResponse, error) {
	backend, ok := s.Backend.(interface {
		Usages(context.Context, graphprotocol.EntityRequest) (graphprotocol.UsagesResponse, error)
	})
	if !ok {
		return graphprotocol.UsagesResponse{}, ErrGraphNotReady
	}
	return aggregateCall(ctx, s, principal, request.AggregateScopeRequest, func(scope graphprotocol.Scope) (graphprotocol.UsagesResponse, error) {
		return backend.Usages(ctx, graphprotocol.EntityRequest{Scope: scope, Occurrence: request.Occurrence})
	}, func(value graphprotocol.UsagesResponse) []graphprotocol.Generation { return value.Generations })
}

func (s *Service) ImpactRadius(ctx context.Context, principal authn.Principal, request EntityAnalysisRequest) (graphprotocol.SubgraphResponse, error) {
	backend, ok := s.Backend.(interface {
		ImpactRadius(context.Context, graphprotocol.EntityImpactRequest) (graphprotocol.SubgraphResponse, error)
	})
	if !ok {
		return graphprotocol.SubgraphResponse{}, ErrGraphNotReady
	}
	return aggregateCall(ctx, s, principal, request.AggregateScopeRequest, func(scope graphprotocol.Scope) (graphprotocol.SubgraphResponse, error) {
		return backend.ImpactRadius(ctx, graphprotocol.EntityImpactRequest{Scope: scope, Occurrence: request.Occurrence, MaxDepth: request.MaxDepth})
	}, func(value graphprotocol.SubgraphResponse) []graphprotocol.Generation { return value.Generations })
}

func (s *Service) ExportedSymbols(ctx context.Context, principal authn.Principal, request EntityFileRequest) (graphprotocol.EntitiesResponse, error) {
	backend, ok := s.Backend.(interface {
		ExportedSymbols(context.Context, graphprotocol.FileEntityRequest) (graphprotocol.EntitiesResponse, error)
	})
	if !ok {
		return graphprotocol.EntitiesResponse{}, ErrGraphNotReady
	}
	return aggregateCall(ctx, s, principal, request.AggregateScopeRequest, func(scope graphprotocol.Scope) (graphprotocol.EntitiesResponse, error) {
		return backend.ExportedSymbols(ctx, graphprotocol.FileEntityRequest{Scope: scope, Path: request.Path})
	}, func(value graphprotocol.EntitiesResponse) []graphprotocol.Generation { return value.Generations })
}

func (s *Service) FindByQualifiedName(ctx context.Context, principal authn.Principal, request QualifiedNameRequest) (graphprotocol.EntitiesResponse, error) {
	backend, ok := s.Backend.(interface {
		FindByQualifiedName(context.Context, graphprotocol.QualifiedNameRequest) (graphprotocol.EntitiesResponse, error)
	})
	if !ok {
		return graphprotocol.EntitiesResponse{}, ErrGraphNotReady
	}
	return aggregateCall(ctx, s, principal, request.AggregateScopeRequest, func(scope graphprotocol.Scope) (graphprotocol.EntitiesResponse, error) {
		return backend.FindByQualifiedName(ctx, graphprotocol.QualifiedNameRequest{Scope: scope, Pattern: request.Pattern})
	}, func(value graphprotocol.EntitiesResponse) []graphprotocol.Generation { return value.Generations })
}

func (s *Service) ModuleStructure(ctx context.Context, principal authn.Principal, request AggregateScopeRequest) (graphprotocol.ModuleStructureResponse, error) {
	backend, ok := s.Backend.(interface {
		ModuleStructure(context.Context, graphprotocol.ScopeRequest) (graphprotocol.ModuleStructureResponse, error)
	})
	if !ok {
		return graphprotocol.ModuleStructureResponse{}, ErrGraphNotReady
	}
	return aggregateCall(ctx, s, principal, request, func(scope graphprotocol.Scope) (graphprotocol.ModuleStructureResponse, error) {
		return backend.ModuleStructure(ctx, graphprotocol.ScopeRequest{Scope: scope})
	}, func(value graphprotocol.ModuleStructureResponse) []graphprotocol.Generation { return value.Generations })
}

func (s *Service) FilteredSubgraph(ctx context.Context, principal authn.Principal, request FilteredSubgraphRequest) (graphprotocol.SubgraphResponse, error) {
	backend, ok := s.Backend.(interface {
		FilteredSubgraph(context.Context, graphprotocol.FilteredSubgraphRequest) (graphprotocol.SubgraphResponse, error)
	})
	if !ok {
		return graphprotocol.SubgraphResponse{}, ErrGraphNotReady
	}
	return aggregateCall(ctx, s, principal, request.AggregateScopeRequest, func(scope graphprotocol.Scope) (graphprotocol.SubgraphResponse, error) {
		return backend.FilteredSubgraph(ctx, graphprotocol.FilteredSubgraphRequest{Scope: scope, Filter: request.Filter, IncludeEdges: request.IncludeEdges})
	}, func(value graphprotocol.SubgraphResponse) []graphprotocol.Generation { return value.Generations })
}
