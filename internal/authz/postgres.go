package authz

import (
	"context"

	"github.com/balcsida/graphnest/internal/authn"
	"github.com/balcsida/graphnest/internal/postgres"
	"github.com/balcsida/graphnest/internal/repository"
)

type Postgres struct{ store *postgres.Store }

func NewPostgres(store *postgres.Store) *Postgres { return &Postgres{store: store} }

func (authorizer *Postgres) AuthorizedRepositories(ctx context.Context, principal authn.Principal, selection RepositorySelection) ([]repository.Repository, error) {
	if principal.Administrator && principal.Method != "api_token" {
		return authorizer.store.AllAuthorizedRepositories(ctx, selection.Names)
	}
	return authorizer.store.AuthorizedRepositories(ctx, principal.InstallationID, principal.RepositoryIDs, selection.Names)
}

func (authorizer *Postgres) AuthorizedRepository(ctx context.Context, principal authn.Principal, repositoryID int64) (repository.Repository, error) {
	if principal.Administrator && principal.Method != "api_token" {
		return authorizer.store.AnyAuthorizedRepository(ctx, repositoryID)
	}
	return authorizer.store.AuthorizedRepository(ctx, principal.InstallationID, principal.RepositoryIDs, repositoryID)
}
