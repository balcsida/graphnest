package search

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/grepnest/grepnest/internal/authn"
	"github.com/grepnest/grepnest/internal/authz"
	"github.com/grepnest/grepnest/pkg/api"
)

var ErrInvalidQuery = errors.New("invalid query")

type Limits struct {
	DefaultResults, MaxResults           int
	DefaultContextLines, MaxContextLines int
	DefaultTimeout, MaxTimeout           time.Duration
	MaxResponseBytes                     int64
}

type BackendRequest struct {
	Query            string
	RepositoryIDs    []uint32
	Limit            int
	ContextLines     int
	Timeout          time.Duration
	MaxResponseBytes int64
}

type SearchBackend interface {
	Search(context.Context, BackendRequest) (api.SearchResponse, error)
	Health(context.Context) error
}

type Service struct {
	backend    SearchBackend
	authorizer authz.Authorizer
	limits     Limits
}

func NewService(backend SearchBackend, authorizer authz.Authorizer, limits Limits) *Service {
	return &Service{backend: backend, authorizer: authorizer, limits: defaults(limits)}
}

func (service *Service) Search(ctx context.Context, principal authn.Principal, request api.SearchRequest) (api.SearchResponse, error) {
	if strings.TrimSpace(request.Query) == "" {
		return api.SearchResponse{}, ErrInvalidQuery
	}
	repositories, err := service.authorizer.AuthorizedRepositories(ctx, principal, authz.RepositorySelection{Names: request.Repositories})
	if err != nil {
		return api.SearchResponse{}, err
	}
	if len(repositories) == 0 {
		return api.SearchResponse{}, nil
	}
	backendRequest := BackendRequest{
		Query: request.Query, RepositoryIDs: make([]uint32, len(repositories)),
		Limit:            clamp(request.Limit, service.limits.DefaultResults, service.limits.MaxResults),
		ContextLines:     clamp(request.ContextLines, service.limits.DefaultContextLines, service.limits.MaxContextLines),
		Timeout:          clampDuration(request.Timeout, service.limits.DefaultTimeout, service.limits.MaxTimeout),
		MaxResponseBytes: clampInt64(request.MaxResponseBytes, service.limits.MaxResponseBytes),
	}
	metadata := make(map[uint32]api.Repository, len(repositories))
	for index, repository := range repositories {
		backendRequest.RepositoryIDs[index] = repository.ZoektID
		metadata[repository.ZoektID] = api.Repository{ID: repository.ID, Name: repository.Name, Branch: repository.Branch, IndexedSHA: repository.IndexedSHA, WebURL: repository.WebURL}
	}
	response, err := service.backend.Search(ctx, backendRequest)
	if err != nil {
		return api.SearchResponse{}, err
	}
	response.Matches = enrich(response.Matches, metadata)
	return response, nil
}

func defaults(limits Limits) Limits {
	if limits.DefaultResults <= 0 {
		limits.DefaultResults = 25
	}
	if limits.MaxResults <= 0 || limits.MaxResults > 100 {
		limits.MaxResults = 100
	}
	if limits.DefaultContextLines <= 0 {
		limits.DefaultContextLines = 3
	}
	if limits.MaxContextLines <= 0 || limits.MaxContextLines > 20 {
		limits.MaxContextLines = 20
	}
	if limits.DefaultTimeout <= 0 {
		limits.DefaultTimeout = 5 * time.Second
	}
	if limits.MaxTimeout <= 0 || limits.MaxTimeout > 5*time.Second {
		limits.MaxTimeout = 5 * time.Second
	}
	if limits.MaxResponseBytes <= 0 || limits.MaxResponseBytes > 256<<10 {
		limits.MaxResponseBytes = 256 << 10
	}
	limits.DefaultResults = clamp(limits.DefaultResults, limits.MaxResults, limits.MaxResults)
	limits.DefaultContextLines = clamp(limits.DefaultContextLines, limits.MaxContextLines, limits.MaxContextLines)
	limits.DefaultTimeout = clampDuration(limits.DefaultTimeout, limits.MaxTimeout, limits.MaxTimeout)
	return limits
}

func clamp(value, fallback, maximum int) int {
	if value <= 0 {
		return fallback
	}
	if value > maximum {
		return maximum
	}
	return value
}
func clampDuration(value, fallback, maximum time.Duration) time.Duration {
	if value <= 0 {
		return fallback
	}
	if value > maximum {
		return maximum
	}
	return value
}
func clampInt64(value, maximum int64) int64 {
	if value <= 0 || value > maximum {
		return maximum
	}
	return value
}

func enrich(matches []api.SearchMatch, metadata map[uint32]api.Repository) []api.SearchMatch {
	result := matches[:0]
	for _, match := range matches {
		if repository, ok := metadata[match.ZoektID]; ok {
			match.Repository = repository
			result = append(result, match)
		}
	}
	return result
}
