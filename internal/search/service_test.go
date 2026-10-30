package search

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/balcsida/graphnest/internal/authn"
	"github.com/balcsida/graphnest/internal/authz"
	"github.com/balcsida/graphnest/internal/repository"
	"github.com/balcsida/graphnest/pkg/api"
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

func TestSearchSuppressesMismatchedIndexedRevision(t *testing.T) {
	backend := &recordingBackend{response: api.SearchResponse{Matches: []api.SearchMatch{{ZoektID: 7, SHA: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", Branches: []string{"main"}}}}}
	service := NewService(backend, authorizerWith(repository.Repository{ID: 1, ZoektID: 7, Name: "acme/one", Branch: "main", IndexedSHA: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}), Limits{})
	response, err := service.Search(t.Context(), principalFor("acme/one"), api.SearchRequest{Query: "needle"})
	if err != nil || len(response.Matches) != 0 {
		t.Fatalf("matches=%#v err=%v", response.Matches, err)
	}
}

func TestSearchSuppressesEmptyIndexedRevision(t *testing.T) {
	backend := &recordingBackend{response: api.SearchResponse{Matches: []api.SearchMatch{{ZoektID: 7, SHA: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", Branches: []string{"main"}}}}}
	service := NewService(backend, authorizerWith(repository.Repository{ID: 1, ZoektID: 7, Name: "acme/one", Branch: "main"}), Limits{})
	response, err := service.Search(t.Context(), principalFor("acme/one"), api.SearchRequest{Query: "needle"})
	if err != nil || len(response.Matches) != 0 {
		t.Fatalf("matches=%#v err=%v", response.Matches, err)
	}
}

func TestSearchSuppressesWrongIndexedBranch(t *testing.T) {
	backend := &recordingBackend{response: api.SearchResponse{Matches: []api.SearchMatch{{ZoektID: 7, SHA: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", Branches: []string{"other"}}}}}
	service := NewService(backend, authorizerWith(repository.Repository{ID: 1, ZoektID: 7, Name: "acme/one", Branch: "main", IndexedSHA: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}), Limits{})
	response, err := service.Search(t.Context(), principalFor("acme/one"), api.SearchRequest{Query: "needle"})
	if err != nil || len(response.Matches) != 0 {
		t.Fatalf("matches=%#v err=%v", response.Matches, err)
	}
}

func TestSearchSuppressesUnknownRepoID(t *testing.T) {
	backend := &recordingBackend{response: api.SearchResponse{Matches: []api.SearchMatch{{ZoektID: 8, SHA: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", Branches: []string{"main"}}}}}
	service := NewService(backend, authorizerWith(repository.Repository{ID: 1, ZoektID: 7, Name: "acme/one", Branch: "main", IndexedSHA: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}), Limits{})
	response, err := service.Search(t.Context(), principalFor("acme/one"), api.SearchRequest{Query: "needle"})
	if err != nil || len(response.Matches) != 0 {
		t.Fatalf("matches=%#v err=%v", response.Matches, err)
	}
}

func TestSearchReturnsExactIndexedRevision(t *testing.T) {
	const sha = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	backend := &recordingBackend{response: api.SearchResponse{Matches: []api.SearchMatch{{ZoektID: 7, SHA: sha, Branches: []string{"main"}}}}}
	service := NewService(backend, authorizerWith(repository.Repository{ID: 1, GitHubID: 101, ZoektID: 7, Name: "acme/one", Branch: "main", IndexedSHA: sha}), Limits{})
	response, err := service.Search(t.Context(), principalFor("acme/one"), api.SearchRequest{Query: "needle"})
	if err != nil || len(response.Matches) != 1 || response.Matches[0].Repository.ID != 101 {
		t.Fatalf("matches=%#v err=%v", response.Matches, err)
	}
}

func TestSearchSkipsBackendForEmptyAuthorization(t *testing.T) {
	backend := &recordingBackend{}
	service := NewService(backend, authorizer(), Limits{MaxResults: 100})
	response, err := service.Search(t.Context(), principalFor("acme/one"), api.SearchRequest{Query: "secret", Repositories: []string{"acme/two"}})
	if err != nil {
		t.Fatal(err)
	}
	if backend.calls != 0 {
		t.Fatalf("backend calls = %d", backend.calls)
	}
	if response.Matches == nil {
		t.Fatal("matches = nil, want empty array")
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
	backend := &recordingBackend{response: api.SearchResponse{Matches: []api.SearchMatch{{Preview: strings.Repeat("x", 300<<10), ZoektID: 7, SHA: "abc", Branches: []string{"main"}}}}}
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
		{Path: "one.go", Preview: "first", ZoektID: 7, SHA: "abc", Branches: []string{"main"}},
		{Path: "two.go", Preview: "second", ZoektID: 7, SHA: "abc", Branches: []string{"main"}},
	}}}
	service := NewService(backend, authorizer(), Limits{MaxResults: 100, MaxResponseBytes: 256 << 10})
	want := api.SearchResponse{Matches: []api.SearchMatch{{
		Repository: api.Repository{ID: 1, Name: "acme/one", Branch: "main", IndexedSHA: "abc"}, Path: "one.go", SHA: "abc", Preview: "first", ZoektID: 7,
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
	backend := &recordingBackend{response: api.SearchResponse{Matches: []api.SearchMatch{{Path: "one.go", ZoektID: 7, SHA: "abc", Branches: []string{"main"}}}}}
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
	return authorizerWith(repository.Repository{ID: 1, ZoektID: 7, Name: "acme/one", Branch: "main", IndexedSHA: "abc"}, repository.Repository{ID: 2, ZoektID: 8, Name: "acme/two", Branch: "main", IndexedSHA: "def"})
}

func authorizerWith(repositories ...repository.Repository) authz.Authorizer {
	registry, err := repository.NewStatic(repositories)
	if err != nil {
		panic(err)
	}
	return authz.NewStatic(registry)
}

func principalFor(name string) authn.Principal {
	return authn.Principal{Subject: "user", RepositoryNames: []string{name}}
}

func TestCaseFlagGroup(t *testing.T) {
	for _, test := range []struct {
		query, group string
		clears, ok   bool
	}{
		{`(?i)brotli`, `(?i)`, false, true},
		{`(?-i)Brotli`, `(?-i)`, true, true},
		{`foo (?i:bar)`, `(?i:`, false, true},
		{`(?im)x`, `(?im)`, false, true},
		{`(?si:x)`, `(?si:`, false, true},
		{`"(?i)brotli"`, `(?i)`, false, true},
		{`\\(?i)x`, `(?i)`, false, true},
		{`\(?i)x`, "", false, false},
		{`\(\?i\)`, "", false, false},
		{`(?s)x`, "", false, false},
		{`FROM node:(?:22|20)`, "", false, false},
		{`(22|20)`, "", false, false},
	} {
		group, clears, ok := caseFlagGroup(test.query)
		if group != test.group || clears != test.clears || ok != test.ok {
			t.Errorf("caseFlagGroup(%q) = %q, %v, %v; want %q, %v, %v", test.query, group, clears, ok, test.group, test.clears, test.ok)
		}
	}
}

func TestSearchRefusesInlineCaseFlags(t *testing.T) {
	for _, test := range []struct{ query, want string }{
		{"(?i)brotli", "search query is invalid: inline flag (?i) is not supported; use case:no brotli"},
		{"(?-i)Brotli", "search query is invalid: inline flag (?-i) is not supported; use case:yes Brotli"},
		{"foo (?i:bar)", "search query is invalid: inline flag (?i: is not supported; use case:no and remove the group"},
	} {
		backend := &recordingBackend{}
		service := NewService(backend, authorizer(), Limits{})
		_, err := service.Search(t.Context(), principalFor("acme/one"), api.SearchRequest{Query: test.query})
		var queryErr *QueryError
		if !errors.As(err, &queryErr) || queryErr.Message() != test.want {
			t.Errorf("Search(%q) error = %v; want message %q", test.query, err, test.want)
		}
		if backend.calls != 0 {
			t.Errorf("Search(%q) reached the backend", test.query)
		}
	}
}

type failingBackend struct{ err error }

func (backend failingBackend) Search(context.Context, BackendRequest) (api.SearchResponse, error) {
	return api.SearchResponse{}, backend.err
}

func (failingBackend) Health(context.Context) error { return nil }

func TestSearchAttachesQueryToBackendQueryError(t *testing.T) {
	service := NewService(failingBackend{NewQueryError("error parsing regexp: missing closing ]: `[0-9`")}, authorizer(), Limits{})
	_, err := service.Search(t.Context(), principalFor("acme/one"), api.SearchRequest{Query: "FROM node:[0-9 ]"})
	var queryErr *QueryError
	if !errors.As(err, &queryErr) || !strings.Contains(queryErr.Message(), `quote the term ("FROM node:[0-9 ]")`) {
		t.Fatalf("error = %v, want a hint quoting the query", err)
	}
}
