package graphservice

import (
	"errors"
	"strings"
	"testing"

	"github.com/grepnest/grepnest/internal/graphprotocol"
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

func TestTraceDefaultsAndCapsDepth(t *testing.T) {
	backend := &fakeBackend{trace: emptyTrace}
	service := &Service{Store: &fakeRepositoryStore{repositories: []repository.Repository{readyRepository("a")}}, Backend: backend}
	_, err := service.Trace(t.Context(), principalFor(101), api.GraphTraceRequest{Repo: api.GraphRepositorySelector{ID: 101}, SourceUID: "a", TargetUID: "b", MaxDepth: 99})
	if err != nil || backend.traceRequest.MaxDepth != 30 {
		t.Fatalf("err=%v request=%#v", err, backend.traceRequest)
	}
	backend = &fakeBackend{trace: emptyTrace}
	service.Backend = backend
	_, err = service.Trace(t.Context(), principalFor(101), api.GraphTraceRequest{Repo: api.GraphRepositorySelector{ID: 101}, SourceUID: "a", TargetUID: "b"})
	if err != nil || backend.traceRequest.MaxDepth != 10 {
		t.Fatalf("err=%v request=%#v", err, backend.traceRequest)
	}
}

func TestTraceMapsAmbiguousCandidates(t *testing.T) {
	backend := &fakeBackend{trace: func() graphprotocol.TraceResponse {
		return graphprotocol.TraceResponse{
			Status:     graphprotocol.StatusAmbiguous,
			Candidates: []graphprotocol.Symbol{{UID: "x", Name: "X", RepositoryID: 1}},
			Commits:    map[string]string{"a": strings.Repeat("a", 40)},
		}
	}}
	got, err := (&Service{Store: &fakeRepositoryStore{repositories: []repository.Repository{readyRepository("a")}}, Backend: backend}).
		Trace(t.Context(), principalFor(101), api.GraphTraceRequest{Repo: api.GraphRepositorySelector{ID: 101}, SourceUID: "x", TargetUID: "y"})
	if err != nil || len(got.Candidates) != 1 || got.Candidates[0].UID != "x" || got.Candidates[0].RepositoryID != 101 {
		t.Fatalf("Trace()=%#v,%v", got, err)
	}
}
