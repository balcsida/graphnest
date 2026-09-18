package graphservice

import (
	"context"
	"errors"
	"testing"

	"github.com/balcsida/graphnest/internal/authn"
	"github.com/balcsida/graphnest/pkg/api"
)

func TestDiscoverUsesSelectedPublicScope(t *testing.T) {
	service, backend, _ := exploreFixture()
	got, err := service.Discover(t.Context(), principalFor(101), api.GraphDiscoverRequest{
		Repo: api.GraphRepositorySelector{ID: 101}, Query: "hello", Symbols: []string{"identity"},
	})
	if err != nil || len(got.Matches) != 1 || len(backend.requests) != 1 {
		t.Fatalf("discover=%+v requests=%d err=%v", got, len(backend.requests), err)
	}
	scope := backend.requests[0].Scope
	if len(scope.Repositories) != 1 || scope.Repositories[0].GitHubID != 101 || scope.SelectedRepositoryID != 1 {
		t.Fatalf("scope=%+v", scope)
	}
}

func TestDiscoverRejectsUntrustedMatchAndFreshCredential(t *testing.T) {
	service, backend, _ := exploreFixture()
	request := api.GraphDiscoverRequest{Repo: api.GraphRepositorySelector{ID: 101}, Query: "hello"}
	backend.matches[0].Entity.RepositoryID = 202
	if _, err := service.Discover(t.Context(), principalFor(101), request); !errors.Is(err, ErrGraphNotReady) {
		t.Fatalf("untrusted match error=%v", err)
	}
	backend.matches[0].Entity.RepositoryID = 101
	ctx := authn.WithFreshPrincipal(t.Context(), func(context.Context) (authn.Principal, error) {
		return authn.Principal{}, authn.ErrUnauthenticated
	})
	if _, err := service.Discover(ctx, principalFor(101), request); !errors.Is(err, authn.ErrUnauthenticated) {
		t.Fatalf("fresh credential error=%v", err)
	}
}

func TestCapabilitiesReportsCurrentV2GenerationAndV1Upload(t *testing.T) {
	service, backend, _ := exploreFixture()
	backend.generation.Capabilities = []string{"relations", "source"}
	got, err := service.Capabilities(t.Context(), principalFor(101), api.GraphCapabilitiesRequest{Repo: api.GraphRepositorySelector{ID: 101}})
	if err != nil {
		t.Fatal(err)
	}
	if got.Version != 1 || got.Status != "ready" || got.Freshness != "current" || got.CurrentIndexedCommit != backend.generation.Commit || got.Generation == nil || got.Generation.UploadID != 1 {
		t.Fatalf("capabilities=%+v", got)
	}
	if len(got.QueryArtifactVersions) != 2 || got.QueryArtifactVersions[0] != 1 || got.QueryArtifactVersions[1] != 2 || len(got.UploadArtifactVersions) != 1 || got.UploadArtifactVersions[0] != 1 {
		t.Fatalf("artifact versions=%+v/%+v", got.QueryArtifactVersions, got.UploadArtifactVersions)
	}
	if !got.DiscoveryProjection || len(got.ProducerCapabilities) != 2 {
		t.Fatalf("availability=%+v", got)
	}
}
