package graphimport

import (
	"strings"
	"testing"

	"github.com/balcsida/graphnest/internal/graphartifact"
	graphv2 "github.com/balcsida/graphnest/internal/graphartifact/v2"
)

func TestConvertUnknownEdgeKinds(t *testing.T) {
	s := &Snapshot{Edges: []Edge{{ID: 1, Source: "a", Target: "b", Kind: "zeta"}, {ID: 2, Source: "a", Target: "b", Kind: "calls"}, {ID: 3, Source: "a", Target: "b", Kind: "alpha"}, {ID: 4, Source: "a", Target: "b", Kind: "zeta"}}}
	_, _, err := Convert(s, Options{Repository: "r", Producer: &graphv2.Producer{Name: "codegraph", Version: "1"}})
	if err == nil || !strings.Contains(err.Error(), `["alpha" "zeta"]`) {
		t.Fatalf("want every unknown kind listed once, got %v", err)
	}
}

func TestConvertDefaultsProducerFromMetadata(t *testing.T) {
	s := &Snapshot{Metadata: []MetadataEntry{{Key: "indexed_with_version", Value: "1.6.0", UpdatedAt: 1}}}
	a, _, err := Convert(s, Options{Repository: "r", Commit: strings.Repeat("a", 40)})
	if err != nil {
		t.Fatal(err)
	}
	if a.Producer.GetName() != "codegraph" || a.Producer.GetVersion() != "1.6.0" || a.ImportedAt != 0 {
		t.Fatalf("producer %v importedAt %d", a.Producer, a.ImportedAt)
	}
	if _, err = graphartifact.MarshalV2(a, graphartifact.Limits{}); err != nil {
		t.Fatal(err)
	}
	if _, _, err = Convert(&Snapshot{}, Options{Repository: "r"}); err == nil {
		t.Fatal("missing indexed_with_version must fail without an explicit producer")
	}
	at := int64(7)
	if a, _, _ = Convert(s, Options{Repository: "r", ImportedAt: &at}); a.ImportedAt != 7 {
		t.Fatal("ImportedAt ignored")
	}
}
