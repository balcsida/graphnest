package search

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/grepnest/grepnest/internal/authn"
	"github.com/grepnest/grepnest/internal/authz"
	"github.com/grepnest/grepnest/internal/repository"
	"github.com/grepnest/grepnest/pkg/api"
)

func TestSearchPassesOnlyAuthorizedZoektIDs(t *testing.T) {
	backend := &recordingBackend{}
	service := NewService(backend, authorizer(), Limits{MaxResults: 100})
	_, err := service.Search(t.Context(), principalFor("acme/one"), api.SearchRequest{Query: "secret", Repositories: []string{"acme/one", "acme/two"}})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal([]uint32{7}, backend.request.RepositoryIDs) {
		t.Fatalf("RepoIDs = %v", backend.request.RepositoryIDs)
	}
}

func TestSearchSkipsBackendForEmptyAuthorization(t *testing.T) {
	backend := &recordingBackend{}
	service := NewService(backend, authorizer(), Limits{MaxResults: 100})
	_, err := service.Search(t.Context(), principalFor("acme/one"), api.SearchRequest{Query: "secret", Repositories: []string{"acme/two"}})
	if err != nil {
		t.Fatal(err)
	}
	if backend.calls != 0 {
		t.Fatalf("backend calls = %d", backend.calls)
	}
}

func TestSearchClampsRequestLimits(t *testing.T) {
	backend := &recordingBackend{}
	service := NewService(backend, authorizer(), Limits{DefaultResults: 25, MaxResults: 100, DefaultContextLines: 3, MaxContextLines: 20, DefaultTimeout: time.Second, MaxTimeout: 5 * time.Second, MaxResponseBytes: 256 << 10})
	_, err := service.Search(t.Context(), principalFor("acme/one"), api.SearchRequest{Query: "secret", Limit: 999, ContextLines: 999, Timeout: 99 * time.Second, MaxResponseBytes: 999 << 10})
	if err != nil {
		t.Fatal(err)
	}
	if backend.request.Limit != 100 || backend.request.ContextLines != 20 || backend.request.Timeout != 5*time.Second {
		t.Fatalf("request = %#v", backend.request)
	}
}

func TestSearchClampsConfiguredDefaults(t *testing.T) {
	backend := &recordingBackend{}
	service := NewService(backend, authorizer(), Limits{DefaultResults: 999, MaxResults: 100, DefaultContextLines: 999, MaxContextLines: 20, DefaultTimeout: 99 * time.Second, MaxTimeout: 5 * time.Second})
	_, err := service.Search(t.Context(), principalFor("acme/one"), api.SearchRequest{Query: "secret"})
	if err != nil {
		t.Fatal(err)
	}
	if backend.request.Limit != 100 || backend.request.ContextLines != 20 || backend.request.Timeout != 5*time.Second {
		t.Fatalf("request = %#v", backend.request)
	}
}

func TestNewServiceClampsConfiguredMaximaToAbsoluteCaps(t *testing.T) {
	backend := &recordingBackend{response: api.SearchResponse{Matches: []api.SearchMatch{{Preview: strings.Repeat("x", 300<<10), ZoektID: 7}}}}
	service := NewService(backend, authorizer(), Limits{
		MaxResults: 999, MaxContextLines: 999, MaxTimeout: 99 * time.Second, MaxResponseBytes: 999 << 10,
	})
	response, err := service.Search(t.Context(), principalFor("acme/one"), api.SearchRequest{
		Query: "secret", Limit: 999, ContextLines: 999, Timeout: 99 * time.Second, MaxResponseBytes: 999 << 10,
	})
	if err != nil {
		t.Fatal(err)
	}
	if backend.request.Limit != 100 || backend.request.ContextLines != 20 || backend.request.Timeout != 5*time.Second || marshaledSize(t, response) > 256<<10 || !response.Truncated {
		t.Fatalf("request = %#v", backend.request)
	}
}

func TestSearchLimitsEnrichedCanonicalResponse(t *testing.T) {
	backend := &recordingBackend{response: api.SearchResponse{Matches: []api.SearchMatch{
		{Path: "one.go", Preview: "first", ZoektID: 7},
		{Path: "two.go", Preview: "second", ZoektID: 7},
	}}}
	service := NewService(backend, authorizer(), Limits{MaxResults: 100, MaxResponseBytes: 256 << 10})
	want := api.SearchResponse{Matches: []api.SearchMatch{{
		Repository: api.Repository{ID: 1, Name: "acme/one"}, Path: "one.go", Preview: "first", ZoektID: 7,
	}}, Truncated: true}
	budget := marshaledSize(t, want)

	got, err := service.Search(t.Context(), principalFor("acme/one"), api.SearchRequest{Query: "secret", MaxResponseBytes: int64(budget)})
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Matches) != 1 || !got.Truncated || marshaledSize(t, got) > budget {
		t.Fatalf("response = %#v, size = %d, budget = %d", got, marshaledSize(t, got), budget)
	}
}

func TestSearchPreservesBackendTruncation(t *testing.T) {
	backend := &recordingBackend{response: api.SearchResponse{Truncated: true}}
	service := NewService(backend, authorizer(), Limits{MaxResults: 100, MaxResponseBytes: 1024})
	got, err := service.Search(t.Context(), principalFor("acme/one"), api.SearchRequest{Query: "secret"})
	if err != nil {
		t.Fatal(err)
	}
	if !got.Truncated {
		t.Fatal("backend truncation was lost")
	}
}

func TestSearchReturnsEmptyTruncatedEnvelopeBelowFloor(t *testing.T) {
	backend := &recordingBackend{response: api.SearchResponse{Matches: []api.SearchMatch{{Path: "one.go", ZoektID: 7}}}}
	service := NewService(backend, authorizer(), Limits{MaxResults: 100, MaxResponseBytes: 1024})
	got, err := service.Search(t.Context(), principalFor("acme/one"), api.SearchRequest{Query: "secret", MaxResponseBytes: 1})
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Matches) != 0 || !got.Truncated || marshaledSize(t, got) <= 1 {
		t.Fatalf("response = %#v, size = %d", got, marshaledSize(t, got))
	}
}

func marshaledSize(t *testing.T, value any) int {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return len(data)
}

type recordingBackend struct {
	calls    int
	request  BackendRequest
	response api.SearchResponse
}

func (backend *recordingBackend) Search(_ context.Context, request BackendRequest) (api.SearchResponse, error) {
	backend.calls++
	backend.request = request
	return backend.response, nil
}

func (*recordingBackend) Health(context.Context) error { return nil }

func authorizer() authz.Authorizer {
	registry, err := repository.NewStatic([]repository.Repository{{ID: 1, ZoektID: 7, Name: "acme/one"}, {ID: 2, ZoektID: 8, Name: "acme/two"}})
	if err != nil {
		panic(err)
	}
	return authz.NewStatic(registry)
}

func principalFor(name string) authn.Principal {
	return authn.Principal{Subject: "user", RepositoryNames: []string{name}}
}
