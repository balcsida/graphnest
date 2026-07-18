package authz

import (
	"testing"

	"github.com/grepnest/grepnest/internal/authn"
	"github.com/grepnest/grepnest/internal/repository"
)

func TestAuthorizedRepositoriesIntersectsRequestedNames(t *testing.T) {
	principal := authn.Principal{Subject: "user", RepositoryNames: []string{"acme/one"}}
	authorizer := NewStatic(repository.NewStatic([]repository.Repository{{ID: 1, ZoektID: 7, Name: "acme/one"}, {ID: 2, ZoektID: 8, Name: "acme/two"}}))
	got, err := authorizer.AuthorizedRepositories(t.Context(), principal, RepositorySelection{Names: []string{"acme/one", "acme/two"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Name != "acme/one" {
		t.Fatalf("got %#v", got)
	}
}
