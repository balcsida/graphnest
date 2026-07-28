package graphservice

import (
	"context"

	"github.com/grepnest/grepnest/internal/authn"
	"github.com/grepnest/grepnest/internal/graphprotocol"
	"github.com/grepnest/grepnest/pkg/api"
)

func (s *Service) Trace(ctx context.Context, principal authn.Principal, request api.GraphTraceRequest) (api.GraphTraceResponse, error) {
	if request.SourceUID == "" || request.TargetUID == "" || request.MaxDepth < 0 {
		return api.GraphTraceResponse{}, ErrInvalidRequest
	}
	selected, scope, _, err := s.scope(ctx, principal, request.Repo, request.Branch)
	if err != nil {
		return api.GraphTraceResponse{}, err
	}
	response, err := s.Backend.Trace(ctx, graphprotocol.TraceRequest{Scope: scope, SourceUID: request.SourceUID, TargetUID: request.TargetUID, MaxDepth: request.MaxDepth})
	if err != nil {
		return api.GraphTraceResponse{}, err
	}
	if err := s.reauthorize(ctx, principal, selected, response.Commits); err != nil {
		return api.GraphTraceResponse{}, err
	}
	result := api.GraphTraceResponse{Status: response.Status, Boundaries: boundaries(response.Boundaries), Commits: response.Commits}
	for _, value := range response.Nodes {
		result.Nodes = append(result.Nodes, symbol(value))
	}
	for _, value := range response.Edges {
		result.Relations = append(result.Relations, reference(value))
	}
	return result, nil
}
