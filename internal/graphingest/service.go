package graphingest

import (
	"context"
	"errors"

	"github.com/grepnest/grepnest/internal/authn"
	"github.com/grepnest/grepnest/internal/graphartifact"
	"github.com/grepnest/grepnest/internal/postgres"
	"github.com/grepnest/grepnest/internal/repository"
	"github.com/grepnest/grepnest/pkg/api"
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
	if err := service.ValidateExternalUpload(ctx, principal, repositoryID, commit); err != nil {
		return api.GraphStatus{}, err
	}
	artifact, err := graphartifact.Parse(data, service.Limits)
	if err != nil || artifact.RepositoryID != repositoryID || artifact.Commit != commit {
		return api.GraphStatus{}, ErrInvalidArtifact
	}
	if err := service.ValidateExternalUpload(ctx, principal, repositoryID, commit); err != nil {
		return api.GraphStatus{}, err
	}
	if _, err := service.Store.ReplaceGraph(ctx, repositoryID, postgres.GraphSourceExternal, artifact); err != nil {
		return api.GraphStatus{}, unavailable(err)
	}
	return service.Status(ctx, principal, repositoryID)
}

func (service *Service) Status(ctx context.Context, principal authn.Principal, repositoryID int64) (api.GraphStatus, error) {
	repository, err := service.Store.AuthorizedRepository(ctx, principal.InstallationID, principal.RepositoryIDs, repositoryID)
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
	repository, err := service.Store.AuthorizedRepository(ctx, principal.InstallationID, principal.RepositoryIDs, repositoryID)
	if err != nil {
		return repository, err
	}
	if repository.IndexedSHA == "" || repository.IndexedSHA != commit {
		return repository, ErrNotIndexed
	}
	return repository, nil
}

func unavailable(error) error { return ErrUnavailable }
