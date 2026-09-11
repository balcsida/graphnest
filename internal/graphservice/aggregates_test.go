package graphservice

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/balcsida/graphnest/internal/graphprotocol"
	"github.com/balcsida/graphnest/internal/graphquery"
	"github.com/balcsida/graphnest/internal/repository"
	"github.com/balcsida/graphnest/pkg/api"
)

type aggregateBackend struct {
	*inspectionBackend
	request graphprotocol.AggregateValuesRequest
}

func (b *aggregateBackend) FanIn(_ context.Context, request graphprotocol.AggregateValuesRequest) (graphprotocol.AggregateCountsResponse, error) {
	b.request = request
	return graphprotocol.AggregateCountsResponse{
		Counts:      []graphprotocol.AggregateCount{{ID: "called", Count: 8}},
		Generations: []graphprotocol.Generation{b.generation},
	}, nil
}

func TestAggregateServiceUsesSelectedAuthorizedGeneration(t *testing.T) {
	service, base, _ := inspectionFixture()
	backend := &aggregateBackend{inspectionBackend: base}
	service.Backend = backend
	got, err := service.FanIn(t.Context(), principalFor(101), AggregateValuesRequest{
		AggregateScopeRequest: AggregateScopeRequest{Repo: api.GraphRepositorySelector{ID: 101}},
		Values:                []string{"called"},
	})
	if err != nil || len(got.Counts) != 1 || got.Counts[0].Count != 8 || backend.request.Scope.SelectedRepositoryID == 0 {
		t.Fatalf("fan-in=%+v request=%+v err=%v", got, backend.request, err)
	}
}

func TestAggregateServiceDiscardsUnauthorizedAndChangedResults(t *testing.T) {
	service, base, store := inspectionFixture()
	backend := &aggregateBackend{inspectionBackend: base}
	service.Backend = backend
	request := AggregateValuesRequest{AggregateScopeRequest: AggregateScopeRequest{Repo: api.GraphRepositorySelector{ID: 101}}, Values: []string{"called"}}

	store.repositories = nil
	if got, err := service.FanIn(t.Context(), principalFor(101), request); err == nil || !reflect.DeepEqual(got, graphprotocol.AggregateCountsResponse{}) || backend.request.Scope.SelectedRepositoryID != 0 {
		t.Fatalf("unauthorized fan-in=%+v request=%+v err=%v", got, backend.request, err)
	}

	store.repositories = []repository.Repository{readyRepository("acme/visible")}
	base.changed = true
	if got, err := service.FanIn(t.Context(), principalFor(101), request); !errors.Is(err, graphquery.ErrGenerationChanged) || !reflect.DeepEqual(got, graphprotocol.AggregateCountsResponse{}) {
		t.Fatalf("changed fan-in=%+v err=%v", got, err)
	}
}
