package graphartifact_test

import (
	"bytes"
	"context"
	"reflect"
	"strings"
	"testing"

	"github.com/balcsida/graphnest/internal/graphartifact"
	graphv2 "github.com/balcsida/graphnest/internal/graphartifact/v2"
	"github.com/balcsida/graphnest/internal/graphimport"
	"google.golang.org/protobuf/proto"
)

// The production importer must reproduce the Python-bridge oracle byte for byte.
func TestImportMatchesOracle(t *testing.T) {
	want := graphartifact.FixtureV2ForOracle(t)
	snapshot, err := graphimport.Read(context.Background(), "../../test/fixtures/codegraph/reference.db", graphartifact.Limits{})
	if err != nil {
		t.Fatal(err)
	}
	got, report, err := graphimport.Convert(snapshot, graphimport.Options{
		Repository:       "https://github.com/example/project",
		Commit:           strings.Repeat("a", 40),
		Producer:         &graphv2.Producer{Name: "codegraph", Version: "1.6.0", Configuration: "portable"},
		IdentityProducer: &graphv2.Producer{Name: "codegraph", Version: "1.6.0"},
		IdentityScope:    "fixture",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !proto.Equal(want, got) {
		t.Fatal("imported artifact differs from oracle")
	}
	wantHash, err := graphartifact.SemanticHashV2(want, graphartifact.Limits{})
	if err != nil {
		t.Fatal(err)
	}
	gotHash, err := graphartifact.SemanticHashV2(got, graphartifact.Limits{})
	if err != nil || !bytes.Equal(wantHash, gotHash) {
		t.Fatalf("semantic hash differs: %x != %x (%v)", wantHash, gotHash, err)
	}
	wantBytes, err := graphartifact.MarshalV2(want, graphartifact.Limits{})
	if err != nil {
		t.Fatal(err)
	}
	gotBytes, err := graphartifact.MarshalV2(got, graphartifact.Limits{})
	if err != nil || !bytes.Equal(wantBytes, gotBytes) {
		t.Fatalf("marshaled bytes differ (%v)", err)
	}
	if report.Nodes != 68 || report.Edges != 93 || report.Files != 13 || report.Unresolved != 6 || report.Metadata != 5 || len(report.NodeKinds) != 20 || len(report.EdgeKinds) != 9 {
		t.Fatalf("report: %+v", report)
	}
	if !reflect.DeepEqual(report.NodeKinds, countKinds(want)) {
		t.Fatal("node kind counts")
	}
}

func countKinds(a *graphv2.Artifact) map[string]int {
	kinds := map[string]int{}
	for _, n := range a.Nodes {
		kinds[n.Kind]++
	}
	return kinds
}
