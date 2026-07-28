package graphquery

import (
	"context"

	"github.com/grepnest/grepnest/internal/graphprotocol"
	"github.com/grepnest/grepnest/internal/ladybug"
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
	if request.SourceUID == "" || request.TargetUID == "" {
		return response, ErrInvalidRequest
	}
	limits := service.limits()
	depth := request.MaxDepth
	if depth <= 0 {
		depth = defaultTraceDepth
	}
	if depth > limits.MaxTraceDepth {
		depth = limits.MaxTraceDepth
		response.Boundaries = appendBoundary(response.Boundaries, "depth_limit", depth)
	}
	parents := map[string]string{request.SourceUID: ""}
	symbols := map[string]graphprotocol.Symbol{}
	edgeCount := 0
	frontier, found := []string{request.SourceUID}, request.SourceUID == request.TargetUID
	err = service.Database.View(ctx, func(session *ladybug.Session) error {
		for currentDepth := 1; currentDepth <= depth && len(frontier) > 0 && !found; currentDepth++ {
			result, queryErr := session.Execute(ctx, relationQueries["calls"].outgoing, map[string]any{
				"scope": ready.parameters, "frontier": qualifyUIDs(ready.snapshots, frontier),
				"depth": int64(currentDepth), "min_confidence": float64(0),
				"offset": int64(0), "limit": int64(limits.MaxFanout + 1),
			}, ladybug.QueryLimits{MaxRows: limits.MaxFanout + 1})
			if queryErr != nil {
				return queryErr
			}
			if len(result.Rows) > limits.MaxFanout {
				result.Rows = result.Rows[:limits.MaxFanout]
				response.Boundaries = appendBoundary(response.Boundaries, "fanout_limit", currentDepth)
			}
			next := []string{}
			for _, row := range result.Rows {
				symbol := symbolFromRow(row)
				edgeCount++
				if edgeCount > limits.MaxEdges || len(parents) > limits.MaxNodes {
					response.Boundaries = appendBoundary(response.Boundaries, "traversal_limit", currentDepth)
					return nil
				}
				if _, seen := parents[symbol.UID]; seen {
					continue
				}
				parent := stripStorageUID(symbol.RepositoryID, row[11].(string))
				parents[symbol.UID], symbols[symbol.UID] = parent, symbol
				next = append(next, symbol.UID)
				if symbol.UID == request.TargetUID {
					found = true
					break
				}
			}
			frontier = next
		}
		return nil
	})
	if err != nil || !found {
		return response, err
	}
	path := []string{request.TargetUID}
	for path[len(path)-1] != request.SourceUID {
		parent, ok := parents[path[len(path)-1]]
		if !ok {
			return response, nil
		}
		path = append(path, parent)
	}
	for left, right := 0, len(path)-1; left < right; left, right = left+1, right-1 {
		path[left], path[right] = path[right], path[left]
	}
	source, lookupErr := service.lookupSymbol(ctx, ready, request.SourceUID)
	if lookupErr != nil {
		return response, lookupErr
	}
	symbols[request.SourceUID] = source
	for i, uid := range path {
		response.Nodes = append(response.Nodes, symbols[uid])
		if i > 0 {
			response.Edges = append(response.Edges, graphprotocol.Relationship{
				SourceUID: path[i-1], TargetUID: uid, Kind: "calls", Confidence: 1,
			})
		}
	}
	response.Status = graphprotocol.StatusOK
	return response, nil
}

func (service *Service) lookupSymbol(ctx context.Context, ready readyScope, uid string) (graphprotocol.Symbol, error) {
	var symbol graphprotocol.Symbol
	err := service.Database.View(ctx, func(session *ladybug.Session) error {
		result, err := session.Execute(ctx, selectSymbols, map[string]any{
			"scope": ready.parameters, "use_uid": true, "uids": qualifyUIDs(ready.snapshots, []string{uid}),
			"name": "", "path": "", "kind": "", "limit": int64(1),
		}, ladybug.QueryLimits{MaxRows: 1})
		if err == nil && len(result.Rows) == 1 {
			symbol = symbolFromRow(result.Rows[0])
		}
		return err
	})
	return symbol, err
}
