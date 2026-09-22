package authz

import (
	"context"

	"github.com/balcsida/graphnest/internal/authn"
	"github.com/balcsida/graphnest/internal/postgres"
	"github.com/balcsida/graphnest/internal/repository"
	"github.com/jackc/pgx/v5"
)

type Postgres struct{ store *postgres.Store }

func NewPostgres(store *postgres.Store) *Postgres { return &Postgres{store: store} }

func (authorizer *Postgres) AuthorizedRepositories(ctx context.Context, principal authn.Principal, selection RepositorySelection) ([]repository.Repository, error) {
	if principal.DelegationOnly {
		// A delegation-only token mints for repositories; it never reads them.
		return nil, nil
	}
	if principal.Administrator && principal.Method != "api_token" {
		return authorizer.store.AllAuthorizedRepositories(ctx, selection.Names)
	}
	return authorizer.store.AuthorizedRepositories(ctx, principal.InstallationID, principal.RepositoryIDs, selection.Names)
}

func (authorizer *Postgres) AuthorizedRepository(ctx context.Context, principal authn.Principal, repositoryID int64) (repository.Repository, error) {
	if principal.DelegationOnly {
		return repository.Repository{}, pgx.ErrNoRows
	}
	if principal.Administrator && principal.Method != "api_token" {
		return authorizer.store.AnyAuthorizedRepository(ctx, repositoryID)
	}
	return authorizer.store.AuthorizedRepository(ctx, principal.InstallationID, principal.RepositoryIDs, repositoryID)
}

// AllAuthorizedRepositories returns every repository the principal may read,
// without a name selection. It satisfies supplychain.Authorizer.
func (authorizer *Postgres) AllAuthorizedRepositories(ctx context.Context, principal authn.Principal) ([]repository.Repository, error) {
	return authorizer.AuthorizedRepositories(ctx, principal, RepositorySelection{})
}
