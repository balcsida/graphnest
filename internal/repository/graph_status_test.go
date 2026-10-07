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
	generation *api.GraphActiveGeneration
	err        error
}

func (reader *graphGenerationReader) ActiveGraphGeneration(context.Context, int64) (*api.GraphActiveGeneration, error) {
	return reader.generation, reader.err
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
		{"generation for an earlier commit", &graphGenerationReader{generation: &api.GraphActiveGeneration{Commit: older, Producer: "scip"}}, api.GraphStatusStale, older, "scip"},
		{"matches indexed revision", &graphGenerationReader{generation: &api.GraphActiveGeneration{Commit: indexed, Producer: "codegraph"}}, api.GraphStatusCurrent, indexed, "codegraph"},
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
