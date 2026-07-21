package scipgraph

import (
	"context"
	"errors"
	"path"
	"strings"

	"github.com/grepnest/grepnest/internal/authn"
	"github.com/grepnest/grepnest/internal/repository"
	"github.com/grepnest/grepnest/pkg/api"
)

const defaultMaxResults = 100

var (
	ErrForbidden      = errors.New("forbidden")
	ErrInvalidRequest = errors.New("invalid_request")
	ErrNotIndexed     = errors.New("not_indexed")
)

type ServiceStore interface {
	AuthorizedRepository(context.Context, int64, []int64, int64) (repository.Repository, error)
	ReplaceSCIP(context.Context, int64, string, Upload) error
	OccurrenceAt(context.Context, int64, string, string, int, int) (StoredOccurrence, error)
	Locations(context.Context, authn.Principal, StoredOccurrence, string, int) ([]Location, bool, error)
	ReplacePackages(context.Context, int64, string, []PackageMapping) error
}

type Service struct {
	Store      ServiceStore
	MaxResults int
}

func (service *Service) Upload(ctx context.Context, principal authn.Principal, repositoryID int64, commit string, data []byte) error {
	if !principal.Administrator {
		return ErrForbidden
	}
	repository, err := service.authorizedRepository(ctx, principal, repositoryID)
	if err != nil {
		return err
	}
	if repository.IndexedSHA == "" || commit != repository.IndexedSHA {
		return ErrNotIndexed
	}
	upload, err := Parse(data)
	if err != nil {
		return err
	}
	return service.Store.ReplaceSCIP(ctx, repository.ID, commit, upload)
}

func (service *Service) Navigate(ctx context.Context, principal authn.Principal, request api.SCIPNavigationRequest) (api.SCIPNavigationResponse, error) {
	if !validNavigationRequest(request) {
		return api.SCIPNavigationResponse{}, ErrInvalidRequest
	}
	repository, err := service.authorizedRepository(ctx, principal, request.RepositoryID)
	if err != nil {
		return api.SCIPNavigationResponse{}, err
	}
	if repository.IndexedSHA == "" {
		return api.SCIPNavigationResponse{}, ErrNotIndexed
	}
	origin, err := service.Store.OccurrenceAt(ctx, repository.ID, repository.IndexedSHA, request.Path, request.Line-1, request.Character)
	if err != nil {
		return api.SCIPNavigationResponse{}, ErrNotIndexed
	}
	locations, truncated, err := service.Store.Locations(ctx, principal, origin, request.Operation, service.maxResults())
	if err != nil {
		return api.SCIPNavigationResponse{}, err
	}
	response := api.SCIPNavigationResponse{Locations: make([]api.SCIPLocation, 0, len(locations)), Truncated: truncated}
	for _, location := range locations {
		target, err := service.authorizedRepository(ctx, principal, location.RepositoryID)
		if err != nil || target.IndexedSHA == "" || target.IndexedSHA != location.Commit {
			continue
		}
		response.Locations = append(response.Locations, api.SCIPLocation{
			RepositoryID: location.RepositoryID, RepositoryName: location.RepositoryName,
			Commit: location.Commit, Path: location.Path, Symbol: location.Symbol,
			StartLine: int(location.StartLine) + 1, StartCharacter: int(location.StartCharacter),
			EndLine: int(location.EndLine) + 1, EndCharacter: int(location.EndCharacter),
			Roles: location.Roles, Approximate: location.Approximate,
		})
	}
	return response, nil
}

func (service *Service) SetDependencies(ctx context.Context, principal authn.Principal, repositoryID int64, purls api.RepositoryPackages) error {
	if !principal.Administrator {
		return ErrForbidden
	}
	repository, err := service.authorizedRepository(ctx, principal, repositoryID)
	if err != nil {
		return err
	}
	mappings := make([]PackageMapping, 0, len(purls.Provides)+len(purls.DependsOn))
	for _, group := range []struct {
		values   []string
		relation string
	}{{purls.Provides, "provides"}, {purls.DependsOn, "depends_on"}} {
		for _, purl := range group.values {
			pkg, err := ParsePackageURL(purl)
			if err != nil {
				return ErrInvalidRequest
			}
			mappings = append(mappings, PackageMapping{Package: pkg, Relation: group.relation, Source: "manual"})
		}
	}
	return service.Store.ReplacePackages(ctx, repository.ID, "manual", mappings)
}

func (service *Service) authorizedRepository(ctx context.Context, principal authn.Principal, repositoryID int64) (repository.Repository, error) {
	return service.Store.AuthorizedRepository(ctx, principal.InstallationID, principal.RepositoryIDs, repositoryID)
}

func (service *Service) maxResults() int {
	if service.MaxResults <= 0 {
		return defaultMaxResults
	}
	return service.MaxResults
}

func validNavigationRequest(request api.SCIPNavigationRequest) bool {
	if request.RepositoryID <= 0 || request.Line < 1 || request.Character < 0 {
		return false
	}
	if request.Operation != "definitions" && request.Operation != "references" && request.Operation != "implementations" {
		return false
	}
	clean := path.Clean(request.Path)
	return request.Path != "" && clean == request.Path && clean != "." && clean != ".." &&
		!path.IsAbs(request.Path) && !strings.HasPrefix(clean, "../") && !strings.ContainsAny(request.Path, "\\\x00")
}
