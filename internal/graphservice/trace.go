package graphservice

import (
	"context"
	"time"

	"github.com/grepnest/grepnest/internal/authn"
	"github.com/grepnest/grepnest/internal/graphprotocol"
	"github.com/grepnest/grepnest/pkg/api"
)

func (s *Service) Trace(ctx context.Context, principal authn.Principal, request api.GraphTraceRequest) (result api.GraphTraceResponse, err error) {
	started := time.Now()
	defer s.observe(started, "trace", &err)
	if request.SourceUID == "" || request.TargetUID == "" || request.MaxDepth < 0 {
		return api.GraphTraceResponse{}, ErrInvalidRequest
	}
	selected, scope, snapshots, err := s.scope(ctx, principal, request.Repo, request.Branch)
	if err != nil {
		return api.GraphTraceResponse{}, err
	}
	limits := s.limits()
	depth := request.MaxDepth
	if depth <= 0 {
		depth = limits.DefaultTraceDepth
	} else if depth > limits.MaxTraceDepth {
		depth = limits.MaxTraceDepth
	}
	response, err := s.Backend.Trace(ctx, graphprotocol.TraceRequest{Scope: scope, SourceUID: request.SourceUID, TargetUID: request.TargetUID, MaxDepth: depth})
	if err != nil {
		return api.GraphTraceResponse{}, err
	}
	if err := s.reauthorize(ctx, principal, selected, response.Commits); err != nil {
		return api.GraphTraceResponse{}, err
	}
	result = api.GraphTraceResponse{Status: response.Status, Boundaries: boundaries(response.Boundaries), Commits: response.Commits}
	for _, value := range response.Candidates {
		converted, convertErr := candidate(value, snapshots)
		if convertErr != nil {
			return api.GraphTraceResponse{}, convertErr
		}
		result.Candidates = append(result.Candidates, converted)
	}
	for _, value := range response.Nodes {
		result.Nodes = append(result.Nodes, symbol(value))
	}
	for _, value := range response.Edges {
		result.Relations = append(result.Relations, reference(value))
	}
	return result, nil
}
