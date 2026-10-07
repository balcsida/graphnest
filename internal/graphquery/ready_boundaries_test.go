package graphquery

import (
	"context"
	"reflect"
	"testing"

	"github.com/balcsida/graphnest/internal/graphartifact"
	"github.com/balcsida/graphnest/internal/graphprotocol"
)

type manifestsTestStore struct {
	stubQueryStore
	manifests map[int64]graphartifact.Manifest
}

func (s *manifestsTestStore) Manifests(context.Context) (map[int64]graphartifact.Manifest, error) {
	return s.manifests, nil
}

func TestReadySummarizesUnrelatedRepositoryBoundaries(t *testing.T) {
	scope := graphprotocol.Scope{Repositories: []graphprotocol.RepositorySnapshot{
		{ID: 1, Name: "a", Commit: "sha"}, {ID: 2, Name: "b", Commit: "sha"},
		{ID: 3, Name: "c", Commit: "sha"}, {ID: 4, Name: "d", Commit: "sha"}, {ID: 5, Name: "e", Commit: "sha"},
	}}
	tests := []struct {
		name      string
		selected  int64
		manifests map[int64]graphartifact.Manifest
		want      []graphprotocol.Boundary
	}{
		{"selected ready", 1, map[int64]graphartifact.Manifest{1: {Commit: "sha"}, 5: {Commit: "old"}},
			[]graphprotocol.Boundary{{Reason: "graph_missing", Count: 3}, {Reason: "graph_not_ready", Count: 1}}},
		{"selected missing", 1, map[int64]graphartifact.Manifest{2: {Commit: "sha"}, 3: {Commit: "old"}},
			[]graphprotocol.Boundary{{RepositoryID: 1, Repository: "a", Reason: "graph_missing"}, {Reason: "graph_missing", Count: 2}, {Reason: "graph_not_ready", Count: 1}}},
		{"selected not ready", 1, map[int64]graphartifact.Manifest{1: {Commit: "old"}, 2: {Commit: "sha"}, 3: {Commit: "sha"}, 4: {Commit: "sha"}, 5: {Commit: "sha"}},
			[]graphprotocol.Boundary{{RepositoryID: 1, Repository: "a", Reason: "graph_not_ready"}}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			scope.SelectedRepositoryID = test.selected
			ready, err := (&Service{Store: &manifestsTestStore{manifests: test.manifests}}).ready(t.Context(), scope)
			if err != nil || !reflect.DeepEqual(ready.boundaries, test.want) {
				t.Fatalf("boundaries = %+v, err = %v, want %+v", ready.boundaries, err, test.want)
			}
		})
	}
}
