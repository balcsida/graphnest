package graphquery

import (
	"context"

	"github.com/grepnest/grepnest/internal/graphprotocol"
	"github.com/grepnest/grepnest/internal/ladybug"
)

const selectSymbols = `UNWIND $scope AS scope MATCH (r:Repository), (s:Symbol) WHERE r.id = scope.id AND r.commit = scope.commit AND s.repository_id = r.id AND ($use_uid = false OR s.uid IN $uids) AND ($name = "" OR s.qualified_name = $name) AND ($path = "" OR s.path = $path) AND ($kind = "" OR s.kind = $kind) RETURN s.repository_id, s.uid, s.qualified_name, s.path, s.language, s.kind, s.signature, s.start_line, s.start_character, s.end_line, s.end_character LIMIT $limit`

func (service *Service) Context(ctx context.Context, request graphprotocol.ContextRequest) (graphprotocol.ContextResponse, error) {
	ready, err := service.ready(ctx, request.Scope)
	response := graphprotocol.ContextResponse{
		Status: graphprotocol.StatusNotFound, Incoming: map[string][]graphprotocol.Symbol{},
		Outgoing: map[string][]graphprotocol.Symbol{}, Boundaries: ready.boundaries, Commits: ready.commits,
	}
	if err != nil {
		return response, err
	}
	if len(ready.snapshots) == 0 {
		return response, nil
	}
	if request.UID == "" && request.Name == "" {
		return response, ErrInvalidRequest
	}
	relations, err := selectedRelations(request.Relations)
	if err != nil {
		return response, err
	}
	categoryLimit := request.PerCategoryLimit
	if categoryLimit <= 0 || categoryLimit > service.limits().PerCategory {
		categoryLimit = service.limits().PerCategory
	}
	if request.PerCategoryOffset < 0 {
		return response, ErrInvalidRequest
	}
	var candidates []graphprotocol.Symbol
	err = service.Database.View(ctx, func(session *ladybug.Session) error {
		result, executeErr := session.Execute(ctx, selectSymbols, map[string]any{
			"scope": ready.parameters, "use_uid": request.UID != "", "uids": qualifyUIDs(ready.snapshots, []string{request.UID}),
			"name": request.Name, "path": request.FilePath, "kind": request.Kind, "limit": int64(101),
		}, ladybug.QueryLimits{MaxRows: 101})
		if executeErr != nil {
			return executeErr
		}
		for _, row := range result.Rows {
			candidates = append(candidates, symbolFromRow(row))
		}
		if len(candidates) != 1 {
			return nil
		}
		frontier := qualifyUIDs(ready.snapshots, []string{candidates[0].UID})
		for _, relation := range relations {
			for _, direction := range []string{"incoming", "outgoing"} {
				query := relationQueries[relation].outgoing
				if direction == "incoming" {
					query = relationQueries[relation].incoming
				}
				result, queryErr := session.Execute(ctx, query, map[string]any{
					"scope": ready.parameters, "frontier": frontier,
					"depth": int64(1), "min_confidence": float64(0),
					"offset": int64(request.PerCategoryOffset), "limit": int64(categoryLimit + 1),
				}, ladybug.QueryLimits{MaxRows: categoryLimit + 1})
				if queryErr != nil {
					return queryErr
				}
				symbols := make([]graphprotocol.Symbol, 0, len(result.Rows))
				for _, row := range result.Rows {
					symbols = append(symbols, symbolFromRow(row))
				}
				if len(symbols) > categoryLimit {
					symbols = symbols[:categoryLimit]
					response.Boundaries = appendBoundary(response.Boundaries, "category_limit", 0)
				}
				if direction == "incoming" {
					response.Incoming[relation] = symbols
				} else {
					response.Outgoing[relation] = symbols
				}
			}
		}
		return nil
	})
	if err != nil {
		return response, err
	}
	switch len(candidates) {
	case 0:
	case 1:
		response.Status, response.Symbol = graphprotocol.StatusFound, &candidates[0]
	default:
		response.Status, response.Candidates = graphprotocol.StatusAmbiguous, candidates
	}
	return response, nil
}
