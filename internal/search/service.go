package search

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"time"

	"github.com/balcsida/graphnest/internal/authn"
	"github.com/balcsida/graphnest/internal/authz"
	"github.com/balcsida/graphnest/pkg/api"
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
	RepositoryScopes []RepositoryScope
	InstallationID   int64
	Limit            int
	ContextLines     int
	Timeout          time.Duration
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
		return api.SearchResponse{Matches: []api.SearchMatch{}}, nil
	}
	maxResponseBytes := clampInt64(request.MaxResponseBytes, service.limits.MaxResponseBytes)
	backendRequest := BackendRequest{
		Query: request.Query, InstallationID: principal.InstallationID, RepositoryIDs: make([]uint32, len(repositories)), RepositoryScopes: make([]RepositoryScope, len(repositories)),
		Limit:        clamp(request.Limit, service.limits.DefaultResults, service.limits.MaxResults),
		ContextLines: clamp(request.ContextLines, service.limits.DefaultContextLines, service.limits.MaxContextLines),
		Timeout:      clampDuration(request.Timeout, service.limits.DefaultTimeout, service.limits.MaxTimeout),
	}
	metadata := make(map[uint32]api.Repository, len(repositories))
	for index, repository := range repositories {
		backendRequest.RepositoryIDs[index] = repository.ZoektID
		backendRequest.RepositoryScopes[index] = RepositoryScope{ID: repository.ID, GitHubID: repository.GitHubID, InstallationID: repository.InstallationID, Name: repository.Name, IndexedSHA: repository.IndexedSHA}
		publicID := repository.GitHubID
		if publicID == 0 {
			publicID = repository.ID
		}
		metadata[repository.ZoektID] = api.Repository{ID: publicID, Name: repository.Name, Branch: repository.Branch, IndexedSHA: repository.IndexedSHA, WebURL: repository.WebURL}
	}
	response, err := service.backend.Search(ctx, backendRequest)
	if err != nil {
		return api.SearchResponse{}, err
	}
	if response.Consistency != nil && response.Consistency.Backend == "github" {
		response.Matches = enrichGitHub(response.Matches, metadata)
	} else {
		response.Matches = enrich(response.Matches, metadata)
	}
	return limitResponse(response, backendRequest.Limit, maxResponseBytes), nil
}

func enrichGitHub(matches []api.SearchMatch, metadata map[uint32]api.Repository) []api.SearchMatch {
	byID := make(map[int64]api.Repository, len(metadata))
	for _, repository := range metadata {
		byID[repository.ID] = repository
	}
	result := matches[:0]
	for _, match := range matches {
		if repository, ok := byID[match.Repository.ID]; ok {
			match.Repository = repository
			result = append(result, match)
		}
	}
	return result
}

func limitResponse(response api.SearchResponse, maxMatches int, maxBytes int64) api.SearchResponse {
	if len(response.Matches) > maxMatches {
		response.Matches = response.Matches[:maxMatches]
		response.Truncated = true
	}
	matches := response.Matches
	limited := api.SearchResponse{Matches: []api.SearchMatch{}, Truncated: response.Truncated || len(matches) > 0, Consistency: response.Consistency}
	if !fits(limited, maxBytes) {
		// The empty truncated JSON envelope is the response floor even when the caller requests fewer bytes.
		return limited
	}
	// ponytail: O(n²) marshaling is bounded by 100 matches; stream/count once if that cap grows.
	for index, match := range matches {
		candidate := api.SearchResponse{
			Matches:     append(limited.Matches, match),
			Truncated:   response.Truncated || index+1 < len(matches),
			Consistency: response.Consistency,
		}
		if !fits(candidate, maxBytes) {
			limited.Truncated = true
			return limited
		}
		limited = candidate
	}
	return limited
}

func fits(response api.SearchResponse, maxBytes int64) bool {
	data, err := json.Marshal(response)
	return err == nil && int64(len(data)) <= maxBytes
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
		if repository, ok := metadata[match.ZoektID]; ok && repository.IndexedSHA != "" && match.SHA == repository.IndexedSHA && slices.Contains(match.Branches, repository.Branch) {
			match.Repository = repository
			result = append(result, match)
		}
	}
	return result
}
