package authz

import (
	"context"

	"github.com/balcsida/graphnest/internal/authn"
	"github.com/balcsida/graphnest/internal/repository"
)

type RepositorySelection struct{ Names []string }

type Authorizer interface {
	AuthorizedRepositories(context.Context, authn.Principal, RepositorySelection) ([]repository.Repository, error)
}

type Static struct{ registry repository.Registry }

func NewStatic(registry repository.Registry) *Static { return &Static{registry: registry} }

func (authorizer *Static) AuthorizedRepositories(ctx context.Context, principal authn.Principal, selection RepositorySelection) ([]repository.Repository, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	allowed := names(principal.RepositoryNames)
	requested := names(selection.Names)
	allRequested := len(requested) == 0
	var authorized []repository.Repository
	for _, candidate := range authorizer.registry.Repositories() {
		if allowed[candidate.Name] && (allRequested || requested[candidate.Name]) {
			authorized = append(authorized, candidate)
		}
	}
	return authorized, nil
}

func names(values []string) map[string]bool {
	result := make(map[string]bool, len(values))
	for _, value := range values {
		result[value] = true
	}
	return result
}
