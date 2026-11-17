package supplychain

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/balcsida/graphnest/internal/authn"
	"github.com/balcsida/graphnest/internal/repository"
)

// graphStore serves a fixed dependency graph and records the filter it got.
type graphStore struct {
	PortfolioStore
	dependencies []GraphDependency
	filter       GraphFilter
}

func (store *graphStore) SupplyChainDependencyGraph(_ context.Context, filter GraphFilter) ([]GraphDependency, int, error) {
	store.filter = filter
	return store.dependencies, len(store.dependencies) + 5, nil
}

type graphAuthorizer struct{ repositories []repository.Repository }

func (authorizer graphAuthorizer) AllAuthorizedRepositories(context.Context, authn.Principal) ([]repository.Repository, error) {
	return authorizer.repositories, nil
}

func (graphAuthorizer) AuthorizedRepository(context.Context, authn.Principal, int64) (repository.Repository, error) {
	return repository.Repository{}, errors.New("not used")
}

func TestPortfolioGraphKeepsTheResponseWithinItsBudgets(t *testing.T) {
	// Four dependencies, each used by all 400 repositories and seen in 30 versions.
	var repositories []repository.Repository
	var githubIDs []int64
	for index := range 400 {
		repositories = append(repositories, repository.Repository{ID: int64(index + 1), GitHubID: int64(9000 - index), Name: fmt.Sprintf("acme/repo-%03d", index)})
		githubIDs = append([]int64{int64(9000 - index)}, githubIDs...)
	}
	var versions []string
	for index := range 30 {
		versions = append(versions, fmt.Sprintf("1.%02d.0", index))
	}
	store := &graphStore{}
	for _, name := range []string{"a", "b", "c", "d"} {
		store.dependencies = append(store.dependencies, GraphDependency{Ecosystem: "npm", Name: name, Versions: versions, AssessmentStatuses: []string{"declared"}, Expressions: []string{}, RepositoryCount: 400, RepositoryGitHubIDs: githubIDs})
	}
	portfolio := &Portfolio{Store: store, Authorizer: graphAuthorizer{repositories: repositories}}

	graph, err := portfolio.Graph(t.Context(), authn.Principal{}, GraphRequest{Ecosystem: "NPM"})
	if err != nil {
		t.Fatal(err)
	}
	if store.filter.MinRepositories != DefaultGraphMinRepositories || store.filter.Limit != MaxGraphDependencies || store.filter.Ecosystem != "npm" || store.filter.StreamKey != StreamGitHubSource || len(store.filter.RepositoryIDs) != 400 {
		t.Fatalf("filter = %+v", store.filter)
	}
	if graph.Matching != 9 || !graph.Truncated || graph.RepositoriesInScope != 400 || graph.MinRepositories != DefaultGraphMinRepositories || len(graph.Dependencies) != 4 {
		t.Fatalf("graph = %+v", graph)
	}
	// 1,600 links exceed the budget of 1,000, so each dependency lists the same 250 lowest GitHub IDs.
	for _, dependency := range graph.Dependencies {
		if dependency.RepositoryCount != 400 || len(dependency.RepositoryIDs) != 250 || dependency.RepositoryIDs[0] != 8601 || dependency.RepositoryIDs[249] != 8850 {
			t.Fatalf("dependency %s lists %d of %d repositories from %d", dependency.Name, len(dependency.RepositoryIDs), dependency.RepositoryCount, dependency.RepositoryIDs[0])
		}
		if dependency.VersionCount != 30 || len(dependency.Versions) != graphListLimit || dependency.Expressions == nil {
			t.Fatalf("dependency %s versions = %d of %d, expressions = %v", dependency.Name, len(dependency.Versions), dependency.VersionCount, dependency.Expressions)
		}
	}
	if len(graph.Repositories) != 250 || graph.Repositories[0].ID != 8601 || graph.Repositories[0].Name != "acme/repo-399" {
		t.Fatalf("repositories = %d, first %+v", len(graph.Repositories), graph.Repositories[0])
	}

	// Within the budget nothing is left out.
	store.dependencies = store.dependencies[:2]
	graph, err = portfolio.Graph(t.Context(), authn.Principal{}, GraphRequest{MinRepositories: 3, Limit: 2})
	if err != nil || len(graph.Dependencies[1].RepositoryIDs) != 400 || len(graph.Repositories) != 400 || store.filter.MinRepositories != 3 || store.filter.Limit != 2 {
		t.Fatalf("graph within the budget lists %d repositories, err %v, filter %+v", len(graph.Repositories), err, store.filter)
	}

	for _, request := range []GraphRequest{{Limit: MaxGraphDependencies + 1}, {Limit: -1}, {MinRepositories: -1}, {AssessmentStatus: "approved"}, {Stream: "nope"}} {
		if _, err := portfolio.Graph(t.Context(), authn.Principal{}, request); !errors.Is(err, ErrInvalidRequest) {
			t.Fatalf("request %+v: err = %v, want invalid request", request, err)
		}
	}
}
