package graphservice

import (
	"errors"
	"strings"
	"testing"

	"github.com/balcsida/graphnest/internal/graphprotocol"
	"github.com/balcsida/graphnest/internal/repository"
	"github.com/balcsida/graphnest/pkg/api"
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

func TestTraceMapsCrossRepositoryNodesEdgesAndBoundaries(t *testing.T) {
	first, second := readyRepository("a"), readyRepository("b")
	second.ID, second.GitHubID = 2, 202
	backend := &fakeBackend{trace: func() graphprotocol.TraceResponse {
		return graphprotocol.TraceResponse{
			Status: graphprotocol.StatusFound,
			Nodes:  []graphprotocol.Symbol{{UID: "a", RepositoryID: 1}, {UID: "b", RepositoryID: 2}},
			Edges: []graphprotocol.Relationship{{
				SourceRepositoryID: 1, TargetRepositoryID: 2, SourceUID: "a", TargetUID: "b",
			}},
			Boundaries: []graphprotocol.Boundary{{RepositoryID: 2, Repository: "internal", Reason: "fanout_limit"}},
			Commits:    map[string]string{"a": first.IndexedSHA, "b": second.IndexedSHA},
		}
	}}
	principal := principalFor(101)
	principal.RepositoryIDs = append(principal.RepositoryIDs, 202)
	got, err := (&Service{Store: &fakeRepositoryStore{repositories: []repository.Repository{first, second}}, Backend: backend}).
		Trace(t.Context(), principal, api.GraphTraceRequest{Repo: api.GraphRepositorySelector{ID: 101}, SourceUID: "a", TargetUID: "b"})
	if err != nil || got.Nodes[0].RepositoryID != 101 || got.Nodes[1].RepositoryID != 202 ||
		got.Relations[0].SourceRepositoryID != 101 || got.Relations[0].TargetRepositoryID != 202 ||
		got.Relations[0].SourceUID != "a" || got.Relations[0].TargetUID != "b" ||
		got.Boundaries[0].RepositoryID != 202 || got.Boundaries[0].Repository != "b" {
		t.Fatalf("Trace() = %#v, %v", got, err)
	}
}
