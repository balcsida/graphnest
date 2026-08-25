package authz

import (
	"testing"

	"github.com/balcsida/graphnest/internal/authn"
	"github.com/balcsida/graphnest/internal/repository"
)

func TestAuthorizedRepositoriesIntersectsRequestedNames(t *testing.T) {
	principal := authn.Principal{Subject: "user", RepositoryNames: []string{"acme/one"}}
	registry, err := repository.NewStatic([]repository.Repository{{ID: 1, ZoektID: 7, Name: "acme/one"}, {ID: 2, ZoektID: 8, Name: "acme/two"}})
	if err != nil {
		t.Fatal(err)
	}
	authorizer := NewStatic(registry)
	got, err := authorizer.AuthorizedRepositories(t.Context(), principal, RepositorySelection{Names: []string{"acme/one", "acme/two"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Name != "acme/one" {
		t.Fatalf("got %#v", got)
	}
}

func TestAdministratorSearchRemainsRepositoryScoped(t *testing.T) {
	principal := authn.Principal{Subject: "admin", Administrator: true, RepositoryNames: []string{"acme/one"}}
	registry, err := repository.NewStatic([]repository.Repository{{ID: 1, ZoektID: 7, Name: "acme/one"}, {ID: 2, ZoektID: 8, Name: "acme/two"}})
	if err != nil {
		t.Fatal(err)
	}
	got, err := NewStatic(registry).AuthorizedRepositories(t.Context(), principal, RepositorySelection{Names: []string{"acme/two"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("got %#v, want no repositories", got)
	}
}
