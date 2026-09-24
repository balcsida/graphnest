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

type typeHierarchyBackend struct {
	*inspectionBackend
	relations graphprotocol.TypeRelationsRequest
	hierarchy graphprotocol.TypeHierarchyRequest
}

func (backend *typeHierarchyBackend) TypeRelations(_ context.Context, request graphprotocol.TypeRelationsRequest) (graphprotocol.TypeRelationsResponse, error) {
	backend.relations = request
	return graphprotocol.TypeRelationsResponse{Status: graphprotocol.StatusOK, Generations: []graphprotocol.Generation{backend.generation}}, nil
}

func (backend *typeHierarchyBackend) TypeHierarchy(_ context.Context, request graphprotocol.TypeHierarchyRequest) (graphprotocol.TypeHierarchyResponse, error) {
	backend.hierarchy = request
	return graphprotocol.TypeHierarchyResponse{Status: graphprotocol.StatusOK, Generations: []graphprotocol.Generation{backend.generation}}, nil
}

func TestTypeHierarchyServiceKeepsSelectedAuthorityAndRejectsDrift(t *testing.T) {
	service, base, store := inspectionFixture()
	backend := &typeHierarchyBackend{inspectionBackend: base}
	service.Backend = backend
	request := EntityAnalysisRequest{AggregateScopeRequest: AggregateScopeRequest{Repo: api.GraphRepositorySelector{ID: 101}}, Occurrence: "service"}
	relations, err := service.TypeRelations(t.Context(), principalFor(101), request)
	if err != nil || relations.Status != graphprotocol.StatusOK || backend.relations.Scope.SelectedRepositoryID == 0 || backend.relations.Occurrence != "service" {
		t.Fatalf("relations=%+v request=%+v err=%v", relations, backend.relations, err)
	}
	hierarchy, err := service.TypeHierarchy(t.Context(), principalFor(101), request)
	if err != nil || hierarchy.Status != graphprotocol.StatusOK || backend.hierarchy.Scope.SelectedRepositoryID == 0 || backend.hierarchy.Occurrence != "service" {
		t.Fatalf("hierarchy=%+v request=%+v err=%v", hierarchy, backend.hierarchy, err)
	}

	store.repositories = nil
	if got, err := service.TypeHierarchy(t.Context(), principalFor(101), request); err == nil || !reflect.DeepEqual(got, graphprotocol.TypeHierarchyResponse{}) {
		t.Fatalf("unauthorized hierarchy=%+v err=%v", got, err)
	}
	store.repositories = []repository.Repository{readyRepository("acme/visible")}
	base.changed = true
	if got, err := service.TypeRelations(t.Context(), principalFor(101), request); !errors.Is(err, graphquery.ErrGenerationChanged) || !reflect.DeepEqual(got, graphprotocol.TypeRelationsResponse{}) {
		t.Fatalf("changed relations=%+v err=%v", got, err)
	}
}
