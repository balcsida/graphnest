package search

import (
	"context"
	"errors"
	"slices"
	"sort"
	"strings"

	"github.com/grepnest/grepnest/pkg/api"
)

var ErrRateLimited = errors.New("GitHub search rate limited")

// GitHubSearchClient is deliberately limited to installation-scoped search.
type GitHubSearchClient interface {
	SearchCode(context.Context, int64, string, []int64) (api.SearchResponse, error)
}

type GitHubBackend struct {
	client GitHubSearchClient
	chunk  int
}

func NewGitHubBackend(client GitHubSearchClient, chunk int) *GitHubBackend {
	if chunk < 1 {
		chunk = 1
	}
	return &GitHubBackend{client: client, chunk: chunk}
}

func (backend *GitHubBackend) Health(context.Context) error { return nil }

func (backend *GitHubBackend) Search(ctx context.Context, request BackendRequest) (api.SearchResponse, error) {
	response := api.SearchResponse{Matches: []api.SearchMatch{}, Consistency: &api.SearchConsistency{Backend: "github", Partial: true}}
	for start := 0; start < len(request.RepositoryScopes); start += backend.chunk {
		end := min(start+backend.chunk, len(request.RepositoryScopes))
		scopes := request.RepositoryScopes[start:end]
		qualifiers, ids := make([]string, 0, len(scopes)), make([]int64, 0, len(scopes))
		for _, scope := range scopes {
			qualifiers = append(qualifiers, "repo:"+scope.Name)
			if scope.GitHubID != 0 {
				ids = append(ids, scope.GitHubID)
			}
		}
		got, err := backend.client.SearchCode(ctx, request.InstallationID, request.Query+" "+strings.Join(qualifiers, " "), ids)
		if err != nil {
			return api.SearchResponse{}, err
		}
		response.Matches = append(response.Matches, got.Matches...)
		response.Truncated = response.Truncated || got.Truncated || got.Consistency != nil && got.Consistency.Partial
	}
	sort.SliceStable(response.Matches, func(i, j int) bool {
		left, right := response.Matches[i], response.Matches[j]
		if left.Repository.Name != right.Repository.Name {
			return left.Repository.Name < right.Repository.Name
		}
		if left.Path != right.Path {
			return left.Path < right.Path
		}
		return left.SHA < right.SHA
	})
	response.Matches = slices.CompactFunc(response.Matches, func(left, right api.SearchMatch) bool {
		return left.Repository.ID == right.Repository.ID && left.Path == right.Path && left.SHA == right.SHA
	})
	return response, nil
}
