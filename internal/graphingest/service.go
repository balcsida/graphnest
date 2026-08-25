package graphingest

import (
	"context"
	"errors"

	"github.com/balcsida/graphnest/internal/authn"
	"github.com/balcsida/graphnest/internal/graphartifact"
	"github.com/balcsida/graphnest/internal/postgres"
	"github.com/balcsida/graphnest/internal/repository"
	"github.com/balcsida/graphnest/pkg/api"
	"github.com/jackc/pgx/v5"
)

var (
	ErrForbidden       = errors.New("forbidden")
	ErrNotIndexed      = errors.New("not_indexed")
	ErrInvalidArtifact = errors.New("invalid_artifact")
	ErrUnavailable     = errors.New("unavailable")
)

type Store interface {
	AuthorizedRepository(context.Context, int64, []int64, int64) (repository.Repository, error)
	ReplaceGraph(context.Context, int64, postgres.GraphSource, graphartifact.Artifact) (postgres.GraphReplacement, error)
	GraphStatus(context.Context, int64) (api.GraphStatus, error)
}

type Service struct {
	Store  Store
	Limits graphartifact.Limits
}

func (service *Service) ValidateExternalUpload(ctx context.Context, principal authn.Principal, repositoryID int64, commit string) error {
	if !principal.Administrator {
		return ErrForbidden
	}
	_, err := service.validate(ctx, principal, repositoryID, commit)
	return err
}

func (service *Service) UploadExternal(ctx context.Context, principal authn.Principal, repositoryID int64, commit string, data []byte) (api.GraphStatus, error) {
	if !principal.Administrator {
		return api.GraphStatus{}, ErrForbidden
	}
	repository, err := service.validate(ctx, principal, repositoryID, commit)
	if err != nil {
		return api.GraphStatus{}, err
	}
	artifact, err := graphartifact.Parse(data, service.Limits)
	if err != nil || artifact.RepositoryID != repositoryID || artifact.Commit != commit {
		return api.GraphStatus{}, ErrInvalidArtifact
	}
	repository, err = service.validate(ctx, principal, repositoryID, commit)
	if err != nil {
		return api.GraphStatus{}, err
	}
	artifact.RepositoryID = repository.ID
	replacement, err := service.Store.ReplaceGraph(ctx, repository.ID, postgres.GraphSourceExternal, artifact)
	if err != nil {
		return api.GraphStatus{}, unavailable(err)
	}
	if !replacement.Applied {
		return api.GraphStatus{}, ErrNotIndexed
	}
	return api.GraphStatus{RepositoryID: repositoryID, Commit: commit, State: api.GraphStateReady, Source: api.GraphSourceExternal}, nil
}

func (service *Service) Status(ctx context.Context, principal authn.Principal, repositoryID int64) (api.GraphStatus, error) {
	repository, err := service.authorizedRepository(ctx, principal, repositoryID)
	if err != nil {
		return api.GraphStatus{}, err
	}
	status, err := service.Store.GraphStatus(ctx, repository.ID)
	if err != nil {
		return api.GraphStatus{}, unavailable(err)
	}
	return status, nil
}

func (service *Service) validate(ctx context.Context, principal authn.Principal, repositoryID int64, commit string) (repository.Repository, error) {
	repository, err := service.authorizedRepository(ctx, principal, repositoryID)
	if err != nil {
		return repository, err
	}
	if repository.IndexedSHA == "" || repository.IndexedSHA != commit {
		return repository, ErrNotIndexed
	}
	return repository, nil
}

func (service *Service) authorizedRepository(ctx context.Context, principal authn.Principal, repositoryID int64) (repository.Repository, error) {
	repository, err := service.Store.AuthorizedRepository(ctx, principal.InstallationID, principal.RepositoryIDs, repositoryID)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return repository, unavailable(err)
	}
	return repository, err
}

func unavailable(_ error) error { return ErrUnavailable }
