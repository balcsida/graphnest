package graphservice

import (
	"context"

	"github.com/balcsida/graphnest/internal/authn"
	"github.com/balcsida/graphnest/internal/graphprotocol"
	"github.com/balcsida/graphnest/pkg/api"
)

type AggregateScopeRequest struct {
	Repo   api.GraphRepositorySelector
	Branch string
}

type AggregateValuesRequest struct {
	AggregateScopeRequest
	Values []string
}

type AggregateLimitRequest struct {
	AggregateScopeRequest
	Limit int
}

type AggregateNodeRequest struct {
	AggregateScopeRequest
	Occurrence string
}

type AggregateFilesRequest struct {
	AggregateScopeRequest
	Paths []string
}

type AggregateModuleRequest struct {
	AggregateScopeRequest
	Assignments     []graphprotocol.ModuleAssignment
	Kinds           []string
	MinConfidence   float64
	TopPairsPerLink int
	PairKinds       []string
}

type AggregateUnresolvedRequest struct {
	AggregateScopeRequest
	Occurrence, Path string
	Limit            *int
}

func aggregateCall[T any](ctx context.Context, service *Service, principal authn.Principal, request AggregateScopeRequest, call func(graphprotocol.Scope) (T, error), generations func(T) []graphprotocol.Generation) (T, error) {
	var zero T
	inspection, err := service.inspectionScope(ctx, principal, request.Repo, request.Branch)
	if err != nil {
		return zero, err
	}
	result, err := call(inspection.scope)
	if err != nil {
		return zero, err
	}
	if err = service.finishInspection(ctx, principal, inspection, generations(result), result); err != nil {
		return zero, err
	}
	return result, nil
}

func (s *Service) GraphStats(ctx context.Context, principal authn.Principal, request AggregateScopeRequest) (graphprotocol.GraphStatsResponse, error) {
	backend, ok := s.Backend.(interface {
		GraphStats(context.Context, graphprotocol.Scope) (graphprotocol.GraphStatsResponse, error)
	})
	if !ok {
		return graphprotocol.GraphStatsResponse{}, ErrGraphNotReady
	}
	return aggregateCall(ctx, s, principal, request, func(scope graphprotocol.Scope) (graphprotocol.GraphStatsResponse, error) {
		return backend.GraphStats(ctx, scope)
	}, func(value graphprotocol.GraphStatsResponse) []graphprotocol.Generation { return value.Generations })
}

func (s *Service) FanIn(ctx context.Context, principal authn.Principal, request AggregateValuesRequest) (graphprotocol.AggregateCountsResponse, error) {
	backend, ok := s.Backend.(interface {
		FanIn(context.Context, graphprotocol.AggregateValuesRequest) (graphprotocol.AggregateCountsResponse, error)
	})
	if !ok {
		return graphprotocol.AggregateCountsResponse{}, ErrGraphNotReady
	}
	return aggregateCall(ctx, s, principal, request.AggregateScopeRequest, func(scope graphprotocol.Scope) (graphprotocol.AggregateCountsResponse, error) {
		return backend.FanIn(ctx, graphprotocol.AggregateValuesRequest{Scope: scope, Values: request.Values})
	}, func(value graphprotocol.AggregateCountsResponse) []graphprotocol.Generation { return value.Generations })
}

func (s *Service) FanOut(ctx context.Context, principal authn.Principal, request AggregateValuesRequest) (graphprotocol.AggregateCountsResponse, error) {
	backend, ok := s.Backend.(interface {
		FanOut(context.Context, graphprotocol.AggregateValuesRequest) (graphprotocol.AggregateCountsResponse, error)
	})
	if !ok {
		return graphprotocol.AggregateCountsResponse{}, ErrGraphNotReady
	}
	return aggregateCall(ctx, s, principal, request.AggregateScopeRequest, func(scope graphprotocol.Scope) (graphprotocol.AggregateCountsResponse, error) {
		return backend.FanOut(ctx, graphprotocol.AggregateValuesRequest{Scope: scope, Values: request.Values})
	}, func(value graphprotocol.AggregateCountsResponse) []graphprotocol.Generation { return value.Generations })
}

func (s *Service) NodeMetrics(ctx context.Context, principal authn.Principal, request AggregateNodeRequest) (graphprotocol.NodeMetricsResponse, error) {
	backend, ok := s.Backend.(interface {
		NodeMetrics(context.Context, graphprotocol.NodeMetricsRequest) (graphprotocol.NodeMetricsResponse, error)
	})
	if !ok {
		return graphprotocol.NodeMetricsResponse{}, ErrGraphNotReady
	}
	return aggregateCall(ctx, s, principal, request.AggregateScopeRequest, func(scope graphprotocol.Scope) (graphprotocol.NodeMetricsResponse, error) {
		return backend.NodeMetrics(ctx, graphprotocol.NodeMetricsRequest{Scope: scope, Occurrence: request.Occurrence})
	}, func(value graphprotocol.NodeMetricsResponse) []graphprotocol.Generation { return value.Generations })
}

func (s *Service) aggregateNames(ctx context.Context, principal authn.Principal, request AggregateValuesRequest, operation string) (graphprotocol.AggregateNamesResponse, error) {
	backend, ok := s.Backend.(interface {
		AmbiguousReferencedNames(context.Context, graphprotocol.AggregateValuesRequest) (graphprotocol.AggregateNamesResponse, error)
		LanguagesWithExports(context.Context, graphprotocol.AggregateValuesRequest) (graphprotocol.AggregateNamesResponse, error)
		UnresolvedNamesAmong(context.Context, graphprotocol.AggregateValuesRequest) (graphprotocol.AggregateNamesResponse, error)
	})
	if !ok {
		return graphprotocol.AggregateNamesResponse{}, ErrGraphNotReady
	}
	return aggregateCall(ctx, s, principal, request.AggregateScopeRequest, func(scope graphprotocol.Scope) (graphprotocol.AggregateNamesResponse, error) {
		query := graphprotocol.AggregateValuesRequest{Scope: scope, Values: request.Values}
		switch operation {
		case "ambiguous":
			return backend.AmbiguousReferencedNames(ctx, query)
		case "exports":
			return backend.LanguagesWithExports(ctx, query)
		default:
			return backend.UnresolvedNamesAmong(ctx, query)
		}
	}, func(value graphprotocol.AggregateNamesResponse) []graphprotocol.Generation { return value.Generations })
}

func (s *Service) AmbiguousReferencedNames(ctx context.Context, principal authn.Principal, request AggregateValuesRequest) (graphprotocol.AggregateNamesResponse, error) {
	return s.aggregateNames(ctx, principal, request, "ambiguous")
}

func (s *Service) LanguagesWithExports(ctx context.Context, principal authn.Principal, request AggregateValuesRequest) (graphprotocol.AggregateNamesResponse, error) {
	return s.aggregateNames(ctx, principal, request, "exports")
}

func (s *Service) UnresolvedNamesAmong(ctx context.Context, principal authn.Principal, request AggregateValuesRequest) (graphprotocol.AggregateNamesResponse, error) {
	return s.aggregateNames(ctx, principal, request, "unresolved")
}

func (s *Service) TopDependedOn(ctx context.Context, principal authn.Principal, request AggregateLimitRequest) (graphprotocol.TopDependedOnResponse, error) {
	backend, ok := s.Backend.(interface {
		TopDependedOn(context.Context, graphprotocol.AggregateLimitRequest) (graphprotocol.TopDependedOnResponse, error)
	})
	if !ok {
		return graphprotocol.TopDependedOnResponse{}, ErrGraphNotReady
	}
	return aggregateCall(ctx, s, principal, request.AggregateScopeRequest, func(scope graphprotocol.Scope) (graphprotocol.TopDependedOnResponse, error) {
		return backend.TopDependedOn(ctx, graphprotocol.AggregateLimitRequest{Scope: scope, Limit: request.Limit})
	}, func(value graphprotocol.TopDependedOnResponse) []graphprotocol.Generation { return value.Generations })
}

func (s *Service) TopCallingFiles(ctx context.Context, principal authn.Principal, request AggregateLimitRequest) (graphprotocol.TopCallingFilesResponse, error) {
	backend, ok := s.Backend.(interface {
		TopCallingFiles(context.Context, graphprotocol.AggregateLimitRequest) (graphprotocol.TopCallingFilesResponse, error)
	})
	if !ok {
		return graphprotocol.TopCallingFilesResponse{}, ErrGraphNotReady
	}
	return aggregateCall(ctx, s, principal, request.AggregateScopeRequest, func(scope graphprotocol.Scope) (graphprotocol.TopCallingFilesResponse, error) {
		return backend.TopCallingFiles(ctx, graphprotocol.AggregateLimitRequest{Scope: scope, Limit: request.Limit})
	}, func(value graphprotocol.TopCallingFilesResponse) []graphprotocol.Generation { return value.Generations })
}

func (s *Service) FileNodes(ctx context.Context, principal authn.Principal, request AggregateFilesRequest) (graphprotocol.EntitiesResponse, error) {
	backend, ok := s.Backend.(interface {
		FileNodes(context.Context, graphprotocol.FileNodesRequest) (graphprotocol.EntitiesResponse, error)
	})
	if !ok {
		return graphprotocol.EntitiesResponse{}, ErrGraphNotReady
	}
	return aggregateCall(ctx, s, principal, request.AggregateScopeRequest, func(scope graphprotocol.Scope) (graphprotocol.EntitiesResponse, error) {
		return backend.FileNodes(ctx, graphprotocol.FileNodesRequest{Scope: scope, Paths: request.Paths})
	}, func(value graphprotocol.EntitiesResponse) []graphprotocol.Generation { return value.Generations })
}

func (s *Service) ModuleAggregation(ctx context.Context, principal authn.Principal, request AggregateModuleRequest) (graphprotocol.ModuleAggregationResponse, error) {
	backend, ok := s.Backend.(interface {
		ModuleAggregation(context.Context, graphprotocol.ModuleAggregationRequest) (graphprotocol.ModuleAggregationResponse, error)
	})
	if !ok {
		return graphprotocol.ModuleAggregationResponse{}, ErrGraphNotReady
	}
	return aggregateCall(ctx, s, principal, request.AggregateScopeRequest, func(scope graphprotocol.Scope) (graphprotocol.ModuleAggregationResponse, error) {
		return backend.ModuleAggregation(ctx, graphprotocol.ModuleAggregationRequest{Scope: scope, Assignments: request.Assignments, Kinds: request.Kinds, MinConfidence: request.MinConfidence, TopPairsPerLink: request.TopPairsPerLink, PairKinds: request.PairKinds})
	}, func(value graphprotocol.ModuleAggregationResponse) []graphprotocol.Generation {
		return value.Generations
	})
}

func (s *Service) UnresolvedReferencesFrom(ctx context.Context, principal authn.Principal, request AggregateUnresolvedRequest) (graphprotocol.UnresolvedReferencesResponse, error) {
	backend, ok := s.Backend.(interface {
		UnresolvedReferencesFrom(context.Context, graphprotocol.UnresolvedReferencesRequest) (graphprotocol.UnresolvedReferencesResponse, error)
	})
	if !ok {
		return graphprotocol.UnresolvedReferencesResponse{}, ErrGraphNotReady
	}
	return aggregateCall(ctx, s, principal, request.AggregateScopeRequest, func(scope graphprotocol.Scope) (graphprotocol.UnresolvedReferencesResponse, error) {
		return backend.UnresolvedReferencesFrom(ctx, graphprotocol.UnresolvedReferencesRequest{Scope: scope, Occurrence: request.Occurrence, Limit: request.Limit})
	}, func(value graphprotocol.UnresolvedReferencesResponse) []graphprotocol.Generation {
		return value.Generations
	})
}

func (s *Service) UnresolvedReferencesInFile(ctx context.Context, principal authn.Principal, request AggregateUnresolvedRequest) (graphprotocol.UnresolvedReferencesResponse, error) {
	backend, ok := s.Backend.(interface {
		UnresolvedReferencesInFile(context.Context, graphprotocol.UnresolvedReferencesRequest) (graphprotocol.UnresolvedReferencesResponse, error)
	})
	if !ok {
		return graphprotocol.UnresolvedReferencesResponse{}, ErrGraphNotReady
	}
	return aggregateCall(ctx, s, principal, request.AggregateScopeRequest, func(scope graphprotocol.Scope) (graphprotocol.UnresolvedReferencesResponse, error) {
		return backend.UnresolvedReferencesInFile(ctx, graphprotocol.UnresolvedReferencesRequest{Scope: scope, Path: request.Path, Limit: request.Limit})
	}, func(value graphprotocol.UnresolvedReferencesResponse) []graphprotocol.Generation {
		return value.Generations
	})
}
