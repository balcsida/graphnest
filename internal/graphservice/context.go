package graphservice

import (
	"context"

	"github.com/grepnest/grepnest/internal/authn"
	"github.com/grepnest/grepnest/internal/graphprotocol"
	"github.com/grepnest/grepnest/pkg/api"
)

func (s *Service) Context(ctx context.Context, principal authn.Principal, request api.GraphContextRequest) (api.GraphContextResponse, error) {
	if (request.UID == "" && request.Name == "") || (request.UID != "" && request.Name != "") || request.PerCategoryLimit < 0 || request.PerCategoryOffset < 0 || !validRelations(request.Relations) {
		return api.GraphContextResponse{}, ErrInvalidRequest
	}
	selected, scope, snapshots, err := s.scope(ctx, principal, request.Repo, request.Branch)
	if err != nil {
		return api.GraphContextResponse{}, err
	}
	limit := request.PerCategoryLimit
	if limit <= 0 || limit > s.limits().PerCategory {
		limit = s.limits().PerCategory
	}
	response, err := s.Backend.Context(ctx, graphprotocol.ContextRequest{Scope: scope, UID: request.UID, Name: request.Name, FilePath: request.FilePath, Kind: request.Kind, Relations: request.Relations, PerCategoryLimit: limit, PerCategoryOffset: request.PerCategoryOffset})
	if err != nil {
		return api.GraphContextResponse{}, err
	}
	if err := s.reauthorize(ctx, principal, selected, response.Commits); err != nil {
		return api.GraphContextResponse{}, err
	}
	result := api.GraphContextResponse{Status: response.Status, Incoming: map[string][]api.GraphReference{}, Outgoing: map[string][]api.GraphReference{}, Boundaries: boundaries(response.Boundaries), Commits: response.Commits}
	if response.Symbol != nil {
		value := symbol(*response.Symbol)
		if request.IncludeContent {
			if snapshot, ok := snapshots[value.RepositoryID]; ok && s.Files != nil {
				file, readErr := s.Files.ReadFileAt(ctx, principal, api.ReadFileRequest{RepositoryID: snapshot.GitHubID, Path: value.FilePath, StartLine: value.Range.StartLine + 1, EndLine: value.Range.EndLine + 1}, snapshot.Commit)
				if readErr != nil {
					return api.GraphContextResponse{}, readErr
				}
				value.Content = file.Content
			}
		}
		result.Symbol = &value
	}
	for _, value := range response.Candidates {
		result.Candidates = append(result.Candidates, candidate(value))
	}
	for relation, values := range response.IncomingEdges {
		for _, value := range values {
			result.Incoming[relation] = append(result.Incoming[relation], reference(value))
		}
	}
	for relation, values := range response.OutgoingEdges {
		for _, value := range values {
			result.Outgoing[relation] = append(result.Outgoing[relation], reference(value))
		}
	}
	return result, nil
}
