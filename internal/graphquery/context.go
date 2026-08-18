package graphquery

import (
	"context"

	"github.com/grepnest/grepnest/internal/graphprotocol"
)

const selectSymbols = `UNWIND $scope AS scope MATCH (r:Repository), (s:Symbol) WHERE r.id = scope.id AND r.commit = scope.commit AND s.repository_id = r.id AND ($use_uid = false OR s.uid IN $uids) AND ($name = "" OR s.qualified_name = $name) AND ($path = "" OR s.path = $path) AND ($kind = "" OR s.kind = $kind) RETURN s.repository_id, s.uid, s.qualified_name, s.path, s.language, s.kind, s.signature, s.start_line, s.start_character, s.end_line, s.end_character ORDER BY s.repository_id, s.uid LIMIT $limit`

func (service *Service) Context(ctx context.Context, request graphprotocol.ContextRequest) (graphprotocol.ContextResponse, error) {
	ready, err := service.ready(ctx, request.Scope)
	response := graphprotocol.ContextResponse{
		Status: graphprotocol.StatusNotFound, Incoming: map[string][]graphprotocol.Symbol{},
		Outgoing: map[string][]graphprotocol.Symbol{}, IncomingEdges: map[string][]graphprotocol.Relationship{},
		OutgoingEdges: map[string][]graphprotocol.Relationship{}, Boundaries: ready.boundaries, Commits: ready.commits,
	}
	if err != nil {
		return response, err
	}
	if len(ready.snapshots) == 0 {
		return response, nil
	}
	if len(ready.selectorSnapshots()) == 0 {
		return response, nil
	}
	if request.UID == "" && request.Name == "" || request.PerCategoryLimit < 0 || request.PerCategoryOffset < 0 {
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
	store := service.queryStore()
	candidates, err := store.Symbols(ctx, SymbolQuery{
		Snapshots: ready.selectorSnapshots(), UID: request.UID, Name: request.Name,
		FilePath: request.FilePath, Kind: request.Kind, Limit: 101,
	})
	if err != nil {
		return response, err
	}
	if len(candidates) == 1 {
		frontier := []SymbolRef{{RepositoryID: candidates[0].RepositoryID, UID: candidates[0].UID}}
		for _, relation := range relations {
			for _, direction := range []string{"incoming", "outgoing"} {
				neighbors, queryErr := store.Neighbors(ctx, NeighborQuery{
					Snapshots: ready.snapshots, Frontier: frontier, Relation: relation, Direction: direction,
					Offset: request.PerCategoryOffset, Limit: categoryLimit + 1,
				})
				if queryErr != nil {
					return response, queryErr
				}
				symbols := make([]graphprotocol.Symbol, 0, len(neighbors))
				for _, neighbor := range neighbors {
					symbols = append(symbols, neighbor.Symbol)
					if direction == "incoming" {
						response.IncomingEdges[relation] = append(response.IncomingEdges[relation], neighbor.Edge)
					} else {
						response.OutgoingEdges[relation] = append(response.OutgoingEdges[relation], neighbor.Edge)
					}
				}
				if len(symbols) > categoryLimit {
					symbols = symbols[:categoryLimit]
					if direction == "incoming" {
						response.IncomingEdges[relation] = response.IncomingEdges[relation][:categoryLimit]
					} else {
						response.OutgoingEdges[relation] = response.OutgoingEdges[relation][:categoryLimit]
					}
					response.Boundaries = appendBoundary(response.Boundaries, "category_limit", 0)
				}
				if direction == "incoming" {
					response.Incoming[relation] = symbols
				} else {
					response.Outgoing[relation] = symbols
				}
			}
		}
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
