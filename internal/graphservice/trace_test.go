package graphservice

import (
	"errors"
	"testing"

	"github.com/grepnest/grepnest/internal/repository"
	"github.com/grepnest/grepnest/pkg/api"
)

func TestTraceRequiresReadyRepository(t *testing.T) {
	repo := readyRepository("a")
	repo.IndexedSHA = ""
	backend := &fakeBackend{trace: emptyTrace}
	_, err := (&Service{Store: &fakeRepositoryStore{repositories: []repository.Repository{repo}}, Backend: backend}).Trace(t.Context(), principalFor(101), api.GraphTraceRequest{Repo: api.GraphRepositorySelector{ID: 101}, SourceUID: "a", TargetUID: "b"})
	if !errors.Is(err, ErrGraphNotReady) || backend.calls != 0 {
		t.Fatalf("err=%v calls=%d", err, backend.calls)
	}
}
