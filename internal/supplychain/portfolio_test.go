package supplychain

import (
	"context"
	"encoding/json"
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

// componentStore serves a fixed component page and records the filter it got.
type componentStore struct {
	PortfolioStore
	components []PortfolioComponent
	filter     PortfolioFilter
}

func (store *componentStore) SupplyChainPortfolioComponents(_ context.Context, filter PortfolioFilter) ([]PortfolioComponent, error) {
	store.filter = filter
	return store.components[:min(len(store.components), filter.Limit)], nil
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

// A page of coordinates that every repository uses named every repository for
// every coordinate, outgrew the response byte limit and was answered with 500.
func TestPortfolioComponentsKeepAPageOfWidelyUsedCoordinatesWithinTheByteLimit(t *testing.T) {
	const maxResponseBytes = 256 << 10
	var repositories []repository.Repository
	var githubIDs []int64
	for index := range 3000 {
		repositories = append(repositories, repository.Repository{ID: int64(index + 1), GitHubID: int64(700000000 + index), Name: fmt.Sprintf("acme-corporation/service-repository-%04d", index)})
		githubIDs = append(githubIDs, int64(700000000+index))
	}
	store := &componentStore{}
	for index := range 101 {
		store.components = append(store.components, PortfolioComponent{Key: fmt.Sprintf("npm\x01@types\x01package-%03d\x011.0.0", index), Ecosystem: "npm", Namespace: "@types", Name: fmt.Sprintf("package-%03d", index), Version: "1.0.0",
			RepositoryCount: 3000, OccurrenceCount: 3000, AssessmentStatuses: []string{"declared"}, DeclaredRaw: []string{"MIT"}, RepositoryGitHubIDs: githubIDs})
	}
	portfolio := &Portfolio{Store: store, Authorizer: graphAuthorizer{repositories: repositories}}

	page, err := portfolio.Components(t.Context(), authn.Principal{}, ComponentsRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Components) != 100 || !page.Truncated || page.NextCursor == "" || store.filter.Limit != 101 {
		t.Fatalf("page = %d components, truncated %v, cursor %q, store limit %d", len(page.Components), page.Truncated, page.NextCursor, store.filter.Limit)
	}
	// 300,000 links exceed the budget of 1,000, so every coordinate names the same 10 lowest GitHub IDs and keeps its count.
	for _, component := range page.Components {
		if component.RepositoryCount != 3000 || len(component.Repositories) != 10 || component.Repositories[0].ID != 700000000 || component.Repositories[9].Name != "acme-corporation/service-repository-0009" {
			t.Fatalf("component %s names %d of %d repositories: %+v", component.Name, len(component.Repositories), component.RepositoryCount, component.Repositories)
		}
	}
	if data, err := json.Marshal(page); err != nil || len(data) > maxResponseBytes {
		t.Fatalf("page is %d bytes, over the limit of %d, err %v", len(data), maxResponseBytes, err)
	}

	// Within the budget every repository is named: 10 coordinates of 100 repositories each.
	for index := range store.components {
		store.components[index].RepositoryCount, store.components[index].RepositoryGitHubIDs = 100, githubIDs[:100]
	}
	page, err = portfolio.Components(t.Context(), authn.Principal{}, ComponentsRequest{Limit: 10})
	if err != nil || len(page.Components) != 10 || len(page.Components[9].Repositories) != 100 {
		t.Fatalf("page within the budget = %d components, err %v", len(page.Components), err)
	}
}
