package graphingest

import (
	"context"
	"encoding/hex"
	"errors"
	"strconv"

	"github.com/balcsida/graphnest/internal/authn"
	"github.com/balcsida/graphnest/internal/graphartifact"
	graphv2 "github.com/balcsida/graphnest/internal/graphartifact/v2"
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
	// ErrConflict means the active generation or indexed commit no longer
	// matches the publisher's preflight; ErrProducerConflict means the active
	// generation came from another producer and replacement was not requested.
	ErrConflict         = errors.New("conflict")
	ErrProducerConflict = errors.New("producer_conflict")
	ErrUnauthenticated  = errors.New("unauthenticated")
)

type Store interface {
	AuthorizedRepository(context.Context, int64, []int64, int64) (repository.Repository, error)
	ReplaceGraph(context.Context, int64, postgres.GraphSource, graphartifact.Artifact) (postgres.GraphReplacement, error)
	GraphStatus(context.Context, int64) (api.GraphStatus, error)
	ReplaceGraphV2(context.Context, int64, postgres.GraphPublication, *graphv2.Artifact) (postgres.GraphReplacement, error)
	ActiveGraphGeneration(context.Context, int64) (*api.GraphActiveGeneration, error)
	GraphPublicationAllowed(context.Context, int64, string) (bool, error)
}

type Service struct {
	Store  Store
	Limits graphartifact.Limits
	// MaxUploadBytes is reported to publishers; the transport enforces it.
	MaxUploadBytes int64
}

// Publication is a v2 publisher's replacement intent from its preflight.
// ExpectedGeneration 0 means no generation was active.
type Publication struct {
	ExpectedGeneration int64
	ReplaceProducer    bool
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

// ValidatePublication checks, before the body is read, that the principal may
// publish a v2 generation for the repository's indexed commit.
func (service *Service) ValidatePublication(ctx context.Context, principal authn.Principal, repositoryID int64, commit string) error {
	_, err := service.authorizePublication(ctx, principal, repositoryID, commit)
	return err
}

// Publish activates a v2 artifact. Administrators need no grant; everyone else
// needs a repository publication grant on top of read access. The credential,
// grant and indexed commit are rechecked after parsing, and storage compares
// the expected generation under the repository lock.
func (service *Service) Publish(ctx context.Context, principal authn.Principal, repositoryID int64, commit string, data []byte, intent Publication) (api.GraphPublicationResult, error) {
	if _, err := service.authorizePublication(ctx, principal, repositoryID, commit); err != nil {
		return api.GraphPublicationResult{}, err
	}
	artifact, err := graphartifact.ParseV2(data, service.Limits)
	if err != nil || artifact.Repository != strconv.FormatInt(repositoryID, 10) || artifact.Commit != commit {
		return api.GraphPublicationResult{}, ErrInvalidArtifact
	}
	if len(artifact.ContentHash) == 0 {
		if artifact.ContentHash, err = graphartifact.SemanticHashV2(artifact, service.Limits); err != nil {
			return api.GraphPublicationResult{}, ErrInvalidArtifact
		}
	}
	// ponytail: authority is final as of this recheck, not the commit; a grant revoked during the copy still lands. Move the grant check into the replacement transaction if that window matters.
	if principal, err = authn.FreshPrincipal(ctx, principal); err != nil {
		return api.GraphPublicationResult{}, ErrUnauthenticated
	}
	repository, err := service.authorizePublication(ctx, principal, repositoryID, commit)
	if err != nil {
		return api.GraphPublicationResult{}, err
	}
	replacement, err := service.Store.ReplaceGraphV2(ctx, repository.ID, postgres.GraphPublication{
		Publisher: principal.Method + ":" + principal.Subject, ExpectedActiveID: intent.ExpectedGeneration, AllowProviderChange: intent.ReplaceProducer,
	}, artifact)
	switch {
	case errors.Is(err, postgres.ErrGraphPrecondition):
		return api.GraphPublicationResult{}, ErrConflict
	case errors.Is(err, postgres.ErrGraphProviderConflict):
		return api.GraphPublicationResult{}, ErrProducerConflict
	case errors.Is(err, graphartifact.ErrInvalidArtifact):
		return api.GraphPublicationResult{}, ErrInvalidArtifact
	case errors.Is(err, pgx.ErrNoRows), errors.Is(err, postgres.ErrGraphRepositoryUnavailable):
		return api.GraphPublicationResult{}, ErrNotIndexed
	case err != nil:
		return api.GraphPublicationResult{}, unavailable(err)
	}
	return api.GraphPublicationResult{
		RepositoryID: repositoryID, Commit: commit, Generation: replacement.Upload.ID, ReplacedGeneration: replacement.ReplacedID,
		ContentHash: hex.EncodeToString(artifact.ContentHash), Deduplicated: replacement.Deduplicated,
	}, nil
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
	active, err := service.Store.ActiveGraphGeneration(ctx, repository.ID)
	if err != nil {
		return api.GraphStatus{}, unavailable(err)
	}
	permitted, err := service.mayPublish(ctx, principal, repository)
	if err != nil {
		return api.GraphStatus{}, err
	}
	status.Publication = &api.GraphPublication{UploadArtifactVersions: []int{1, 2}, MaxUploadBytes: service.MaxUploadBytes, Permitted: permitted, ActiveGeneration: active}
	return status, nil
}

func (service *Service) authorizePublication(ctx context.Context, principal authn.Principal, repositoryID int64, commit string) (repository.Repository, error) {
	repository, err := service.validate(ctx, principal, repositoryID, commit)
	if err != nil {
		return repository, err
	}
	permitted, err := service.mayPublish(ctx, principal, repository)
	if err == nil && !permitted {
		err = ErrForbidden
	}
	return repository, err
}

func (service *Service) mayPublish(ctx context.Context, principal authn.Principal, repository repository.Repository) (bool, error) {
	if principal.Subject == "" {
		return false, nil
	}
	if principal.Administrator {
		return true, nil
	}
	allowed, err := service.Store.GraphPublicationAllowed(ctx, repository.ID, principal.Subject)
	if err != nil {
		return false, unavailable(err)
	}
	return allowed, nil
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
