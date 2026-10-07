package repository

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/balcsida/graphnest/internal/authn"
	"github.com/balcsida/graphnest/pkg/api"
)

type graphGenerationReader struct {
	generations []api.GraphActiveGeneration
	err         error
}

func (reader *graphGenerationReader) ActiveGraphGenerations(context.Context, int64) ([]api.GraphActiveGeneration, error) {
	return reader.generations, reader.err
}

func generations(values ...api.GraphActiveGeneration) *graphGenerationReader {
	return &graphGenerationReader{generations: values}
}

func published(commit string) api.GraphActiveGeneration {
	return api.GraphActiveGeneration{Commit: commit, Source: api.GraphSourceExternal, Producer: "codegraph"}
}

func derived(commit string) api.GraphActiveGeneration {
	return api.GraphActiveGeneration{Commit: commit, Source: api.GraphSourceSCIP, Producer: "scip"}
}

func TestStatusReportsGraphGeneration(t *testing.T) {
	indexed := strings.Repeat("a", 40)
	older := strings.Repeat("b", 40)
	principal := authn.Principal{InstallationID: 10, RepositoryIDs: []int64{101}}

	for _, testCase := range []struct {
		name         string
		reader       GraphGenerationReader
		wantStatus   string
		wantCommit   string
		wantProducer string
	}{
		{"no reader wired", nil, api.GraphStatusUnknown, "", ""},
		{"no active generation", &graphGenerationReader{}, api.GraphStatusAbsent, "", ""},
		{"published current and SCIP-derived current", generations(published(indexed), derived(indexed)), api.GraphStatusCurrent, indexed, "codegraph"},
		{"published stale and SCIP-derived current", generations(published(older), derived(indexed)), api.GraphStatusCurrent, indexed, "scip"},
		{"published stale, no SCIP-derived", generations(published(older)), api.GraphStatusStale, older, "codegraph"},
		{"SCIP-derived stale only", generations(derived(older)), api.GraphStatusStale, older, "scip"},
		{"both stale", generations(published(older), derived(older)), api.GraphStatusStale, older, "codegraph"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			store := &serviceStore{repository: Repository{ID: 1, GitHubID: 101, Name: "acme/one", IndexedSHA: indexed, SearchNode: "node-a"}}
			got, err := (&Service{Store: store, Graph: testCase.reader}).Status(t.Context(), principal, 101)
			if err != nil {
				t.Fatal(err)
			}
			if got.GraphStatus != testCase.wantStatus || got.GraphCommit != testCase.wantCommit || got.GraphProducer != testCase.wantProducer {
				t.Fatalf("graph = %q %q %q, want %q %q %q", got.GraphStatus, got.GraphCommit, got.GraphProducer, testCase.wantStatus, testCase.wantCommit, testCase.wantProducer)
			}
		})
	}
}

func TestStatusFailsWhenGraphReaderFails(t *testing.T) {
	want := errors.New("graph store down")
	store := &serviceStore{repository: Repository{ID: 1, GitHubID: 101, Name: "acme/one", SearchNode: "node-a"}}
	principal := authn.Principal{InstallationID: 10, RepositoryIDs: []int64{101}}
	_, err := (&Service{Store: store, Graph: &graphGenerationReader{err: want}}).Status(t.Context(), principal, 101)
	if !errors.Is(err, want) {
		t.Fatalf("err = %v, want %v", err, want)
	}
}
