package graphservice

import (
	"context"

	"github.com/grepnest/grepnest/internal/authn"
	"github.com/grepnest/grepnest/internal/graphprotocol"
	"github.com/grepnest/grepnest/pkg/api"
)

func (s *Service) Impact(ctx context.Context, principal authn.Principal, request api.GraphImpactRequest) (api.GraphImpactResponse, error) {
	if request.TargetUID == "" || (request.Direction != "upstream" && request.Direction != "downstream") || request.MinConfidence < 0 || request.MinConfidence > 1 || request.MaxDepth < 0 || request.Limit < 0 || request.Offset < 0 || !validRelations(request.Relations) {
		return api.GraphImpactResponse{}, ErrInvalidRequest
	}
	selected, scope, _, err := s.scope(ctx, principal, request.Repo, request.Branch)
	if err != nil {
		return api.GraphImpactResponse{}, err
	}
	response, err := s.Backend.Impact(ctx, graphprotocol.ImpactRequest{Scope: scope, TargetUID: request.TargetUID, Direction: request.Direction, Relations: request.Relations, MinConfidence: request.MinConfidence, IncludeTests: request.IncludeTests, MaxDepth: request.MaxDepth, Limit: request.Limit, Offset: request.Offset, SummaryOnly: request.SummaryOnly})
	if err != nil {
		return api.GraphImpactResponse{}, err
	}
	if err := s.reauthorize(ctx, principal, selected, response.Commits); err != nil {
		return api.GraphImpactResponse{}, err
	}
	result := api.GraphImpactResponse{Status: response.Status, ByDepth: map[int][]api.GraphSymbol{}, Boundaries: boundaries(response.Boundaries), Commits: response.Commits, Partial: response.Partial}
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
