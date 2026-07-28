package graphservice

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/grepnest/grepnest/internal/authn"
	"github.com/grepnest/grepnest/internal/graphprotocol"
	"github.com/grepnest/grepnest/internal/repository"
	"github.com/grepnest/grepnest/pkg/api"
)

type fakeRepositoryStore struct {
	repositories []repository.Repository
	calls        int
}

func (s *fakeRepositoryStore) GraphRepositories(context.Context, authn.Principal) ([]repository.Repository, error) {
	s.calls++
	return append([]repository.Repository(nil), s.repositories...), nil
}

type fakeBackend struct {
	context        func() graphprotocol.ContextResponse
	impact         func() graphprotocol.ImpactResponse
	trace          func() graphprotocol.TraceResponse
	cypher         func() graphprotocol.CypherResponse
	after          func()
	calls          int
	contextRequest graphprotocol.ContextRequest
	impactRequest  graphprotocol.ImpactRequest
	traceRequest   graphprotocol.TraceRequest
	cypherRequest  graphprotocol.CypherRequest
}

func (b *fakeBackend) Context(_ context.Context, request graphprotocol.ContextRequest) (graphprotocol.ContextResponse, error) {
	b.contextRequest = request
	b.calls++
	if b.after != nil {
		b.after()
	}
	return b.context(), nil
}
func (b *fakeBackend) Impact(_ context.Context, request graphprotocol.ImpactRequest) (graphprotocol.ImpactResponse, error) {
	b.calls++
	b.impactRequest = request
	return b.impact(), nil
}
func (b *fakeBackend) Trace(_ context.Context, request graphprotocol.TraceRequest) (graphprotocol.TraceResponse, error) {
	b.calls++
	b.traceRequest = request
	return b.trace(), nil
}
func (b *fakeBackend) Cypher(_ context.Context, request graphprotocol.CypherRequest) (graphprotocol.CypherResponse, error) {
	b.calls++
	b.cypherRequest = request
	return b.cypher(), nil
}

func readyRepository(name string) repository.Repository {
	return repository.Repository{ID: 1, GitHubID: 101, Name: name, Branch: "main", IndexedSHA: strings.Repeat("a", 40)}
}
func principalFor(id int64) authn.Principal {
	return authn.Principal{InstallationID: 1, RepositoryIDs: []int64{id}}
}
func testLimits() Limits {
	return Limits{PerCategory: 2, DefaultImpactDepth: 2, MaxDepth: 3, DefaultTraceDepth: 2, MaxTraceDepth: 3, MaxRows: 2}
}
func emptyContext() graphprotocol.ContextResponse {
	return graphprotocol.ContextResponse{Status: graphprotocol.StatusNotFound, Commits: map[string]string{"a": strings.Repeat("a", 40)}}
}
func emptyImpact() graphprotocol.ImpactResponse {
	return graphprotocol.ImpactResponse{Status: graphprotocol.StatusNotFound, ByDepth: map[int][]graphprotocol.Symbol{}, Commits: map[string]string{"a": strings.Repeat("a", 40)}}
}
func emptyTrace() graphprotocol.TraceResponse {
	return graphprotocol.TraceResponse{Status: graphprotocol.StatusNoPath, Commits: map[string]string{"a": strings.Repeat("a", 40)}}
}
func emptyCypher() graphprotocol.CypherResponse {
	return graphprotocol.CypherResponse{Commits: map[string]string{"a": strings.Repeat("a", 40)}}
}

func TestContextReauthorizesAfterBackend(t *testing.T) {
	store := &fakeRepositoryStore{repositories: []repository.Repository{readyRepository("a")}}
	backend := &fakeBackend{context: emptyContext, after: func() { store.repositories[0].IndexedSHA = strings.Repeat("b", 40) }}
	service := Service{Store: store, Backend: backend, Limits: testLimits()}
	_, err := service.Context(t.Context(), principalFor(101), api.GraphContextRequest{Repo: api.GraphRepositorySelector{ID: 101}, GraphSymbolSelector: api.GraphSymbolSelector{UID: "symbol:a"}})
	if !errors.Is(err, ErrGraphNotReady) {
		t.Fatalf("err=%v", err)
	}
}

func TestContextRejectsUnauthorizedBeforeBackend(t *testing.T) {
	backend := &fakeBackend{context: emptyContext}
	_, err := (&Service{Store: &fakeRepositoryStore{}, Backend: backend}).Context(t.Context(), principalFor(101), api.GraphContextRequest{Repo: api.GraphRepositorySelector{ID: 101}, GraphSymbolSelector: api.GraphSymbolSelector{UID: "x"}})
	if !errors.Is(err, ErrRepositoryNotFound) || backend.calls != 0 {
		t.Fatalf("err=%v calls=%d", err, backend.calls)
	}
}

func TestContextRejectsAliasConflictBeforeBackend(t *testing.T) {
	backend := &fakeBackend{context: emptyContext}
	_, err := (&Service{Store: &fakeRepositoryStore{repositories: []repository.Repository{readyRepository("a")}}, Backend: backend}).Context(t.Context(), principalFor(101), api.GraphContextRequest{Repo: api.GraphRepositorySelector{ID: 101}, GraphSymbolSelector: api.GraphSymbolSelector{UID: "id", Name: "name"}})
	if !errors.Is(err, ErrInvalidRequest) || backend.calls != 0 {
		t.Fatalf("err=%v calls=%d", err, backend.calls)
	}
}

func TestContextCapsPaginationAndRejectsBackendCommitMismatch(t *testing.T) {
	backend := &fakeBackend{context: func() graphprotocol.ContextResponse {
		return graphprotocol.ContextResponse{Commits: map[string]string{"a": strings.Repeat("b", 40)}}
	}}
	service := &Service{Store: &fakeRepositoryStore{repositories: []repository.Repository{readyRepository("a")}}, Backend: backend, Limits: Limits{PerCategory: 2}}
	_, err := service.Context(t.Context(), principalFor(101), api.GraphContextRequest{Repo: api.GraphRepositorySelector{ID: 101}, GraphSymbolSelector: api.GraphSymbolSelector{UID: "x"}, PerCategoryLimit: 99, PerCategoryOffset: 7})
	if !errors.Is(err, ErrGraphNotReady) || backend.contextRequest.PerCategoryLimit != 2 || backend.contextRequest.PerCategoryOffset != 7 {
		t.Fatalf("err=%v request=%#v", err, backend.contextRequest)
	}
}
