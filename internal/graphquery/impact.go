package graphquery

import (
	"context"

	"github.com/grepnest/grepnest/internal/graphprotocol"
	"github.com/grepnest/grepnest/internal/ladybug"
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
		(request.Direction != "upstream" && request.Direction != "downstream") || request.Offset < 0 {
		return response, ErrInvalidRequest
	}
	relations, err := selectedRelations(request.Relations)
	if err != nil {
		return response, err
	}
	limits := service.limits()
	depth := request.MaxDepth
	if depth <= 0 {
		depth = defaultImpactDepth
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
	visited := map[string]struct{}{request.TargetUID: {}}
	frontier := []string{request.TargetUID}
	err = service.Database.View(ctx, func(session *ladybug.Session) error {
		for currentDepth := 1; currentDepth <= depth && len(frontier) > 0; currentDepth++ {
			next, seenDepth := []string{}, map[string]struct{}{}
			for _, relation := range relations {
				query := relationQueries[relation].outgoing
				if request.Direction == "upstream" {
					query = relationQueries[relation].incoming
				}
				result, queryErr := session.Execute(ctx, query, map[string]any{
					"scope": ready.parameters, "frontier": qualifyUIDs(ready.snapshots, frontier),
					"depth": int64(currentDepth), "min_confidence": request.MinConfidence,
					"offset": int64(0), "limit": int64(limits.MaxFanout + 1),
				}, ladybug.QueryLimits{MaxRows: limits.MaxFanout + 1})
				if queryErr != nil {
					return queryErr
				}
				if len(result.Rows) > limits.MaxFanout {
					result.Rows = result.Rows[:limits.MaxFanout]
					response.Partial = true
					response.Boundaries = appendBoundary(response.Boundaries, "fanout_limit", currentDepth)
				}
				for _, row := range result.Rows {
					symbol := symbolFromRow(row)
					if _, ok := visited[symbol.UID]; ok || (!request.IncludeTests && symbol.Test) {
						continue
					}
					if _, ok := seenDepth[symbol.UID]; ok {
						continue
					}
					if len(visited)-1 >= limits.MaxNodes || len(response.Edges) >= limits.MaxEdges {
						response.Partial = true
						response.Boundaries = appendBoundary(response.Boundaries, "traversal_limit", currentDepth)
						return nil
					}
					seenDepth[symbol.UID] = struct{}{}
					visited[symbol.UID] = struct{}{}
					next = append(next, symbol.UID)
					response.ByDepth[currentDepth] = append(response.ByDepth[currentDepth], symbol)
					edge := graphprotocol.Relationship{Kind: relation, Confidence: 1}
					parent := stripStorageUID(symbol.RepositoryID, row[11].(string))
					if request.Direction == "upstream" {
						edge.SourceUID, edge.TargetUID = symbol.UID, parent
					} else {
						edge.SourceUID, edge.TargetUID = parent, symbol.UID
					}
					response.Edges = append(response.Edges, edge)
				}
			}
			frontier = next
		}
		return nil
	})
	if err != nil {
		return response, err
	}
	if len(visited) > 1 {
		response.Status = graphprotocol.StatusFound
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
