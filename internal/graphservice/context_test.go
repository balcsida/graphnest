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
	context func() graphprotocol.ContextResponse
	impact  func() graphprotocol.ImpactResponse
	trace   func() graphprotocol.TraceResponse
	cypher  func() graphprotocol.CypherResponse
	after   func()
	calls   int
}

func (b *fakeBackend) Context(context.Context, graphprotocol.ContextRequest) (graphprotocol.ContextResponse, error) {
	b.calls++
	if b.after != nil {
		b.after()
	}
	return b.context(), nil
}
func (b *fakeBackend) Impact(context.Context, graphprotocol.ImpactRequest) (graphprotocol.ImpactResponse, error) {
	b.calls++
	return b.impact(), nil
}
func (b *fakeBackend) Trace(context.Context, graphprotocol.TraceRequest) (graphprotocol.TraceResponse, error) {
	b.calls++
	return b.trace(), nil
}
func (b *fakeBackend) Cypher(context.Context, graphprotocol.CypherRequest) (graphprotocol.CypherResponse, error) {
	b.calls++
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
