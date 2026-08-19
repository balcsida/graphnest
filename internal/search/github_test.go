package search

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/grepnest/grepnest/pkg/api"
)

func TestGitHubBackendChunksAuthorizedRepositoryQualifiers(t *testing.T) {
	client := &githubSearchClient{responses: []api.SearchResponse{
		{Matches: []api.SearchMatch{{Path: "a.go", SHA: "one", Repository: api.Repository{ID: 1, Name: "acme/one"}}}},
		{Matches: []api.SearchMatch{{Path: "b.go", SHA: "two", Repository: api.Repository{ID: 2, Name: "acme/two"}}}},
	}}
	backend := NewGitHubBackend(client, 1)
	got, err := backend.Search(t.Context(), BackendRequest{Query: "needle", InstallationID: 10, RepositoryScopes: []RepositoryScope{
		{ID: 1, GitHubID: 101, Name: "acme/one"}, {ID: 2, GitHubID: 102, Name: "acme/two"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(client.queries, []string{"needle repo:acme/one", "needle repo:acme/two"}) {
		t.Fatalf("queries = %q", client.queries)
	}
	if len(got.Matches) != 2 || got.Consistency == nil || got.Consistency.Backend != "github" || got.Consistency.Exact || got.Consistency.Revision != "" || !got.Consistency.Partial {
		t.Fatalf("response = %#v", got)
	}
}

func TestGitHubBackendDeduplicatesDeterministicallyAndPreservesPartial(t *testing.T) {
	client := &githubSearchClient{responses: []api.SearchResponse{
		{Matches: []api.SearchMatch{{Path: "b.go", SHA: "two", Repository: api.Repository{ID: 2, Name: "acme/two"}}, {Path: "a.go", SHA: "one", Repository: api.Repository{ID: 1, Name: "acme/one"}}}},
		{Matches: []api.SearchMatch{{Path: "a.go", SHA: "one", Repository: api.Repository{ID: 1, Name: "acme/one"}}}, Truncated: true},
	}}
	backend := NewGitHubBackend(client, 1)
	got, err := backend.Search(t.Context(), BackendRequest{Query: "needle", InstallationID: 10, RepositoryScopes: []RepositoryScope{{Name: "acme/one"}, {Name: "acme/two"}}})
	if err != nil {
		t.Fatal(err)
	}
	if got.Truncated != true || len(got.Matches) != 2 || got.Matches[0].Path != "a.go" || got.Matches[1].Path != "b.go" {
		t.Fatalf("response = %#v", got)
	}
}

func TestGitHubBackendStopsOnRateLimit(t *testing.T) {
	client := &githubSearchClient{err: ErrRateLimited}
	backend := NewGitHubBackend(client, 1)
	_, err := backend.Search(t.Context(), BackendRequest{Query: "needle", InstallationID: 10, RepositoryScopes: []RepositoryScope{{Name: "acme/one"}}})
	if !errors.Is(err, ErrRateLimited) {
		t.Fatalf("error = %v", err)
	}
}

type githubSearchClient struct {
	queries   []string
	responses []api.SearchResponse
	err       error
}

func (client *githubSearchClient) SearchCode(_ context.Context, _ int64, query string, _ []int64) (api.SearchResponse, error) {
	client.queries = append(client.queries, query)
	if client.err != nil {
		return api.SearchResponse{}, client.err
	}
	response := client.responses[0]
	client.responses = client.responses[1:]
	return response, nil
}
