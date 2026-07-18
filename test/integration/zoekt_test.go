//go:build integration

package integration

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"

	"github.com/grepnest/grepnest/internal/authn"
	"github.com/grepnest/grepnest/internal/authz"
	"github.com/grepnest/grepnest/internal/observability"
	"github.com/grepnest/grepnest/internal/repository"
	"github.com/grepnest/grepnest/internal/search"
	"github.com/grepnest/grepnest/internal/zoekt"
	"github.com/grepnest/grepnest/pkg/api"
)

func TestServiceSendsOnlyAuthorizedRepoIDsToZoekt(t *testing.T) {
	var repoIDs []uint32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		var body struct {
			RepoIDs []uint32 `json:"RepoIDs"`
		}
		if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		repoIDs = body.RepoIDs
		_, _ = writer.Write([]byte(`{"Result":{"Files":[]}}`))
	}))
	defer server.Close()
	backend, err := zoekt.New(server.URL, server.Client(), 1024, observability.New())
	if err != nil {
		t.Fatal(err)
	}
	registry, err := repository.NewStatic([]repository.Repository{{ID: 1, ZoektID: 7, Name: "acme/one"}, {ID: 2, ZoektID: 8, Name: "acme/two"}})
	if err != nil {
		t.Fatal(err)
	}
	service := search.NewService(backend, authz.NewStatic(registry), search.Limits{MaxResults: 100})
	if _, err := service.Search(t.Context(), authn.Principal{Subject: "user", RepositoryNames: []string{"acme/one"}}, api.SearchRequest{Query: "needle", Repositories: []string{"acme/one", "acme/two"}}); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(repoIDs, []uint32{7}) {
		t.Fatalf("RepoIDs = %v", repoIDs)
	}
}
