//go:build integration

package postgres

import (
	"encoding/hex"
	"errors"
	"testing"

	graphv2 "github.com/balcsida/graphnest/internal/graphartifact/v2"
	"github.com/balcsida/graphnest/pkg/api"
	"google.golang.org/protobuf/proto"
)

func TestActiveGraphGenerationDescribesAnySchema(t *testing.T) {
	s, id := readyGraphStore(t, testSHA('a'))
	if active, err := s.ActiveGraphGeneration(t.Context(), id); err != nil || active != nil {
		t.Fatalf("empty repository active=%#v err=%v", active, err)
	}
	managed, err := s.ReplaceGraph(t.Context(), id, GraphSourceManaged, artifactFor(id, testSHA('a'), "managed"))
	if err != nil {
		t.Fatal(err)
	}
	active, err := s.ActiveGraphGeneration(t.Context(), id)
	want := api.GraphActiveGeneration{ID: managed.Upload.ID, Commit: testSHA('a'), SchemaVersion: 1, Source: api.GraphSourceManaged, Producer: "managed", ProducerVersion: "1", ContentHash: hex.EncodeToString(artifactFor(id, testSHA('a'), "").ContentHash)}
	if err != nil || active == nil || *active != want {
		t.Fatalf("v1 active=%#v err=%v", active, err)
	}
	artifact := storageV2Artifact()
	published, err := s.ReplaceGraphV2(t.Context(), id, GraphPublication{Publisher: "api_token:42", ExpectedActiveID: managed.Upload.ID, AllowProviderChange: true}, artifact)
	if err != nil || published.ReplacedID != managed.Upload.ID {
		t.Fatalf("publish=%#v err=%v", published, err)
	}
	active, err = s.ActiveGraphGeneration(t.Context(), id)
	want = api.GraphActiveGeneration{ID: published.Upload.ID, Commit: testSHA('a'), SchemaVersion: 2, Source: api.GraphSourceExternal, Producer: "codegraph", ProducerVersion: "pinned", ContentHash: hex.EncodeToString(artifact.ContentHash)}
	if err != nil || active == nil || *active != want {
		t.Fatalf("v2 active=%#v err=%v", active, err)
	}
}

func TestGraphPublicationRetriesCannotOverwriteTheWrongGeneration(t *testing.T) {
	s, id := readyGraphStore(t, testSHA('a'))
	first := storageV2Artifact()
	original, err := s.ReplaceGraphV2(t.Context(), id, GraphPublication{Publisher: "api_token:42"}, first)
	if err != nil || original.Deduplicated || original.ReplacedID != 0 {
		t.Fatalf("first=%#v err=%v", original, err)
	}
	// A retry after a lost response still names the old expectation; the
	// scoped content hash recognizes it, whoever retries.
	retry, err := s.ReplaceGraphV2(t.Context(), id, GraphPublication{Publisher: "api_token:43"}, first)
	if err != nil || !retry.Applied || !retry.Deduplicated || retry.Upload.ID != original.Upload.ID {
		t.Fatalf("retry=%#v err=%v", retry, err)
	}
	var generations int
	var publisher string
	if err := s.pool.QueryRow(t.Context(), `select count(*),min(publisher) from graph_uploads where repository_id=$1`, id).Scan(&generations, &publisher); err != nil || generations != 1 || publisher != "api_token:42" {
		t.Fatalf("generations=%d publisher=%q err=%v", generations, publisher, err)
	}
	second := changedStorageArtifact("second")
	if _, err := s.ReplaceGraphV2(t.Context(), id, GraphPublication{Publisher: "api_token:42"}, second); !errors.Is(err, ErrGraphPrecondition) {
		t.Fatalf("stale expectation replaced a generation: %v", err)
	}
	replaced, err := s.ReplaceGraphV2(t.Context(), id, GraphPublication{Publisher: "api_token:42", ExpectedActiveID: original.Upload.ID}, second)
	if err != nil || replaced.Deduplicated || replaced.ReplacedID != original.Upload.ID {
		t.Fatalf("replacement=%#v err=%v", replaced, err)
	}
	// A late retry of retired content must not reactivate it.
	if _, err := s.ReplaceGraphV2(t.Context(), id, GraphPublication{Publisher: "api_token:42"}, first); !errors.Is(err, ErrGraphPrecondition) {
		t.Fatalf("retired content retry=%v", err)
	}
	// An advanced indexed SHA fails even an identical retry.
	if _, err := s.pool.Exec(t.Context(), `update repositories set indexed_sha=$2 where id=$1`, id, testSHA('b')); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ReplaceGraphV2(t.Context(), id, GraphPublication{Publisher: "api_token:42", ExpectedActiveID: replaced.Upload.ID}, second); !errors.Is(err, ErrGraphPrecondition) {
		t.Fatalf("advanced SHA retry=%v", err)
	}
	var active int64
	if err := s.pool.QueryRow(t.Context(), `select id from graph_uploads where repository_id=$1 and active`, id).Scan(&active); err != nil || active != replaced.Upload.ID {
		t.Fatalf("active=%d err=%v", active, err)
	}
}

func changedStorageArtifact(message string) *graphv2.Artifact {
	a := proto.Clone(storageV2Artifact()).(*graphv2.Artifact)
	a.Diagnostics[0].Message = message
	a.ContentHash = nil
	return a
}
