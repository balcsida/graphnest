package graphservice

import (
	"errors"
	"testing"

	"github.com/grepnest/grepnest/internal/repository"
	"github.com/grepnest/grepnest/pkg/api"
)

func TestCypherRejectsNonAdminBeforeBackend(t *testing.T) {
	backend := &fakeBackend{cypher: emptyCypher}
	_, err := (&Service{Store: &fakeRepositoryStore{repositories: []repository.Repository{readyRepository("a")}}, Backend: backend}).Cypher(t.Context(), principalFor(101), api.GraphCypherRequest{Repo: api.GraphRepositorySelector{ID: 101}, Statement: "RETURN 1"})
	if !errors.Is(err, ErrAdminRequired) || backend.calls != 0 {
		t.Fatalf("err=%v calls=%d", err, backend.calls)
	}
}
