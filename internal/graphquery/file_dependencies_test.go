package graphquery

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/balcsida/graphnest/internal/graphprotocol"
)

type fileDependencyTestStore struct {
	entityTestStore
	pairs   []FileDependencyPairRow
	queries int
}

func TestFileDependencyCycleAndNonCallAffectedControl(t *testing.T) {
	store := &fileDependencyTestStore{pairs: []FileDependencyPairRow{
		{RepositoryID: 1, Source: "cycle-a.ts", Target: "cycle-b.ts", References: 2},
		{RepositoryID: 1, Source: "cycle-b.ts", Target: "cycle-a.ts", References: 2},
		{RepositoryID: 1, Source: "non-call-only.test.ts", Target: "non-call-base.ts", References: 2},
	}}
	service := &Service{Store: store}
	cycles, err := service.CircularDependencies(t.Context(), graphprotocol.FileDependencyRequest{Scope: entityTestScope()})
	if err != nil || !reflect.DeepEqual(cycles.Cycles, [][]string{{"cycle-a.ts", "cycle-b.ts"}}) {
		t.Fatalf("cycles=%+v err=%v", cycles, err)
	}
	affected, err := service.AffectedTests(t.Context(), graphprotocol.AffectedTestsRequest{Scope: entityTestScope(), ChangedFiles: []string{"non-call-base.ts"}})
	if err != nil || !reflect.DeepEqual(affected.AffectedTests, []string{"non-call-only.test.ts"}) || affected.TotalDependentsTraversed != 1 {
		t.Fatalf("non-call affected=%+v err=%v", affected, err)
	}
}

func TestCircularDependenciesPreservePinnedOverlappingPaths(t *testing.T) {
	store := &fileDependencyTestStore{pairs: []FileDependencyPairRow{
		{RepositoryID: 1, Source: "a.ts", Target: "b.ts", References: 1},
		{RepositoryID: 1, Source: "b.ts", Target: "a.ts", References: 1},
		{RepositoryID: 1, Source: "b.ts", Target: "c.ts", References: 1},
		{RepositoryID: 1, Source: "c.ts", Target: "b.ts", References: 1},
	}}
	got, err := (&Service{Store: store}).CircularDependencies(t.Context(), graphprotocol.FileDependencyRequest{Scope: entityTestScope()})
	want := [][]string{{"a.ts", "b.ts"}, {"b.ts", "c.ts"}}
	if err != nil || !reflect.DeepEqual(got.Cycles, want) {
		t.Fatalf("cycles=%v want=%v err=%v", got.Cycles, want, err)
	}
}

func TestAffectedTestsOptionsMatchPinnedAnswers(t *testing.T) {
	store := &fileDependencyTestStore{pairs: []FileDependencyPairRow{
		{RepositoryID: 1, Source: "Widget.vue", Target: "core.ts", References: 1},
		{RepositoryID: 1, Source: "consumer.test.ts", Target: "consumer.ts", References: 2},
		{RepositoryID: 1, Source: "consumer.ts", Target: "main.ts", References: 1},
		{RepositoryID: 1, Source: "main.ts", Target: "core.ts", References: 5},
		{RepositoryID: 1, Source: "unicode.ts", Target: "core.ts", References: 1},
	}}
	service := &Service{Store: store}
	tests := []struct {
		name    string
		request graphprotocol.AffectedTestsRequest
		want    []string
		count   int
	}{
		{"changed test", graphprotocol.AffectedTestsRequest{ChangedFiles: []string{"consumer.test.ts"}}, []string{"consumer.test.ts"}, 0},
		{"custom filter", graphprotocol.AffectedTestsRequest{ChangedFiles: []string{"core.ts"}, TestGlob: "consumer.test.ts"}, []string{"consumer.test.ts"}, 5},
		{"custom regex question", graphprotocol.AffectedTestsRequest{ChangedFiles: []string{"core.ts"}, TestGlob: "consumer?.test.ts"}, []string{"consumer.test.ts"}, 5},
		{"depth two", graphprotocol.AffectedTestsRequest{ChangedFiles: []string{"core.ts"}, MaxDepth: 2}, []string{}, 4},
		{"depth three", graphprotocol.AffectedTestsRequest{ChangedFiles: []string{"core.ts"}, MaxDepth: 3}, []string{"consumer.test.ts"}, 5},
		{"multiple", graphprotocol.AffectedTestsRequest{ChangedFiles: []string{"core.ts", "orphan.ts"}}, []string{"consumer.test.ts"}, 5},
		{"missing", graphprotocol.AffectedTestsRequest{ChangedFiles: []string{"__missing__.ts"}}, []string{}, 0},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			test.request.Scope = entityTestScope()
			got, err := service.AffectedTests(t.Context(), test.request)
			if err != nil || !reflect.DeepEqual(got.AffectedTests, test.want) || got.TotalDependentsTraversed != test.count {
				t.Fatalf("affected=%+v err=%v", got, err)
			}
			if test.name == "depth two" && (!got.Partial || len(got.Boundaries) != 1 || got.Boundaries[0].Reason != "depth_limit") {
				t.Fatalf("missing depth boundary: %+v", got)
			}
		})
	}
}

func TestFileDependenciesRejectStaleHiddenAndInvalidResults(t *testing.T) {
	for _, test := range []struct {
		name  string
		store *fileDependencyTestStore
		req   graphprotocol.FileDependencyRequest
		want  error
	}{
		{"generation", &fileDependencyTestStore{entityTestStore: entityTestStore{changed: true}}, graphprotocol.FileDependencyRequest{Scope: entityTestScope()}, ErrGenerationChanged},
		{"hidden", &fileDependencyTestStore{pairs: []FileDependencyPairRow{{RepositoryID: 99, Source: "a.ts", Target: "b.ts", References: 1}}}, graphprotocol.FileDependencyRequest{Scope: entityTestScope()}, ErrGenerationChanged},
		{"parent path", &fileDependencyTestStore{}, graphprotocol.FileDependencyRequest{Scope: entityTestScope(), Paths: []string{"../secret"}}, ErrInvalidRequest},
		{"negative limit", &fileDependencyTestStore{}, graphprotocol.FileDependencyRequest{Scope: entityTestScope(), Limit: -1}, ErrInvalidRequest},
	} {
		t.Run(test.name, func(t *testing.T) {
			got, err := (&Service{Store: test.store}).FileDependencyPairs(t.Context(), test.req)
			if !errors.Is(err, test.want) || !reflect.DeepEqual(got, graphprotocol.FileDependencyResponse{}) {
				t.Fatalf("result=%+v err=%v", got, err)
			}
		})
	}
}

func TestFileDependencyBoundsAndCancellation(t *testing.T) {
	store := &fileDependencyTestStore{pairs: []FileDependencyPairRow{
		{RepositoryID: 1, Source: "a.ts", Target: "root.ts", References: 1},
		{RepositoryID: 1, Source: "b.ts", Target: "root.ts", References: 1},
	}}
	got, err := (&Service{Store: store, Limits: Limits{MaxEdges: 1}}).FileDependencyPairs(t.Context(), graphprotocol.FileDependencyRequest{Scope: entityTestScope()})
	if err != nil || !got.Partial || len(got.Pairs) != 1 || len(got.Boundaries) != 1 || got.Boundaries[0].Reason != "edge_limit" {
		t.Fatalf("bounded=%+v err=%v", got, err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	got, err = (&Service{Store: store}).FileDependencyPairs(ctx, graphprotocol.FileDependencyRequest{Scope: entityTestScope()})
	if !errors.Is(err, context.Canceled) || !reflect.DeepEqual(got, graphprotocol.FileDependencyResponse{}) {
		t.Fatalf("canceled=%+v err=%v", got, err)
	}
}

func TestFileDependencyNilServiceIsInvalid(t *testing.T) {
	var service *Service
	got, err := service.FileDependencyPairs(t.Context(), graphprotocol.FileDependencyRequest{Scope: entityTestScope()})
	if !errors.Is(err, ErrInvalidRequest) || !reflect.DeepEqual(got, graphprotocol.FileDependencyResponse{}) {
		t.Fatalf("result=%+v err=%v", got, err)
	}
}

func (s *fileDependencyTestStore) QueryFileDependencyPairs(_ context.Context, q FileDependencyQuery) ([]FileDependencyPairRow, error) {
	s.queries++
	rows := make([]FileDependencyPairRow, 0, len(s.pairs))
	requested := map[string]bool{}
	for _, path := range q.Paths {
		requested[path] = true
	}
	for _, pair := range s.pairs {
		if len(requested) > 0 && q.Direction == "source" && !requested[pair.Source] ||
			len(requested) > 0 && q.Direction == "target" && !requested[pair.Target] {
			continue
		}
		rows = append(rows, pair)
	}
	if len(rows) > q.Limit {
		rows = rows[:q.Limit]
	}
	return rows, nil
}

func TestFileDependencyEmptyCountsKeepGenerationWithoutQuery(t *testing.T) {
	store := &fileDependencyTestStore{}
	service := &Service{Store: store}
	request := graphprotocol.FileDependencyRequest{Scope: entityTestScope()}
	for _, query := range []func(context.Context, graphprotocol.FileDependencyRequest) (graphprotocol.FileDependencyResponse, error){service.FileDependentCounts, service.FileReachCounts} {
		got, err := query(t.Context(), request)
		if err != nil || len(got.Counts) != 0 || len(got.Generations) != 1 || store.queries != 0 {
			t.Fatalf("response=%+v queries=%d err=%v", got, store.queries, err)
		}
	}
}

func TestFileDependencyOperationsMatchPinnedAnswers(t *testing.T) {
	store := &fileDependencyTestStore{pairs: []FileDependencyPairRow{
		{RepositoryID: 1, Source: "Widget.vue", Target: "core.ts", References: 1},
		{RepositoryID: 1, Source: "app/index.tsx", Target: "app/details.tsx", References: 1},
		{RepositoryID: 1, Source: "consumer.test.ts", Target: "consumer.ts", References: 2},
		{RepositoryID: 1, Source: "consumer.ts", Target: "main.ts", References: 1},
		{RepositoryID: 1, Source: "main.ts", Target: "core.ts", References: 5},
		{RepositoryID: 1, Source: "unicode.ts", Target: "core.ts", References: 1},
	}}
	service := &Service{Store: store}
	request := graphprotocol.FileDependencyRequest{Scope: entityTestScope()}

	pairs, err := service.FileDependencyPairs(t.Context(), request)
	if err != nil || len(pairs.Pairs) != 6 || pairs.Pairs[4].Source != "main.ts" || pairs.Pairs[4].Target != "core.ts" {
		t.Fatalf("pairs=%+v err=%v", pairs, err)
	}
	request.Paths = []string{"main.ts"}
	dependencies, err := service.FileDependencies(t.Context(), request)
	if err != nil || !reflect.DeepEqual(dependencies.Files, []string{"core.ts"}) {
		t.Fatalf("dependencies=%+v err=%v", dependencies, err)
	}
	request.Paths = []string{"core.ts"}
	dependents, err := service.FileDependents(t.Context(), request)
	if err != nil || !reflect.DeepEqual(dependents.Files, []string{"Widget.vue", "main.ts", "unicode.ts"}) {
		t.Fatalf("dependents=%+v err=%v", dependents, err)
	}
	request.Paths = []string{"core.ts", "main.ts", "orphan.ts", "__missing__.ts"}
	counts, err := service.FileDependentCounts(t.Context(), request)
	if err != nil || !reflect.DeepEqual(counts.Counts, []graphprotocol.FileDependencyCount{{Path: "core.ts", Dependents: 3}, {Path: "main.ts", Dependents: 1}}) {
		t.Fatalf("dependent counts=%+v err=%v", counts, err)
	}
	request.Paths = []string{"main.ts", "consumer.test.ts", "orphan.ts", "__missing__.ts"}
	reach, err := service.FileReachCounts(t.Context(), request)
	if err != nil || !reflect.DeepEqual(reach.Counts, []graphprotocol.FileDependencyCount{{Path: "consumer.test.ts", Reaches: 1, References: 2}, {Path: "main.ts", Reaches: 1, References: 5}}) {
		t.Fatalf("reach counts=%+v err=%v", reach, err)
	}
	cycles, err := service.CircularDependencies(t.Context(), graphprotocol.FileDependencyRequest{Scope: entityTestScope()})
	if err != nil || len(cycles.Cycles) != 0 {
		t.Fatalf("cycles=%+v err=%v", cycles, err)
	}
	affected, err := service.AffectedTests(t.Context(), graphprotocol.AffectedTestsRequest{Scope: entityTestScope(), ChangedFiles: []string{"core.ts"}})
	if err != nil || !reflect.DeepEqual(affected.AffectedTests, []string{"consumer.test.ts"}) || affected.TotalDependentsTraversed != 5 {
		t.Fatalf("affected=%+v err=%v", affected, err)
	}
}
