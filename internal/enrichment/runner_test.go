//go:build unix

package enrichment

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/grepnest/grepnest/internal/graphartifact"
	"github.com/grepnest/grepnest/internal/indexer"
	"github.com/grepnest/grepnest/internal/repository"
)

type fakeProcess struct {
	output []byte
	err    error
	args   []string
}

func (process *fakeProcess) Output(_ context.Context, _ string, args, _ []string, _ string) ([]byte, error) {
	process.args = slices.Clone(args)
	return process.output, process.err
}

func validResponse(t *testing.T) []byte {
	t.Helper()
	artifact := graphartifact.Artifact{
		SchemaVersion: 1,
		RepositoryID:  4,
		Commit:        "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		Analyzer:      graphartifact.Analyzer{Name: "grepnest-scanner", Version: "1"},
		ContentHash:   bytes.Repeat([]byte{1}, sha256.Size),
	}
	output, err := json.Marshal(Response{Version: ProtocolVersion, Artifact: artifact})
	if err != nil {
		t.Fatal(err)
	}
	return output
}

func TestRunnerUsesSnapshotWithoutSecretsOrOutputPath(t *testing.T) {
	process := &fakeProcess{output: validResponse(t)}
	runner := Runner{Binary: "/scanner", Process: process, Timeout: time.Second}
	snapshot := indexer.Snapshot{Root: "/snapshot", RepositoryID: 4, JobID: 11, CommitSHA: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}
	status, err := runner.Enrich(t.Context(), snapshot, repository.Repository{ID: 4}, snapshot.CommitSHA)
	if err != nil || status.Artifact == nil || status.Artifact.Commit != snapshot.CommitSHA {
		t.Fatalf("Enrich() = %#v, %v", status, err)
	}
	want := []string{"enrich", "--protocol=1", "--root=/snapshot", "--repository-id=4", "--commit=" + snapshot.CommitSHA}
	if !slices.Equal(process.args, want) {
		t.Fatalf("args = %q, want %q", process.args, want)
	}
}

func TestRunnerRejectsStaleAndInvalidOutput(t *testing.T) {
	snapshot := indexer.Snapshot{Root: "/snapshot", RepositoryID: 4, JobID: 11, CommitSHA: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}
	for _, test := range []struct {
		name   string
		output []byte
		target string
	}{
		{name: "stale input", output: validResponse(t), target: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"},
		{name: "invalid JSON", output: []byte("{}"), target: snapshot.CommitSHA},
		{name: "stale output", output: []byte(`{"version":1,"artifact":{"schema_version":1,"repository_id":4,"commit":"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"}}`), target: snapshot.CommitSHA},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, err := (Runner{Binary: "/scanner", Process: &fakeProcess{output: test.output}, Timeout: time.Second}).
				Enrich(t.Context(), snapshot, repository.Repository{ID: 4}, test.target)
			if err == nil {
				t.Fatal("Enrich() succeeded")
			}
		})
	}
}

func TestRunnerAppliesStrictTimeout(t *testing.T) {
	want := context.DeadlineExceeded
	process := &fakeProcess{err: want}
	snapshot := indexer.Snapshot{Root: "/snapshot", RepositoryID: 4, JobID: 11, CommitSHA: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}
	_, err := (Runner{Binary: "/scanner", Process: process, Timeout: time.Nanosecond}).
		Enrich(t.Context(), snapshot, repository.Repository{ID: 4}, snapshot.CommitSHA)
	if !errors.Is(err, want) {
		t.Fatalf("error = %v", err)
	}
}
