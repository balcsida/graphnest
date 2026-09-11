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

type entityImpactBackend struct {
	*inspectionBackend
	request graphprotocol.EntityImpactRequest
}

func (backend *entityImpactBackend) ImpactRadius(_ context.Context, request graphprotocol.EntityImpactRequest) (graphprotocol.SubgraphResponse, error) {
	backend.request = request
	return graphprotocol.SubgraphResponse{Status: graphprotocol.StatusOK, Generations: []graphprotocol.Generation{backend.generation}}, nil
}

func TestEntityImpactServiceKeepsSelectedAuthorityAndRejectsDrift(t *testing.T) {
	service, base, store := inspectionFixture()
	backend := &entityImpactBackend{inspectionBackend: base}
	service.Backend = backend
	request := EntityAnalysisRequest{AggregateScopeRequest: AggregateScopeRequest{Repo: api.GraphRepositorySelector{ID: 101}}, Occurrence: "service"}
	got, err := service.ImpactRadius(t.Context(), principalFor(101), request)
	if err != nil || got.Status != graphprotocol.StatusOK || backend.request.Scope.SelectedRepositoryID == 0 || backend.request.Occurrence != "service" {
		t.Fatalf("impact=%+v request=%+v err=%v", got, backend.request, err)
	}

	store.repositories = nil
	if got, err = service.ImpactRadius(t.Context(), principalFor(101), request); err == nil || !reflect.DeepEqual(got, graphprotocol.SubgraphResponse{}) {
		t.Fatalf("unauthorized impact=%+v err=%v", got, err)
	}
	store.repositories = []repository.Repository{readyRepository("acme/visible")}
	base.changed = true
	if got, err = service.ImpactRadius(t.Context(), principalFor(101), request); !errors.Is(err, graphquery.ErrGenerationChanged) || !reflect.DeepEqual(got, graphprotocol.SubgraphResponse{}) {
		t.Fatalf("changed impact=%+v err=%v", got, err)
	}
}
