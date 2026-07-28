package graphservice

import (
	"errors"
	"testing"

	"github.com/grepnest/grepnest/internal/repository"
	"github.com/grepnest/grepnest/pkg/api"
)

func TestImpactValidatesBeforeBackend(t *testing.T) {
	backend := &fakeBackend{impact: emptyImpact}
	_, err := (&Service{Store: &fakeRepositoryStore{repositories: []repository.Repository{readyRepository("a")}}, Backend: backend, Limits: testLimits()}).Impact(t.Context(), principalFor(101), api.GraphImpactRequest{Repo: api.GraphRepositorySelector{ID: 101}, TargetUID: "x", Direction: "downstream", Relations: []string{"invalid"}})
	if !errors.Is(err, ErrInvalidRequest) || backend.calls != 0 {
		t.Fatalf("err=%v calls=%d", err, backend.calls)
	}
}
