package graphservice

import (
	"context"

	"github.com/balcsida/graphnest/internal/authn"
	"github.com/balcsida/graphnest/internal/graphprotocol"
)

func (s *Service) TypeRelations(ctx context.Context, principal authn.Principal, request EntityAnalysisRequest) (graphprotocol.TypeRelationsResponse, error) {
	backend, ok := s.Backend.(interface {
		TypeRelations(context.Context, graphprotocol.TypeRelationsRequest) (graphprotocol.TypeRelationsResponse, error)
	})
	if !ok {
		return graphprotocol.TypeRelationsResponse{}, ErrGraphNotReady
	}
	return aggregateCall(ctx, s, principal, request.AggregateScopeRequest, func(scope graphprotocol.Scope) (graphprotocol.TypeRelationsResponse, error) {
		return backend.TypeRelations(ctx, graphprotocol.TypeRelationsRequest{Scope: scope, Occurrence: request.Occurrence})
	}, func(value graphprotocol.TypeRelationsResponse) []graphprotocol.Generation { return value.Generations })
}

func (s *Service) TypeHierarchy(ctx context.Context, principal authn.Principal, request EntityAnalysisRequest) (graphprotocol.TypeHierarchyResponse, error) {
	backend, ok := s.Backend.(interface {
		TypeHierarchy(context.Context, graphprotocol.TypeHierarchyRequest) (graphprotocol.TypeHierarchyResponse, error)
	})
	if !ok {
		return graphprotocol.TypeHierarchyResponse{}, ErrGraphNotReady
	}
	return aggregateCall(ctx, s, principal, request.AggregateScopeRequest, func(scope graphprotocol.Scope) (graphprotocol.TypeHierarchyResponse, error) {
		return backend.TypeHierarchy(ctx, graphprotocol.TypeHierarchyRequest{Scope: scope, Occurrence: request.Occurrence})
	}, func(value graphprotocol.TypeHierarchyResponse) []graphprotocol.Generation { return value.Generations })
}
