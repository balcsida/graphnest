package graphservice

import (
	"errors"
	"strings"
	"testing"

	"github.com/grepnest/grepnest/internal/graphprotocol"
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

func TestImpactPropagatesPartialBoundary(t *testing.T) {
	backend := &fakeBackend{impact: func() graphprotocol.ImpactResponse {
		return graphprotocol.ImpactResponse{
			Status: graphprotocol.StatusFound, Partial: true, Commits: map[string]string{"a": strings.Repeat("a", 40)},
			ByDepth: map[int][]graphprotocol.Symbol{}, Boundaries: []graphprotocol.Boundary{{Reason: "depth_limit", Depth: 32}},
		}
	}}
	service := &Service{Store: &fakeRepositoryStore{repositories: []repository.Repository{readyRepository("a")}}, Backend: backend}
	got, err := service.Impact(t.Context(), principalFor(101), api.GraphImpactRequest{Repo: api.GraphRepositorySelector{ID: 101}, TargetUID: "x", Direction: "downstream"})
	if err != nil || !got.Partial || len(got.Boundaries) != 1 || got.Boundaries[0] != (api.GraphBoundary{Reason: "depth_limit", Depth: 32}) {
		t.Fatalf("got=%#v err=%v", got, err)
	}
}

func TestImpactDefaultsCapsAndPreservesFilters(t *testing.T) {
	backend := &fakeBackend{impact: emptyImpact}
	service := &Service{Store: &fakeRepositoryStore{repositories: []repository.Repository{readyRepository("a")}}, Backend: backend}
	_, err := service.Impact(t.Context(), principalFor(101), api.GraphImpactRequest{Repo: api.GraphRepositorySelector{ID: 101}, TargetUID: "x", Direction: "downstream", MinConfidence: .75, IncludeTests: true, MaxDepth: 99, Limit: 999, Offset: 4})
	if err != nil {
		t.Fatal(err)
	}
	request := backend.impactRequest
	if request.MaxDepth != 32 || request.Limit != 100 || request.Offset != 4 || request.MinConfidence != .75 || !request.IncludeTests {
		t.Fatalf("request=%#v", request)
	}

	backend = &fakeBackend{impact: emptyImpact}
	service.Backend = backend
	_, err = service.Impact(t.Context(), principalFor(101), api.GraphImpactRequest{Repo: api.GraphRepositorySelector{ID: 101}, TargetUID: "x", Direction: "upstream"})
	if err != nil || backend.impactRequest.MaxDepth != 3 {
		t.Fatalf("err=%v request=%#v", err, backend.impactRequest)
	}
}

func TestImpactMapsAmbiguousCandidates(t *testing.T) {
	backend := &fakeBackend{impact: func() graphprotocol.ImpactResponse {
		return graphprotocol.ImpactResponse{
			Status: graphprotocol.StatusAmbiguous, ByDepth: map[int][]graphprotocol.Symbol{},
			Candidates: []graphprotocol.Symbol{{UID: "x", Name: "X", RepositoryID: 1}},
			Commits:    map[string]string{"a": strings.Repeat("a", 40)},
		}
	}}
	got, err := (&Service{Store: &fakeRepositoryStore{repositories: []repository.Repository{readyRepository("a")}}, Backend: backend}).
		Impact(t.Context(), principalFor(101), api.GraphImpactRequest{Repo: api.GraphRepositorySelector{ID: 101}, TargetUID: "x", Direction: "downstream"})
	if err != nil || len(got.Candidates) != 1 || got.Candidates[0].UID != "x" || got.Candidates[0].RepositoryID != 101 {
		t.Fatalf("Impact()=%#v,%v", got, err)
	}
}

func TestImpactMapsCrossRepositorySymbolsEdgesAndBoundaries(t *testing.T) {
	first, second := readyRepository("a"), readyRepository("b")
	second.ID, second.GitHubID = 2, 202
	backend := &fakeBackend{impact: func() graphprotocol.ImpactResponse {
		return graphprotocol.ImpactResponse{
			Status:  graphprotocol.StatusFound,
			ByDepth: map[int][]graphprotocol.Symbol{1: {{UID: "b", RepositoryID: 2}}},
			Edges: []graphprotocol.Relationship{
				{SourceRepositoryID: 1, TargetRepositoryID: 2, SourceUID: "a", TargetUID: "b"},
				{SourceRepositoryID: 2, TargetRepositoryID: 1, SourceUID: "b", TargetUID: "a"},
			},
			Boundaries: []graphprotocol.Boundary{{RepositoryID: 2, Repository: "internal", Reason: "depth_limit"}},
			Commits:    map[string]string{"a": first.IndexedSHA, "b": second.IndexedSHA},
		}
	}}
	principal := principalFor(101)
	principal.RepositoryIDs = append(principal.RepositoryIDs, 202)
	got, err := (&Service{Store: &fakeRepositoryStore{repositories: []repository.Repository{first, second}}, Backend: backend}).
		Impact(t.Context(), principal, api.GraphImpactRequest{Repo: api.GraphRepositorySelector{ID: 101}, TargetUID: "a", Direction: "downstream"})
	if err != nil || got.ByDepth[1][0].RepositoryID != 202 ||
		got.Relations[0].SourceRepositoryID != 101 || got.Relations[0].TargetRepositoryID != 202 ||
		got.Relations[1].SourceRepositoryID != 202 || got.Relations[1].TargetRepositoryID != 101 ||
		got.Relations[0].SourceUID != "a" || got.Relations[0].TargetUID != "b" ||
		got.Boundaries[0].RepositoryID != 202 || got.Boundaries[0].Repository != "b" {
		t.Fatalf("Impact() = %#v, %v", got, err)
	}
}
