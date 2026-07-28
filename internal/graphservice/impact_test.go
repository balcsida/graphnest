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
