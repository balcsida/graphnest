package graphservice

import (
	"context"
	"time"

	"github.com/balcsida/graphnest/internal/authn"
	"github.com/balcsida/graphnest/internal/graphprotocol"
	"github.com/balcsida/graphnest/pkg/api"
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
	publicBoundaries, err := publicBoundaries(response.Boundaries, snapshots)
	if err != nil {
		return api.GraphTraceResponse{}, err
	}
	result = api.GraphTraceResponse{Status: response.Status, Boundaries: publicBoundaries, Commits: response.Commits}
	for _, value := range response.Candidates {
		converted, convertErr := candidate(value, snapshots)
		if convertErr != nil {
			return api.GraphTraceResponse{}, convertErr
		}
		result.Candidates = append(result.Candidates, converted)
	}
	for _, value := range response.Nodes {
		converted, convertErr := publicSymbol(value, snapshots)
		if convertErr != nil {
			return api.GraphTraceResponse{}, convertErr
		}
		result.Nodes = append(result.Nodes, converted)
	}
	for _, value := range response.Edges {
		converted, convertErr := publicReference(value, snapshots)
		if convertErr != nil {
			return api.GraphTraceResponse{}, convertErr
		}
		result.Relations = append(result.Relations, converted)
	}
	return result, nil
}
