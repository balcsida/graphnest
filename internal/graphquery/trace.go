package graphquery

import (
	"context"

	"github.com/grepnest/grepnest/internal/graphprotocol"
)

func (service *Service) Trace(ctx context.Context, request graphprotocol.TraceRequest) (graphprotocol.TraceResponse, error) {
	ready, err := service.ready(ctx, request.Scope)
	response := graphprotocol.TraceResponse{
		Status: graphprotocol.StatusNoPath, Boundaries: ready.boundaries, Commits: ready.commits,
	}
	if err != nil {
		return response, err
	}
	if len(ready.snapshots) == 0 {
		return response, nil
	}
	if request.SourceUID == "" || request.TargetUID == "" || request.MaxDepth < 0 {
		return response, ErrInvalidRequest
	}
	limits := service.limits()
	depth := request.MaxDepth
	if depth <= 0 {
		depth = limits.DefaultTraceDepth
	}
	if depth > limits.MaxTraceDepth {
		depth = limits.MaxTraceDepth
		response.Boundaries = appendBoundary(response.Boundaries, "depth_limit", depth)
	}
	sources, err := service.lookupSymbols(ctx, ready, request.SourceUID, true)
	if err != nil {
		return response, err
	}
	targets, err := service.lookupSymbols(ctx, ready, request.TargetUID, true)
	if err != nil {
		return response, err
	}
	switch {
	case len(sources) > 1:
		response.Status, response.Candidates = graphprotocol.StatusAmbiguous, sources
		return response, nil
	case len(targets) > 1:
		response.Status, response.Candidates = graphprotocol.StatusAmbiguous, targets
		return response, nil
	}
	targetKeys := make(map[nodeKey]struct{}, len(targets))
	symbols := make(map[nodeKey]graphprotocol.Symbol, len(sources)+len(targets))
	for _, target := range targets {
		key := nodeKey{repositoryID: target.RepositoryID, uid: target.UID}
		targetKeys[key], symbols[key] = struct{}{}, target
	}
	parents := make(map[nodeKey]nodeKey, len(sources))
	roots := make(map[nodeKey]struct{}, len(sources))
	frontier := make([]nodeKey, 0, len(sources))
	var found nodeKey
	foundOK := false
	for _, source := range sources {
		key := nodeKey{repositoryID: source.RepositoryID, uid: source.UID}
		roots[key], symbols[key] = struct{}{}, source
		parents[key] = nodeKey{}
		frontier = append(frontier, key)
		if _, ok := targetKeys[key]; ok && !foundOK {
			found, foundOK = key, true
		}
	}
	edgeCount := 0
	via := map[nodeKey]graphprotocol.Relationship{}
	store := service.queryStore()
	for currentDepth := 1; currentDepth <= depth && len(frontier) > 0 && !foundOK; currentDepth++ {
		refs := make([]SymbolRef, 0, len(frontier))
		for _, key := range frontier {
			refs = append(refs, SymbolRef{RepositoryID: key.repositoryID, UID: key.uid})
		}
		neighbors, queryErr := store.Neighbors(ctx, NeighborQuery{
			Snapshots: ready.snapshots, Frontier: refs, Relation: "calls", Direction: "outgoing",
			Limit: limits.MaxFanout + 1,
		})
		if queryErr != nil {
			return response, queryErr
		}
		if len(neighbors) > limits.MaxFanout {
			neighbors = neighbors[:limits.MaxFanout]
			response.Boundaries = appendBoundary(response.Boundaries, "fanout_limit", currentDepth)
		}
		next := []nodeKey{}
		for _, neighbor := range neighbors {
			symbol := neighbor.Symbol
			key := nodeKey{repositoryID: symbol.RepositoryID, uid: symbol.UID}
			edgeCount++
			if edgeCount > limits.MaxEdges || len(parents)-len(sources) >= limits.MaxNodes {
				response.Boundaries = appendBoundary(response.Boundaries, "traversal_limit", currentDepth)
				return response, nil
			}
			if _, seen := parents[key]; seen {
				continue
			}
			parent := nodeKey{repositoryID: neighbor.Parent.RepositoryID, uid: neighbor.Parent.UID}
			parents[key], symbols[key] = parent, symbol
			via[key] = neighbor.Edge
			next = append(next, key)
			if _, ok := targetKeys[key]; ok {
				found, foundOK = key, true
				break
			}
		}
		frontier = next
	}
	if !foundOK {
		return response, nil
	}
	path := []nodeKey{found}
	for {
		if _, root := roots[path[len(path)-1]]; root {
			break
		}
		parent, ok := parents[path[len(path)-1]]
		if !ok {
			return response, nil
		}
		path = append(path, parent)
	}
	for left, right := 0, len(path)-1; left < right; left, right = left+1, right-1 {
		path[left], path[right] = path[right], path[left]
	}
	for i, key := range path {
		response.Nodes = append(response.Nodes, symbols[key])
		if i > 0 {
			response.Edges = append(response.Edges, via[key])
		}
	}
	response.Status = graphprotocol.StatusOK
	return response, nil
}

func (service *Service) lookupSymbols(ctx context.Context, ready readyScope, uid string, selected bool) ([]graphprotocol.Symbol, error) {
	snapshots := ready.snapshots
	if selected {
		snapshots = ready.selectorSnapshots()
	}
	if len(snapshots) == 0 {
		return nil, nil
	}
	return service.queryStore().Symbols(ctx, SymbolQuery{Snapshots: snapshots, UID: uid, Limit: len(snapshots)})
}
