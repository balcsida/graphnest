package graphservice

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/balcsida/graphnest/internal/graphprotocol"
	"github.com/balcsida/graphnest/pkg/api"
)

type affectedTestsBackend struct {
	*inspectionBackend
	request graphprotocol.AffectedTestsRequest
	result  graphprotocol.AffectedTestsResponse
	after   func()
}

type fileDependencyBackend struct {
	*affectedTestsBackend
	request graphprotocol.FileDependencyRequest
	result  graphprotocol.FileDependencyResponse
}

func (b *fileDependencyBackend) FileDependencyPairs(_ context.Context, request graphprotocol.FileDependencyRequest) (graphprotocol.FileDependencyResponse, error) {
	b.request = request
	if b.after != nil {
		b.after()
	}
	return b.result, nil
}
func (b *fileDependencyBackend) FileDependencies(context.Context, graphprotocol.FileDependencyRequest) (graphprotocol.FileDependencyResponse, error) {
	return b.result, nil
}
func (b *fileDependencyBackend) FileDependents(context.Context, graphprotocol.FileDependencyRequest) (graphprotocol.FileDependencyResponse, error) {
	return b.result, nil
}
func (b *fileDependencyBackend) FileDependentCounts(context.Context, graphprotocol.FileDependencyRequest) (graphprotocol.FileDependencyResponse, error) {
	return b.result, nil
}
func (b *fileDependencyBackend) FileReachCounts(context.Context, graphprotocol.FileDependencyRequest) (graphprotocol.FileDependencyResponse, error) {
	return b.result, nil
}
func (b *fileDependencyBackend) CircularDependencies(context.Context, graphprotocol.FileDependencyRequest) (graphprotocol.FileDependencyResponse, error) {
	return b.result, nil
}

func (b *affectedTestsBackend) AffectedTests(_ context.Context, request graphprotocol.AffectedTestsRequest) (graphprotocol.AffectedTestsResponse, error) {
	b.request = request
	if b.after != nil {
		b.after()
	}
	return b.result, nil
}

func TestAffectedTestsServiceKeepsSelectedAuthority(t *testing.T) {
	service, base, store := inspectionFixture()
	backend := &affectedTestsBackend{inspectionBackend: base, result: graphprotocol.AffectedTestsResponse{
		ChangedFiles: []string{"core.ts"}, AffectedTests: []string{"consumer.test.ts"}, TotalDependentsTraversed: 5,
		Generations: []graphprotocol.Generation{base.generation},
	}}
	service.Backend = backend
	got, err := service.AffectedTests(t.Context(), principalFor(101), AffectedTestsRequest{Repo: api.GraphRepositorySelector{ID: 101}, ChangedFiles: []string{"./core.ts"}})
	if err != nil || !reflect.DeepEqual(got.AffectedTests, []string{"consumer.test.ts"}) || len(backend.request.Scope.Repositories) != 1 || backend.request.Scope.Repositories[0].ID != store.repositories[0].ID || !reflect.DeepEqual(backend.request.ChangedFiles, []string{"core.ts"}) {
		t.Fatalf("result=%+v request=%+v err=%v", got, backend.request, err)
	}
	backend.after = func() { store.repositories = nil }
	got, err = service.AffectedTests(t.Context(), principalFor(101), AffectedTestsRequest{Repo: api.GraphRepositorySelector{ID: 101}, ChangedFiles: []string{"core.ts"}})
	if err == nil || !reflect.DeepEqual(got, graphprotocol.AffectedTestsResponse{}) {
		t.Fatalf("revoked result=%+v err=%v", got, err)
	}
}

func TestAffectedTestsServiceRejectsUntrustedOutputAndInput(t *testing.T) {
	service, base, _ := inspectionFixture()
	backend := &affectedTestsBackend{inspectionBackend: base, result: graphprotocol.AffectedTestsResponse{ChangedFiles: []string{"other.ts"}, Generations: []graphprotocol.Generation{base.generation}}}
	service.Backend = backend
	got, err := service.AffectedTests(t.Context(), principalFor(101), AffectedTestsRequest{Repo: api.GraphRepositorySelector{ID: 101}, ChangedFiles: []string{"core.ts"}})
	if !errors.Is(err, ErrGraphNotReady) || !reflect.DeepEqual(got, graphprotocol.AffectedTestsResponse{}) {
		t.Fatalf("untrusted result=%+v err=%v", got, err)
	}
	got, err = service.AffectedTests(t.Context(), principalFor(101), AffectedTestsRequest{Repo: api.GraphRepositorySelector{ID: 101}, ChangedFiles: []string{"../secret"}})
	if !errors.Is(err, ErrInvalidRequest) || !reflect.DeepEqual(got, graphprotocol.AffectedTestsResponse{}) {
		t.Fatalf("invalid result=%+v err=%v", got, err)
	}
}

func TestAffectedTestsServiceRejectsUntrustedOrderingAndBoundaries(t *testing.T) {
	service, base, _ := inspectionFixture()
	backend := &affectedTestsBackend{inspectionBackend: base, result: graphprotocol.AffectedTestsResponse{
		ChangedFiles:  []string{"core.ts"},
		AffectedTests: []string{"z.test.ts", "a.test.ts"},
		Generations:   []graphprotocol.Generation{base.generation},
	}}
	service.Backend = backend
	request := AffectedTestsRequest{Repo: api.GraphRepositorySelector{ID: 101}, ChangedFiles: []string{"core.ts"}}
	if got, err := service.AffectedTests(t.Context(), principalFor(101), request); !errors.Is(err, ErrGraphNotReady) || !reflect.DeepEqual(got, graphprotocol.AffectedTestsResponse{}) {
		t.Fatalf("unordered result=%+v err=%v", got, err)
	}
	backend.result.AffectedTests = nil
	backend.result.Partial = true
	backend.result.Boundaries = []graphprotocol.Boundary{{Repository: "internal", Reason: "depth_limit", Depth: 1}}
	if got, err := service.AffectedTests(t.Context(), principalFor(101), request); !errors.Is(err, ErrGraphNotReady) || !reflect.DeepEqual(got, graphprotocol.AffectedTestsResponse{}) {
		t.Fatalf("boundary result=%+v err=%v", got, err)
	}
}

func TestFileDependencyServiceKeepsSelectedAuthority(t *testing.T) {
	service, base, store := inspectionFixture()
	affected := &affectedTestsBackend{inspectionBackend: base}
	backend := &fileDependencyBackend{affectedTestsBackend: affected, result: graphprotocol.FileDependencyResponse{
		Pairs:       []graphprotocol.FileDependencyPair{{Source: "main.ts", Target: "core.ts", References: 5}},
		Generations: []graphprotocol.Generation{base.generation},
	}}
	service.Backend = backend
	got, err := service.FileDependencyPairs(t.Context(), principalFor(101), FileDependencyRequest{Repo: api.GraphRepositorySelector{ID: 101}})
	if err != nil || len(got.Pairs) != 1 || len(backend.request.Scope.Repositories) != 1 || backend.request.Scope.Repositories[0].ID != store.repositories[0].ID {
		t.Fatalf("result=%+v request=%+v err=%v", got, backend.request, err)
	}
	backend.after = func() { store.repositories = nil }
	got, err = service.FileDependencyPairs(t.Context(), principalFor(101), FileDependencyRequest{Repo: api.GraphRepositorySelector{ID: 101}})
	if err == nil || !reflect.DeepEqual(got, graphprotocol.FileDependencyResponse{}) {
		t.Fatalf("revoked result=%+v err=%v", got, err)
	}
}

func TestFileDependencyServicePreservesPinnedCyclePaths(t *testing.T) {
	service, base, _ := inspectionFixture()
	backend := &fileDependencyBackend{affectedTestsBackend: &affectedTestsBackend{inspectionBackend: base}, result: graphprotocol.FileDependencyResponse{
		Cycles:      [][]string{{"z.ts", "a.ts"}},
		Generations: []graphprotocol.Generation{base.generation},
	}}
	service.Backend = backend
	got, err := service.CircularDependencies(t.Context(), principalFor(101), FileDependencyRequest{Repo: api.GraphRepositorySelector{ID: 101}})
	if err != nil || !reflect.DeepEqual(got.Cycles, backend.result.Cycles) {
		t.Fatalf("cycles=%v err=%v", got.Cycles, err)
	}
}
