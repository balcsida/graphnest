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

func TestCypherCapsPublicRowsAndBytes(t *testing.T) {
	backend := &fakeBackend{cypher: emptyCypher}
	service := &Service{Store: &fakeRepositoryStore{repositories: []repository.Repository{readyRepository("a")}}, Backend: backend}
	principal := principalFor(101)
	principal.Administrator = true
	_, err := service.Cypher(t.Context(), principal, api.GraphCypherRequest{Repo: api.GraphRepositorySelector{ID: 101}, Statement: "RETURN 1", MaxRows: 999, MaxBytes: 999 << 10})
	if err != nil || backend.cypherRequest.MaxRows != 100 || backend.cypherRequest.MaxBytes != 256<<10 {
		t.Fatalf("err=%v request=%#v", err, backend.cypherRequest)
	}
}
