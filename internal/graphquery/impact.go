package graphquery

import (
	"context"

	"github.com/grepnest/grepnest/internal/graphprotocol"
)

func (service *Service) Impact(ctx context.Context, request graphprotocol.ImpactRequest) (graphprotocol.ImpactResponse, error) {
	ready, err := service.ready(ctx, request.Scope)
	response := graphprotocol.ImpactResponse{
		Status: graphprotocol.StatusNotFound, ByDepth: map[int][]graphprotocol.Symbol{},
		Boundaries: ready.boundaries, Commits: ready.commits,
	}
	if err != nil {
		return response, err
	}
	if len(ready.snapshots) == 0 {
		return response, nil
	}
	if request.TargetUID == "" || request.MinConfidence < 0 || request.MinConfidence > 1 ||
		(request.Direction != "upstream" && request.Direction != "downstream") ||
		request.MaxDepth < 0 || request.Limit < 0 || request.Offset < 0 {
		return response, ErrInvalidRequest
	}
	relations, err := selectedRelations(request.Relations)
	if err != nil {
		return response, err
	}
	limits := service.limits()
	depth := request.MaxDepth
	if depth <= 0 {
		depth = limits.DefaultImpactDepth
	}
	if depth > limits.MaxDepth {
		depth = limits.MaxDepth
		response.Partial = true
		response.Boundaries = appendBoundary(response.Boundaries, "depth_limit", depth)
	}
	pageLimit := request.Limit
	if pageLimit <= 0 || pageLimit > limits.MaxNodes {
		pageLimit = limits.MaxNodes
	}
	targets, err := service.lookupSymbols(ctx, ready, request.TargetUID, true)
	if err != nil {
		return response, err
	}
	if len(targets) > 1 {
		response.Status, response.Candidates = graphprotocol.StatusAmbiguous, targets
		return response, nil
	}
	visited := make(map[nodeKey]struct{}, len(targets))
	frontier := make([]nodeKey, 0, len(targets))
	for _, target := range targets {
		key := nodeKey{repositoryID: target.RepositoryID, uid: target.UID}
		visited[key] = struct{}{}
		frontier = append(frontier, key)
	}
	if len(frontier) > 0 {
		response.Status = graphprotocol.StatusFound
	}
	store := service.queryStore()

traversal:
	for currentDepth := 1; currentDepth <= depth && len(frontier) > 0; currentDepth++ {
		next, seenDepth := []nodeKey{}, map[nodeKey]struct{}{}
		for _, relation := range relations {
			direction := "outgoing"
			if request.Direction == "upstream" {
				direction = "incoming"
			}
			refs := make([]SymbolRef, 0, len(frontier))
			for _, key := range frontier {
				refs = append(refs, SymbolRef{RepositoryID: key.repositoryID, UID: key.uid})
			}
			neighbors, queryErr := store.Neighbors(ctx, NeighborQuery{
				Snapshots: ready.snapshots, Frontier: refs, Relation: relation, Direction: direction,
				MinConfidence: request.MinConfidence, Limit: limits.MaxFanout + 1,
			})
			if queryErr != nil {
				return response, queryErr
			}
			if len(neighbors) > limits.MaxFanout {
				neighbors = neighbors[:limits.MaxFanout]
				response.Partial = true
				response.Boundaries = appendBoundary(response.Boundaries, "fanout_limit", currentDepth)
			}
			for _, neighbor := range neighbors {
				symbol := neighbor.Symbol
				key := nodeKey{repositoryID: symbol.RepositoryID, uid: symbol.UID}
				if _, ok := visited[key]; ok || (!request.IncludeTests && symbol.Test) {
					continue
				}
				if _, ok := seenDepth[key]; ok {
					continue
				}
				if len(visited)-len(targets) >= limits.MaxNodes || len(response.Edges) >= limits.MaxEdges {
					response.Partial = true
					response.Boundaries = appendBoundary(response.Boundaries, "traversal_limit", currentDepth)
					break traversal
				}
				seenDepth[key] = struct{}{}
				visited[key] = struct{}{}
				next = append(next, key)
				response.ByDepth[currentDepth] = append(response.ByDepth[currentDepth], symbol)
				response.Edges = append(response.Edges, neighbor.Edge)
			}
		}
		frontier = next
	}
	for currentDepth, symbols := range response.ByDepth {
		if request.Offset >= len(symbols) {
			response.ByDepth[currentDepth] = nil
			continue
		}
		symbols = symbols[request.Offset:]
		if len(symbols) > pageLimit {
			symbols = symbols[:pageLimit]
			response.Partial = true
			response.Boundaries = appendBoundary(response.Boundaries, "page_limit", currentDepth)
		}
		if request.SummaryOnly {
			symbols = nil
		}
		response.ByDepth[currentDepth] = symbols
	}
	return response, nil
}
