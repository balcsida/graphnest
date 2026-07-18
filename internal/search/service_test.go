package search

import (
	"context"
	"slices"
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
	if backend.request.Limit != 100 || backend.request.ContextLines != 20 || backend.request.Timeout != 5*time.Second || backend.request.MaxResponseBytes != 256<<10 {
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
	backend := &recordingBackend{}
	service := NewService(backend, authorizer(), Limits{
		MaxResults: 999, MaxContextLines: 999, MaxTimeout: 99 * time.Second, MaxResponseBytes: 999 << 10,
	})
	_, err := service.Search(t.Context(), principalFor("acme/one"), api.SearchRequest{
		Query: "secret", Limit: 999, ContextLines: 999, Timeout: 99 * time.Second, MaxResponseBytes: 999 << 10,
	})
	if err != nil {
		t.Fatal(err)
	}
	if backend.request.Limit != 100 || backend.request.ContextLines != 20 || backend.request.Timeout != 5*time.Second || backend.request.MaxResponseBytes != 256<<10 {
		t.Fatalf("request = %#v", backend.request)
	}
}

type recordingBackend struct {
	calls   int
	request BackendRequest
}

func (backend *recordingBackend) Search(_ context.Context, request BackendRequest) (api.SearchResponse, error) {
	backend.calls++
	backend.request = request
	return api.SearchResponse{}, nil
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
