package search

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

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
	client := &githubSearchClient{err: rateLimitError{status: 429}}
	backend := NewGitHubBackend(client, 1)
	got, err := backend.Search(t.Context(), BackendRequest{Query: "needle", InstallationID: 10, RepositoryScopes: []RepositoryScope{{Name: "acme/one"}}})
	if err != nil || !got.Truncated || got.Consistency == nil || !got.Consistency.Partial {
		t.Fatalf("response=%#v error=%v", got, err)
	}
}

func TestGitHubBackendUsesScopeInstallationAndRequestTimeout(t *testing.T) {
	client := &githubSearchClient{wait: true}
	backend := NewGitHubBackend(client, 1)
	_, err := backend.Search(t.Context(), BackendRequest{Query: "needle", Timeout: time.Millisecond, RepositoryScopes: []RepositoryScope{{InstallationID: 10, GitHubID: 101, Name: "acme/one"}, {InstallationID: 20, GitHubID: 201, Name: "acme/two"}}})
	if !errors.Is(err, context.DeadlineExceeded) || !reflect.DeepEqual(client.installations, []int64{10}) || !reflect.DeepEqual(client.repositoryIDs, [][]int64{{101}}) {
		t.Fatalf("error=%v installations=%v repositoryIDs=%v", err, client.installations, client.repositoryIDs)
	}
}

type githubSearchClient struct {
	queries       []string
	installations []int64
	repositoryIDs [][]int64
	responses     []api.SearchResponse
	err           error
	wait          bool
}

func (client *githubSearchClient) SearchCode(ctx context.Context, installationID int64, query string, repositoryIDs []int64) (api.SearchResponse, error) {
	client.queries = append(client.queries, query)
	client.installations = append(client.installations, installationID)
	client.repositoryIDs = append(client.repositoryIDs, append([]int64(nil), repositoryIDs...))
	if client.wait {
		<-ctx.Done()
		return api.SearchResponse{}, ctx.Err()
	}
	if client.err != nil {
		return api.SearchResponse{}, client.err
	}
	response := client.responses[0]
	client.responses = client.responses[1:]
	return response, nil
}

type rateLimitError struct{ status int }

func (err rateLimitError) Error() string       { return "rate limited" }
func (err rateLimitError) HTTPStatus() int     { return err.status }
func (err rateLimitError) IsRateLimited() bool { return true }
