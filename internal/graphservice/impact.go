package graphservice

import (
	"context"
	"time"

	"github.com/grepnest/grepnest/internal/authn"
	"github.com/grepnest/grepnest/internal/graphprotocol"
	"github.com/grepnest/grepnest/pkg/api"
)

func (s *Service) Impact(ctx context.Context, principal authn.Principal, request api.GraphImpactRequest) (result api.GraphImpactResponse, err error) {
	started := time.Now()
	defer s.observe(started, "impact", &err)
	if request.TargetUID == "" || (request.Direction != "upstream" && request.Direction != "downstream") || request.MinConfidence < 0 || request.MinConfidence > 1 || request.MaxDepth < 0 || request.Limit < 0 || request.Offset < 0 || !validRelations(request.Relations) {
		return api.GraphImpactResponse{}, ErrInvalidRequest
	}
	selected, scope, snapshots, err := s.scope(ctx, principal, request.Repo, request.Branch)
	if err != nil {
		return api.GraphImpactResponse{}, err
	}
	limits := s.limits()
	depth := request.MaxDepth
	if depth <= 0 {
		depth = limits.DefaultImpactDepth
	} else if depth > limits.MaxDepth {
		depth = limits.MaxDepth
	}
	limit := request.Limit
	if limit <= 0 || limit > limits.MaxRows {
		limit = limits.MaxRows
	}
	response, err := s.Backend.Impact(ctx, graphprotocol.ImpactRequest{Scope: scope, TargetUID: request.TargetUID, Direction: request.Direction, Relations: request.Relations, MinConfidence: request.MinConfidence, IncludeTests: request.IncludeTests, MaxDepth: depth, Limit: limit, Offset: request.Offset, SummaryOnly: request.SummaryOnly})
	if err != nil {
		return api.GraphImpactResponse{}, err
	}
	if err := s.reauthorize(ctx, principal, selected, response.Commits); err != nil {
		return api.GraphImpactResponse{}, err
	}
	result = api.GraphImpactResponse{Status: response.Status, ByDepth: map[int][]api.GraphSymbol{}, Boundaries: boundaries(response.Boundaries), Commits: response.Commits, Partial: response.Partial}
	for _, value := range response.Candidates {
		converted, convertErr := candidate(value, snapshots)
		if convertErr != nil {
			return api.GraphImpactResponse{}, convertErr
		}
		result.Candidates = append(result.Candidates, converted)
	}
	for depth, values := range response.ByDepth {
		for _, value := range values {
			result.ByDepth[depth] = append(result.ByDepth[depth], symbol(value))
		}
	}
	for _, value := range response.Edges {
		result.Relations = append(result.Relations, reference(value))
	}
	return result, nil
}
